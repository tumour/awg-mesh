package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// Транспортные тесты gossip (Store + http). Доменный merge покрыт в
// internal/mesh; здесь проверяем именно склейку doRound (fetch → MergePeers →
// запись по persist → onNewPeers) и handlePeers — persist-баг жил ровно тут.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePeerServerFull поднимает HTTP-узел, отдающий заданные peer-list и tombstones
// на /v1/peers (как настоящий gossip-сервер). Возвращает loopback host:port для
// NodeIP цели.
func fakePeerServerFull(t *testing.T, peers []proto.PeerInfo, tombstones []state.Tombstone) (host string, port int, ts *httptest.Server) {
	t.Helper()
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(PeersResponse{Peers: peers, Tombstones: tombstones})
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

// fakePeerServer — частый случай без tombstones.
func fakePeerServer(t *testing.T, peers []proto.PeerInfo) (host string, port int, ts *httptest.Server) {
	t.Helper()
	return fakePeerServerFull(t, peers, nil)
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
	}, nil, discardLogger())

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
	}, nil, discardLogger())

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

// doRound применяет tombstone: отозванный peer снимается с device (onRemovedPeers),
// убирается из state и НЕ реанонсится, даже если сосед всё ещё шлёт его в Peers.
func TestDoRoundAppliesTombstone(t *testing.T) {
	const (
		self      = "selfkey"
		targetKey = "targetkey"
		orphanKey = "orphankey"
	)
	// Цель всё ещё знает orphan в peer-list (реанонс), но уже несёт tombstone на него.
	host, port, ts := fakePeerServerFull(t,
		[]proto.PeerInfo{
			{Label: "target", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"},
			{Label: "orphan", PublicKey: orphanKey, NodeIP: "100.64.0.4", Endpoint: "203.0.113.9:51820"},
		},
		[]state.Tombstone{{PublicKey: orphanKey, Label: "orphan"}},
	)
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"},
			// orphan без endpoint → не gossip-цель (детерминизм: pull всегда к target).
			// Реанонс orphan'а С endpoint прилетает из ответа сервера — его и блокируем.
			{Label: "orphan", PublicKey: orphanKey, NodeIP: "100.64.0.4"},
		},
	})

	var removed []state.Peer
	c := NewClient(store, self, time.Minute, port,
		func([]state.Peer) {},
		func(ps []state.Peer) { removed = append(removed, ps...) },
		discardLogger())

	c.doRound(context.Background())

	if len(removed) != 1 || removed[0].PublicKey != orphanKey {
		t.Fatalf("onRemovedPeers: got %+v, want 1 orphan", removed)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if findPeer(got.Peers, orphanKey) != nil {
		t.Fatal("отозванный orphan должен быть убран из state.Peers")
	}
	if findPeer(got.Peers, targetKey) == nil {
		t.Fatal("живой target не должен пострадать")
	}
	if !mesh.IsRevoked(got.Tombstones, orphanKey) {
		t.Fatalf("tombstone на orphan должен осесть в state.Tombstones: %+v", got.Tombstones)
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
	}, nil, discardLogger())

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

// handlePeers отдаёт и tombstones — revoke раздаётся тем же ответом, что и peers.
func TestHandlePeersIncludesTombstones(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		Tombstones: []state.Tombstone{{PublicKey: "GONE", Label: "gone"}},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers", nil))

	var resp PeersResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !mesh.IsRevoked(resp.Tombstones, "GONE") {
		t.Fatalf("tombstones not served: %+v", resp.Tombstones)
	}
}

// handleTombstone (leave-push) кладёт СВОЙ отзыв в state — но только self-leave:
// pushed pubkey должен принадлежать ноде, с чьего mesh-IP пришёл запрос.
func TestHandleTombstoneAcceptsSelfLeave(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		Peers: []state.Peer{{Label: "leaver", PublicKey: "LEAVER", NodeIP: "100.64.0.2"}},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	body, _ := json.Marshal(state.Tombstone{PublicKey: "LEAVER", Label: "leaver"})
	req := httptest.NewRequest(http.MethodPost, "/v1/tombstone", bytes.NewReader(body))
	req.RemoteAddr = "100.64.0.2:40000" // leaver объявляет себя со своего mesh-IP
	rec := httptest.NewRecorder()
	srv.handleTombstone(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("self-leave: status %d, want 204", rec.Code)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !mesh.IsRevoked(got.Tombstones, "LEAVER") {
		t.Fatalf("self-leave tombstone not stored: %+v", got.Tombstones)
	}
}

// M1 (security): один POST НЕ должен «убить» чужую ноду. handleTombstone обязан
// принимать только tombstone на pubkey, владеющий source mesh-IP. Иначе любой
// инсайдер изнутри туннеля перманентно выселяет кого угодно (включая seed).
func TestHandleTombstoneRejectsForeignPubkey(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		Peers: []state.Peer{
			{Label: "attacker", PublicKey: "ATTACKER", NodeIP: "100.64.0.2"},
			{Label: "victim", PublicKey: "VICTIM", NodeIP: "100.64.0.3"},
		},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	// Атакующий (mesh-IP .2) пытается отозвать VICTIM (.3) — чужой pubkey.
	body, _ := json.Marshal(state.Tombstone{PublicKey: "VICTIM"})
	req := httptest.NewRequest(http.MethodPost, "/v1/tombstone", bytes.NewReader(body))
	req.RemoteAddr = "100.64.0.2:40000"
	rec := httptest.NewRecorder()
	srv.handleTombstone(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("форж tombstone на чужой pubkey должен отклоняться (M1)")
	}
	got, _ := store.Read()
	if mesh.IsRevoked(got.Tombstones, "VICTIM") {
		t.Fatal("чужой tombstone не должен оседать в state")
	}
}

// F3: при двух peer с одним mesh-IP (нарушение инварианта) авторизация должна
// требовать совпадения И IP, И pubkey у ОДНОГО peer — иначе first-match отверг бы
// легитимный self-leave второго peer (или авторизовал чужой). На уникальных IP
// поведение не меняется.
func TestSelfLeaveAuthorizedSharedIP(t *testing.T) {
	peers := []state.Peer{
		{PublicKey: "X", NodeIP: "100.64.0.2"},
		{PublicKey: "Y", NodeIP: "100.64.0.2"}, // дубль IP (аномалия)
	}
	if !selfLeaveAuthorized(peers, "100.64.0.2", "X") {
		t.Fatal("X (первый на shared-IP) — легитимный self, должен пройти")
	}
	if !selfLeaveAuthorized(peers, "100.64.0.2", "Y") {
		t.Fatal("Y (второй на shared-IP) — тоже легитимный self, должен пройти (match-both)")
	}
	if selfLeaveAuthorized(peers, "100.64.0.2", "Z") {
		t.Fatal("Z не на этом IP — форж, должен отклониться")
	}
}

// M2: огромное тело POST не должно прочитываться целиком (DoS на память роутера).
func TestHandleTombstoneBodyTooLarge(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		Peers: []state.Peer{{Label: "leaver", PublicKey: "LEAVER", NodeIP: "100.64.0.2"}},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	// 1 МБ мусора в поле label — должно быть отвергнуто лимитом, а не прочитано.
	huge := state.Tombstone{PublicKey: "LEAVER", Label: strings.Repeat("A", 1<<20)}
	body, _ := json.Marshal(huge)
	req := httptest.NewRequest(http.MethodPost, "/v1/tombstone", bytes.NewReader(body))
	req.RemoteAddr = "100.64.0.2:40000"
	rec := httptest.NewRecorder()
	srv.handleTombstone(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("гигантское тело должно быть отвергнуто (M2 MaxBytesReader)")
	}
}

// handleTombstone отвергает не-POST и пустой pubkey.
func TestHandleTombstoneRejectsBad(t *testing.T) {
	store := saveState(t, &state.State{NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1"})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handleTombstone(rec, httptest.NewRequest(http.MethodGet, "/v1/tombstone", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}

	body, _ := json.Marshal(state.Tombstone{PublicKey: ""})
	rec = httptest.NewRecorder()
	srv.handleTombstone(rec, httptest.NewRequest(http.MethodPost, "/v1/tombstone", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty pubkey: status %d, want 400", rec.Code)
	}

	// Битый JSON в теле → 400 (ветка decode-error).
	rec = httptest.NewRecorder()
	srv.handleTombstone(rec, httptest.NewRequest(http.MethodPost, "/v1/tombstone", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body: status %d, want 400", rec.Code)
	}
}

// leave end-to-end: PushTombstone (отправляющая сторона) → handleTombstone оседает
// в state приёмника. Закрывает дыру «отправитель leave не покрыт».
func TestPushTombstoneEndToEnd(t *testing.T) {
	// Приёмник знает leaver'а под loopback-IP (источник httptest-запроса).
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
		Peers: []state.Peer{{Label: "leaver", PublicKey: "LEAVER", NodeIP: "127.0.0.1"}},
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())
	ts := httptest.NewServer(http.HandlerFunc(srv.handleTombstone))
	defer ts.Close()
	host, p, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(p)

	hc := &http.Client{Timeout: 5 * time.Second}
	if err := PushTombstone(context.Background(), hc, host, port, state.Tombstone{PublicKey: "LEAVER", Label: "leaver"}); err != nil {
		t.Fatalf("PushTombstone: %v", err)
	}
	got, _ := store.Read()
	if !mesh.IsRevoked(got.Tombstones, "LEAVER") {
		t.Fatal("pushed tombstone не осел в state приёмника")
	}
	// Идемпотентность: повторный push (уже знаем) → 204, без ошибки.
	if err := PushTombstone(context.Background(), hc, host, port, state.Tombstone{PublicKey: "LEAVER"}); err != nil {
		t.Fatalf("повторный PushTombstone: %v", err)
	}
}

// doRound устойчив к сбойному ответу цели (5xx / битый JSON): не паникует, state
// не меняет. Реальный кейс — сосед в плохом состоянии.
func TestDoRoundHandlesBadResponse(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"5xx":        func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
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
			c := NewClient(store, "self", time.Minute, port, nil, nil, discardLogger())

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
	c := NewClient(store, self, time.Minute, port, nil, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if got.Pending == nil || got.Pending.Version != 5 || got.Pending.Params.S4 != 16 {
		t.Fatalf("newer pending not adopted: %+v", got.Pending)
	}
}

// doRound подхватывает committed ApplyAt на УЖЕ принятый announced-Pending той же
// версии. Это сценарий из эксплуатации, дважды клавший сеть: нода держит анонс (ApplyAt=0),
// seed закоммитил (ApplyAt назначен, версия НЕ менялась) — без распространения этого
// перехода момент применения не доходит и флипает только seed.
func TestDoRoundAdoptsCommitOnAnnounced(t *testing.T) {
	const self, targetKey = "selfkey", "targetkey"
	applyAt := time.Now().Add(2 * time.Minute).UTC()
	committedPend := &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: awgparams.Params{S4: 16}}
	host, port, ts := fakeServerWithPending(t,
		[]proto.PeerInfo{{Label: "t", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"}},
		4, committedPend)
	defer ts.Close()

	// Локально уже лежит ТОТ ЖЕ Pending v5, но ещё announced (ApplyAt нулевой).
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: self, NodeIP: "100.64.0.1",
		ParamsVersion: 4,
		Pending:       &state.PendingParams{Version: 5, Params: awgparams.Params{S4: 16}}, // ApplyAt=0
		Peers:         []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}},
	})
	c := NewClient(store, self, time.Minute, port, nil, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if got.Pending == nil || got.Pending.Version != 5 {
		t.Fatalf("Pending v5 должен сохраниться: %+v", got.Pending)
	}
	if got.Pending.ApplyAt.IsZero() {
		t.Fatal("committed ApplyAt НЕ подхвачен — flip снова случится только на seed (тот самый баг)")
	}
	if !got.Pending.ApplyAt.Equal(applyAt) {
		t.Errorf("ApplyAt = %v, want %v", got.Pending.ApplyAt, applyAt)
	}
}

// doRound НЕ пере-планирует уже committed Pending (анти-reschedule): чужой committed
// с другим ApplyAt на той же версии отвергается.
func TestDoRoundDoesNotRescheduleCommitted(t *testing.T) {
	const self, targetKey = "selfkey", "targetkey"
	ours := time.Now().Add(time.Minute).UTC()
	theirs := time.Now().Add(10 * time.Minute).UTC()
	host, port, ts := fakeServerWithPending(t,
		[]proto.PeerInfo{{Label: "t", PublicKey: targetKey, NodeIP: "100.64.0.2", Endpoint: "203.0.113.1:51820"}},
		4, &state.PendingParams{Version: 5, ApplyAt: theirs})
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: self, NodeIP: "100.64.0.1",
		ParamsVersion: 4,
		Pending:       &state.PendingParams{Version: 5, ApplyAt: ours}, // уже committed у нас
		Peers:         []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}},
	})
	c := NewClient(store, self, time.Minute, port, nil, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if !got.Pending.ApplyAt.Equal(ours) {
		t.Fatalf("ApplyAt пере-планирован чужим committed: got %v, want %v", got.Pending.ApplyAt, ours)
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
	c := NewClient(store, self, time.Minute, port, nil, nil, discardLogger())

	c.doRound(context.Background())

	got, _ := store.Read()
	if got.Pending != nil {
		t.Fatalf("stale pending must be ignored, got %+v", got.Pending)
	}
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
