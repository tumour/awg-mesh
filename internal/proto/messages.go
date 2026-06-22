// Package proto — wire-формат control-plane: типы сообщений, length-prefixed
// framing (WriteFrame/ReadFrame) и конверсии wire↔домен (PeerInfo ↔ state.Peer).
//
// Сообщения bootstrap'а (HelloRequest/HelloResponse) шифруются через CipherState
// после Noise IKpsk2; кадр — 2-байтовая длина (big-endian) + ciphertext. PeerInfo
// переиспользуется и gossip'ом (HTTP /v1/peers, открытый текст внутри туннеля).
//
// Зависимость на internal/state однонаправленна (wire-слой знает домен, не
// наоборот) — направление зависимостей внутрь сохранено.
//
// ProtoVersion инкрементируется при breaking-changes wire-формата.
package proto

import (
	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

// ProtoVersion — версия wire-протокола. При несовпадении handshake отклоняется.
const ProtoVersion = 1

// HelloRequest — первое сообщение клиента после Noise-handshake'а.
type HelloRequest struct {
	Version   int    `json:"version"`    // ProtoVersion
	Label     string `json:"label"`      // human-readable метка ноды
	PublicKey string `json:"public_key"` // base64 WG-pubkey клиента (для записи в peer-list)
	// Endpoint — публичный host:port WG-порта ноды (из join --public-endpoint).
	// Раздаётся остальным нодам через gossip — они смогут инициировать прямой
	// handshake. Пусто = нода за NAT, initiator-only. Поле опционально:
	// старые клиенты его не шлют, ProtoVersion не меняется.
	Endpoint string `json:"endpoint,omitempty"`
}

// HelloResponse — ответ seed'а с допуском и параметрами mesh-сети.
type HelloResponse struct {
	Version     int              `json:"version"`
	Status      string           `json:"status"`            // "ok" или "error"
	Error       string           `json:"error,omitempty"`   // текст ошибки если Status=error
	YourIP      string           `json:"your_ip,omitempty"` // выделенный IP в mesh
	NetworkCIDR string           `json:"network_cidr,omitempty"`
	AwgParams   awgparams.Params `json:"awg_params,omitempty"`
	WGPort      int              `json:"wg_port,omitempty"` // WG-port на котором seed слушает
	Peers       []PeerInfo       `json:"peers,omitempty"`   // текущий peer-list seed'а
}

// PeerInfo — wire-представление одного peer'а. ЕДИНЫЙ DTO для обоих control-plane
// каналов: bootstrap (внутри Noise-шифрованного HelloResponse) и gossip
// (HTTP-ответ /v1/peers). json-теги фиксируют формат gossip-API.
//
// LastSeen на wire НЕ передаётся — это локальная метка (refresh при merge),
// конверсии ниже её опускают/занулят.
type PeerInfo struct {
	Label     string `json:"label"`
	PublicKey string `json:"public_key"`
	Endpoint  string `json:"endpoint,omitempty"` // host:port, пусто если клиент без public-endpoint
	NodeIP    string `json:"node_ip"`
	IsSeed    bool   `json:"is_seed"`
}

// PeerInfoFromState конвертирует доменный state.Peer в wire-DTO.
func PeerInfoFromState(p state.Peer) PeerInfo {
	return PeerInfo{
		Label:     p.Label,
		PublicKey: p.PublicKey,
		Endpoint:  p.Endpoint,
		NodeIP:    p.NodeIP,
		IsSeed:    p.IsSeed,
	}
}

// ToState — обратная конверсия wire-DTO → доменный state.Peer.
func (pi PeerInfo) ToState() state.Peer {
	return state.Peer{
		Label:     pi.Label,
		PublicKey: pi.PublicKey,
		Endpoint:  pi.Endpoint,
		NodeIP:    pi.NodeIP,
		IsSeed:    pi.IsSeed,
	}
}

// PeerInfosFromState — батч-конверсия peer-list'а в wire (HelloResponse / gossip).
func PeerInfosFromState(ps []state.Peer) []PeerInfo {
	out := make([]PeerInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, PeerInfoFromState(p))
	}
	return out
}

// PeerInfosToState — батч-конверсия wire-списка в доменный (для merge/save).
func PeerInfosToState(pis []PeerInfo) []state.Peer {
	out := make([]state.Peer, 0, len(pis))
	for _, pi := range pis {
		out = append(out, pi.ToState())
	}
	return out
}
