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

### 1. Установка через .deb-пакет

Качаем готовый `.deb` из [GitHub Releases](https://github.com/tumour/awg-mesh/releases) и ставим. Архитектура — `amd64` или `arm64`:

```bash
# скачать (подставь нужную версию):
wget https://github.com/tumour/awg-mesh/releases/latest/download/meshd_0.2.0_amd64.deb

# установить — postinst автоматом enable'ит systemd-unit:
sudo dpkg -i meshd_0.2.0_amd64.deb
```

После `dpkg -i`:
- бинарник в `/usr/bin/meshd`
- systemd-юнит в `/lib/systemd/system/meshd.service` (enabled, но не started — daemon ждёт state.json)
- директория `/etc/meshd/` (chmod 700, для state и ключей)

Для OpenWrt-роутеров есть `.apk`-пакет — см. секцию
[Установка на OpenWrt](#установка-на-openwrt-2512).

### 2. Bootstrap первой ноды (seed)

```bash
# открыть порты в UFW (только на seed-ноде):
sudo ufw allow 51820/tcp comment 'awg-mesh bootstrap'
sudo ufw allow 51820/udp comment 'awg-mesh data'

# initialize mesh — этот узел становится seed:
sudo meshd init --label seed-node --public-endpoint <SEED_PUBLIC_IP>:51820
# auto-start выполняется автоматом

# при активном UFW разреши входящий gossip с mesh-интерфейса — иначе соседи
# не смогут забирать peer-list (meshd сам firewall НЕ трогает, только hint):
sudo ufw allow in on awg0 to any port 9100 proto tcp comment 'awg-mesh gossip'
# ...или то же одним флагом: meshd init ... --ufw gossip
```

Из вывода скопируй **--token** — пригодится для следующих нод.

### 3. Onboarding следующих нод

На любой Linux-VPS (любая страна, любой провайдер, нужен `dpkg -i meshd_*.deb` сначала).

**Нода с публичным IP (рекомендуется для VPS)** — объяви endpoint, чтобы остальные
могли строить к ней прямые туннели:

```bash
sudo ufw allow 51820/udp comment 'awg-mesh data'
sudo meshd join --label new-node --token <ТОКЕН-ИЗ-INIT> \
  --public-endpoint <PUBLIC_IP>:51820 --ufw gossip
# auto-start включается сам; --ufw gossip (опционально) разрешает в UFW
# входящий peer-list-sync с mesh-интерфейса (9100/tcp on awg0)
```

**Нода за NAT (домашняя машина, ноутбук)** — без `--public-endpoint`:

```bash
sudo meshd join --label laptop --token <ТОКЕН-ИЗ-INIT>
```

Публичные порты на NAT-ноде открывать **не надо** — она initiator, исходящий
outbound и так открыт. К ней нельзя инициировать туннель, но сама она достучится
до любой ноды с endpoint'ом. Если UFW активен — gossip-порт на awg0 нужен
и здесь (`--ufw gossip` или команда из шага 2): соседи опрашивают её peer-list
через уже установленный туннель.

Через минуту-две новая нода появится в `meshd status` на всех остальных нодах (через gossip).

### 4. Проверка

```bash
meshd status                 # peer-list, mesh-IP
ip addr show awg0            # интерфейс с mesh-IP
ping 100.64.0.1              # ping к seed через wg-туннель
journalctl -u meshd -f       # логи демона
systemctl status meshd       # статус сервиса
```

### Сборка из исходников (опционально)

Если хочется собрать локально:

```bash
git clone https://github.com/tumour/awg-mesh && cd awg-mesh
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest

make build-all       # бинарники в bin/ под amd64/arm64/armv7/mipsle/mips
make package-all     # .deb-пакеты в dist/ под amd64/arm64
make package-openwrt # .apk-пакеты в dist/ под все арки
```

## Установка на OpenWrt (25.12+)

OpenWrt с релиза 25.x использует apk (apk-tools v3) вместо opkg — пакет
собирается в формате `.apk`. Проверено на OpenWrt 25.12.4 (rootfs-контейнер):
установка, procd-сервис, поднятие awg0 с mesh-IP.

Требования: **≥16 MB flash** (бинарник ~8 MB распакованный) и желательно
**≥128 MB RAM** — data plane это userspace-процесс (Go runtime + amneziawg-go).

### Выбор пакета под арку роутера

Пакет помечен `arch = noarch` (ставится на любую арку), правильный CPU-вариант
выбираешь по имени файла:

| Файл | OpenWrt target | Примеры устройств |
|---|---|---|
| `…openwrt-arm64.apk` | aarch64_* (mediatek/filogic, qualcommax, bcm27xx) | современные Wi-Fi 6/7 роутеры, RPi 4/5 |
| `…openwrt-armv7.apk` | arm_cortex-a7/a9/a15 (ipq40xx, mvebu) | старые ARM-роутеры |
| `…openwrt-mipsle.apk` | mipsel_24kc (ramips: mt7621, mt76x8) | Xiaomi, Keenetic и пр. |
| `…openwrt-mips.apk` | mips_24kc, big-endian (ath79, lantiq) | TP-Link на Atheros |
| `…openwrt-amd64.apk` | x86/64 | мини-ПК, VM |

Узнать свою арку: `grep ARCH /etc/openwrt_release`.

### Установка

```bash
# на роутере (подставь версию и арку):
wget https://github.com/tumour/awg-mesh/releases/latest/download/meshd_0.2.0_openwrt-mipsle.apk

apk update   # нужен для резолва зависимости kmod-tun
apk add --allow-untrusted meshd_0.2.0_openwrt-mipsle.apk
```

`--allow-untrusted` обязателен — пакет не подписан ключом OpenWrt-фида.
После установки:
- бинарник в `/usr/bin/meshd`, procd-скрипт в `/etc/init.d/meshd` (enabled)
- `kmod-tun` подтянется автоматически из стандартного фида
- дальше обычный `meshd init` / `meshd join` — daemon стартует сам через procd

### Firewall (fw4)

meshd firewall не трогает и на OpenWrt (флаг `--ufw` работает только с UFW,
на роутере он no-op) — настраиваем fw4 руками. Минимум для работы mesh:
зона для awg0 + входящий gossip (9100/tcp), чтобы соседи забирали peer-list:

```bash
uci add firewall zone
uci set firewall.@zone[-1].name='mesh'
uci add_list firewall.@zone[-1].device='awg0'
uci set firewall.@zone[-1].input='REJECT'
uci set firewall.@zone[-1].output='ACCEPT'
uci set firewall.@zone[-1].forward='REJECT'

uci add firewall rule
uci set firewall.@rule[-1].name='awg-mesh-gossip'
uci set firewall.@rule[-1].src='mesh'
uci set firewall.@rule[-1].proto='tcp'
uci set firewall.@rule[-1].dest_port='9100'
uci set firewall.@rule[-1].target='ACCEPT'

uci commit firewall
service firewall restart
```

Если хочешь полный доступ к роутеру из mesh (Tailscale-style trust-by-tunneling,
аналог `--ufw all`) — поставь у зоны `input='ACCEPT'`, gossip-правило тогда
не нужно. Помни: сервисы роутера (LuCI, dropbear) станут видны всем нодам mesh.

Если роутер — seed или объявляет `--public-endpoint` (есть белый IP), открой
bootstrap/data-порты со стороны WAN:

```bash
uci add firewall rule
uci set firewall.@rule[-1].name='awg-mesh'
uci set firewall.@rule[-1].src='wan'
uci set firewall.@rule[-1].proto='tcp udp'
uci set firewall.@rule[-1].dest_port='51820'
uci set firewall.@rule[-1].target='ACCEPT'
uci commit firewall
service firewall restart
```

Роутер за NAT (typical home) — firewall трогать не надо, он initiator-only,
как и NAT-нода на Debian.

### Эксплуатация

```bash
/etc/init.d/meshd start|stop|restart|enable
logread -e meshd          # логи (procd шлёт stdout в syslog)
meshd status              # peer-list, mesh-IP
```

Удаление: `apk del meshd` — pre-deinstall останавливает сервис и сносит awg0;
`/etc/meshd/state.json` остаётся (у apk нет аналога purge), полный wipe —
`rm -rf /etc/meshd` руками.

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
- **Нода с public-endpoint** (`join --public-endpoint`): слушает фиксированный
  UDP-порт, к ней можно инициировать прямой туннель. Endpoint расходится по mesh'у
  через gossip.
- **Нода за NAT** (join без endpoint'а): ephemeral UDP-порт, initiator-only —
  сама строит туннели к нодам с endpoint'ом, к ней инициировать нельзя.

### Связность

Прямой туннель между двумя нодами возможен, если **хотя бы одна** объявила
public-endpoint:

| | B с endpoint | B за NAT |
|---|---|---|
| **A с endpoint** | напрямую (любая инициирует) | напрямую (инициирует B) |
| **A за NAT** | напрямую (инициирует A) | ✗ нет связности (relay/STUN — v2) |

Повторный `meshd join` с новым `--public-endpoint` обновляет endpoint ноды на
seed'е (mesh-IP и ключи сохраняются), дальше gossip разносит его остальным.

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
- **UFW**: meshd и пакет firewall сами **не трогают** — никаких молчаливых
  правил. Минимум для работы mesh — входящий gossip (`9100/tcp on awg0`):
  открывается руками или явным opt-in `meshd init/join --ufw gossip`.
  `--ufw all` разрешает весь трафик с mesh-интерфейса (Tailscale-pattern:
  default-allow внутри tunnel'я) — осознанный выбор, если нужен доступ
  к сервисам ноды по mesh-IP; помни, что тогда сервисы на 0.0.0.0 видны
  всем участникам mesh. Снаружи (eth0) ничего не открывается в любом случае.
  Если UFW активен, а gossip закрыт — `init/join` и `meshd run` предупредят.
- **State at rest**: `state.json` `chmod 600`, owned by root. Не пиши никуда
  кроме `/etc/meshd/`.
- **Cluster-secret leak**: эквивалентно root-доступу к mesh. Передавать токен
  через scp/ssh, не через мессенджеры.

### Что защищено снаружи (три слоя)

1. **Bind на mesh-IP**: gossip-server слушает `100.64.0.X:9100`, не `0.0.0.0`.
   На eth0 порт **физически не слушает**.
2. **UFW**: даже если бы listener был на 0.0.0.0, UFW дропает входящий 9100
   с публичного интерфейса.
3. **RFC 6598 CGNAT-подсеть** `100.64.0.0/24` — не маршрутизируется в публичный
   интернет. Адресовать mesh-IP снаружи физически невозможно.

### Что **не** защищено

Compromise одной из mesh-нод → атакующий через mesh может стучаться к другим
по любым портам. Защита — app-layer auth (SSH-key, Bearer-token, UUID).
Mitigation — revoke pubkey (в v2 — tombstone-распространение через gossip).

## Команды

| Команда | Описание |
|---|---|
| `meshd init` | Инициализирует новую mesh-сеть (первая нода = seed) |
| `meshd join --token X` | Подключение к существующему mesh'у через токен |
| `meshd run` | Главный foreground-daemon (запускает systemd) |
| `meshd serve` | Только bootstrap-listener, без wg-device (для отладки) |
| `meshd status` | Показывает peer-list и mesh-IP |
| `meshd version` | Версия бинарника |

## Размеры артефактов

| Артефакт | amd64 | arm64 | armv7 | mipsle | mips |
|---|---|---|---|---|---|
| Статический бинарник (Go, без CGO) | 7.9 MB | 7.3 MB | 7.6 MB | 8.7 MB | 8.7 MB |
| .deb-пакет (compressed) | 3.3 MB | 3.0 MB | — | — | — |
| .apk-пакет OpenWrt (compressed) | 3.4 MB | 3.1 MB | 3.3 MB | 3.2 MB | 3.2 MB |

`.deb` и `.apk` собираются через [nfpm](https://nfpm.goreleaser.com)
(`make package-all` / `make package-openwrt`). В .deb: бинарник, systemd-юнит,
postinst/prerm/postrm hook-скрипты, LICENSE + README. В .apk: бинарник,
procd-init-скрипт, post-install/pre-deinstall хуки, LICENSE.

## Uninstall

```bash
# Снять systemd-юнит и убрать пакет, оставить state.json:
sudo apt remove meshd

# Полный wipe — включая /etc/meshd с ключами:
sudo apt purge meshd
```

`prerm` сам делает `systemctl stop` + `disable`, `ip link delete awg0` и снимает
UFW-правила mesh-интерфейса, если они добавлялись (только при remove, не при
upgrade). `postrm purge` удаляет state-директорию.

## Лицензия

MIT (TBD). В составе используются:
- [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT) — data plane
- [flynn/noise](https://github.com/flynn/noise) (BSD) — Noise IKpsk2
