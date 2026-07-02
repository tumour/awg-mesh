# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версионирование — [SemVer](https://semver.org/lang/ru/) (0.x — API не стабилен).

## [0.7.1] — 2026-07-02

Security-фикс control plane: закрыт спуф seed-статуса через gossip — обычная нода
больше не может объявить себя seed'ом и получить доступ к push-каналам управления
сетью. Плюс supply-chain-харднинг release-конвейера (SHA-пины actions, Dependabot).
Data plane и wire-протоколы не затронуты, обновление обратно совместимо.

### Security
- **`is_seed` из gossip больше не доверяется.** Раньше `MergePeers` копировал флаг
  `is_seed` из gossip-ответа: любая нода могла объявить себя seed'ом в своём
  peer-list'е, сосед мерджил это в state — и самозванец проходил проверку
  `seedAuthorized`, получая право пушить flag-day `POST /v1/params` (согласованный
  разрыв всей сети) и `/v1/obf`. Теперь seed-статус узнаётся ТОЛЬКО из
  Noise-аутентифицированного bootstrap-response (join) и локального `init`; gossip
  не может ни назначить его (существующему или новому peer'у), ни снять
  (downgrade локального seed'а тоже игнорируется).
- **Supply-chain-харднинг CI/Release.** GitHub Actions пиннуты по commit-SHA
  (мажорный тег `vX` мутабелен — угон аккаунта action'а означал бы подпись
  зловредного пакета нашим release-ключом); `nfpm` пиннут на v2.47.0 вместо
  `@latest` — версия тула в подписанном артефакте должна быть детерминирована.

### Added
- **Dependabot** (weekly): двигает SHA-пины actions (вместе с human-читаемым
  комментарием версии) и Go-зависимости — пины не застаивают security-патчи.

## [0.7.0] — 2026-07-01

Веб-морда: read-only HTTP-API и живой дашборд сети на seed. meshd поднимает
control-API (`/api/v1/status|peers|health`) и отдаёт статическую SPA — карту
топологии mesh (кто с кем связан по объявленным endpoint'ам, NAT↔NAT без прямого
пути) и таблицу нод, с живым online/offline из wg-handshake. Данные — тот же
единый backend `mesh.BuildStatus`, что у CLI и `--json`; data plane не затронут.

### Added
- **Read-only control-API для веб-морды (`internal/api`).** HTTP JSON-API под будущий
  дашборд: `GET /api/v1/status` (статус ноды + peers), `/api/v1/peers` (список пиров),
  `/api/v1/health` (роллап: версия params, число нод, запланированный flag-day). Единый
  success-конверт `{"data":…}` и error-конверт `{"error":{code,message}}`, версия в пути.
  Данные собирают `mesh.BuildStatus`/`BuildHealth` — тот же единый backend, что CLI и
  `--json` (без дублирования логики); секретов API не отдаёт. Слушает ТОЛЬКО mesh-IP
  (как gossip, trust-by-tunneling) и поднимается ТОЛЬКО на seed — тонкий бинарь на
  роутерах его не запускает.
- **Live-статус пиров в API (online/offline из wg-handshake).** `GET /api/v1/status`
  и `/api/v1/peers` обогащают каждого пира полем `live_status` (`online`/`offline`;
  пусто = неизвестно) и `last_handshake`, читая UAPI wg-device (`Device.PeerStats`):
  online, если handshake был не позже 180с назад (3× rekey; keepalive=25с у
  endpoint-пиров держит свежесть). Домен `mesh.BuildStatusLive` — чистый маппер (now
  инъектируется); отказ источника (device недоступен) деградирует к state-only, а не
  роняет в 500. «degraded» (DPI душит поток при живом handshake) в этот сигнал не
  входит — требует анализа rx/tx-потока, отдельным инкрементом.
- **Веб-морда (дашборд) на seed.** Статическая SPA (Alpine.js + vanilla JS, без
  сборки, вендоренная): карта сети (радар-граф — топология по объявленным endpoint'ам,
  NAT↔NAT без прямого пути, живость туннелей к наблюдателю из wg-handshake) + таблица
  нод + панель узла. meshd отдаёт её **same-origin** с API (`api.Server`, каталог
  `/usr/share/meshd/web`, поднимается ТОЛЬКО на seed) — фронтенд ходит в API
  относительным путём, без CORS. Роутинг: `/api/*` — JSON, всё прочее — статика.
  Ставится с deb-пакетом; без установленного UI meshd работает API-only.

## [0.6.6] — 2026-06-28

Seed раздаёт весь конфиг сам и надёжно: смена сетевых params и обфускация-обход
доводятся до КАЖДОЙ ноды прямым push'ем с подтверждением, а не случайным gossip-pull'ом.
Единая команда `set-params` принимает явные аргументы для всего набора.

### Added
- **Active-push раздача обфускации-обхода (per-node I1).** `set-params --sni <домен>`:
  seed генерит для каждой ноды уникальный QUIC-мимик I1 из домена и АКТИВНО раздаёт его
  по туннелю с прямым ACK, ретраит до подтверждения всех. I1 initiator-local (получатель
  игнорирует) → применяется на лету, туннель не рвётся, изоляция невозможна. Генератор
  generic: домен — аргумент, конкретных SNI/байт в коде нет.
- **Единая команда `set-params` с явными аргументами.** seed принимает
  `--s1..--s4/--jc/--jmin/--jmax/--h1..--h4` (override поверх текущих params; незаданные
  поля не меняются), либо `--regenerate` (свежий случайный набор), либо `--sni` (обход).
- **`awgparams.Validate`** гонится ДО анонса flag-day: невалидный набор (совпавшие
  padded-размеры init/response, пересекающиеся/некорректные H-диапазоны, пустой junk-
  диапазон) отвергается, чтобы смена params не положила `Configure` на ВСЕЙ сети.

### Changed
- **Flag-day-смена сетевых params (S/H/J) переведена с пассивного pull на active-push +
  прямой ACK.** Раньше подтверждения (announce-ack/commit-ack) собирались случайным
  gossip-pull'ом — при нескольких нодах это давало долгую несходимость и риск оставить
  медленную ноду на старом наборе. Теперь seed сам POST'ит `Pending` каждой ноде и
  получает её версии прямо в ответе, ретраит до подтверждения ВСЕХ; назначение `ApplyAt`
  и abort идут по этим прямым ack'ам. Доменное ядро (commit-when-all-acked,
  abort-on-stuck, анти-split при flip) НЕ изменилось — заменён только транспорт.
  Доставка `Pending` через pull сохранена резервным каналом (избыточность committed
  `ApplyAt` → нода вернее флипает синхронно со всеми).

### Removed
- Пассивный сбор ack через gossip-query (`?node=&v=&cv=`) — вытеснен прямым ACK
  active-push'а.

### Note
- Раскатка: обновите все ноды ДО `set-params` со сменой сетевых params — ack нового
  канала собирается только с обновлённых нод. Это безопасно: flip не стартует, пока
  не подтвердят ВСЕ, поэтому смешанные версии лишь откладывают flag-day, не изолируют.

## [0.6.5] — 2026-06-27

Watchdog self-upgrade больше не может посчитать апгрейд здоровым, если нода
осталась без связи. Закрывает self-first дыру: нода без надёжного
out-of-band-доступа теперь авто-откатывается на прежний бинарь, а не остаётся
изолированной без пути назад.

### Fixed
- **Peer-gated health-check watchdog'а.** Раньше апгрейд считался успешным, как
  только поднимался СВОЙ демон (забиндил gossip-сокет на mesh-IP) — даже если
  туннель к соседу был мёртв. Кривой бинарь мог «выздороветь» в глазах
  watchdog'а, тот удалял бэкап, и нода без out-of-band оставалась изолированной.
  Теперь watchdog ДО апгрейда запоминает, был ли сосед достижим через туннель;
  если был — оставляет новый бинарь, только когда туннель вернулся (peer-gate),
  иначе откат. Если соседа не было/он недостижим и до апгрейда — fallback на
  прежнюю проверку «свой демон поднялся» (без ложных откатов на отсутствующем
  соседе). Probe идёт по mesh-IP (CGNAT, маршрутизируется только через awg0),
  поэтому достижимость соседа невозможно подделать физической сетью. Решение —
  чистая функция `health.UpgradeHealthy`, покрыто таблицей.

## [0.6.4] — 2026-06-27

Безопасность flag-day-смены сетевых params: `set-params` больше не может оставить
часть нод на старом наборе. Плюс ускорение сходимости gossip и дефолтный I1 на
мигрированных нодах.

### Added
- **Двойной ack + abort для flag-day (гарантия «ноду не теряем»).** Раньше apply
  держался на таймере: seed назначал `ApplyAt`, когда все подтвердили АНОНС, но если
  момент применения не успевал дойти до медленной ноды, она не флипала и застревала
  на старом наборе → рассинхрон и изоляция. Теперь нода отдельным ack'ом (`cv`)
  сообщает, что уже ДЕРЖИТ committed `ApplyAt`; seed «вооружает» flip только когда это
  подтвердили ВСЕ, иначе **abort** — переанонс следующей версии с `ApplyAt=0` (не
  флипает никто, сеть остаётся на старом наборе, ретрай). Нода вдобавок не применяет
  flip, если получила `ApplyAt` позже, чем за `abortMargin` до срока (возможен abort
  в полёте). Доменное ядро покрыто таблицами.
- **gossip-backoff недостижимых пиров.** Стабильно-недостижимый endpoint-пир уходит
  в экспоненциальный backoff и временно исключается из выбора цели опроса — узел
  сходится на живых каналах каждый цикл, а не тратит половину циклов на таймауты.
  Ускоряет распространение peer-list/ack/committed `ApplyAt`, убирает спам в логах.
  Состояние in-memory (запасной soonest-eligible, чтобы одиночный канал не замолкал).
- **Backfill дефолтного I1** при пустом `local_obf`: демон при старте проставляет
  `DefaultLocalObf` (QUIC-мимик), если обфускация не задана. Мигрированные ноды
  оставались без I1; I1 initiator-local (получатель игнорирует) → backfill безопасен
  для живых туннелей, заданная вручную обфускация не трогается.

### Changed
- `commitGrace` 2 → 4 gossip-цикла, добавлен `abortMargin` (2 цикла); инвариант
  `commitGrace > abortMargin` (зазор, чтобы собрать commit-ack'и до дедлайна abort'а).

## [0.6.3] — 2026-06-26

Истинная причина того, что flag-day-смена сетевых params применялась только на
seed (а остальные оставались на старом наборе → рассинхрон). 0.6.2 чинил лишь
вторичный симптом (тайминг); здесь — корень.

### Fixed
- **Момент применения (`ApplyAt`) теперь распространяется по gossip на все ноды.**
  Seed при commit проставляет `ApplyAt`, НЕ меняя номер версии. Приём Pending
  (`ShouldAdoptPending`) смотрел только на версию и отвергал такой Pending как «не
  новее» — поэтому ноды, уже принявшие анонс, никогда не получали `ApplyAt` и не
  применяли flip. Теперь дополнительно принимается переход «announced → committed»
  в пределах одной версии (локально `ApplyAt` пуст, пришёл с `ApplyAt`), строго
  один раз: уже закоммиченный Pending не пере-планируется (защита от сдвига flip
  чужой нодой) и не откатывается обратно в announced.

### Changed
- `commitGrace` уменьшен с 6 до 2 gossip-циклов: после фикса распространения
  `ApplyAt` большой запас не нужен (плюс окно `maxStale` сверху).

## [0.6.2] — 2026-06-26

Исправление flag-day-смены сетевых params: при первом боевом применении часть нод
не успевала получить момент применения (`ApplyAt`) до самого flip → набор менял
только seed, остальные оставались на старом → рассинхрон и потеря связности.

### Fixed
- **`commitGrace` привязан к gossip-интервалу** (`commitGraceFor` = 6× gossip,
  ранее фиксированные 30с). Раньше grace был меньше gossip-интервала, поэтому
  seed применял flip раньше, чем `ApplyAt` успевал разойтись по gossip до нод —
  особенно за NAT, которые опрашивают seed не каждый цикл. Теперь момент применения
  гарантированно доходит до всех до flip.
- **Защита от «бродячего» `Pending`** (`PendingDue` с окном валидности `maxStale`):
  Pending с давно прошедшим `ApplyAt` больше не применяется. Раньше такой Pending,
  подхваченный по gossip уже после отката ноды на старый набор, применялся мгновенно
  при приёме → незапланированный flip и рассинхрон.

### Note
- Это снижает риск рассинхрона, но `commitGrace` — таймер, а не гарантия доставки:
  если нода не опросит seed за окно grace, flip может разойтись неравномерно.
  Полную атомарность даст двойной ack (apply только после подтверждения получения
  `ApplyAt` всеми) — отдельным шагом.

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

[0.7.1]: https://github.com/tumour/awg-mesh/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/tumour/awg-mesh/compare/v0.6.6...v0.7.0
[0.6.6]: https://github.com/tumour/awg-mesh/compare/v0.6.5...v0.6.6
[0.6.5]: https://github.com/tumour/awg-mesh/compare/v0.6.4...v0.6.5
[0.6.4]: https://github.com/tumour/awg-mesh/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/tumour/awg-mesh/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/tumour/awg-mesh/compare/v0.6.1...v0.6.2
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
