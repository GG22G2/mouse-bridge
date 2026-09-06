// Mouse Bridge — side panel page (opens as a regular tab when loaded directly
// for diagnostics). Shows daemon/extension connection state, the live cursor
// position in PAGE coordinates (css clientX/Y, exactly what pages see), and
// lets the user move the real cursor to a css coordinate via the daemon's
// move_css op (which reuses the measured affine calibration + landing
// verification).

const $ = (id) => document.getElementById(id);
const setDot = (id, cls) => { $(id).className = 'dot' + (cls ? ' ' + cls : ''); };

// Same browser brand the service worker reports, so the daemon can route this
// panel's move request to ITS browser (Chrome and Edge share one extension id).
const BROWSER = navigator.userAgent.includes('Edg/') ? 'edge' : 'chrome';

let moveBusy = false;

// ---------- connection status ----------

async function refreshStatus() {
  let daemon = null;
  try {
    const ctl = new AbortController();
    setTimeout(() => ctl.abort(), 1500);
    const r = await fetch('http://127.0.0.1:10087/status', { signal: ctl.signal, cache: 'no-store' });
    daemon = await r.json();
  } catch {}

  if (daemon && daemon.ok) {
    setDot('dot-daemon', 'ok');
    $('txt-daemon').textContent = `守护进程：运行中 v${daemon.version}（pid ${daemon.pid}）`;
    $('row-wake').style.display = 'none';
  } else {
    setDot('dot-daemon', 'bad');
    $('txt-daemon').textContent = '守护进程：未运行';
    $('row-wake').style.display = 'flex';
  }

  let wsState = { connected: false };
  try {
    wsState = await chrome.runtime.sendMessage({ type: 'ws_state' }) || wsState;
  } catch {}
  if (daemon && daemon.ok && wsState.connected) {
    setDot('dot-ws', 'ok');
    $('txt-ws').textContent = '扩展连接：已连接';
  } else if (daemon && daemon.ok) {
    setDot('dot-ws', 'bad');
    $('txt-ws').textContent = '扩展连接：未连接（自动重连中）';
  } else {
    setDot('dot-ws', '');
    $('txt-ws').textContent = '扩展连接：等待守护进程';
  }
}

async function wake() {
  const btn = $('btn-wake');
  btn.disabled = true;
  $('wake-note').textContent = '正在拉起…';
  try {
    const res = await chrome.runtime.sendMessage({ type: 'wake_daemon' });
    if (res && res.ok) {
      $('wake-note').textContent = res.already_running ? '已在运行' : '已启动';
    } else {
      $('wake-note').textContent = '失败：' + ((res && (res.error || res.throttled && '稍后重试')) || '未知原因');
    }
  } catch (e) {
    $('wake-note').textContent = '失败：' + e;
  }
  btn.disabled = false;
  await refreshStatus();
}
$('btn-wake').addEventListener('click', wake);

// ---------- bottom-right live coordinates overlay (toggle) ----------

const INTERNAL = /^(chrome|edge|about|devtools|view-source|chrome-extension):/i;

async function pageTab() {
  // Normal popup case: the active tab under this popup. Fallback (panel also
  // opens as a regular tab during testing): the most recent real web page.
  const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (active && !INTERNAL.test(active.url || '')) return active;
  const all = await chrome.tabs.query({ url: ['http://*/*', 'https://*/*'] });
  all.sort((a, b) => (b.lastAccessed || 0) - (a.lastAccessed || 0));
  return all[0] || null;
}

const ovToggle = $('ov-toggle');
chrome.storage.local.get({ overlay: false }, (v) => { ovToggle.checked = !!v.overlay; });
ovToggle.addEventListener('change', async () => {
  await chrome.storage.local.set({ overlay: ovToggle.checked });
  // Make it appear immediately on the current page even if the content
  // script predates this browser session (it re-reads storage on inject).
  if (ovToggle.checked) {
    try {
      const tab = await pageTab();
      if (tab) {
        try {
          await chrome.tabs.sendMessage(tab.id, { type: 'ping_cs' });
        } catch {
          await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['content.js'] });
        }
      }
    } catch {}
  }
});

// ---------- move to css coordinate ----------

$('moveform').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  if (moveBusy) return;
  const x = parseFloat($('mx').value);
  const y = parseFloat($('my').value);
  if (!Number.isFinite(x) || !Number.isFinite(y)) {
    show('err', '请输入数字坐标');
    return;
  }
  moveBusy = true;
  $('btn-move').disabled = true;
  show('', '移动中…');
  try {
    const r = await fetch('http://127.0.0.1:10087/command', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
	body: JSON.stringify({ action: 'move_css', args: { x, y, ext_id: chrome.runtime.id + '@' + BROWSER } }),
    });
    const d = await r.json();
    if (d.ok && d.landing_verified) {
      show('ok', `✔ 已移动到 (${d.css_target.x}, ${d.css_target.y})，页面确认落点残差 ${(d.landing_residual_px ?? 0).toFixed(2)}px`);
    } else if (d.ok) {
      show('err', '⚠ 光标已移动，但页面没有反馈（落点可能被浏览器 UI 遮挡）：' + (d.error || ''));
    } else {
      show('err', '✘ ' + (d.error || 'move_css 失败'));
    }
  } catch (e) {
    show('err', '✘ 无法连接守护进程：' + e);
  }
  moveBusy = false;
  $('btn-move').disabled = false;
});

function show(cls, text) {
  const el = $('result');
  el.className = cls;
  el.textContent = text;
}

// Surface side-panel handoff errors (diagnostics for browser differences).
chrome.storage.local.get({ panelErr: '' }, (v) => {
  if (v.panelErr) show('err', '侧栏诊断: ' + v.panelErr);
});

refreshStatus();
setInterval(refreshStatus, 1500);
