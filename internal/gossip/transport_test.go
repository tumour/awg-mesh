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
