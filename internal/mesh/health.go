package mesh

import (
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// HealthView — представление здоровья ноды для фронтендов (health-эндпоинт API,
// будущий дашборд). Единый источник, как StatusView: логика сбора — только тут,
// фронтенды лишь рендерят. Секретов (ключи, cluster-secret) НЕТ.
//
// v1: все поля выводятся ЧИСТО из state, без сетевых probe'ов. Live-достижимость
// пиров (online/offline из wg-handshake) — отдельный инкремент, см. feature-backlog;
// json-теги стабильны как контракт web-API.
type HealthView struct {
	NodeIP         string    `json:"node_ip"`
	IsSeed         bool      `json:"is_seed"`
	ParamsVersion  uint64    `json:"params_version"`
	PeersTotal     int       `json:"peers_total"`      // всего известных нод, включая себя
	PendingFlagDay bool      `json:"pending_flag_day"` // запланирован синхронный flip params
	UpdatedAt      time.Time `json:"updated_at"`
}

// BuildHealth — чистая функция state → HealthView. Не лезет в сеть/ОС/CLI.
func BuildHealth(s *state.State) HealthView {
	return HealthView{
		NodeIP:         s.NodeIP,
		IsSeed:         s.IsSeed,
		ParamsVersion:  s.ParamsVersion,
		PeersTotal:     len(s.Peers),
		PendingFlagDay: s.Pending != nil,
		UpdatedAt:      s.UpdatedAt,
	}
}
