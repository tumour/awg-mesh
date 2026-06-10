# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версионирование — [SemVer](https://semver.org/lang/ru/) (0.x — API не стабилен).

## [0.2.0] — 2026-06-11

### Added
- **Пакет для OpenWrt 25.12+** (`.apk`, apk-tools v3): procd-init-скрипт,
  post-install/pre-deinstall хуки, зависимость kmod-tun. Пять арок:
  amd64 / arm64 / armv7 / mipsle / mips (`make package-openwrt`).
  Проверено на OpenWrt 25.12.4 и Xiaomi AX3200 (aarch64_cortex-a53).
- Сборки под mips/mipsle (softfloat, ath79/ramips-роутеры) в `make build-all`
  и release-артефактах.
- Авто-старт демона через procd после `init`/`join` на OpenWrt
  (аналог systemd-пути на Debian).
- `join --public-endpoint`: не-seed ноды объявляют свой публичный endpoint —
  mesh строит прямые p2p-туннели между любыми нодами, где хотя бы одна
  с endpoint'ом. Повторный `join` обновляет endpoint (ключи и mesh-IP
  сохраняются), gossip разносит его по mesh'у.
- `state.Store` — потокобезопасный доступ к state.json + тесты.
- Флаг `init/join --ufw gossip|all` — opt-in настройка UFW:
  `gossip` открывает только peer-list-sync (9100/tcp on awg0),
  `all` — весь трафик с mesh-интерфейса (trust-by-tunneling).
- README: секция установки на OpenWrt (выбор арки, fw4-настройка через uci,
  минимальные firewall-правила).

### Changed
- **UFW больше не настраивается молча** (breaking для тех, кто полагался
  на авто-правило): postinst не добавляет правил, демон не делает
  reconciliation при старте. Без правила для gossip `init`/`join` печатают
  hint с командами, `meshd run` — предупреждение в лог. Открытие портов —
  явное действие пользователя или флаг `--ufw`.

### Fixed
- prerm: UFW-правила снимаются только при настоящем `remove` — при `apt
  upgrade` правило больше не терялось бы безвозвратно.

## [0.1.3] — 2026-05-22

### Fixed
- Правки по результатам code-review.

## [0.1.2] — 2026-05-15

### Added
- postinst/prerm: автоматическое UFW-правило `allow in on awg0`
  (в 0.2.0 заменено на opt-in).

## [0.1.1] — 2026-05-15

### Fixed
- systemd-unit: `ExecStart=/usr/bin/meshd` (Debian-конвенция).

## [0.1.0] — 2026-05-15

Первый рабочий MVP.

### Added
- `meshd init/join/run/serve/status`: bootstrap mesh-сети одной командой.
- Data plane: AmneziaWG (userspace, amneziawg-go) — DPI-resistant туннели,
  TUN-интерфейс awg0, CGNAT-подсеть 100.64.0.0/24.
- Bootstrap-протокол: Noise IKpsk2 (X25519 + ChaCha20-Poly1305 + BLAKE2s),
  cluster-secret как HKDF-derived PSK, join-token одной строкой.
- Gossip-протокол: периодический pull peer-list'а через wg-туннель,
  сервер только на mesh-IP.
- Packaging: `.deb` через nfpm (amd64/arm64), systemd-unit, GHA CI/Release.

[0.2.0]: https://github.com/tumour/awg-mesh/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/tumour/awg-mesh/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/tumour/awg-mesh/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/tumour/awg-mesh/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/tumour/awg-mesh/releases/tag/v0.1.0
