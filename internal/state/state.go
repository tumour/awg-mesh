// Package state — persistent state ноды mesh-сети.
//
// Хранится в DefaultPath (платформенный путь, см. path_unix.go/path_windows.go),
// chmod 600. Содержит:
//   - наш node-keypair (приватный + публичный)
//   - cluster-secret (32 байта base32) — общий для всех нод в этой сети
//   - AmneziaWG-параметры (Jc/Jmin/Jmax/S1/S2/H1-H4) — общие для всех нод
//   - network CIDR + наш IP в mesh-сети
//   - список известных peer'ов (через gossip)
//   - роль ноды (seed/regular)
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/fsutil"
)

// DefaultPath — стандартное место хранения state.json (chmod 600). Зависит от
// ОС, поэтому определён в path_unix.go / path_windows.go (build-tags).

// CurrentVersion — текущая версия схемы state.json. Save проставляет её.
// Load дочитывает ЛЮБУЮ version <= CurrentVersion (старую — с дефолтами для
// новых полей), реджектит только version > CurrentVersion (state новее бинаря —
// бинарь не знает его полей, читать опасно). Поднимать при изменениях схемы.
//
// v2 (AWG-2.0): H1-H4 стали диапазонами (HeaderRange) вместо uint32, добавлены
// S3/S4 и per-node LocalObf (I1-I5). v1 дочитывается БЕСШУМНО:
// HeaderRange.UnmarshalJSON ловит старое число H → {H,H}, s3/s4=0, local_obf
// пуст → на проводе идентично v1 (self-upgrade не рвёт связь). На диск новая
// схема ляжет при первом Save.
const CurrentVersion = 2

// State — корневая структура persistent state.
type State struct {
	Version   int    `json:"version"`    // схема файла (== CurrentVersion); ставит Save
	NodeLabel string `json:"node_label"` // человекочитаемая метка ('beget', 'hetzner')

	// Cluster identity
	ClusterSecret string             `json:"cluster_secret"` // base32, 32 байта
	AwgParams     awgparams.Params   `json:"awg_params"`     // СЕТЕВЫЕ (flag-day), раздаются
	LocalObf      awgparams.LocalObf `json:"local_obf"`      // ПО-НОДНЫЕ I1-I5, НЕ раздаются
	NetworkCIDR   string             `json:"network_cidr"`   // например "100.64.0.0/24"

	// Наша node identity
	PrivateKey string `json:"private_key"` // base64 WG-encoded (32 байта)
	PublicKey  string `json:"public_key"`  // base64 WG-encoded
	NodeIP     string `json:"node_ip"`     // наш IP в mesh, например "100.64.0.1"
	ListenPort int    `json:"listen_port"` // НАШ WG listen-port (0 = ephemeral, нода-initiator за NAT)

	// Роль
	IsSeed bool `json:"is_seed"` // true — принимаем bootstrap-join'ы и выделяем IP новым нодам

	// Известные peer'ы (через gossip / bootstrap-response)
	Peers []Peer `json:"peers"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Peer — другая нода в mesh-сети, известная нам.
type Peer struct {
	Label     string    `json:"label"`
	PublicKey string    `json:"public_key"` // base64 WG-encoded
	Endpoint  string    `json:"endpoint"`   // "ip:port", может быть пустым для клиентов за NAT
	NodeIP    string    `json:"node_ip"`    // mesh-IP
	IsSeed    bool      `json:"is_seed"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// Load читает state.json. Возвращает ErrNotInitialized если файла нет —
// caller должен решить, init это или ошибка.
func Load(path string) (*State, error) {
	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Version > CurrentVersion {
		return nil, fmt.Errorf("state %s: schema version %d newer than this meshd supports (%d) — upgrade meshd",
			path, s.Version, CurrentVersion)
	}
	// version < CurrentVersion дочитывается бесшумно: HeaderRange.UnmarshalJSON
	// уже сконвертил старые H-числа, недостающие поля (s3/s4/local_obf) — нулевые.
	return &s, nil
}

// Save durable и атомарно пишет state.json (chmod 600). Потеря state.json =
// потеря приватника и cluster identity (на seed'е ещё и peer-list), поэтому
// запись идёт через fsutil.WriteFile (tmp+fsync+rename+fsync-dir) — durable
// против потери питания на роутере, а не только атомарная против чтения.
func (s *State) Save(path string) error {
	if path == "" {
		path = DefaultPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	s.Version = CurrentVersion
	s.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	return fsutil.WriteFile(path, data, 0600)
}

// ErrNotInitialized — state.json отсутствует, нода ещё не сделала init/join.
var ErrNotInitialized = fmt.Errorf("meshd not initialized (no state file)")
