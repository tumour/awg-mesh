package mesh

import "github.com/tumour/awg-mesh/internal/state"

// PickHealthPeer выбирает соседа для health-check'а после self-upgrade:
// предпочитаем seed (стабилен, с объявленным endpoint'ом), иначе любой другой
// пир с выделенным IP. "" — мы одни в mesh. Чистый доменный выбор на state.State,
// без сети (саму достижимость проверяет internal/health).
func PickHealthPeer(s *state.State) string {
	var fallback string
	for _, p := range s.Peers {
		if p.NodeIP == "" || p.NodeIP == s.NodeIP {
			continue
		}
		if p.IsSeed {
			return p.NodeIP
		}
		if fallback == "" {
			fallback = p.NodeIP
		}
	}
	return fallback
}
