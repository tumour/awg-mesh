package gossip

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// Транспортные тесты gossip (Store + http). Доменный merge покрыт в
// internal/mesh; здесь проверяем именно склейку doRound (fetch → MergePeers →
// запись по persist → onNewPeers) и handlePeers — persist-баг жил ровно тут.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePeerServer поднимает HTTP-узел, отдающий заданный peer-list на /v1/peers
// (как настоящий gossip-сервер). Возвращает loopback host:port для NodeIP цели.
func fakePeerServer(t *testing.T, peers []proto.PeerInfo) (host string, port int, ts *httptest.Server) {
	t.Helper()
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(PeersResponse{Peers: peers})
	}))
	h, p, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split server addr: %v", err)
	}
	pn, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return h, pn, ts
}

func saveState(t *testing.T, st *state.State) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return state.NewStore(path)
}

func findPeer(ps []state.Peer, key string) *state.Peer {
	for i := range ps {
		if ps[i].PublicKey == key {
			return &ps[i]
		}
	}
	return nil
}

// doRound: новый peer из ответа цели попадает и в onNewPeers (пуш в device),
// и на диск.
func TestDoRoundAddsNewPeer(t *testing.T) {
	const (
		self      = "selfkey"
		targetKey = "targetkey"
		newKey    = "newkey"
	)
	// NodeIP цели = loopback тест-сервера: gossip-pull (HTTP GET) пойдёт туда.
	host, port, ts := fakePeerServer(t, []proto.PeerInfo{
		{Label: "newnode", PublicKey: newKey, NodeIP: "100.64.0.5", Endpoint: "198.51.100.7:51820"},
	})
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			// Endpoint непустой → цель проходит mesh.GossipCandidates.
			{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"},
		},
	})

	var pushed []state.Peer
	c := NewClient(store, self, time.Minute, port, func(ps []state.Peer) {
		pushed = append(pushed, ps...)
	}, discardLogger())

	c.doRound(context.Background())

	if len(pushed) != 1 || pushed[0].PublicKey != newKey {
		t.Fatalf("onNewPeers: got %+v, want 1 peer %q", pushed, newKey)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if findPeer(got.Peers, newKey) == nil {
		t.Fatalf("new peer %q not persisted: %+v", newKey, got.Peers)
	}
}

// doRound-регресс на persist-баг: обновление label существующего peer'а НЕ идёт
// в onNewPeers (на wg-маршрутизацию label не влияет), но ОБЯЗАНО осесть на диск.
// До фикса doRound решал запись по len(changed) и молча терял такие обновления.
func TestDoRoundPersistsLabelUpdateWithoutDevicePush(t *testing.T) {
	const (
		self      = "selfkey"
		targetKey = "targetkey"
	)
	// Тот же peer (targetKey), но с новым label и ТЕМ ЖЕ endpoint → changed пуст,
	// persist=true.
	host, port, ts := fakePeerServer(t, []proto.PeerInfo{
		{Label: "renamed", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"},
	})
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "oldname", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"},
		},
	})

	var pushed []state.Peer
	c := NewClient(store, self, time.Minute, port, func(ps []state.Peer) {
		pushed = append(pushed, ps...)
	}, discardLogger())

	c.doRound(context.Background())

	if len(pushed) != 0 {
		t.Fatalf("onNewPeers must not fire for label-only change, got %+v", pushed)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if p := findPeer(got.Peers, targetKey); p == nil || p.Label != "renamed" {
		t.Fatalf("label update not persisted: %+v", got.Peers)
	}
}

// doRound без достижимых целей (все за NAT) — no-op, без паники/записи.
func TestDoRoundNoCandidatesIsNoop(t *testing.T) {
	const self = "selfkey"
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			// Без endpoint → не кандидат для gossip-pull.
			{Label: "nat", PublicKey: "natkey", NodeIP: "100.64.0.9"},
		},
	})

	called := false
	c := NewClient(store, self, time.Minute, DefaultPort, func([]state.Peer) {
		called = true
	}, discardLogger())

	c.doRound(context.Background())

	if called {
		t.Fatal("onNewPeers fired without reachable candidates")
	}
}

// handlePeers отдаёт текущий peer-list как JSON на GET.
func TestHandlePeersReturnsState(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "selfkey",
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "a", PublicKey: "akey", NodeIP: "100.64.0.2", IsSeed: true},
		},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var resp PeersResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Peers) != 1 || resp.Peers[0].PublicKey != "akey" {
		t.Fatalf("peers: %+v", resp.Peers)
	}
}

// doRound при pull сообщает цели свой ack: node=selfPub и v=max(ParamsVersion,
// Pending.Version) — это вход для seed-commit. Без корректного ack flip не
// закоммитится никогда, поэтому путь обязателен к проверке.
func TestDoRoundSendsAck(t *testing.T) {
	var gotNode, gotV string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNode = r.URL.Query().Get("node")
		gotV = r.URL.Query().Get("v")
		_ = json.NewEncoder(w).Encode(PeersResponse{})
	}))
	defer ts.Close()
	host, p, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(p)

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELFPUB", NodeIP: "100.64.0.1",
		ParamsVersion: 4,
		Pending:       &state.PendingParams{Version: 7}, // acked = max(4,7) = 7
		Peers:         []state.Peer{{Label: "t", PublicKey: "tk", NodeIP: host, Endpoint: "203.0.113.1:51820"}},
	})
	c := NewClient(store, "SELFPUB", time.Minute, port, nil, discardLogger())

	c.doRound(context.Background())

	if gotNode != "SELFPUB" {
		t.Errorf("ack node = %q, want SELFPUB", gotNode)
	}
	if gotV != "7" {
		t.Errorf("ack v = %q, want 7 (max of paramsVersion=4, pending=7)", gotV)
	}
}

// doRound устойчив к сбойному ответу цели (5xx / битый JSON): не паникует, state
// не меняет. Реальный кейс — сосед в плохом состоянии.
func TestDoRoundHandlesBadResponse(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"5xx":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"битый JSON": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(h)
			defer ts.Close()
			host, p, _ := net.SplitHostPort(ts.Listener.Addr().String())
			port, _ := strconv.Atoi(p)

			store := saveState(t, &state.State{
				NetworkCIDR: "100.64.0.0/24", PublicKey: "self", NodeIP: "100.64.0.1", ParamsVersion: 2,
				Peers: []state.Peer{{Label: "t", PublicKey: "tk", NodeIP: host, Endpoint: "203.0.113.1:51820"}},
			})
			c := NewClient(store, "self", time.Minute, port, nil, discardLogger())

			c.doRound(context.Background()) // не должен паниковать

			s, _ := store.Read()
			if s.ParamsVersion != 2 || s.Pending != nil {
				t.Errorf("state не должен меняться от битого ответа: %+v", s)
			}
		})
	}
}

// fakeServerWithPending — узел, отдающий peers + версию params + Pending.
func fakeServerWithPending(t *testing.T, peers []proto.PeerInfo, version uint64, pend *state.PendingParams) (string, int, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(PeersResponse{Peers: peers, ParamsVersion: version, Pending: pend})
	}))
	h, p, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	pn, _ := strconv.Atoi(p)
	return h, pn, ts
}

// handlePeers раздаёт версию params и Pending (для flag-day-смены).
func TestHandlePeersReturnsPending(t *testing.T) {
	pend := &state.PendingParams{Version: 7, ApplyAt: time.Now().Add(time.Minute).UTC(),
		Params: awgparams.Params{S4: 16}}
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		ParamsVersion: 6, Pending: pend,
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers", nil))

	var resp PeersResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ParamsVersion != 6 || resp.Pending == nil || resp.Pending.Version != 7 ||
		resp.Pending.Params.S4 != 16 {
		t.Fatalf("pending not served: version=%d pending=%+v", resp.ParamsVersion, resp.Pending)
	}
}

// doRound принимает СТРОГО более новый Pending от цели (раздача flag-day).
func TestDoRoundAdoptsNewerPending(t *testing.T) {
	const self, targetKey = "selfkey", "targetkey"
	pend := &state.PendingParams{Version: 5, ApplyAt: time.Now().Add(time.Minute).UTC(),
		Params: awgparams.Params{S4: 16}}
	host, port, ts := fakeServerWithPending(t,
		[]proto.PeerInfo{{Label: "t", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"}},
		5, pend)
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: self, NodeIP: "100.64.0.1",
		ParamsVersion: 4, // pend.Version=5 строго новее → принимаем
		Peers:         []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}},
	})
	c := NewClient(store, self, time.Minute, port, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if got.Pending == nil || got.Pending.Version != 5 || got.Pending.Params.S4 != 16 {
		t.Fatalf("newer pending not adopted: %+v", got.Pending)
	}
}

// doRound ИГНОРИРУЕТ устаревший Pending (версия не новее уже применённой).
func TestDoRoundIgnoresStalePending(t *testing.T) {
	const self, targetKey = "selfkey", "targetkey"
	stale := &state.PendingParams{Version: 3, ApplyAt: time.Now().Add(time.Minute).UTC()}
	host, port, ts := fakeServerWithPending(t,
		[]proto.PeerInfo{{Label: "t", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"}},
		3, stale)
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: self, NodeIP: "100.64.0.1",
		ParamsVersion: 5, // уже новее, чем pend.Version=3 → не принимаем
		Peers:         []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}},
	})
	c := NewClient(store, self, time.Minute, port, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if got.Pending != nil {
		t.Fatalf("stale pending must be ignored, got %+v", got.Pending)
	}
}

// handlePeers записывает ack из query (?node=&v=) монотонно — seed по ним решает,
// все ли получили Pending.
func TestHandlePeersRecordsAck(t *testing.T) {
	store := saveState(t, &state.State{NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1"})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	get := func(q string) {
		rec := httptest.NewRecorder()
		srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers?"+q, nil))
	}
	get("node=peerX&v=5")
	if srv.Acks()["peerX"] != 5 {
		t.Fatalf("ack не записан: %v", srv.Acks())
	}
	get("node=peerX&v=3") // меньшая версия не откатывает
	if srv.Acks()["peerX"] != 5 {
		t.Errorf("ack откатился назад: %v", srv.Acks())
	}
	get("node=peerX&v=7") // большая — обновляет
	if srv.Acks()["peerX"] != 7 {
		t.Errorf("ack не вырос до 7: %v", srv.Acks())
	}
	get("") // без node — игнор, без паники
}

// handlePeers отвечает 405 на не-GET (gossip read-only).
func TestHandlePeersRejectsNonGet(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "selfkey",
		NodeIP:      "100.64.0.1",
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodPost, "/v1/peers", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}
