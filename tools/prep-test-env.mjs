// Prepares the test browser: reloads the test page so content scripts are
// fresh. NOTE: never developerPrivate.reload() a --load-extension/unpacked
// extension on Chrome 137+ — it triggers the unsupportedDeveloperExtension
// disable. Pick up extension file changes by RESTARTING the browser instead
// (a launch re-reads the files from disk).
const port = process.argv[2] || '9226';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
const target = list.find((t) => t.type === 'page' && t.url.includes('127.0.0.1:10087/test'));
if (!target) {
  console.log('test page tab not found');
  process.exit(0);
}
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let id = 0;
const pending = new Map();
ws.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
});
const call = (method, params = {}) => new Promise((res) => {
  const mid = ++id;
  pending.set(mid, res);
  ws.send(JSON.stringify({ id: mid, method, params }));
});
await call('Page.enable');
await call('Page.reload');
await sleep(2200);
console.log('test page reloaded');
ws.close();
