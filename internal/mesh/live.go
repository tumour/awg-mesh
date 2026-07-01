package mesh

import (
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// PeerLive — live-сигнал по одному peer'у: нейтральный вход для домена. wg.PeerStat
// конвертируется в это на стыке node — домен не знает про wg/UAPI/handshake-байты.
type PeerLive struct {
	LastHandshake time.Time // zero = handshake ещё не было
}

// LivenessThreshold — максимальный возраст последнего handshake, при котором peer
// считается online. 3× rekey (120с) с запасом: keepalive=25с у endpoint-пиров
// держит handshake свежим, так что старше порога — реально пропавший узел.
const LivenessThreshold = 180 * time.Second

// classifyLiveness — online, если последний handshake не позже LivenessThreshold
// назад (граница включена). Нулевой handshake (никогда) или старше порога → offline.
func classifyLiveness(lastHandshake, now time.Time) string {
	if lastHandshake.IsZero() {
		return "offline"
	}
	if now.Sub(lastHandshake) <= LivenessThreshold {
		return "online"
	}
	return "offline"
}

// BuildStatusLive — как BuildStatus, но обогащает peers live-сигналом из wg-handshake.
// live: pubkey → PeerLive; nil-map ⇒ поведение BuildStatus (LiveStatus пуст у всех).
// now инъектируется ради чистоты (без time.Now() внутри). Себя (is_self) и пиров без
// записи в live НЕ классифицируем — LiveStatus остаётся "" (неизвестно), чтобы контракт
// не выдавал догадку за факт.
func BuildStatusLive(s *state.State, live map[string]PeerLive, now time.Time) StatusView {
	view := BuildStatus(s)
	for i := range view.Peers {
		p := &view.Peers[i]
		if p.IsSelf {
			continue
		}
		lv, ok := live[p.PublicKey]
		if !ok {
			continue
		}
		p.LiveStatus = classifyLiveness(lv.LastHandshake, now)
		if !lv.LastHandshake.IsZero() {
			hs := lv.LastHandshake
			p.LastHandshake = &hs
		}
	}
	return view
}
