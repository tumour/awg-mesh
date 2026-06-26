package mesh

import (
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

// Доменное ядро flag-day-смены СЕТЕВЫХ awg-params (S3/S4/H) на живой mesh-сети.
//
// Проблема: S/H применяются на send И receive, dual-stack невозможен (params на
// уровне интерфейса) → менять можно только синхронно у всех, иначе рассинхрон.
// А раздать новые params нельзя по туннелям, которые сама смена ломает — значит
// «применить на seed первым» = deadlock.
//
// Модель «announce → ack → commit → apply»:
//  1. announce: seed кладёт новый набор в Pending БЕЗ ApplyAt и раздаёт по gossip;
//  2. ack: каждая нода, приняв Pending, сообщает seed свою версию (gossip-запрос);
//  3. commit: когда ВСЕ ноды подтвердили приём, seed назначает ApplyAt = now+grace;
//  4. apply: все (включая seed) переключаются синхронно в ApplyAt, reconfigure на лету.
//
// Гарантия «ноду не потерять»: пока хоть одна нода не подтвердила Pending, ApplyAt
// не назначается и flip не происходит — сеть остаётся на старом наборе целиком.
//
// Здесь только чистые решения (без сети/времени-как-глобала); применение к state
// и device — в internal/node, транспорт — в internal/gossip. Покрыто таблицами.

// ShouldAdoptPending решает, принять ли Pending, полученный от пира через gossip.
//
// Базово принимаем СТРОГО более новую версию (выше уже применённой currentVersion И
// выше уже отложенной локально) — монотонность даёт идемпотентность и защищает от
// отката на старый набор.
//
// ДОПОЛНИТЕЛЬНО принимаем переход «announced → committed» в пределах ОДНОЙ версии:
// локально лежит анонс (ApplyAt=0), а пришёл тот же Pending уже с назначенным
// ApplyAt. Без этого момент применения (его проставляет seed при commit, НЕ меняя
// версию) не распространяется на ноды, уже принявшие анонс, — и flip происходит
// только на seed → рассинхрон (баг, дважды клавший сеть). Принимаем строго один раз:
// уже committed повторно не пере-планируется (иначе злая нода сдвинула бы flip) и
// назад на announced не откатывается.
func ShouldAdoptPending(currentVersion uint64, local, remote *state.PendingParams) bool {
	if remote == nil || remote.Version <= currentVersion {
		return false // нет анонса / не новее уже применённого — устарело
	}
	if local == nil || remote.Version > local.Version {
		return true // локального нет / пришёл строго новее — принять
	}
	if remote.Version < local.Version {
		return false // пришёл старее отложенного — отвергнуть
	}
	// Та же версия: принять ТОЛЬКО commit поверх локального анонса (announced→committed).
	return local.ApplyAt.IsZero() && !remote.ApplyAt.IsZero()
}

// PendingDue сообщает, наступил ли момент применить Pending. ApplyAt с нулевым
// значением = «ещё не закоммичен» (ждём ack от всех нод) → НЕ применяем.
//
// maxStale защищает от «бродячего» Pending: если ApplyAt прошёл БОЛЬШЕ чем maxStale
// назад, Pending считается протухшим и НЕ применяется. Иначе Pending с давно
// прошедшим ApplyAt (например подхваченный по gossip уже ПОСЛЕ отката ноды на старый
// набор) применился бы мгновенно при adopt → незапланированный flip и рассинхрон.
// maxStale должен покрывать легитимное опоздание (нода получила ApplyAt чуть позже
// срока из-за gossip-задержки), но быть много меньше «давнего» бродячего Pending.
func PendingDue(p *state.PendingParams, now time.Time, maxStale time.Duration) bool {
	if p == nil || p.ApplyAt.IsZero() || now.Before(p.ApplyAt) {
		return false
	}
	return now.Sub(p.ApplyAt) <= maxStale
}

// NewPending формирует анонс смены для seed: версия = NextPendingVersion (строго
// новее и применённой, и уже висящего Pending — повторный set-params поверх активного
// flip остаётся монотонным), params новые, ApplyAt НЕ назначен (нулевой) — момент
// применения проставит CommitPending, когда все ноды подтвердят приём.
func NewPending(applied uint64, pending *state.PendingParams, params awgparams.Params) *state.PendingParams {
	return &state.PendingParams{
		Params:  params,
		Version: NextPendingVersion(applied, pending),
		// ApplyAt намеренно нулевой: announced, not committed.
	}
}

// AllPeersAcked сообщает, подтвердили ли ВСЕ пиры (кроме себя) приём Pending
// версии version. acks — последняя известная seed'у версия Pending у каждого
// пира (по pubkey), сообщённая через gossip-запрос. Нет ack у хотя бы одной
// ноды с валидным mesh-IP → false (flip не коммитим — иначе её потеряем).
func AllPeersAcked(peers []state.Peer, selfPub string, acks map[string]uint64, version uint64) bool {
	for _, p := range peers {
		if p.PublicKey == selfPub || p.NodeIP == "" {
			continue
		}
		if acks[p.PublicKey] < version {
			return false
		}
	}
	return true
}

// CommitPending назначает момент синхронного применения уже анонсированного
// Pending: ApplyAt = now+grace. Зовётся seed'ом, когда AllPeersAcked. Идемпотентно
// для уже закоммиченного (ApplyAt не сдвигаем второй раз). grace — фора, чтобы
// сам commit (ApplyAt) успел разойтись по gossip до flip.
func CommitPending(p *state.PendingParams, now time.Time, grace time.Duration) bool {
	if p == nil || !p.ApplyAt.IsZero() {
		return false // нечего коммитить / уже закоммичен
	}
	p.ApplyAt = now.Add(grace).UTC()
	return true
}

// --- double-ack + abort: гарантия «никого не теряем» при flip ---
//
// Commit-гейт (AllPeersAcked по обычным ack'ам) доказывает лишь, что все получили
// АНОНС. Дальше committed ApplyAt едет по gossip, и медленная нода может не успеть
// его получить до flip → застрянет на старом наборе → изолируется. Решение: второй
// ack — нода отдельно репортит, что держит уже COMMITTED ApplyAt (CommitAckVersion);
// seed «вооружает» flip только когда все commit-acked, иначе ABORT (переанонс v+1)
// до момента flip — тогда не флипает никто, сеть цела, ретрай.

// AnnounceAckVersion — высшая версия, АНОНС которой нода видела (применённая или
// принятый Pending любого состояния); репортится seed'у как announce-ack.
func AnnounceAckVersion(s *state.State) uint64 {
	v := s.ParamsVersion
	if s.Pending != nil && s.Pending.Version > v {
		v = s.Pending.Version
	}
	return v
}

// CommitAckVersion — версия, которую нода держит COMMITTED (или уже применила); её
// она репортит seed'у как commit-ack. Анонс (ApplyAt=0) НЕ считается committed.
func CommitAckVersion(s *state.State) uint64 {
	cv := s.ParamsVersion
	if s.Pending != nil && !s.Pending.ApplyAt.IsZero() && s.Pending.Version > cv {
		cv = s.Pending.Version
	}
	return cv
}

// NextPendingVersion — версия следующего анонса: на 1 больше максимума из уже
// применённой и текущего Pending. Учёт Pending нужен для abort'а — переанонс обязан
// быть строго новее висящего Pending, иначе его не примут как «новее».
func NextPendingVersion(applied uint64, pending *state.PendingParams) uint64 {
	next := applied
	if pending != nil && pending.Version > next {
		next = pending.Version
	}
	return next + 1
}

// ShouldAbort — пора ли seed'у отменить застрявший flip. Abort только для committed
// Pending (announced ещё ждёт ack'ов), у которого НЕ все подтвердили приём ApplyAt
// (allCommitAcked=false) и настал дедлайн решения ApplyAt-abortMargin. abortMargin —
// запас, чтобы переанонс успел дойти до уже-committed нод ДО их flip.
func ShouldAbort(p *state.PendingParams, now time.Time, allCommitAcked bool, abortMargin time.Duration) bool {
	if p == nil || p.ApplyAt.IsZero() || allCommitAcked {
		return false
	}
	return !now.Before(p.ApplyAt.Add(-abortMargin))
}

// ShouldFire — флипать ли ноде в ApplyAt. Помимо due (PendingDue) требует, чтобы нода
// держала committed ApplyAt не меньше abortMargin: seenAt — когда она впервые увидела
// этот ApplyAt. Коммит, полученный впритык к ApplyAt (seenAt > ApplyAt-abortMargin),
// не флипаем — возможен abort в полёте, который мы ещё не получили (анти-split).
func ShouldFire(p *state.PendingParams, seenAt, now time.Time, abortMargin, maxStale time.Duration) bool {
	if !PendingDue(p, now, maxStale) {
		return false
	}
	return !seenAt.IsZero() && !seenAt.After(p.ApplyAt.Add(-abortMargin))
}
