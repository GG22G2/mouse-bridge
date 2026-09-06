// Minimal CDP helper: node cdp.mjs <port> "<js expression>" [targetUrlFilter]
// Evaluates JS in the first matching target (or opens a new tab).
const port = process.argv[2] || 9222;
const expr = process.argv[3];
const urlFilter = process.argv[4] || '';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
  let target = list.find((t) => t.type === 'page' && t.url.includes(urlFilter));
  let created = false;
  if (!target) {
    const t = await (await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(urlFilter || 'about:blank')}`, { method: 'PUT' })).json();
    target = t;
    created = true;
    await sleep(1200);
  }
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let id = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  };
  const call = (method, params = {}) => new Promise((res) => {
    const mid = ++id;
    pending.set(mid, res);
    ws.send(JSON.stringify({ id: mid, method, params }));
  });
  if (created && urlFilter && !target.url.startsWith(urlFilter)) {
    await call('Page.enable');
    await call('Page.navigate', { url: urlFilter });
    await sleep(2000);
  }
  const r = await call('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true, userGesture: true });
  console.log(JSON.stringify(r.result, null, 1));
  ws.close();
}
main().catch((e) => { console.error('ERR', e.message); process.exit(1); });
