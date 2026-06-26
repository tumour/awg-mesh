# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версионирование — [SemVer](https://semver.org/lang/ru/) (0.x — API не стабилен).

## [0.6.1] — 2026-06-26

Удаление ноды из mesh — **revoke** (seed отзывает ноду) и **leave** (нода уходит
сама). Раньше забыть ноду было нельзя: gossip — union, вычеркнутый peer возвращался
следующим pull'ом; orphan-запись держала mesh-IP и блокировала flag-day-commit
(ack от мёртвого pubkey не приходит никогда → `set-params` не применился бы).

### Added
- **`meshd revoke <mesh-ip|pubkey>` (seed)** — отзывает ноду перманентным
  tombstone по её pubkey. Раздаётся по gossip; каждая нода снимает отозванного с
  wg-device **на лету (`RemovePeer`, без рестарта демона)** и перекрывает его
  реанонс. Подтверждение печатает точную идентичность (защита от дублей label).
- **`meshd leave`** — чистый выход: нода объявляет свой tombstone и пушит его
  endpoint-пирам (`POST /v1/tombstone`), т.к. за NAT её никто не пуллит
  (pull-gossip однонаправлен). Best-effort: если push не дошёл — ноду уберёт
  seed-side `revoke`.
- **Tombstone** в `state` (перманентный по pubkey, `omitempty`), раздаётся через
  gossip-ответ `/v1/peers` и bootstrap-`HelloResponse`. Доменное ядро —
  `internal/mesh/tombstone.go` (чистые `MergeTombstones`/`IsRevoked`/`ApplyTombstones`).
  Реанонс перекрыт в `MergePeers`, re-join отозванного отклоняется в `RegisterPeer`.

### Note
- Схема state НЕ бампится (tombstones — аддитивное `omitempty`-поле, version
  остаётся 2): откат self-upgrade на предыдущий бинарь стартует чисто — версии
  совпадают, поле просто игнорируется (watchdog-safety для нод без out-of-band).
- Tombstone принимается без подписи (trust-by-tunneling, как `Pending`/`set-params`) —
  рассчитано на mesh из доверенных нод. Подпись отзыва (неподделываемость инсайдером) —
  отдельный security-эпик перед раздачей на недоверенные узлы. Вернуть отозванную
  ноду можно только re-join'ом с НОВЫМ keypair (старый pubkey мёртв навсегда).

## [0.6.0] — 2026-06-26

Flag-day-смена сетевых AWG-params (S3/S4/H-диапазоны) на ЖИВОЙ сети — без
ре-init/ре-join и без потери нод. Раньше S/H фиксировались при init и сменить
их на работающей mesh было нельзя (только пересоздание сети).

### Added
- **`meshd set-params` (seed)** — анонсирует flag-day-смену сетевых params:
  генерит свежий 2.0-набор (непересекающиеся H-диапазоны + S3/S4), кладёт в
  `Pending` и раздаёт по gossip. Применяется не сразу — см. ack-then-commit.
- **Распределённый flip по модели «announce → ack → commit → apply»** (домен
  `internal/mesh/paramsync.go`, чистые функции):
  - announce: seed кладёт `Pending` (версия+params, БЕЗ `ApplyAt`), раздаёт по
    gossip по ещё живым туннелям; ноды принимают строго более новый (монотонно).
  - ack: при gossip-pull нода сообщает seed свою версию (`?node=&v=`).
  - commit: seed назначает `ApplyAt` ТОЛЬКО когда ВСЕ ноды подтвердили приём
    (`AllPeersAcked`) — пока хоть одна молчит, flip не стартует и сеть остаётся
    на старом наборе целиком (**гарантия: ни одну ноду не теряем**).
  - apply: все (включая seed) переключаются синхронно в `ApplyAt` через
    `device.ApplyParams` (reconfigure S/H на лету, без пересоздания awg0).
- `state`: `ParamsVersion` + `Pending` (params/version/apply_at), раздаются gossip.

### Note
- Согласованный откат после flip намеренно НЕ реализован: в hub-spoke его не
  раздать (туннели рвутся сменой params) — основной риск закрыт ack-then-commit
  (flip не происходит, пока не подтвердили все). Окно рассинхрона — разброс часов
  вокруг `ApplyAt` (минимизируется NTP), как и предписывает природа interface-level
  params в AWG (zero-downtime для S/H невозможен).

## [0.5.0] — 2026-06-25

AmneziaWG-2.0 обфускация — против stateful-DPI, который душит поток после
успешного handshake (handshake проходит, transport-данные дропаются — особенно
на трансграничных путях к дата-центровым сетям). AWG-1.0-обфускации
(junk + S1/S2 + фикс. H1-H4) хватает на старт, но НЕ на устойчивый поток.

### Added
- **AWG-2.0-параметры** в `awgparams.Params` (сетевые, flag-day, раздаются через
  bootstrap): `S3` (padding cookie-reply), `S4` (padding КАЖДОГО transport-пакета —
  ключ против flow-анализа сессии), `H1-H4` теперь **диапазоны** (`HeaderRange{Min,Max}`,
  на каждый пакет случайное значение → нет статической сигнатуры). `Generate()` выдаёт
  непересекающиеся H-диапазоны в safe-half `[5, 2^31-1]` + S3/S4 из реком. диапазонов.
- **`LocalObf` (I1-I5)** — per-node initiator-local CPS-пакеты (obf-chain spec
  amneziawg-go: `<b 0xHEX>`/`<t>`/`<r N>`), маскирующие старт потока под
  легитимный протокол. Ресивер их игнорит → совпадение не требуется, крутятся
  под конкретный ISP/путь БЕЗ flag-day. Хранятся в `state.local_obf`, НЕ раздаются.
  `init`/`join` автоматически проставляют **дефолтный I1 = QUIC-Initial-мимик**
  (`awgparams.DefaultLocalObf`) — нода из коробки поднимается с полной AWG-2.0-
  обфускацией, без ручной настройки. Валидность дефолта на реальном amneziawg-go
  покрыта тестом (`tuntest` + `device.IpcSet`, 100 прогонов).

### Fixed
- **PMTU-блэкхол на путях с path-MTU < 1500 (крупный TCP колом).** WG-дефолт TUN
  MTU 1420 рассчитан на path-MTU 1500; на «трудных» путях (РФ→загранка, PPPoE) с
  меньшим path-MTU и блэкхолом ICMP «fragmentation needed» полноразмерные пакеты
  молча дропались. AWG-2.0 усугублял: `s4` добавляет паддинг к каждому data-пакету.
  Замер на трудном пути (path-MTU ~1450): крупный TCP **0.7 Мбит/с** при
  MTU 1420 → **583 Мбит/с** при computed-MTU. Теперь `wg.TunMTU(s4)` авто-считает
  MTU awg0 = `1400 − 60 − s4` (пол 1280, RFC 8200), на каждой ноде из сетевого s4 —
  без ручной настройки. Мобильные пути (<1400) при необходимости занижать вручную.

### Changed
- **Схема state.json v1 → v2 с бесшумной миграцией.** `awg_params.h1-h4` сменили
  тип `uint32` → объект `{min,max}`, добавлены `s3/s4` и `local_obf`. `Load`
  дочитывает старый v1-state БЕЗ потери данных: `HeaderRange.UnmarshalJSON` ловит
  старое число `H` → вырожденный диапазон `{H,H}`, недостающие `s3/s4=0`,
  `local_obf` пуст. На проводе это **идентично v1** (S4-паддинга нет, H тот же),
  поэтому `self-upgrade`/`apt`/`apk`-апгрейд переводит ноду на v2-бинарь **без
  разрыва mesh-связи** — никакого ре-init/ре-join. При старте `node.Run` разово
  форс-переписывает мигрированный state на диск в v2 (иначе он застрял бы в старой
  схеме). Реджектится только version > CurrentVersion (state новее бинаря).
  Interop v1↔v2 проверен end-to-end (последовательный межверсионный апгрейд двух
  узлов: связь 0% потерь на каждом шаге v1↔v1 / v1↔v2 / v2↔v2).
  Включение реальной AWG-2.0-обфускации (S4/H-диапазоны/I1) — отдельный шаг после
  перехода всех нод на v2.

## [0.4.3] — 2026-06-24

Подписанный apt-репозиторий для Debian/Ubuntu. Бинарь `meshd` без изменений
относительно 0.4.2 — релиз добавляет канал установки/обновления.

### Added
- **apt-репозиторий (Debian/Ubuntu)** — `meshd` ставится и обновляется как
  системный пакет (`apt-get install meshd`); доверие по OpenPGP-подписи
  репозитория (`signed-by`, suite `stable`, арки amd64/arm64). Сборка и подпись
  в release-CI (`deploy/debian/build-apt-repo.sh`), публикация на GitHub Pages
  (дерево `debian/`) рядом с apk-фидом OpenWrt. Приватный ключ подписи — repo-
  secret `APT_SIGN_KEY`, публичный закоммичен (`deploy/debian/meshd-archive-keyring.asc`).
  Подключение — секция README «Подключение apt-репозитория».

## [0.4.2] — 2026-06-24

Багфиксы control plane по итогам ревью 0.4.1 + закрытие тех-долга.

### Fixed
- **gossip больше не теряет обновления `label`/`is_seed`** — `doRound` решал, писать
  ли state на диск, по числу изменений для wg-device (`changed`), но `label`/`is_seed`
  на wg-маршрутизацию не влияют и в `changed` не попадали → молча терялись.
  `MergePeers` теперь возвращает отдельный флаг `persist` (значимое для диска ≠ что
  пушить в device), `doRound` пишет по нему. Чистый refresh `LastSeen` по-прежнему НЕ
  персистится (flash-wear на роутерах).
- **Ужесточена валидация endpoint'а (`mesh.ValidEndpoint`)** — `net.SplitHostPort`
  пропускал `host:notaport` и `:port`; такой «endpoint» доходил до UAPI wg-device
  (отказ уже там), а hostname ещё и дёрнул бы блокирующий DNS в gossip-горутине.
  Теперь явная проверка непустого host + числового порта — единая граница и в
  gossip-merge, и в bootstrap-join (иначе seed принял бы кривой endpoint, а merge
  отверг → рассинхрон state↔device).
- **Убран ложный WARN `interface down failed` на shutdown (systemd)** — `node.Run`
  на остановке звал `ip link set down awg0`, а `KillMode=control-group` уже слал
  SIGTERM всей cgroup → дочерний `ip` умирал с «signal: terminated». Вызов избыточен
  (`awg0` — userspace-TUN, его целиком сносит `device.Close()`), убран. На procd-
  роутерах проблемы не было.

### Internal
- Юнит-тесты на gossip-транспорт (`doRound`/`handlePeers`) — раньше был покрыт лишь
  доменный `MergePeers`, а persist-баг жил именно в транспорте.
- Helper `shortKey` (был продублирован в mesh и bootstrap) сведён к единому
  `mesh.ShortKey`.

## [0.4.1] — 2026-06-24

Багфиксы по итогам аудита трёх живых нод после 0.4.0.

### Fixed
- **gossip больше не спамит к NAT-узлам с hub'а** — `GossipCandidates` ошибочно
  считал NAT-spoke достижимыми, если у НАС есть endpoint (`|| selfEndpoint != ""`).
  Из-за этого seed/hub после рестарта слал handshake-инициации к узлам без
  endpoint → лог-спам `no known endpoint for peer`. Теперь фильтр строго по
  endpoint ПИРА (инициировать gossip-pull можно только к узлу с объявленным
  адресом). Регресс-тесты на все топологии (hub↛spoke, hub↔hub, spoke→hub, all-NAT).
- **`Linker.Delete` идемпотентен на OpenWrt** — проверка «интерфейса нет» матчила
  только текст iproute2 (`Cannot find device`), а busybox `ip` (OpenWrt) пишет
  `can't find device` → ложный WARN `cleanup stale interface failed` при каждом
  старте демона на роутере. Матч обеих форм без учёта регистра.
- **`ip`-команды форсируют `LC_ALL=C`** — idempotency-проверки матчат текст stdout;
  под локализованной локалью сообщения переводились бы и матч ломался. Фиксируем
  английский вывод.

## [0.4.0] — 2026-06-23

Крупный релиз: апгрейд обфускации до актуальной линии AmneziaWG, закрытие
security-векторов control plane, надёжность записи на роутерах и большой
рефакторинг доменного ядра.

### Security
- **Identity binding на bootstrap** — seed регистрирует только тот pubkey, которым
  клиент реально прошёл Noise-handshake (`hs.PeerStatic()` == заявленный). Раньше
  нода с cluster-secret могла зарегистрировать фантомный чужой pubkey.
- **Anti-hijack в gossip-merge** — новый peer отвергается, если его mesh-IP уже
  принадлежит другому pubkey, вне network-CIDR или невалиден. Закрывает угон
  cryptokey-routing (`/32`) и перехват трафика соседа одной злой нодой.
- **Лимит конкурентных bootstrap-handshake'ей** — публичный порт seed'а считает
  Noise (Curve25519) до проверки PSK; семафор не даёт потоку коннектов истощить
  CPU/горутины/FD.

### Changed
- **Bump amneziawg-go v1.0.4 → v0.2.19** — актуальная поддерживаемая линия
  (v1.0.4 — orphan-тег, снят апстримом). Wire-совместимо со старыми нодами
  (одиночный H = вырожденный диапазон [X,X]); подтверждено interop old↔new на
  живой mixed-сети. Go 1.26.2 → 1.26.4.
- **Рефакторинг ядра** — домен вынесен в `internal/mesh` (платформо-независимый,
  единый backend для CLI/`--json`/будущего web), транспорт в
  `internal/bootstrap`+`internal/gossip`, оркестрация в `internal/node`; ОС-вызовы
  за build-tags (`wg.Linker`); единый wire-DTO + framing-примитив; декомпозиция
  self-upgrade (`internal/health`, `internal/selfupdate`); структурное логирование
  (`slog`) с инъекцией логгера. Добавлен `meshd status --json`.

### Added
- **Durable запись** state.json и бинаря при self-upgrade (fsync файла+директории
  через `internal/fsutil`) — переживает потерю питания на роутере.
- **Cross-process flock** на state — `meshd join` при живом демоне больше не теряет
  выученные через gossip peer'ы.
- **govulncheck в CI** (пиннут) и **SHA256SUMS** на релизе.

### Fixed
- **awgparams S1/S2 + H>4** — `Generate` гарантирует, что padded init/response
  различаются по размеру (иначе amneziawg-go реджектит конфиг → awg-params общие,
  вся сеть не стартует; раньше ~0.43% генераций) и что H-заголовки > 4 (иначе
  молчаливый откат на стандартный message-type без обфускации).
- **gossip не опрашивает недостижимых NAT-пиров** (spoke↔spoke) — убирает спам
  `no known endpoint` + бесполезные таймауты в логах роутеров.
- **self-upgrade watchdog** зовёт `systemctl reset-failed` перед рестартом —
  systemd start-limit больше не блокирует авто-откат при крэш-лупе.
- Валидация endpoint нового peer в merge, точная граница имени интерфейса в
  ufw-детекции, и прочие правки корректности из ревью.

## [0.3.0] — 2026-06-21

### Added
- **Подписанный apk-фид для OpenWrt 25.12+** — установка и обновление
  `apk add/upgrade meshd` из собственного репозитория. Сборка и подпись в
  release-CI (`deploy/openwrt/build-feed.sh`: per-arch пакеты + `apk mkndx`
  с подписью индекса), публикация на GitHub Pages (через GitHub Actions), зеркало
  jsDelivr для регионов с заблокированным GitHub. Приватный ключ — repo-secret
  `APK_SIGN_KEY`, публичный — `deploy/openwrt/awg-mesh-apk.rsa.pub`. nfpm-конфиг
  параметризован (`APK_ARCH`: `all` для standalone-.apk, конкретная арка для фида)
  и подписывает пакет (`postupgrade`-хук рестартует демон при `apk upgrade`).
- **`meshd self-upgrade [<binary>]`** — безопасное обновление ноды, единственный
  канал к которой — сам mesh-туннель. Два режима: без аргумента — через apk-фид
  (`apk update && apk add --latest meshd`), с путём — заменой бинарника (airgapped).
  В обоих: detached-применение (переживает разрыв SSH при пересоздании awg0) +
  watchdog из старой копии бинаря, который по таймауту откатывает апгрейд, если
  mesh-связность не вернулась. Health-check — TCP-достижимость соседа по mesh
  (auto-pick seed'а; `--health-peer/-timeout/-grace`). File-режим проверяет, что
  новый бинарь запускается под текущую арку, до подмены.
- **`meshd token [--quiet]`** — перепечатывает join-token из существующего state
  (init печатает его лишь раз; в файл токен не пишется). Работает на любой ноде:
  cluster-secret общий, seed-инфо есть в peer-list.

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

[0.6.1]: https://github.com/tumour/awg-mesh/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/tumour/awg-mesh/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/tumour/awg-mesh/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/tumour/awg-mesh/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/tumour/awg-mesh/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/tumour/awg-mesh/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/tumour/awg-mesh/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/tumour/awg-mesh/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tumour/awg-mesh/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/tumour/awg-mesh/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/tumour/awg-mesh/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/tumour/awg-mesh/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/tumour/awg-mesh/releases/tag/v0.1.0
