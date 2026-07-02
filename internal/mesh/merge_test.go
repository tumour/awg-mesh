package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

const (
	selfKey  = "SELF-KEY"
	testCIDR = "100.64.0.0/24"
)

func TestMergeAddsNewPeerAndFiltersSelf(t *testing.T) {
	local := []state.Peer{{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}}
	remote := []state.Peer{
		{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}, // мы сами — игнор
		{Label: "new", PublicKey: "NEW", Endpoint: "1.2.3.4:51820", NodeIP: "100.64.0.3"},
	}

	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(rejected) != 0 {
		t.Fatalf("no rejections expected, got %v", rejected)
	}
	if len(merged) != 2 {
		t.Fatalf("want 2 merged peers (self + new), got %d: %+v", len(merged), merged)
	}
	if len(changed) != 1 || changed[0].PublicKey != "NEW" {
		t.Fatalf("want exactly NEW in changed, got %+v", changed)
	}
	if changed[0].Endpoint != "1.2.3.4:51820" {
		t.Fatalf("endpoint not propagated: %+v", changed[0])
	}
	if changed[0].LastSeen.IsZero() {
		t.Fatal("LastSeen not set on new peer")
	}
}

func TestMergeEndpointUpdatePushedToDevice(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey},
		{Label: "b", PublicKey: "B", Endpoint: "old.host:1", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "new.host:2", NodeIP: "100.64.0.3"},
	}

	merged, changed, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 1 || changed[0].Endpoint != "new.host:2" {
		t.Fatalf("endpoint change must land in changed, got %+v", changed)
	}
	for _, p := range merged {
		if p.PublicKey == "B" && p.Endpoint != "new.host:2" {
			t.Fatalf("merged peer keeps stale endpoint: %+v", p)
		}
	}
}

func TestMergeEmptyRemoteEndpointDoesNotErase(t *testing.T) {
	local := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "known.host:51820", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "", NodeIP: "100.64.0.3"},
	}

	merged, changed, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("nothing to push, got %+v", changed)
	}
	if merged[0].Endpoint != "known.host:51820" {
		t.Fatalf("empty remote endpoint erased local: %+v", merged[0])
	}
	if merged[0].LastSeen.IsZero() {
		t.Fatal("LastSeen must refresh for peer confirmed by remote")
	}
}

// Регресс на БАГ #1 (латентный): label-only изменение НЕ пушится в device
// (changed пуст), но ОБЯЗАНО персиститься (persist=true) — иначе caller по
// len(changed)==0 решал бы «не писать» и обновление label молча терялось бы.
func TestMergeLabelChangeNotPushedButPersisted(t *testing.T) {
	local := []state.Peer{
		{Label: "old-name", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "new-name", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}

	merged, changed, _, persist := MergePeers(local, remote, nil, selfKey, testCIDR)
	if merged[0].Label != "new-name" {
		t.Fatalf("label not merged: %+v", merged[0])
	}
	if len(changed) != 0 {
		t.Fatalf("label-only change must not be pushed to device, got %+v", changed)
	}
	if !persist {
		t.Fatal("label-only change MUST set persist=true (else it is silently dropped — BUG #1)")
	}
}

// АДВЕРСАРИАЛЬНЫЙ: обычная нода в своём gossip-ответе объявляет про СЕБЯ
// is_seed=true. MergePeers НЕ должен присваивать seed-статус из недоверенного
// gossip — иначе самозванец проходит seedAuthorized (gossip/obf.go) и пушит
// flag-day POST /v1/params (согласованный разрыв mesh) и /v1/obf. Источник
// истины по is_seed — ТОЛЬКО bootstrap-response (join, Noise-аутентифицирован)
// и локальный init; gossip-поле is_seed не доверяем.
func TestMergeIgnoresGossipedSeedClaimOnExistingPeer(t *testing.T) {
	local := []state.Peer{
		{PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3", IsSeed: false},
	}
	remote := []state.Peer{ // B врёт, что он seed
		{PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3", IsSeed: true},
	}
	merged, changed, _, persist := MergePeers(local, remote, nil, selfKey, testCIDR)
	if merged[0].IsSeed {
		t.Fatalf("gossiped is_seed=true MUST be ignored for existing peer, got seed: %+v", merged[0])
	}
	if len(changed) != 0 {
		t.Fatalf("is_seed-only claim must not touch device, got %+v", changed)
	}
	if persist {
		t.Fatal("ignored is_seed claim must not trigger a disk write")
	}
}

// Смешанный апдейт: existing peer одним gossip-ответом прислал легитимную смену
// endpoint И вражеский is_seed=true. Фильтрация — по полю, не по ответу целиком:
// endpoint применяется (merged+changed+persist), seed-клейм игнорируется.
func TestMergeAppliesEndpointButIgnoresSeedClaimInSameUpdate(t *testing.T) {
	local := []state.Peer{
		{PublicKey: "B", Endpoint: "old.host:1", NodeIP: "100.64.0.3", IsSeed: false},
	}
	remote := []state.Peer{ // B двигает endpoint и заодно врёт, что он seed
		{PublicKey: "B", Endpoint: "new.host:2", NodeIP: "100.64.0.3", IsSeed: true},
	}
	merged, changed, _, persist := MergePeers(local, remote, nil, selfKey, testCIDR)
	if merged[0].Endpoint != "new.host:2" {
		t.Fatalf("legit endpoint change must apply, got %+v", merged[0])
	}
	if merged[0].IsSeed {
		t.Fatalf("seed claim must be ignored even alongside legit changes: %+v", merged[0])
	}
	if len(changed) != 1 || changed[0].Endpoint != "new.host:2" {
		t.Fatalf("endpoint change must be pushed to device, got %+v", changed)
	}
	if changed[0].IsSeed {
		t.Fatalf("device push must not carry the seed claim: %+v", changed[0])
	}
	if !persist {
		t.Fatal("endpoint change MUST set persist=true")
	}
}

// Новый пир, впервые увиденный ЧЕРЕЗ gossip, тоже не может назначить себя seed:
// новые узлы приходят только не-seed'ами (единственный seed известен с init/join).
func TestMergeIgnoresGossipedSeedClaimOnNewPeer(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.2"}}
	remote := []state.Peer{ // впервые видим C, и он врёт про seed
		{PublicKey: "C", Endpoint: "h:2", NodeIP: "100.64.0.9", IsSeed: true},
	}
	merged, _, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	for _, p := range merged {
		if p.PublicKey == "C" && p.IsSeed {
			t.Fatalf("new gossiped peer MUST NOT be trusted as seed: %+v", p)
		}
	}
}

// РЕГРЕССИЯ/устойчивость: локальный seed (узнан из bootstrap) НЕ должен терять
// статус, даже если remote — по ошибке или злонамеренно — прислал is_seed=false.
// is_seed берётся только из local, поэтому downgrade через gossip невозможен.
func TestMergeKeepsLocalSeedDespiteGossipDowngrade(t *testing.T) {
	local := []state.Peer{
		{PublicKey: "S", Endpoint: "h:1", NodeIP: "100.64.0.1", IsSeed: true},
	}
	remote := []state.Peer{
		{PublicKey: "S", Endpoint: "h:1", NodeIP: "100.64.0.1", IsSeed: false},
	}
	merged, _, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if !merged[0].IsSeed {
		t.Fatalf("local seed status MUST survive gossip downgrade, got %+v", merged[0])
	}
}

// Чистый refresh LastSeen (ничего значимого не изменилось) — persist=false:
// не пишем файл каждый gossip-цикл (flash-wear).
func TestMergePureLastSeenRefreshNotPersisted(t *testing.T) {
	local := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{ // те же label/endpoint/IsSeed — меняется только LastSeen
		{Label: "b", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}
	_, changed, _, persist := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("nothing to push, got %+v", changed)
	}
	if persist {
		t.Fatal("pure LastSeen-refresh must NOT persist (avoid flash-wear each cycle)")
	}
}

func TestMergeDeduplicatesRemoteDuplicates(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey}}
	// remote с дублем pubkey "NEW" — должен добавиться ровно один раз.
	remote := []state.Peer{
		{Label: "new", PublicKey: "NEW", NodeIP: "100.64.0.3", Endpoint: "h:1"},
		{Label: "new-dup", PublicKey: "NEW", NodeIP: "100.64.0.3", Endpoint: "h:1"},
	}
	merged, changed, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	cnt := 0
	for _, p := range merged {
		if p.PublicKey == "NEW" {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("duplicate pubkey in remote must be added once, got %d", cnt)
	}
	if len(changed) != 1 {
		t.Fatalf("want 1 changed, got %d", len(changed))
	}
}

func TestMergeKeepsLocalUnknownToRemote(t *testing.T) {
	local := []state.Peer{
		{Label: "c", PublicKey: "C", NodeIP: "100.64.0.4"},
	}
	remote := []state.Peer{} // remote про C не знает

	merged, changed, _, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(merged) != 1 || merged[0].PublicKey != "C" {
		t.Fatalf("local peer dropped: %+v", merged)
	}
	if !merged[0].LastSeen.IsZero() {
		t.Fatal("LastSeen refreshed though remote did not confirm the peer")
	}
	if len(changed) != 0 {
		t.Fatalf("nothing changed, got %+v", changed)
	}
}

// --- revoke: tombstone исключает ноду из merge (см. tombstone.go) ---

// Отозванный peer, ещё живущий в local, выкидывается из merged и помечается на
// запись (persist) — на wg-device его снимет RemovePeer у caller'а.
func TestMergeRevokedPeerDroppedFromLocal(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey, NodeIP: "100.64.0.1"},
		{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4", Endpoint: "h:1"},
	}
	remote := []state.Peer{
		{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4", Endpoint: "h:1"},
	}
	tomb := []state.Tombstone{{PublicKey: "ORPH"}}

	merged, changed, _, persist := MergePeers(local, remote, tomb, selfKey, testCIDR)
	for _, p := range merged {
		if p.PublicKey == "ORPH" {
			t.Fatal("отозванный peer не должен оставаться в merged")
		}
	}
	if len(changed) != 0 {
		t.Fatalf("отозванного нельзя пушить в device, got %+v", changed)
	}
	if !persist {
		t.Fatal("удаление отозванного из local ОБЯЗАНО persist=true")
	}
}

// Перекрытие реанонса: сосед прислал отозванного как «нового» — НЕ воскрешаем.
func TestMergeRevokedPeerNotReanimatedFromRemote(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	remote := []state.Peer{
		{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4", Endpoint: "h:1"},
	}
	tomb := []state.Tombstone{{PublicKey: "ORPH"}}

	merged, changed, rejected, _ := MergePeers(local, remote, tomb, selfKey, testCIDR)
	for _, p := range merged {
		if p.PublicKey == "ORPH" {
			t.Fatal("реанонс отозванного должен блокироваться")
		}
	}
	if len(changed) != 0 {
		t.Fatalf("реанонс не должен доходить до device, got %+v", changed)
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection (revoked), got %v", rejected)
	}
}

// Security: форж tombstone на НАШ собственный pubkey не должен выкидывать нас из
// своего же peer-list (иначе злой сосед «убивал» бы нас по сети одним tombstone).
// self обрабатывается ДО revoke-проверки — этот тест страхует тот порядок.
func TestMergeSelfSurvivesOwnTombstone(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey, NodeIP: "100.64.0.1"},
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "h:1"},
	}
	tomb := []state.Tombstone{{PublicKey: selfKey}} // форж на себя

	merged, _, _, _ := MergePeers(local, nil, tomb, selfKey, testCIDR)
	found := false
	for _, p := range merged {
		if p.PublicKey == selfKey {
			found = true
		}
	}
	if !found {
		t.Fatal("self под форженным tombstone должен ОСТАТЬСЯ в merged")
	}
}

func TestValidEndpoint(t *testing.T) {
	cases := []struct {
		ep   string
		want bool
	}{
		{"1.2.3.4:51820", true},       // IP:port
		{"vpn.example.com:443", true}, // hostname:port (допустим)
		{"", false},                   // пусто (непустоту проверяет caller)
		{"1.2.3.4:notaport", false},   // нечисловой порт — SplitHostPort пропускал, мы нет
		{"1.2.3.4:", false},           // пустой порт
		{":51820", false},             // пустой host
		{"no-port-here", false},       // не host:port вовсе
		{"1.2.3.4", false},            // без порта
	}
	for _, c := range cases {
		if got := ValidEndpoint(c.ep); got != c.want {
			t.Errorf("ValidEndpoint(%q) = %v, want %v", c.ep, got, c.want)
		}
	}
}

// --- security: одна нода не должна угнать чужой mesh-IP/маршрут через gossip ---

func TestMergeRejectsIPHijack(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey, NodeIP: "100.64.0.1"},
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "b.host:51820"},
	}
	// Злая нода анонсирует НОВЫЙ pubkey на УЖЕ ЗАНЯТОМ IP B → попытка угона /32.
	remote := []state.Peer{
		{Label: "evil", PublicKey: "EVIL", NodeIP: "100.64.0.3", Endpoint: "evil.host:51820"},
	}
	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	for _, p := range merged {
		if p.PublicKey == "EVIL" {
			t.Fatal("EVIL peer hijacking 100.64.0.3 must not be merged")
		}
	}
	if len(changed) != 0 {
		t.Fatalf("hijack must not be pushed to device, got %+v", changed)
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection reason, got %v", rejected)
	}
}

func TestMergeRejectsOutOfCIDR(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	// node_ip вне mesh-CIDR → попытка угнать маршрут к внешнему адресу (8.8.8.8).
	remote := []state.Peer{{Label: "x", PublicKey: "X", NodeIP: "8.8.8.8", Endpoint: "x.host:1"}}

	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 0 || len(rejected) != 1 {
		t.Fatalf("out-of-cidr peer must be rejected: changed=%+v rejected=%v", changed, rejected)
	}
	for _, p := range merged {
		if p.PublicKey == "X" {
			t.Fatal("out-of-cidr peer must not be merged")
		}
	}
}

func TestMergeRejectsInvalidNodeIP(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	remote := []state.Peer{
		{Label: "noip", PublicKey: "N", NodeIP: ""},
		{Label: "bad", PublicKey: "M", NodeIP: "not-an-ip"},
	}
	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("invalid-IP peers must not be pushed, got %+v", changed)
	}
	if len(rejected) != 2 {
		t.Fatalf("want 2 rejections, got %v", rejected)
	}
	if len(merged) != 1 { // только self
		t.Fatalf("only self should remain, got %+v", merged)
	}
}

func TestMergeNewPeerInvalidEndpointNulled(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	// Новый peer с валидным IP, но кривым непустым endpoint → endpoint зануляется,
	// сам peer добавляется initiator-only (в state/device мусор не попадает).
	remote := []state.Peer{
		{Label: "n", PublicKey: "N", NodeIP: "100.64.0.7", Endpoint: "garbage-no-port"},
	}
	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 1 || changed[0].PublicKey != "N" {
		t.Fatalf("peer with valid IP must still be added, got changed=%+v", changed)
	}
	if changed[0].Endpoint != "" {
		t.Fatalf("invalid endpoint must be nulled, got %q", changed[0].Endpoint)
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection note, got %v", rejected)
	}
	for _, p := range merged {
		if p.PublicKey == "N" && p.Endpoint != "" {
			t.Fatalf("merged peer keeps garbage endpoint: %+v", p)
		}
	}
}

func TestMergeRejectsInvalidEndpointFormat(t *testing.T) {
	local := []state.Peer{
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "good.host:51820"},
	}
	// Существующему B пытаются подсунуть endpoint без порта → не применяем, старый
	// (рабочий) endpoint сохраняем и в device мусор не пушим.
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "no-port-here"},
	}
	merged, changed, rejected, _ := MergePeers(local, remote, nil, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("garbage endpoint must not reach device, got %+v", changed)
	}
	if merged[0].Endpoint != "good.host:51820" {
		t.Fatalf("garbage endpoint must not overwrite good one: %+v", merged[0])
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection, got %v", rejected)
	}
}
