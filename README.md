# awg-mesh

Tailscale-like mesh-сеть на AmneziaWG. Один Go-бинарник, zero-config onboarding,
DPI-resistant против ТСПУ. Self-hosted, без внешних координаторов.

## Зачем это нужно

Стандартный WireGuard детектируется ТСПУ и блокируется в РФ. Готовые
mesh-решения (Tailscale, Headscale, Netbird) используют обычный WG как
data plane → подвержены тем же блокировкам. Этот проект заменяет data plane
на [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go) — форк WG с
обфускацией handshake'а через junk-пакеты, randomized headers и padding.
Control plane (peer discovery, авторизация) — самописный gossip-протокол,
без зависимости от внешних SaaS-сервисов.

## Что внутри

- **Data plane**: AmneziaWG (userspace, через `amneziawg-go` library). Создаёт
  TUN-интерфейс через `/dev/net/tun`, обрабатывает crypto/encapsulation в
  процессе meshd. Никаких kernel-модулей, никаких DKMS. Совместим с любым
  Linux 4.x+ и кросс-компилируется под ARM/MIPS.
- **Bootstrap**: Noise IKpsk2 handshake (X25519 + ChaCha20-Poly1305 +
  BLAKE2s) — тот же cipher-suite что в WG и Tailscale control protocol.
  cluster-secret служит pre-shared key через HKDF-derived PSK.
- **Gossip**: каждая нода периодически (раз в минуту) опрашивает random
  peer'а через wg-туннель за обновлённым peer-list'ом. Новые ноды
  обнаруживаются всем mesh'ом за пару циклов gossip'а.

## Quick start

### 1. Сборка

```bash
git clone https://github.com/tumour/awg-mesh && cd awg-mesh
make build-all   # бинарники под amd64/arm64/armv7 в bin/
```

Или скачать готовые бинарники из [Releases](https://github.com/tumour/awg-mesh/releases).

### 2. Установка на первую (seed) ноду

```bash
scp bin/meshd-linux-amd64 root@seed-host:/tmp/meshd
ssh root@seed-host

# install:
bash <(curl -sSL https://raw.githubusercontent.com/tumour/awg-mesh/main/deploy/install.sh) --binary /tmp/meshd

# initialize mesh (этот узел становится seed):
meshd init \
    --label seed-node \
    --public-endpoint <SEED_PUBLIC_IP>:51820

# скопировать --token из вывода!

# открыть порты в UFW:
ufw allow 51820/tcp comment 'awg-mesh bootstrap'
ufw allow 51820/udp comment 'awg-mesh data'

# запустить:
systemctl enable --now meshd
systemctl status meshd
```

### 3. Onboarding каждой следующей ноды

На любой Linux-VPS:

```bash
scp bin/meshd-linux-amd64 root@new-host:/tmp/meshd
ssh root@new-host

bash <(curl -sSL https://raw.githubusercontent.com/tumour/awg-mesh/main/deploy/install.sh) --binary /tmp/meshd

# подключиться к существующему mesh'у:
meshd join --label new-node --token <ТОКЕН-ИЗ-ВЫВОДА-INIT>

systemctl enable --now meshd
```

Через минуту-две новая нода появится в `meshd status` на всех остальных нодах
(через gossip-обмен).

### 4. Проверка

```bash
meshd status                 # peer-list, mesh-IP
ip addr show awg0            # интерфейс с mesh-IP
ip link show awg0            # MTU 1420
ping 100.64.0.1              # ping к seed через wg-туннель
journalctl -u meshd -f       # логи демона
```

## Архитектура

```
              ┌──────────────────────────────────────────┐
              │ Node A (seed, 100.64.0.1)                 │
              │ ┌────────────────────────────────────┐    │
              │ │ meshd:                              │    │
              │ │  - bootstrap-listener  TCP :51820   │    │
              │ │  - AmneziaWG device    UDP :51820   │    │
              │ │  - gossip API          100.64.0.1:9100 │ │
              │ │  - gossip client       (periodic)   │    │
              │ │  - state /etc/meshd/state.json      │    │
              │ └────────────────────────────────────┘    │
              └──────┬───────────────────────────────────┘
                     │ AmneziaWG tunnel
              ┌──────┴───────────────────────────────────┐
              │ Node B (100.64.0.2)                       │
              │ ┌────────────────────────────────────┐    │
              │ │ meshd (no bootstrap-listener)       │    │
              │ │  - gossip API 100.64.0.2:9100       │    │
              │ │  - AmneziaWG device                 │    │
              │ │  - peers: [A, C, D, ...]            │    │
              │ └────────────────────────────────────┘    │
              └──────┬───────────────────────────────────┘
                     │ AmneziaWG tunnel (direct, peer-to-peer)
              ┌──────┴───────────────────────────────────┐
              │ Node C (100.64.0.3)                       │
              │  ...                                       │
              └──────────────────────────────────────────┘
```

### Состояния:

- **Seed-нода** (одна на mesh): listener на TCP :51820 (bootstrap) + UDP :51820 (WG).
  Принимает join'ы новых нод, выделяет им mesh-IP.
- **Обычная нода**: только UDP-endpoint для wg, peer-to-peer туннели с другими.
  Обновляет peer-list через gossip раз в минуту.

### Источник правды:

- `/etc/meshd/state.json` (chmod 600 на каждой ноде) — peer-list, AWG-params,
  cluster-secret, наш keypair. Распространяется через gossip (eventual
  consistency).
- Cluster-secret 32 байта — генерируется один раз при `meshd init`,
  раздаётся через join-token, никогда не меняется автоматически.

## Безопасность

- **Bootstrap**: Noise IKpsk2 поверх TCP, cluster-secret = HKDF-derived PSK.
  Без правильного secret'а handshake падает на ChaCha20-Poly1305 MAC verify.
  Forward secrecy через ephemeral X25519.
- **Gossip**: HTTP без extra-auth, **но gossip-сервер биндится только на mesh-IP**
  — с публичного интерфейса недоступен. Trust-by-tunneling: внутри wg-сети
  все peers уже прошли cluster-secret-проверку.
- **State at rest**: `state.json` `chmod 600`, owned by root. Не пиши никуда
  кроме `/etc/meshd/`.
- **Cluster-secret leak**: эквивалентно root-доступу к mesh. Передавать токен
  через scp/ssh, не через мессенджеры.

См. также сравнение с Tailscale security model в проектной документации.

## Команды

| Команда | Описание |
|---|---|
| `meshd init` | Инициализирует новую mesh-сеть (первая нода = seed) |
| `meshd join --token X` | Подключение к существующему mesh'у через токен |
| `meshd run` | Главный foreground-daemon (запускает systemd) |
| `meshd serve` | Только bootstrap-listener, без wg-device (для отладки) |
| `meshd status` | Показывает peer-list и mesh-IP |
| `meshd version` | Версия бинарника |

## Production-сборка

```bash
make build-prod         # для текущей архитектуры
# или
make build-all          # cross-compile под amd64/arm64/armv7
```

Размер production-бинарника (`-ldflags="-s -w" -trimpath`, без CGO):
- amd64: ~7.9 MB
- arm64: ~7.3 MB
- armv7: ~7.6 MB

Statically linked — никаких runtime-зависимостей на target-машине, никакого
Go не нужно.

## Лицензия

MIT (TBD). В составе используются:
- [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT) — data plane
- [flynn/noise](https://github.com/flynn/noise) (BSD) — Noise IKpsk2
