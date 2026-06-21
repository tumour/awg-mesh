package mesh

import (
	"fmt"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// JoinRequest — данные ноды, регистрирующейся на seed'е (из bootstrap-протокола).
type JoinRequest struct {
	Label     string
	PublicKey string
	Endpoint  string // может быть пустым (нода за NAT)
}

// Registration — результат RegisterPeer.
type Registration struct {
	AssignedIP string // выделенный (или уже существующий) mesh-IP
	Rejoined   bool   // peer уже был зарегистрирован (идемпотентный re-join)
	Changed    bool   // изменился ли state (false → caller не пишет файл)
}

// RegisterPeer регистрирует/обновляет ноду в state (мутирует s.Peers in-place).
// Чистая доменная логика bootstrap-регистрации — без сети и Store; caller
// оборачивает её в store.Update для атомарности (и пишет state только если Changed).
//
// Идемпотентность: повторный join с тем же pubkey → существующий IP. Endpoint
// обновляется только если прислан непустой и новый — пустой endpoint при resume
// не затирает объявленный ранее. Новая нода получает следующий свободный IP,
// IsSeed=false (seed только тот, кто сделал init).
func RegisterPeer(s *state.State, req JoinRequest) (Registration, error) {
	now := time.Now().UTC()

	for i, p := range s.Peers {
		if p.PublicKey != req.PublicKey {
			continue
		}
		reg := Registration{AssignedIP: p.NodeIP, Rejoined: true}
		if req.Endpoint != "" && req.Endpoint != p.Endpoint {
			s.Peers[i].Endpoint = req.Endpoint
			s.Peers[i].LastSeen = now
			reg.Changed = true
		}
		return reg, nil
	}

	ip, err := AllocateNextIP(s)
	if err != nil {
		return Registration{}, fmt.Errorf("IP allocation: %w", err)
	}
	s.Peers = append(s.Peers, state.Peer{
		Label:     req.Label,
		PublicKey: req.PublicKey,
		Endpoint:  req.Endpoint,
		NodeIP:    ip,
		IsSeed:    false,
		LastSeen:  now,
	})
	return Registration{AssignedIP: ip, Changed: true}, nil
}
