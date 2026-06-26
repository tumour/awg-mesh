package gossip

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// handlePeers пишет ДВА разных ack'а: announce (v) и commit (cv). Seed по commit-ack'ам
// решает «вооружать» ли flip — это и есть гарантия против strand'а медленной ноды.
func TestHandlePeers_RecordsAnnounceAndCommitAcks(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers?node=X&v=8&cv=7", nil))

	if got := srv.Acks()["X"]; got != 8 {
		t.Errorf("announce-ack = %d, want 8", got)
	}
	if got := srv.CommitAcks()["X"]; got != 7 {
		t.Errorf("commit-ack = %d, want 7", got)
	}
}

// Старый бинарь не шлёт cv: commit-ack обязан остаться 0 (seed НЕ armʼит → безопасный
// no-op в mixed-version раскатке), но announce-ack всё равно записывается.
func TestHandlePeers_MissingCommitAckStaysZero(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "selfkey", NodeIP: "100.64.0.1",
	})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handlePeers(rec, httptest.NewRequest(http.MethodGet, "/v1/peers?node=X&v=8", nil))

	if got := srv.Acks()["X"]; got != 8 {
		t.Errorf("announce-ack = %d, want 8", got)
	}
	if got := srv.CommitAcks()["X"]; got != 0 {
		t.Errorf("commit-ack без cv = %d, want 0", got)
	}
}

// commit-ack монотонен и не откатывается назад (как и announce-ack).
func TestRecordCommitAck_Monotonic(t *testing.T) {
	store := saveState(t, &state.State{PublicKey: "selfkey", NodeIP: "100.64.0.1"})
	srv := NewServer("100.64.0.1", DefaultPort, store, discardLogger())

	srv.recordCommitAck("X", 5)
	srv.recordCommitAck("X", 3) // старее — игнор
	if got := srv.CommitAcks()["X"]; got != 5 {
		t.Errorf("commit-ack = %d, want 5 (не откатывается)", got)
	}
	srv.recordCommitAck("X", 9)
	if got := srv.CommitAcks()["X"]; got != 9 {
		t.Errorf("commit-ack = %d, want 9", got)
	}
}

// doRound шлёт оба ack'а: v=видел-анонс, cv=держу-committed. У ноды с АНОНСОМ (ApplyAt=0)
// cv обязан быть = применённой версии (анонс НЕ считается committed) — иначе seed решит,
// что нода готова флипать, не получив ApplyAt (исходный баг flint2).
func TestDoRound_SendsAnnounceAndCommitAcks(t *testing.T) {
	var gotV, gotCV string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotV = r.URL.Query().Get("v")
		gotCV = r.URL.Query().Get("cv")
		_ = json.NewEncoder(w).Encode(PeersResponse{})
	}))
	defer ts.Close()
	host, portStr, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	store := saveState(t, &state.State{
		NetworkCIDR:   "100.64.0.0/24",
		PublicKey:     "selfkey",
		NodeIP:        "100.64.0.1",
		ParamsVersion: 3,
		Pending:       &state.PendingParams{Version: 5}, // announced (ApplyAt=0)
		Peers: []state.Peer{
			{Label: "target", PublicKey: "tkey", NodeIP: host, Endpoint: "203.0.113.1:51820"},
		},
	})
	c := NewClient(store, "selfkey", time.Minute, port, func([]state.Peer) {}, nil, discardLogger())
	c.doRound(context.Background())

	if gotV != "5" {
		t.Errorf("announce-ack v = %q, want 5 (нода видела анонс)", gotV)
	}
	if gotCV != "3" {
		t.Errorf("commit-ack cv = %q, want 3 (анонс НЕ committed → cv=применённая)", gotCV)
	}
}
