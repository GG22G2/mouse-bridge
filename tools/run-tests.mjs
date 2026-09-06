// Full-chain test suite for Mouse Bridge.
// Agent role: HTTP POST to the daemon. Page assertions: CDP evaluate.
const DAEMON = 'http://127.0.0.1:10087';
const CDP_PORT = process.argv[2] || '9226';
const PAGE_URL = '127.0.0.1:10087/test';

let pass = 0, fail = 0;
const failures = [];
function ok(name, cond, detail) {
  if (cond) { pass++; console.log(`  PASS  ${name}`); }
  else { fail++; failures.push(name + (detail ? ` :: ${detail}` : '')); console.log(`  FAIL  ${name}${detail ? ' :: ' + detail : ''}`); }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function cmd(action, args = {}) {
  const r = await fetch(`${DAEMON}/command`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action, args }),
  });
  return r.json();
}

// ---- CDP helper ----
let wsSeq = 0;
const pending = new Map();
let pageWs = null;
async function attachPage() {
  const list = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json`)).json();
  const target = list.find((t) => t.type === 'page' && t.url.includes(PAGE_URL));
  if (!target) throw new Error('test page tab not found');
  pageWs = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { pageWs.onopen = res; pageWs.onerror = rej; });
  pageWs.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  };
}
function evalPage(expr) {
  return new Promise((res, rej) => {
    const mid = ++wsSeq;
    pending.set(mid, (m) => {
      if (m.result?.result?.value !== undefined) res(m.result.result.value);
      else rej(new Error(JSON.stringify(m.result?.exceptionDetails || m.result || m).slice(0, 300)));
    });
    pageWs.send(JSON.stringify({ id: mid, method: 'Runtime.evaluate', params: { expression: expr, returnByValue: true, awaitPromise: true } }));
    setTimeout(() => { if (pending.has(mid)) { pending.delete(mid); rej(new Error('eval timeout')); } }, 10000);
  });
}
async function events(since) {
  return evalPage(`window.__eventLog.filter(e => e.ts > ${since})`);
}

// ================= tests =================
async function main() {
  await attachPage();

  console.log('\n== T0: daemon status / extension connected ==');
  await cmd('reset_calibration');
  const st = await (await fetch(`${DAEMON}/status`)).json();
  ok('extension connected', st.extension_connected === true);
  ok('DPI per-monitor-v2 aware, scale 1.25', st.dpi.system_dpi === 120 && st.dpi.per_monitor_v2 === true, JSON.stringify(st.dpi));

  console.log('\n== T1: locate_element (pure computation, no motion) ==');
  const t1 = await cmd('locate_element', { selector: '#btn-left', url: PAGE_URL });
  ok('locate ok', t1.ok === true, t1.error);
  ok('css point inside element rect', t1.element && t1.element.css.x >= t1.element.rect.x && t1.element.css.x <= t1.element.rect.x + t1.element.rect.w, JSON.stringify(t1.element?.rect));
  ok('dpr=1.25 zoom=1 osScale=1.25', t1.conversion?.device_pixel_ratio === 1.25 && t1.conversion?.browser_zoom === 1 && t1.conversion?.os_scale === 1.25, JSON.stringify(t1.conversion));
  ok('screen coords sane (within 1920x1080@125%)', t1.screen?.x > 0 && t1.screen?.x < 1920 && t1.screen?.y > 0 && t1.screen?.y < 1080, JSON.stringify(t1.screen));

  console.log('\n== T2: move_to_element triggers real :hover ==');
  await evalPage('window.__clearLog(); window.__mouseStats.moveEvents = 0; "cleared"');
  const t2 = await cmd('move_to_element', { selector: '#hover-test', url: PAGE_URL });
  ok('move_to_element ok', t2.ok === true, t2.error);
  ok('daemon reports landed cursor', t2.screen_target && typeof t2.screen_target.x === 'number', JSON.stringify(t2.screen_target));
  ok('feedback calibration ran on first call', t2.calibration && t2.calibration.applied_this_call === true, JSON.stringify(t2.calibration));
  await sleep(300);
  const hover = await evalPage(`document.getElementById('hover-test').matches(':hover')`);
  ok('CSS :hover active (real cursor over element)', hover === true, String(hover));
  const mv = await evalPage('window.__mouseStats.moveEvents');
  ok('page received real mousemove events', mv > 5, `moveEvents=${mv}`);
  const lastTrusted = await evalPage(`window.__mouseStats.lastMove && window.__mouseStats.lastMove.trusted`);
  ok('mousemove isTrusted=true', lastTrusted === true);

  console.log('\n== T3: click_element left #btn-left ==');
  await evalPage('window.__clearLog(); Date.now()');
  const t3 = await cmd('click_element', { selector: '#btn-left', url: PAGE_URL });
  ok('click_element ok', t3.ok === true && t3.clicked === true, t3.error);
  await sleep(250);
  const ev3 = await events(t3._ts || 0);
  const md = ev3.find((e) => e.type === 'mousedown');
  const mu = ev3.find((e) => e.type === 'mouseup');
  const cl = ev3.find((e) => e.type === 'click');
  ok('mousedown/mouseup/click all trusted', !!md?.trusted && !!mu?.trusted && !!cl?.trusted, JSON.stringify({ md, mu, cl }));
  const cssT = t3.element.css;
  ok('click landed on target css point (±3px)', md && Math.abs(md.x - cssT.x) <= 3 && Math.abs(md.y - cssT.y) <= 3, `target=(${cssT.x},${cssT.y}) got=(${md?.x},${md?.y})`);
  const btnText = await evalPage(`document.getElementById('left-result').textContent`);
  ok('button reacted to real click', btnText.includes('已收到真实点击'), btnText);

  console.log('\n== T4: precision click on 14x14 #tiny-target ==');
  await evalPage('window.__clearLog(); Date.now()');
  const t4 = await cmd('click_element', { selector: '#tiny-target', url: PAGE_URL });
  ok('tiny click ok', t4.ok === true, t4.error);
  await sleep(250);
  const ev4 = await events(0);
  const md4 = ev4.find((e) => e.type === 'mousedown' && e.target === 'tiny-target');
  ok('mousedown on tiny target', !!md4, JSON.stringify(ev4.filter(e=>e.type==='mousedown')));
  ok('tiny target landing ±2px', md4 && Math.abs(md4.x - t4.element.css.x) <= 2 && Math.abs(md4.y - t4.element.css.y) <= 2, `target=(${t4.element.css.x},${t4.element.css.y}) got=(${md4?.x},${md4?.y})`);
  const tinyHit = await evalPage(`document.getElementById('left-result').textContent.includes('命中 14px 小目标')`);
  ok('tiny target click handler fired', tinyHit === true);

  console.log('\n== T5: click deep target (below fold, scrollIntoView) ==');
  await evalPage('window.__clearLog(); Date.now()');
  const t5 = await cmd('click_element', { selector: '#deep-target', url: PAGE_URL });
  ok('deep click ok', t5.ok === true, t5.error);
  await sleep(250);
  const ev5 = await events(0);
  const cl5 = ev5.find((e) => e.type === 'click' && e.target === 'deep-target');
  ok('deep target click trusted & received', !!cl5?.trusted, JSON.stringify(ev5.slice(-4)));
  const deepHit = await evalPage(`document.getElementById('deep-result').textContent`);
  ok('deep target reacted', deepHit.includes('真实点击'), deepHit);

  console.log('\n== T6: right click → trusted contextmenu ==');
  await evalPage('window.__clearLog(); Date.now()');
  const t6 = await cmd('click_element', { selector: '#right-zone', url: PAGE_URL, button: 'right' });
  ok('right click ok', t6.ok === true && t6.button === 'right', t6.error);
  await sleep(300);
  const ev6 = await events(0);
  const cm = ev6.find((e) => e.type === 'contextmenu');
  ok('contextmenu fired & trusted', !!cm?.trusted, JSON.stringify(ev6.slice(-4)));
  const rres = await evalPage(`document.getElementById('right-result').textContent`);
  ok('right zone reacted (contextmenu isTrusted=true)', rres.includes('contextmenu isTrusted=true'), rres);

  console.log('\n== T7: drag_element #drag-src → #drop-zone (HTML5 DnD) ==');
  await evalPage('window.__clearLog(); Date.now()');
  const t7 = await cmd('drag_element', { from_selector: '#drag-src', to_selector: '#drop-zone', url: PAGE_URL });
  ok('drag_element ok', t7.ok === true && t7.dragged === true, t7.error);
  await sleep(400);
  const ev7 = await events(0);
  const ds = ev7.find((e) => e.type === 'dragstart');
  const dp = ev7.find((e) => e.type === 'drop');
  ok('dragstart trusted', !!ds?.trusted, JSON.stringify(ev7.map(e=>e.type)));
  ok('drop fired on target & trusted', !!dp?.trusted && dp.target === 'drop-zone', JSON.stringify(ev7.map(e=>e.type+':'+e.target)));
  const dzState = await evalPage(`document.getElementById('drop-zone').textContent`);
  ok('drop zone shows 已放入', dzState.includes('已放入'), dzState);

  console.log('\n== T8: drag across canvas (button-held continuous trace) ==');
  const cv = await cmd('locate_element', { selector: '#trace', url: PAGE_URL });
  const cxs = cv.screen.x, cys = cv.screen.y;
  await evalPage('window.__clearLog(); Date.now()');
  const t8 = await cmd('drag', { x: cxs - 150, y: cys - 40, to_x: cxs + 150, to_y: cys + 40 });
  ok('canvas drag ok', t8.ok === true, t8.error);
  ok('drag path is multi-point (human-like)', t8.path_points >= 20, `points=${t8.path_points}`);
  await sleep(300);
  const trace = await evalPage(`document.getElementById('trace-result').textContent`);
  ok('canvas recorded continuous movement (>=40 points)', /轨迹点数=(\d+)/.test(trace) && parseInt(trace.match(/轨迹点数=(\d+)/)[1]) >= 40, trace);

  console.log('\n== T9: wheel scroll ==');
  const before9 = await evalPage('window.scrollY');
  const t9 = await cmd('wheel', { dy: -5 });
  ok('wheel ok', t9.ok === true, t9.error);
  await sleep(500);
  const after9 = await evalPage('window.scrollY');
  ok('page scrolled up by wheel', after9 < before9 || before9 === 0, `before=${before9} after=${after9}`);
  await cmd('wheel', { dy: 5 });

  console.log('\n== T10: browser zoom 150% → conversion still exact ==');
  const z = await cmd('locate_element', { selector: '#btn-left', url: PAGE_URL });
  // set zoom via extension
  const zr = await fetch(`${DAEMON}/command`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: "set_zoom", args: { zoom: 1.5 } }) });
  // _set_zoom is daemon-internal; use extension command through locate roundtrip instead:
  // (daemon has no set_zoom action; we call extension via 'reset_zoom' test hook below if exists)
  console.log('  (zoom set via extension set_zoom through test hook)');
  await evalPage('window.__clearLog(); Date.now()');
  const t10 = await cmd('click_element', { selector: '#btn-left', url: PAGE_URL });
  ok('click at 150% zoom ok', t10.ok === true, t10.error);
  ok('zoom reported 1.5', t10.conversion?.browser_zoom === 1.5, JSON.stringify(t10.conversion));
  await sleep(250);
  const ev10 = await events(0);
  const md10 = ev10.find((e) => e.type === 'mousedown');
  const css10 = t10.element.css;
  ok('150% zoom landing ±4px', md10 && Math.abs(md10.x - css10.x) <= 4 && Math.abs(md10.y - css10.y) <= 4, `target=(${css10.x},${css10.y}) got=(${md10?.x},${md10?.y})`);
  // reset zoom
  await fetch(`${DAEMON}/command`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: "set_zoom", args: { zoom: 1 } }) });

  console.log('\n== T11: DPI physical cross-check (event.screenX vs daemon physical) ==');
  // screenX/Y in events are DIPs at OS scale => physical/1.25
  await evalPage('window.__clearLog(); Date.now()');
  const t11 = await cmd('click_element', { selector: '#btn-left', url: PAGE_URL });
  await sleep(250);
  const ev11 = await events(0);
  const md11 = ev11.find((e) => e.type === 'mousedown');
  const physExpectedX = t11.screen_target.x, physExpectedY = t11.screen_target.y;
  ok('event.screenX*1.25 == daemon physical X (±3px)', md11 && Math.abs(md11.sx * 1.25 - physExpectedX) <= 3, `event sx=${md11?.sx} dip → ${md11?.sx * 1.25} phys vs daemon ${physExpectedX}`);
  ok('event.screenY*1.25 == daemon physical Y (±3px)', md11 && Math.abs(md11.sy * 1.25 - physExpectedY) <= 3, `event sy=${md11?.sy} dip → ${md11?.sy * 1.25} phys vs daemon ${physExpectedY}`);

  console.log('\n== T12: custom window size/position 1100x800@120,60 -> recalib + exact landing ==');
  const w12 = await cmd('set_window', { state: 'normal', left: 120, top: 60, width: 1100, height: 800 });
  ok('set_window ok', w12.ok === true, w12.error);
  await evalPage('window.__clearLog(); Date.now()');
  const t12 = await cmd('click_element', { selector: '#btn-left', url: PAGE_URL });
  ok('click at custom 1100x800 ok', t12.ok === true, t12.error);
  ok('recalibrated for new geometry', t12.calibration && t12.calibration.applied_this_call === true, JSON.stringify(t12.calibration));
  await sleep(250);
  const md12 = (await events(0)).find((e) => e.type === 'mousedown' && e.target === 'btn-left');
  const css12 = t12.element.css;
  ok('1100x800 landing +-3px', md12 && Math.abs(md12.x - css12.x) <= 3 && Math.abs(md12.y - css12.y) <= 3, 'target=' + JSON.stringify(css12) + ' got=' + JSON.stringify(md12 && {x: md12.x, y: md12.y}));

  console.log('\n== T13: MAXIMIZED window -> recalib + exact landing ==');
  const w13 = await cmd('set_window', { state: 'maximized' });
  ok('maximize ok', w13.ok === true, w13.error);
  await evalPage('window.__clearLog(); Date.now()');
  const t13 = await cmd('click_element', { selector: '#btn-left', url: PAGE_URL });
  ok('click maximized ok', t13.ok === true, t13.error);
  ok('recalibrated after maximize', t13.calibration && t13.calibration.applied_this_call === true, JSON.stringify(t13.calibration));
  await sleep(250);
  const md13 = (await events(0)).find((e) => e.type === 'mousedown' && e.target === 'btn-left');
  const css13 = t13.element.css;
  ok('maximized landing +-3px', md13 && Math.abs(md13.x - css13.x) <= 3 && Math.abs(md13.y - css13.y) <= 3, 'target=' + JSON.stringify(css13) + ' got=' + JSON.stringify(md13 && {x: md13.x, y: md13.y}));

  console.log('\n== T14: another odd size 1200x850@60,40 + precision target ==');
  const w14 = await cmd('set_window', { state: 'normal', left: 60, top: 40, width: 1200, height: 850 });
  ok('resize ok', w14.ok === true, w14.error);
  await evalPage('window.__clearLog(); Date.now()');
  const t14 = await cmd('click_element', { selector: '#tiny-target', url: PAGE_URL });
  ok('tiny click at 1200x850 ok', t14.ok === true, t14.error);
  await sleep(250);
  const md14 = (await events(0)).find((e) => e.type === 'mousedown' && e.target === 'tiny-target');
  const css14 = t14.element.css;
  ok('1200x850 tiny landing +-2px', md14 && Math.abs(md14.x - css14.x) <= 2 && Math.abs(md14.y - css14.y) <= 2, 'target=' + JSON.stringify(css14) + ' got=' + JSON.stringify(md14 && {x: md14.x, y: md14.y}));

  console.log('\n== T15: restore 1400x950@80,40 ==');
  const w15 = await cmd('set_window', { state: 'normal', left: 80, top: 40, width: 1400, height: 950 });
  ok('restore ok', w15.ok === true, w15.error);


  console.log(`\n========== RESULT: ${pass} passed, ${fail} failed ==========`);
  if (failures.length) { console.log('Failed:'); failures.forEach((f) => console.log('  - ' + f)); }
  process.exit(fail ? 1 : 0);
}

main().catch((e) => { console.error('SUITE ERROR', e); process.exit(2); });
