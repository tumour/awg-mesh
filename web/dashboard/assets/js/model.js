// model.js — ЧИСТЫЕ преобразования StatusView (из API) в view-model дашборда.
// Ни DOM, ни fetch, ни Alpine — только маппинг данных (легко читать и переносить).

// reachability — роль узла по объявленному endpoint'у (а НЕ по «за NAT»):
//   seed | endpoint (инициируема) | nat (без endpoint, initiator-only).
export function reachability(peer) {
  if (peer.is_seed) return 'seed';
  return peer.endpoint ? 'endpoint' : 'nat';
}

// nodeState — состояние для раскраски. Себя (is_self) не классифицируем; иначе
// из live_status API ('online' | 'offline'); пусто/нет данных → 'unknown'.
export function nodeState(peer) {
  if (peer.is_self) return 'self';
  return peer.live_status || 'unknown';
}

// toNodes — StatusView.peers → массив view-model узлов.
export function toNodes(status) {
  return (status?.peers ?? []).map((p) => ({
    id: p.public_key, // стабильный id узла
    label: p.label,
    ip: p.node_ip,
    endpoint: p.endpoint || '',
    role: reachability(p),
    state: nodeState(p),
    isSelf: !!p.is_self,
    isSeed: !!p.is_seed,
    publicKey: p.public_key,
    lastHandshake: p.last_handshake || null,
  }));
}

// toEdges — рёбра capability-графа: прямой туннель ВОЗМОЖЕН, если хотя бы у одной
// стороны объявлен endpoint (NAT↔NAT — нет). Это структура «кто с кем может»,
// выводимая из endpoint'ов (инвариант связности awg-mesh) — endpoint-нода связана
// со всеми, две NAT-ноды между собой не связаны.
//
// Живость ребра: одна нода видит ТОЛЬКО свои туннели, поэтому надёжно знаем лишь
// состояние рёбер, инцидентных наблюдателю (self) — берём из live_status другого
// конца ('active'/'down'). Рёбра «пир↔пир» помечаем 'possible': структура известна,
// живость отсюда не наблюдаема (полная живость — будущий инкремент с агрегацией
// per-node handshakes).
export function toEdges(nodes) {
  const observerId = nodes.find((n) => n.isSelf)?.id ?? null;
  const edges = [];
  for (let i = 0; i < nodes.length; i += 1) {
    for (let j = i + 1; j < nodes.length; j += 1) {
      const a = nodes[i];
      const b = nodes[j];
      if (!a.endpoint && !b.endpoint) continue; // NAT↔NAT — прямого пути нет
      edges.push({ a: a.id, b: b.id, state: edgeState(a, b, observerId) });
    }
  }
  return edges;
}

// edgeState — ребро, инцидентное наблюдателю, красим по состоянию ДРУГОГО конца
// (это наш реально наблюдаемый туннель); остальные — 'possible'.
function edgeState(a, b, observerId) {
  if (observerId && (a.id === observerId || b.id === observerId)) {
    const other = a.id === observerId ? b : a;
    if (other.state === 'online') return 'active';
    if (other.state === 'offline') return 'down';
  }
  return 'possible';
}

// health — счётчики по НЕ-self узлам (для роллапа хедера/рельса).
export function health(nodes) {
  const c = { online: 0, offline: 0, unknown: 0 };
  for (const n of nodes) {
    if (n.isSelf) continue;
    if (n.state === 'online') c.online += 1;
    else if (n.state === 'offline') c.offline += 1;
    else c.unknown += 1;
  }
  return c;
}
