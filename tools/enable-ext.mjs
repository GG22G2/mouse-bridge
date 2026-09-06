// Enable a disabled extension on chrome://extensions via trusted CDP click.
const port = process.argv[2] || 9223;
const extId = process.argv[3];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
let target = list.find((t) => t.type === 'page' && t.url.startsWith('chrome://extensions'));
if (!target) { console.error('no chrome://extensions tab'); process.exit(1); }
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let id = 0;
const pending = new Map();
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
const call = (method, params = {}) => new Promise((res) => { const mid = ++id; pending.set(mid, res); ws.send(JSON.stringify({ id: mid, method, params })); });

await call('Runtime.enable');
const findRect = `(() => {
  function pierce(root, fn) { for (const el of root.querySelectorAll('*')) { fn(el); if (el.shadowRoot) pierce(el.shadowRoot, fn); } }
  let result = null;
  pierce(document, (el) => {
    if (result || el.tagName !== 'EXTENSIONS-ITEM') return;
    if (el.id !== '${extId}') return;
    const toggle = el.shadowRoot.querySelector('cr-toggle');
    if (toggle) { const r = toggle.getBoundingClientRect(); result = { x: r.x + r.width / 2, y: r.y + r.height / 2, checked: toggle.checked }; }
  });
  return result;
})()`;
const r1 = await call('Runtime.evaluate', { expression: findRect, returnByValue: true });
const rect = r1.result?.result?.value;
if (!rect) { console.error('toggle not found'); process.exit(1); }
console.log('toggle at', JSON.stringify(rect));
for (const type of ['mousePressed', 'mouseReleased']) {
  await call('Input.dispatchMouseEvent', { type, x: rect.x, y: rect.y, button: 'left', clickCount: 1 });
  await sleep(120);
}
await sleep(1500);
const r2 = await call('Runtime.evaluate', { expression: findRect, returnByValue: true });
console.log('after click:', JSON.stringify(r2.result?.result?.value));
ws.close();
