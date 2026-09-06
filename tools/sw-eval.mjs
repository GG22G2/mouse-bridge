// Evaluate in the Mouse Bridge service worker: node sw-eval.mjs <cdpport> <expr>
const port = process.argv[2], expr = process.argv[3];
const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
const target = list.find((t) => t.type === 'service_worker' && t.url.includes('background.js'));
if (!target) { console.error('SW target not found'); process.exit(1); }
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id === 1) { console.log(JSON.stringify(m.result, null, 1)); ws.close(); process.exit(0); }
};
ws.send(JSON.stringify({ id: 1, method: 'Runtime.evaluate', params: { expression: expr, returnByValue: true, awaitPromise: true } }));
setTimeout(() => { console.error('SW eval timeout'); process.exit(1); }, 15000);
