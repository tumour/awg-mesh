// app.js — точка входа: связывает Alpine (реактивная обвязка) с API и графом.
// Явный import/start ESM-сборки Alpine — без глобалей и гонок автозапуска.

import Alpine from './alpine.esm.js';
import { fetchStatus } from './api.js';
import { toNodes, toEdges, health } from './model.js';
import { mountMap } from './map.js';

const POLL_MS = 7000; // как часто опрашивать API (статус меняется медленно)

Alpine.data('dashboard', () => ({
  loading: true,
  error: null,
  view: 'map', // 'map' | 'table'
  network: { cidr: '', label: '', role: '' },
  nodes: [],
  edges: [],
  selectedId: null,

  _map: null,
  _timer: null,
  _sig: '', // подпись графа — чтобы не перерисовывать DOM без реальных изменений

  init() {
    // $nextTick: ждём, пока Alpine пройдёт DOM и наполнит $refs — иначе на момент
    // init() дочерний $refs.map ещё пуст (классическая ловушка Alpine).
    this.$nextTick(() => {
      this._map = mountMap(this.$refs.map, (id) => {
        this.selectedId = id;
      });
      this.syncMap(); // отрисовать данные, если refresh успел до монтирования графа
      this._map.setSelected(this.selectedId);
    });

    this.refresh().finally(() => {
      this.loading = false;
    });
    this._timer = setInterval(() => this.refresh(), POLL_MS);

    this.$watch('nodes', () => this.syncMap());
    this.$watch('selectedId', (id) => this._map?.setSelected(id));
    // при возврате на карту перерисовать рёбра — пока была скрыта, размеры были нулевые
    this.$watch('view', (v) => {
      if (v === 'map') this.$nextTick(() => this._map?.redraw());
    });
  },

  destroy() {
    clearInterval(this._timer);
  },

  async refresh() {
    try {
      const status = await fetchStatus();
      this.network = { cidr: status.network_cidr, label: status.label, role: status.role };
      this.nodes = toNodes(status);
      this.edges = toEdges(this.nodes);
      // держим валидный выбор: по умолчанию seed (или первый узел)
      if (!this.selectedId || !this.nodes.some((n) => n.id === this.selectedId)) {
        const seed = this.nodes.find((n) => n.role === 'seed');
        this.selectedId = (seed || this.nodes[0])?.id ?? null;
      }
      this.error = null;
    } catch (e) {
      this.error = e.message || String(e);
    }
  },

  // syncMap перерисовывает граф ТОЛЬКО при реальной смене топологии/состояний —
  // иначе каждый poll дёргал бы DOM и рестартил анимацию рёбер (фликер).
  syncMap() {
    if (!this._map) return;
    const sig =
      JSON.stringify(this.nodes.map((n) => [n.id, n.state, n.role])) +
      '|' +
      JSON.stringify(this.edges.map((e) => [e.a, e.b, e.state]));
    if (sig === this._sig) return;
    this._sig = sig;
    this._map.update({ nodes: this.nodes, edges: this.edges, selectedId: this.selectedId });
  },

  get selected() {
    return this.nodes.find((n) => n.id === this.selectedId) || null;
  },
  get health() {
    return health(this.nodes);
  },
  get nodeCount() {
    return this.nodes.length;
  },

  // --- презентационные хелперы (используются в шаблоне) ---
  roleLabel(role) {
    return { seed: 'seed', endpoint: 'с endpoint', nat: 'без endpoint' }[role] || role;
  },
  stateLabel(state) {
    return { online: 'Онлайн', offline: 'Оффлайн', unknown: 'неизвестно', self: '—' }[state] || state;
  },
  reachLabel(node) {
    return node?.role === 'nat' ? 'initiator-only' : 'инициируема';
  },
  trunc(key) {
    return key && key.length > 14 ? `${key.slice(0, 7)}…${key.slice(-5)}` : key || '';
  },
  pct(count) {
    const h = this.health;
    const total = h.online + h.offline + h.unknown;
    return total ? Math.round((count / total) * 100) : 0;
  },
  fmtAge(iso) {
    if (!iso) return '—';
    const secs = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (secs < 60) return `${secs}с назад`;
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}м назад`;
    return `${Math.floor(mins / 60)}ч назад`;
  },
}));

Alpine.start();
