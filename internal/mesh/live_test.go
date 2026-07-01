package mesh

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

func peersByLabel(v StatusView) map[string]PeerView {
	m := make(map[string]PeerView, len(v.Peers))
	for _, p := range v.Peers {
		m[p.Label] = p
	}
	return m
}

func TestBuildStatusLiveClassifies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := &state.State{
		PublicKey: "SELF",
		NodeIP:    "100.64.0.1",
		Peers: []state.Peer{
			{Label: "seed", PublicKey: "SELF", NodeIP: "100.64.0.1", IsSeed: true},
			{Label: "gw", PublicKey: "GW", NodeIP: "100.64.0.2", Endpoint: "203.0.113.24:51820"},
			{Label: "lap", PublicKey: "LAP", NodeIP: "100.64.0.3"},
		},
	}
	live := map[string]PeerLive{
		"GW":   {LastHandshake: now.Add(-30 * time.Second)}, // свежий → online
		"LAP":  {LastHandshake: now.Add(-10 * time.Minute)}, // старый → offline
		"SELF": {LastHandshake: now},                        // себя не классифицируем
	}

	byLabel := peersByLabel(BuildStatusLive(s, live, now))

	if got := byLabel["gw"].LiveStatus; got != "online" {
		t.Errorf("gw live_status = %q, want online", got)
	}
	if byLabel["gw"].LastHandshake == nil {
		t.Error("gw LastHandshake = nil, want выставленный")
	}
	if got := byLabel["lap"].LiveStatus; got != "offline" {
		t.Errorf("lap live_status = %q, want offline", got)
	}
	if got := byLabel["seed"].LiveStatus; got != "" {
		t.Errorf("self live_status = %q, want пусто (себя не классифицируем)", got)
	}
}

// nil live-map ⇒ поведение как у BuildStatus: никакого live_status.
func TestBuildStatusLiveNilMapIsStateOnly(t *testing.T) {
	s := &state.State{PublicKey: "SELF", Peers: []state.Peer{{Label: "gw", PublicKey: "GW", NodeIP: "100.64.0.2"}}}
	for _, p := range BuildStatusLive(s, nil, time.Unix(1_700_000_000, 0).UTC()).Peers {
		if p.LiveStatus != "" || p.LastHandshake != nil {
			t.Errorf("nil live-map: got live_status=%q handshake=%v, want state-only", p.LiveStatus, p.LastHandshake)
		}
	}
}

// Нулевой handshake (peer в device, но ни разу не хендшейкнулся) → offline, без времени.
func TestBuildStatusLiveZeroHandshakeIsOffline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := &state.State{PublicKey: "SELF", Peers: []state.Peer{{Label: "gw", PublicKey: "GW", NodeIP: "100.64.0.2"}}}
	live := map[string]PeerLive{"GW": {}} // zero handshake

	p := BuildStatusLive(s, live, now).Peers[0]
	if p.LiveStatus != "offline" {
		t.Errorf("zero handshake live_status = %q, want offline", p.LiveStatus)
	}
	if p.LastHandshake != nil {
		t.Errorf("zero handshake LastHandshake = %v, want nil", p.LastHandshake)
	}
}

// Peer, которого нет в live-map, остаётся неизвестным (не online и не offline).
func TestBuildStatusLiveUnknownPeerStaysEmpty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := &state.State{PublicKey: "SELF", Peers: []state.Peer{{Label: "gw", PublicKey: "GW", NodeIP: "100.64.0.2"}}}

	if got := BuildStatusLive(s, map[string]PeerLive{}, now).Peers[0].LiveStatus; got != "" {
		t.Errorf("unknown peer live_status = %q, want пусто", got)
	}
}

// Граница порога: ровно LivenessThreshold назад — ещё online (граница включена).
func TestClassifyLivenessThresholdBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	if got := classifyLiveness(now.Add(-LivenessThreshold), now); got != "online" {
		t.Errorf("handshake ровно threshold назад = %q, want online", got)
	}
	if got := classifyLiveness(now.Add(-LivenessThreshold-time.Second), now); got != "offline" {
		t.Errorf("handshake threshold+1с назад = %q, want offline", got)
	}
}
