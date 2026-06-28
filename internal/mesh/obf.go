package mesh

// Доменное ядро seed-раздаваемого obf-конфига (per-node CPS-пакет I1, мимикрия под
// QUIC Initial с allowlisted-SNI — обход стейтфул-DPI).
//
// Модель (seed-центрично, без strand-риска):
//  1. админ на seed задаёт SNI → seed бампит версию obf-политики;
//  2. seed ГЕНЕРИТ per-node уникальный I1 из SNI и АКТИВНО пушит каждой ноде её I1;
//  3. нода применяет присланный I1 к живому awg0 и шлёт ACK; seed ретраит до подтверждения всех.
//
// Почему безопасно (в отличие от flag-day-смены S/H): I1 — initiator-local, получатель
// его игнорирует (свойство WG-протокола) → применение НЕ рвёт туннель, синхронность не
// нужна, потерять ноду нечем. Приём монотонен по версии (как ShouldAdoptPending для
// params): идемпотентность + защита от отката.

// ShouldAdoptObf решает, применять ли присланный seed'ом obf версии incomingVersion при
// уже применённой currentVersion. Принимаем строго более новую — это даёт идемпотентный
// повторный push (та же версия не переприменяется) и не откатывает на старый I1.
func ShouldAdoptObf(currentVersion, incomingVersion uint64) bool {
	return incomingVersion > currentVersion
}
