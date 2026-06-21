package mesh

import "github.com/tumour/awg-mesh/internal/state"

// StatusView — представление состояния ноды для ВСЕХ фронтендов (CLI-текст,
// `status --json`, web-дашборд, LuCI). Единый источник: каждый фронтенд лишь
// по-своему рендерит этот тип, логика сбора — только здесь (паттерн «mapper»).
//
// json-теги стабильны: это контракт для --json и web-API. Секретов (приватный
// ключ, cluster-secret) тут НЕТ — только публично-безопасные поля.
type StatusView struct {
	Label       string     `json:"label"`
	Role        string     `json:"role"` // "seed" | "regular"
	NetworkCIDR string     `json:"network_cidr"`
	NodeIP      string     `json:"node_ip"`
	PublicKey   string     `json:"public_key"`
	ListenPort  int        `json:"listen_port"`
	IsSeed      bool       `json:"is_seed"`
	Peers       []PeerView `json:"peers"`
}

// PeerView — публично-безопасное представление одного пира.
type PeerView struct {
	Label     string `json:"label"`
	NodeIP    string `json:"node_ip"`
	Endpoint  string `json:"endpoint"`
	PublicKey string `json:"public_key"`
	IsSeed    bool   `json:"is_seed"`
	IsSelf    bool   `json:"is_self"`
	// Online / LastHandshake — позже, из live wg-handshake (см. feature-backlog).
}

// BuildStatus — чистая функция state → StatusView. Не лезет в сеть/ОС/CLI.
func BuildStatus(s *state.State) StatusView {
	role := "regular"
	if s.IsSeed {
		role = "seed"
	}
	peers := make([]PeerView, 0, len(s.Peers))
	for _, p := range s.Peers {
		peers = append(peers, PeerView{
			Label:     p.Label,
			NodeIP:    p.NodeIP,
			Endpoint:  p.Endpoint,
			PublicKey: p.PublicKey,
			IsSeed:    p.IsSeed,
			IsSelf:    p.PublicKey == s.PublicKey,
		})
	}
	return StatusView{
		Label:       s.NodeLabel,
		Role:        role,
		NetworkCIDR: s.NetworkCIDR,
		NodeIP:      s.NodeIP,
		PublicKey:   s.PublicKey,
		ListenPort:  s.ListenPort,
		IsSeed:      s.IsSeed,
		Peers:       peers,
	}
}

// SelfEndpoint — public-endpoint нашей ноды из её собственной peer-записи в
// state (его кладёт seed при регистрации, а для seed'а — meshd init). Пусто =
// нода за NAT / endpoint не объявлен (к ней нельзя инициировать туннель).
func SelfEndpoint(s *state.State) string {
	for _, p := range s.Peers {
		if p.PublicKey == s.PublicKey {
			return p.Endpoint
		}
	}
	return ""
}
