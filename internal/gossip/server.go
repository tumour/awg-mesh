// Package gossip — periodic peer-list synchronization между нодами через wg-туннель.
//
// Сервер биндится ИСКЛЮЧИТЕЛЬНО на mesh-IP (например 100.64.0.1:9100), не на
// 0.0.0.0. С eth0/публичного интерфейса к нему не достучаться — единственный
// путь это через wg-туннель. Trust-by-tunneling: внутри wg все peers уже
// прошли Noise IKpsk2 с правильным cluster-secret, доверяем им.
//
// Wire-protocol: HTTP JSON. Endpoint:
//
//	GET /v1/peers → {"peers":[{label,public_key,endpoint,node_ip,is_seed},...]}
//
// Клиент мерджит ответ со своим peer-list'ом и применяет diff к wg-device.
package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// DefaultPort — порт gossip-API. Слушает только на mesh-IP.
const DefaultPort = 9100

// HTTP-таймауты gossip-сервера: даже внутри доверенного туннеля медленный/
// зависший peer не должен держать соединение и goroutine бесконечно (slowloris).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
)

// Server — HTTP API для отдачи peer-listа.
type Server struct {
	store *state.Store
	addr  string
	srv   *http.Server
	log   *slog.Logger

	// acks — последняя версия params, о которой отрепортил каждый пир (по pubkey)
	// в gossip-запросе (?node=&v=). Seed по ним решает, все ли получили Pending
	// (mesh.AllPeersAcked) перед назначением ApplyAt. В памяти: ack'и эфемерны.
	mu   sync.Mutex
	acks map[string]uint64
}

// PeersResponse — JSON-форма ответа на /v1/peers. Peers — общий wire-DTO
// proto.PeerInfo (тот же, что в bootstrap-HelloResponse). ParamsVersion+Pending
// раздают запланированную flag-day-смену сетевых params: получатель принимает
// строго более новый Pending (mesh.ShouldAdoptPending) и применит его синхронно
// в ApplyAt. PendingParams сериализуется как есть (domain-тип, JSON-готов).
//
// Tombstones — отозванные ноды (revoke/leave). Раздаются union'ом, как peers;
// получатель мерджит их (mesh.MergeTombstones), снимает отозванных с wg-device и
// перекрывает их реанонс. omitempty: пустой набор не пишется на провод — старый
// бинарь (без поля) поймёт ответ как раньше (совместимость во время self-upgrade).
type PeersResponse struct {
	Peers         []proto.PeerInfo     `json:"peers"`
	UpdatedAt     time.Time            `json:"updated_at"`
	ParamsVersion uint64               `json:"params_version"`
	Pending       *state.PendingParams `json:"pending_params,omitempty"`
	Tombstones    []state.Tombstone    `json:"tombstones,omitempty"`
}

// NewServer создаёт сервер на mesh-IP:port. host обычно = state.NodeIP.
// logger (nil → slog.Default()) инъектируется для embeddability.
func NewServer(host string, port int, store *state.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	return &Server{store: store, addr: addr, log: logger.With("component", "gossip"), acks: map[string]uint64{}}
}

// recordAck монотонно запоминает версию, о которой сообщил пир (gossip-запрос).
func (s *Server) recordAck(pubkey string, version uint64) {
	if pubkey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if version > s.acks[pubkey] {
		s.acks[pubkey] = version
	}
}

// Acks возвращает копию собранных ack'ов (pubkey → версия). Snapshot под локом,
// чтобы seed-commit-loop читал без гонки. Пустая мапа, если репортов ещё не было.
func (s *Server) Acks() map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]uint64, len(s.acks))
	for k, v := range s.acks {
		out[k] = v
	}
	return out
}

// Start запускает сервер. Останавливается при отмене ctx.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/peers", s.handlePeers)
	mux.HandleFunc("/v1/tombstone", s.handleTombstone)

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()

	s.log.Info("server listening", "url", fmt.Sprintf("http://%s/v1/peers", s.addr))
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("gossip server: %w", err)
	}
	return nil
}

// handlePeers — отдаёт текущий state.peers как JSON.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// ack: «пир node в курсе версий params до v». Trust-by-tunneling — внутри
	// туннеля источник уже прошёл cluster-secret-проверку.
	if node := r.URL.Query().Get("node"); node != "" {
		if v, err := strconv.ParseUint(r.URL.Query().Get("v"), 10, 64); err == nil {
			s.recordAck(node, v)
		}
	}
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("load state failed", "err", err)
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}

	resp := PeersResponse{
		Peers:         proto.PeerInfosFromState(st.Peers),
		UpdatedAt:     st.UpdatedAt,
		ParamsVersion: st.ParamsVersion,
		Pending:       st.Pending,
		Tombstones:    st.Tombstones,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Статус 200 уже ушёл (заголовок записан) — починить тело нельзя, но факт
		// битого ответа фиксируем у себя.
		s.log.Warn("encode peers response failed", "err", err)
	}
}

// maxTombstoneBody — лимит тела POST /v1/tombstone. Tombstone — это pubkey + короткий
// label + время; 4 КиБ с запасом, чтобы инсайдер не флудил гигантскими телами (DoS
// на память роутера).
const maxTombstoneBody = 4 << 10

// handleTombstone принимает отзыв, запушенный уходящей нодой (meshd leave). Нода
// под NAT не дождётся pull (её никто не пуллит, см. mesh.GossipCandidates), поэтому
// сама пушит свой tombstone endpoint-пирам. Кладём его в state union'ом — дальше он
// разойдётся обычным pull-gossip'ом, а демон снимет ушедшего с device в своём цикле.
//
// Принимаем ТОЛЬКО self-leave: pushed pubkey должен принадлежать ноде, с чьего
// mesh-IP пришёл запрос. Иначе один POST изнутри туннеля перманентно отзывал бы
// любую ноду (включая seed) — отзыв без отката. Отзыв ДРУГИХ нод — прерогатива seed
// через state+gossip (meshd revoke), не через этот эндпоинт.
func (s *Server) handleTombstone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ts state.Tombstone
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTombstoneBody)).Decode(&ts); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if ts.PublicKey == "" {
		http.Error(w, "empty pubkey", http.StatusBadRequest)
		return
	}
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("load state failed", "err", err)
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	srcIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !selfLeaveAuthorized(st.Peers, srcIP, ts.PublicKey) {
		s.log.Warn("rejected foreign leave-push", "src", srcIP, "pubkey", mesh.ShortKey(ts.PublicKey))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.store.Update(func(st *state.State) error {
		merged, added := mesh.MergeTombstones(st.Tombstones, []state.Tombstone{ts})
		if len(added) == 0 {
			return state.ErrNoChange // уже знаем этот отзыв — идемпотентно
		}
		st.Tombstones = merged
		return nil
	}); err != nil {
		s.log.Error("apply pushed tombstone failed", "err", err)
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	s.log.Info("tombstone received via leave-push", "pubkey", mesh.ShortKey(ts.PublicKey), "label", ts.Label)
	w.WriteHeader(http.StatusNoContent)
}

// selfLeaveAuthorized — pushed tombstone легитимен, только если его pubkey
// принадлежит peer'у, чей mesh-IP = источник запроса (нода объявляет САМА СЕБЯ).
// Требуем совпадения И NodeIP, И pubkey у ОДНОГО peer (не first-match-then-compare):
// если бы два peer делили один mesh-IP (нарушение инварианта ip-hijack из MergePeers),
// first-match отверг бы легитимный self второго или авторизовал чужой отзыв.
// Пустой/неизвестный srcIP, или pubkey не того узла → отказ (fail-closed).
func selfLeaveAuthorized(peers []state.Peer, srcIP, pubkey string) bool {
	if srcIP == "" {
		return false
	}
	for _, p := range peers {
		if p.NodeIP == srcIP && p.PublicKey == pubkey {
			return true
		}
	}
	return false
}
