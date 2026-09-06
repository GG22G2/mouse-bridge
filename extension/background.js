// Mouse Bridge — MV3 service worker.
// Connects to the local daemon (ws://127.0.0.1:10087/ws) and executes
// element-location / measurement commands inside browser tabs.

const DAEMON_WS = 'ws://127.0.0.1:10087/ws';
const VERSION = chrome.runtime.getManifest().version;

// The toolbar icon opens popup.html, whose script (popup-open.js) immediately
// hands off to the persistent SIDE PANEL via chrome.sidePanel.open() and
// closes itself. We deliberately do NOT use
// sidePanel.setPanelBehavior({openPanelOnActionClick:true}): Edge accepts the
// flag (which suppresses action.onClicked) but never actually opens the
// panel, and this Edge build dispatches no action event at all for
// side-panel extensions — a popup is the one entry that fires everywhere.
// The side panel itself is also reachable natively: extensions-menu ->
// "在侧边栏中打开", or right-click the toolbar icon -> "打开边栏" (Edge).

let ws = null;
let wsWantConnected = false;
let reconnectDelay = 1000;
let lastLocateTabId = null;

// ---------- WebSocket lifecycle ----------

function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  wsWantConnected = true;
  try {
    ws = new WebSocket(DAEMON_WS);
  } catch (e) {
    scheduleReconnect();
    return;
  }
  ws.onopen = () => {
    reconnectDelay = 1000;
    send({
      type: 'hello',
      version: VERSION,
      ext_id: chrome.runtime.id,
      caps: ['ping', 'get_state', 'locate', 'measure', 'measure_page', 'set_zoom', 'set_window'],
    });
  };
  ws.onmessage = (ev) => {
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    handleMessage(msg).then(
      (data) => send({ id: msg.id, ok: true, data }),
      (err) => send({ id: msg.id, ok: false, error: String((err && err.message) || err) })
    );
  };
  ws.onclose = () => { ws = null; scheduleReconnect(); };
  ws.onerror = () => { try { ws && ws.close(); } catch {} };
}

function send(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
  }
}

function scheduleReconnect() {
  if (!wsWantConnected) return;
  wakeDaemon(); // fire-and-forget: ask the browser to start the daemon if it is down
  const delay = reconnectDelay;
  reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  setTimeout(connect, delay);
}

// ---------- daemon wake-up (native messaging) ----------

// The daemon (mouse-bridge.exe) is a separate local process; the extension
// has no process-launching power of its own, so it asks the browser to spawn
// the registered native messaging host, which starts the daemon detached.
// Throttled to one attempt per 10s; duplicate spawns are impossible anyway
// (the daemon's port bind is the single-instance lock).
const NATIVE_HOST = 'com.mousebridge.daemon';
let lastWakeAt = 0;

function wakeDaemon(force = false) {
  return new Promise((resolve) => {
    const now = Date.now();
    if (!force && now - lastWakeAt < 10000) return resolve({ ok: false, throttled: true });
    lastWakeAt = now;
    let port;
    let done = false;
    const finish = (r) => {
      if (done) return;
      done = true;
      try { port && port.disconnect(); } catch {}
      resolve(r);
    };
    try {
      port = chrome.runtime.connectNative(NATIVE_HOST);
    } catch (e) {
      return resolve({ ok: false, error: 'native host not available: ' + String(e) });
    }
    port.onMessage.addListener((msg) => {
      finish(msg || { ok: false });
      if (msg && msg.ok) {
        reconnectDelay = 1000; // daemon just came up — reconnect immediately
        connect();
      }
    });
    port.onDisconnect.addListener(() => {
      if (!done) finish({ ok: false, error: (chrome.runtime.lastError || {}).message || 'native host exited' });
    });
    port.postMessage({ cmd: 'wake' });
    setTimeout(() => finish({ ok: false, error: 'wake timed out after 15s' }), 15000);
  });
}

// Popup panel talks to the service worker here.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  (async () => {
    switch (msg && msg.type) {
      case 'ws_state':
        return sendResponse({
          connected: !!(ws && ws.readyState === WebSocket.OPEN),
          version: VERSION,
        });
      case 'wake_daemon':
        return sendResponse(await wakeDaemon(true));
      default:
        return sendResponse({ ok: false, error: 'unknown message: ' + (msg && msg.type) });
    }
  })();
  return true; // async response
});

// App-level ping keeps the MV3 service worker alive (WS activity extends
// its lifetime) and lets the daemon report liveness.
setInterval(() => send({ type: 'ping' }), 20000);

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
chrome.alarms.create('mb-keepalive', { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener((a) => { if (a.name === 'mb-keepalive') connect(); });
connect();

// ---------- Daemon command handling ----------

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function withTimeout(promise, ms, what) {
  return Promise.race([
    promise,
    new Promise((_, rej) => setTimeout(() => rej(new Error(what + ' timed out after ' + ms + 'ms')), ms)),
  ]);
}

async function handleMessage(msg) {
  const args = msg.args || {};
  switch (msg.cmd) {
    case 'ping':
      return { pong: true, version: VERSION };
    case 'get_state':
      return await getState();
    case 'locate':
      return await locate(args);
    case 'measure':
      return await measure(args);
    case 'measure_page':
      return await measurePage(args);
    case 'set_zoom':
      return await setZoom(args);
    case 'set_window':
      return await setWindow(args);
    default:
      throw new Error('unknown cmd: ' + msg.cmd);
  }
}

async function getState() {
  const tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  return {
    version: VERSION,
    active_tab: tabs[0] ? { id: tabs[0].id, url: tabs[0].url, title: tabs[0].title, windowId: tabs[0].windowId } : null,
    last_locate_tab_id: lastLocateTabId,
  };
}

// Resolve a tab from args: explicit tab_id > url substring > active tab.
async function resolveTab(args) {
  if (args.tab_id) {
    return await chrome.tabs.get(Number(args.tab_id));
  }
  if (args.url) {
    const needle = String(args.url);
    const all = await chrome.tabs.query({});
    const hit = all.find((t) => (t.url || '').includes(needle));
    if (!hit) throw new Error('no open tab matching url: ' + needle);
    return hit;
  }
  const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!active) throw new Error('no active tab found; pass tab_id or url');
  return active;
}

async function ensureContentScript(tab) {
  try {
    const pong = await withTimeout(chrome.tabs.sendMessage(tab.id, { type: 'ping_cs' }), 1500, 'content script ping');
    if (pong && pong.ok) return;
  } catch {}
  await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['content.js'] });
  await sleep(80);
  const pong2 = await withTimeout(chrome.tabs.sendMessage(tab.id, { type: 'ping_cs' }), 1500, 'content script ping after inject');
  if (!pong2 || !pong2.ok) throw new Error('content script not reachable on this page');
}

// Bring tab + window to the foreground so a REAL mouse event lands on it.
async function focusTab(tab) {
  let win = await chrome.windows.get(tab.windowId);
  if (win.state === 'minimized') {
    win = await chrome.windows.update(tab.windowId, { state: 'normal' });
    await sleep(350);
  }
  const [active] = await chrome.tabs.query({ active: true, windowId: tab.windowId });
  if (!active || active.id !== tab.id) {
    await chrome.tabs.update(tab.id, { active: true });
    await sleep(200);
  }
  if (!win.focused) {
    await chrome.windows.update(tab.windowId, { focused: true });
    await sleep(250);
  }
  // Re-read after any state change; bounds may have changed.
  win = await chrome.windows.get(tab.windowId);
  return win;
}

async function locate(args) {
  if (!args.selector) throw new Error('locate needs a selector');
  let tab = await resolveTab(args);
  const unsupported = /^(chrome|edge|about|devtools|view-source|chrome-extension):/i;
  if (unsupported.test(tab.url || '')) {
    throw new Error('cannot locate on browser-internal page: ' + tab.url);
  }
  const win = await focusTab(tab);
  await ensureContentScript(tab);
  tab = await chrome.tabs.get(tab.id); // refresh url/title after activation

  const res = await withTimeout(
    chrome.tabs.sendMessage(tab.id, { type: 'locate', selector: String(args.selector) }),
    9000,
    'element locate'
  );
  if (!res || !res.ok) throw new Error((res && res.error) || 'locate failed in page');

  const zoom = await chrome.tabs.getZoom(tab.id);
  lastLocateTabId = tab.id;
  return {
    tab: { id: tab.id, url: tab.url, title: tab.title, windowId: tab.windowId },
    css: res.css,
    rect: res.rect,
    occluded: !!res.occluded,
    element: res.element || {},
    metrics: res.metrics,
    win: {
      id: win.id,
      left: win.left,
      top: win.top,
      width: win.width,
      height: win.height,
      state: win.state,
      focused: !!win.focused,
    },
    zoom,
  };
}

async function measure(args) {
  const tabId = Number(args.tab_id) || lastLocateTabId;
  if (!tabId) throw new Error('no tab to measure (do a locate first)');
  // Fail with a clear message if the tab is gone instead of letting
  // sendMessage's generic "receiver" error leak to the daemon.
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch {
    throw new Error('measure: tab ' + tabId + ' no longer exists');
  }
  const res = await withTimeout(chrome.tabs.sendMessage(tabId, { type: 'measure' }), 3000, 'measure');
  if (!res || !res.ok) throw new Error((res && res.error) || 'measure failed');
  return { ...res, tab_id: tabId, tab_url: (tab.url || '').slice(0, 80) };
}

// measurePage is locate without an element: page geometry only (no scroll).
// Used by the daemon's move_css op to convert viewport css coordinates.
async function measurePage(args) {
  let tab = await resolveTab(args);
  const unsupported = /^(chrome|edge|about|devtools|view-source|chrome-extension):/i;
  if (unsupported.test(tab.url || '')) {
    throw new Error('cannot measure browser-internal page: ' + tab.url);
  }
  const win = await focusTab(tab);
  await ensureContentScript(tab);
  tab = await chrome.tabs.get(tab.id); // refresh url/title after activation
  const res = await withTimeout(
    chrome.tabs.sendMessage(tab.id, { type: 'measure_page' }),
    5000,
    'page measure'
  );
  if (!res || !res.ok) throw new Error((res && res.error) || 'measure_page failed in page');
  const zoom = await chrome.tabs.getZoom(tab.id);
  lastLocateTabId = tab.id;
  return {
    tab: { id: tab.id, url: tab.url, title: tab.title, windowId: tab.windowId },
    metrics: res.metrics,
    win: {
      id: win.id,
      left: win.left,
      top: win.top,
      width: win.width,
      height: win.height,
      state: win.state,
      focused: !!win.focused,
    },
    zoom,
  };
}

async function setZoom(args) {
  const tabId = Number(args.tab_id) || lastLocateTabId;
  if (!tabId) throw new Error('set_zoom needs tab_id or a prior locate');
  await chrome.tabs.setZoom(tabId, Number(args.zoom));
  await sleep(150);
  return { zoom: await chrome.tabs.getZoom(tabId), tab_id: tabId };
}

// Arrange the window that owns the last located tab: state
// ('normal'|'maximized'|'minimized') and/or left/top/width/height (DIP).
async function setWindow(args) {
  let winId;
  if (args.window_id) {
    winId = Number(args.window_id);
  } else if (lastLocateTabId) {
    const tab = await chrome.tabs.get(lastLocateTabId);
    winId = tab.windowId;
  } else {
    const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    if (!active) throw new Error('set_window: no window (do a locate first)');
    winId = active.windowId;
  }
  const update = {};
  for (const k of ['left', 'top', 'width', 'height']) {
    if (args[k] !== undefined) update[k] = Number(args[k]);
  }
  if (args.state) update.state = String(args.state);
  const win = await chrome.windows.update(winId, update);
  await sleep(400); // let resize/reposition + layout settle
  return { window_id: winId, left: win.left, top: win.top, width: win.width, height: win.height, state: win.state };
}
