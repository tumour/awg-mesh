// map.js — отрисовка радар-графа mesh в SVG. Чистый DOM/SVG, БЕЗ Alpine:
// граф императивный (позиции узлов и линии-рёбра считаются), декларативно его
// не выразить — поэтому Alpine на обвязку, plain JS на рисование.

const SVG_NS = 'http://www.w3.org/2000/svg';
const NODE_RADIUS = 30; // px — на сколько укорачивать ребро у края узла
const RING_RADII = [90, 170, 260]; // px — концентрические радар-кольца от seed
const CIRCLE_PCT = 34; // % — радиус круга, по которому раскладываются не-seed узлы

// mountMap монтирует граф в container. onSelect(id) — колбэк клика по узлу.
// Возвращает контроллер:
//   update(model)   — перерисовать топологию (звать при СМЕНЕ данных),
//   setSelected(id) — только подсветка выбранного (звать при клике/выборе).
export function mountMap(container, onSelect) {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('class', 'edges');
  svg.setAttribute('preserveAspectRatio', 'none');
  container.append(svg);

  let model = { nodes: [], edges: [], selectedId: null };
  let pos = new Map(); // id → {x, y} в процентах контейнера
  const nodeEls = new Map(); // id → <button>

  // layout — детерминированная раскладка: seed в центре, остальные равномерно
  // по кругу (старт сверху). Позиции в процентах → не зависят от размера контейнера.
  function layout(nodes) {
    const p = new Map();
    const seed = nodes.find((n) => n.role === 'seed');
    if (seed) p.set(seed.id, { x: 50, y: 50 });
    const rest = nodes.filter((n) => !seed || n.id !== seed.id);
    rest.forEach((n, i) => {
      const angle = -Math.PI / 2 + (2 * Math.PI * i) / rest.length;
      p.set(n.id, {
        x: 50 + CIRCLE_PCT * Math.cos(angle),
        y: 50 + CIRCLE_PCT * Math.sin(angle),
      });
    });
    return p;
  }

  function buildNodes() {
    for (const [id, el] of nodeEls) {
      if (!model.nodes.some((n) => n.id === id)) {
        el.remove();
        nodeEls.delete(id);
      }
    }
    for (const n of model.nodes) {
      let el = nodeEls.get(n.id);
      if (!el) {
        el = document.createElement('button');
        el.type = 'button';
        el.addEventListener('click', () => onSelect(n.id));
        container.append(el);
        nodeEls.set(n.id, el);
      }
      el.dataset.id = n.id;
      el.className = `node role-${n.role} state-${n.state}${n.isSelf ? ' is-self' : ''}`;
      el.setAttribute('aria-label', `${n.label}, ${n.ip}, ${n.state}`);
      el.innerHTML =
        '<span class="glyph"><span class="dot"></span></span>' +
        `<span class="lbl">${esc(n.label)}<span class="ip">${esc(n.ip)}</span></span>`;
      const at = pos.get(n.id);
      if (at) {
        el.style.left = `${at.x}%`;
        el.style.top = `${at.y}%`;
      }
    }
  }

  function drawEdges() {
    const w = container.clientWidth;
    const h = container.clientHeight;
    svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
    const px = (id) => {
      const a = pos.get(id);
      return a ? { x: (a.x / 100) * w, y: (a.y / 100) * h } : null;
    };

    let out = '';
    const seed = model.nodes.find((n) => n.role === 'seed');
    if (seed) {
      const c = px(seed.id);
      if (c) for (const r of RING_RADII) out += `<circle class="ring" cx="${c.x}" cy="${c.y}" r="${r}"/>`;
    }
    for (const e of model.edges) {
      const A = px(e.a);
      const B = px(e.b);
      if (!A || !B) continue;
      const dx = B.x - A.x;
      const dy = B.y - A.y;
      const len = Math.hypot(dx, dy) || 1;
      const ux = dx / len;
      const uy = dy / len;
      const touches = e.a === model.selectedId || e.b === model.selectedId;
      const emphasis = model.selectedId ? (touches ? ' hot' : ' dim') : '';
      out +=
        `<line class="edge edge-${e.state}${emphasis}" ` +
        `x1="${A.x + ux * NODE_RADIUS}" y1="${A.y + uy * NODE_RADIUS}" ` +
        `x2="${B.x - ux * NODE_RADIUS}" y2="${B.y - uy * NODE_RADIUS}"/>`;
    }
    svg.innerHTML = out;
  }

  function applySelection() {
    for (const [id, el] of nodeEls) el.classList.toggle('sel', id === model.selectedId);
    drawEdges();
  }

  window.addEventListener('resize', drawEdges);

  return {
    update(next) {
      model = {
        nodes: next.nodes,
        edges: next.edges,
        selectedId: next.selectedId ?? model.selectedId,
      };
      pos = layout(model.nodes);
      buildNodes();
      applySelection();
    },
    setSelected(id) {
      model.selectedId = id;
      applySelection();
    },
    redraw: drawEdges, // перерисовать рёбра (после показа скрытого контейнера)
  };
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}
