package gossip

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
	"github.com/tumour/awg-mesh/internal/state"
)

// Отказ в кривом Pending повторяется на каждом цикле: seed пушит каждые ~10 с, pull —
// раз в gossip-интервал, пока оператор не переанонсирует. Лог на каждый повтор вымывает
// маленький RAM-буфер логов роутера (единицы МБ) за день-два — пропадает вся остальная
// диагностика. Отказ по ОДНОЙ версии логируется один раз; новая версия — снова.

func TestVersionOnce(t *testing.T) {
	var o versionOnce
	steps := []struct {
		v    uint64
		want bool
	}{{5, true}, {5, false}, {5, false}, {6, true}, {5, false}, {6, false}, {9, true}}
	for i, s := range steps {
		if got := o.first(s.v); got != s.want {
			t.Fatalf("шаг %d: first(%d) = %v, want %v", i, s.v, got, s.want)
		}
	}
}

func TestVersionOnce_Concurrent(t *testing.T) {
	var o versionOnce
	var wg sync.WaitGroup
	var mu sync.Mutex
	firsts := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o.first(7) {
				mu.Lock()
				firsts++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firsts != 1 {
		t.Fatalf("first(7) вернул true %d раз из 64 конкурентных вызовов, want 1", firsts)
	}
}

// syncBuf — потокобезопасный буфер для slog (handler пишет из горутин запросов).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) count(substr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.b.String(), substr)
}

func TestHandleParams_RejectionLoggedOncePerVersion(t *testing.T) {
	var buf syncBuf
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	store := saveState(t, nodeState(nil))
	srv := NewServer("100.64.0.2", DefaultPort, store, logger)
	bad := awgtest.DeviceInvalid(awgtest.Valid(16))["jmin>jmax"]

	for i := 0; i < 5; i++ {
		pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: &state.PendingParams{Version: 5, Params: bad}})
	}
	if n := buf.count("rejected pushed pending"); n != 1 {
		t.Fatalf("отказ v5 залогирован %d раз за 5 push'ей, want 1", n)
	}
	pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: &state.PendingParams{Version: 6, Params: bad}})
	if n := buf.count("rejected pushed pending"); n != 2 {
		t.Fatalf("отказ новой версии v6 не залогирован (всего %d), want 2", n)
	}
}

func TestDoRound_RejectionLoggedOncePerVersion(t *testing.T) {
	var buf syncBuf
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	bad := awgtest.DeviceInvalid(awgtest.Valid(16))["jmin>jmax"]
	const self, targetKey = "SELF", "targetkey"
	host, port, ts := fakeServerWithPending(t,
		nil, 4, &state.PendingParams{Version: 5, ApplyAt: time.Now().Add(time.Minute).UTC(), Params: bad})
	defer ts.Close()
	st := nodeState(nil)
	st.Peers = []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}}
	c := NewClient(saveState(t, st), self, time.Minute, port, nil, nil, logger)

	for i := 0; i < 5; i++ {
		c.doRound(t.Context())
	}
	if n := buf.count("pending params rejected"); n != 1 {
		t.Fatalf("отказ v5 залогирован %d раз за 5 раундов, want 1", n)
	}
}
