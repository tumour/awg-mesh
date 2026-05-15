// Package proto — wire-сообщения bootstrap-протокола (после Noise-handshake'а).
//
// Все сообщения шифруются через CipherState, полученный после Noise IKpsk2.
// Формат: 2-байтовая длина (big-endian) + ciphertext.
//
// ProtoVersion инкрементируется при breaking-changes wire-формата.
package proto

import "github.com/tumour/awg-mesh/internal/awgparams"

// ProtoVersion — версия wire-протокола. При несовпадении handshake отклоняется.
const ProtoVersion = 1

// HelloRequest — первое сообщение клиента после Noise-handshake'а.
type HelloRequest struct {
	Version   int    `json:"version"`     // ProtoVersion
	Label     string `json:"label"`       // human-readable метка ноды
	PublicKey string `json:"public_key"`  // base64 WG-pubkey клиента (для записи в peer-list)
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

// PeerInfo — описание одного peer'а, передаётся в HelloResponse.
type PeerInfo struct {
	Label     string `json:"label"`
	PublicKey string `json:"public_key"`
	Endpoint  string `json:"endpoint,omitempty"` // host:port, пусто если клиент без public-endpoint
	NodeIP    string `json:"node_ip"`
	IsSeed    bool   `json:"is_seed"`
}
