// api.js — тонкий клиент read-only control-API. Разворачивает конверт
// {data} / {error}, бросает Error с кодом ошибки на неуспешный ответ.
// Пути относительные (same-origin: meshd отдаёт и SPA, и API) → без CORS.

const BASE = '/api/v1';

async function getJSON(path) {
  const resp = await fetch(BASE + path, { headers: { Accept: 'application/json' } });
  const body = await resp.json().catch(() => null);
  if (!resp.ok) {
    throw new Error(body?.error?.code || `http_${resp.status}`);
  }
  return body?.data;
}

export const fetchStatus = () => getJSON('/status');
export const fetchHealth = () => getJSON('/health');
