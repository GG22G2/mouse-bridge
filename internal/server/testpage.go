package server

import (
	"fmt"
	"net/http"
)

// handleTestPage serves an instrumented page for verifying real mouse
// interaction: isTrusted events, landing coordinates, hover, drag & drop.
func handleTestPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, testPageHTML)
}

const testPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>Mouse Bridge 测试页</title>
<style>
  * { box-sizing: border-box; }
  body { font-family: "Segoe UI", "Microsoft YaHei", sans-serif; margin: 0; background:#f5f6fa; color:#222; }
  #hdr { position: sticky; top: 0; background:#1e2a3a; color:#fff; padding:10px 16px; font-size:13px; z-index:10;}
  #hdr b { color:#7fd1ff; }
  main { max-width: 980px; margin: 24px auto; padding: 0 16px 400px; }
  .card { background:#fff; border-radius:10px; padding:18px 20px; margin-bottom:20px; box-shadow:0 1px 4px rgba(0,0,0,.08); }
  h2 { margin:0 0 10px; font-size:16px; }
  .target { display:inline-block; padding:14px 26px; border-radius:8px; border:2px solid #4a6cf7;
            background:#eef1ff; font-size:15px; cursor:pointer; user-select:none; }
  .target:hover { background:#4a6cf7; color:#fff; }
  #hover-test.hovered { background:#2bb673 !important; color:#fff !important; }
  #tiny-target { padding:0; width:14px; height:14px; border-radius:3px; vertical-align:middle; }
  #right-zone { border:2px dashed #d9534f; background:#fff5f5; padding:30px; text-align:center; border-radius:8px; }
  #drag-src { display:inline-block; background:#ffa726; color:#fff; padding:14px 22px; border-radius:8px; cursor:grab; font-size:15px;}
  #drop-zone { display:inline-block; border:2px dashed #888; border-radius:8px; padding:34px 40px; margin-left:40px;
               vertical-align:middle; color:#888; }
  #drop-zone.over { border-color:#2bb673; background:#eafff3; }
  #drop-zone.dropped { border-color:#2bb673; background:#d9f7e7; color:#1d7a4f; }
  #trace { width:100%; height:180px; background:#fff; border:1px solid #ccc; border-radius:8px; touch-action:none; }
  #log { font: 12px/1.5 Consolas, monospace; background:#111; color:#9f9; padding:12px; border-radius:8px;
         max-height:220px; overflow:auto; white-space:pre-wrap; }
  .pos { position:fixed; right:12px; bottom:12px; background:rgba(0,0,0,.75); color:#0f0; font:12px Consolas,monospace;
         padding:6px 10px; border-radius:6px; z-index:99; }
  .gap { height:900px; background:repeating-linear-gradient(45deg,#fafafa,#fafafa 12px,#f0f0f0 12px,#f0f0f0 24px);
         border-radius:8px; margin:10px 0; display:flex; align-items:center; justify-content:center; color:#aaa; }
</style>
</head>
<body>
<div id="hdr">Mouse Bridge 测试页 · devicePixelRatio=<b id="hdpr">?</b> · innerW×H=<b id="hinner">?</b> · screenX,Y=<b id="hsxy">?</b> · zoom=<b id="hzoom">?</b></div>
<main>
  <div class="card">
    <h2>1. 左键点击 <span style="font-weight:normal;color:#888">(记录 mousedown/up/click + isTrusted)</span></h2>
    <button id="btn-left" class="target">左键目标按钮</button>
    <span style="margin-left:16px">精准小目标：</span><button id="tiny-target" class="target" title="14x14px"></button>
    <span id="left-result" style="margin-left:12px;color:#2bb673;font-weight:600"></span>
  </div>

  <div class="card">
    <h2>2. 右键点击 <span style="font-weight:normal;color:#888">(记录 contextmenu + isTrusted)</span></h2>
    <div id="right-zone">在此区域点右键</div>
    <span id="right-result" style="margin-left:12px;color:#d9534f;font-weight:600"></span>
  </div>

  <div class="card">
    <h2>3. 拖拽（HTML5 Drag &amp; Drop）</h2>
    <div id="drag-src" draggable="true">拖我 →</div>
    <div id="drop-zone">放下区域</div>
    <span id="drop-result" style="margin-left:12px;color:#2bb673;font-weight:600"></span>
  </div>

  <div class="card">
    <h2>4. 按住拖动轨迹 <span style="font-weight:normal;color:#888">(在画布上按住左键拖动，验证连续真实移动)</span></h2>
    <canvas id="trace" width="940" height="180"></canvas>
    <span id="trace-result" style="color:#4a6cf7;font-weight:600"></span>
  </div>

  <div class="card">
    <h2>5. 悬停验证 <span style="font-weight:normal;color:#888">(真实移动才会触发 :hover)</span></h2>
    <button id="hover-test" class="target">移动到我上面</button>
    <span id="hover-result" style="margin-left:12px;color:#888"></span>
  </div>

  <div class="gap">↓ 滚动区域（测试 scrollIntoView 定位）↓</div>

  <div class="card" id="deep-card">
    <h2>6. 深处目标</h2>
    <button id="deep-target" class="target">页面深处的按钮</button>
    <span id="deep-result" style="margin-left:12px;color:#2bb673;font-weight:600"></span>
  </div>

  <div class="card">
    <h2>事件日志（最近 30 条）</h2>
    <div id="log"></div>
  </div>
</main>
<div class="pos" id="pos">x=0 y=0</div>
<script>
(function () {
  var logEl = document.getElementById('log');
  window.__eventLog = [];
  window.__mouseStats = { moveEvents: 0, lastMove: null };

  function rec(type, e) {
    var t = e.target && e.target.id ? e.target.id : (e.target ? e.target.tagName.toLowerCase() : '?');
    var entry = {
      ts: Date.now(), type: type, trusted: !!e.isTrusted, target: t,
      x: Math.round(e.clientX || 0), y: Math.round(e.clientY || 0),
      sx: Math.round(e.screenX || 0), sy: Math.round(e.screenY || 0),
      button: e.button === undefined ? null : e.button,
      buttons: e.buttons === undefined ? null : e.buttons,
      movementX: e.movementX || 0, movementY: e.movementY || 0,
      detail: e.detail || 0, text: (e.target && e.target.textContent || '').trim().slice(0, 24)
    };
    window.__eventLog.push(entry);
    if (window.__eventLog.length > 3000) window.__eventLog.shift();
    render();
  }
  function render() {
    var last = window.__eventLog.slice(-30);
    var lines = last.map(function (e) {
      return e.ts.toString().slice(-6) + ' ' + pad(e.type, 12) + ' trusted=' + pad(String(e.trusted), 5)
        + ' tgt=' + pad(e.target, 12) + ' (' + e.x + ',' + e.y + ') scr(' + e.sx + ',' + e.sy + ')'
        + ' btn=' + pad(String(e.button), 4) + ' btns=' + pad(String(e.buttons), 4) + ' ' + e.text;
    });
    logEl.textContent = lines.join('\n');
  }
  function pad(s, n) { s = String(s); while (s.length < n) s += ' '; return s; }
  window.__clearLog = function () { window.__eventLog = []; render(); };

  ['mousedown','mouseup','click','dblclick','contextmenu','wheel',
   'dragstart','drag','dragenter','dragover','dragleave','drop','dragend'].forEach(function (t) {
    document.addEventListener(t, function (e) { rec(t, e); }, true);
  });
  document.addEventListener('mousemove', function (e) {
    window.__mouseStats.moveEvents++;
    window.__mouseStats.lastMove = { x: e.clientX, y: e.clientY, t: performance.now(), trusted: !!e.isTrusted };
    document.getElementById('pos').textContent = 'css(' + e.clientX + ',' + e.clientY + ')  dev('
      + Math.round(e.clientX * devicePixelRatio) + ',' + Math.round(e.clientY * devicePixelRatio) + ')';
  }, true);

  // header info
  function hdr() {
    document.getElementById('hdpr').textContent = devicePixelRatio;
    document.getElementById('hinner').textContent = innerWidth + 'x' + innerHeight;
    document.getElementById('hsxy').textContent = screenX + ',' + screenY;
  }
  hdr(); addEventListener('resize', hdr);

  document.getElementById('btn-left').addEventListener('click', function () {
    document.getElementById('left-result').textContent = '✔ 已收到真实点击 ' + new Date().toLocaleTimeString();
  });
  document.getElementById('tiny-target').addEventListener('click', function () {
    document.getElementById('left-result').textContent = '✔ 命中 14px 小目标';
  });
  document.getElementById('right-zone').addEventListener('contextmenu', function (e) {
    e.preventDefault();
    document.getElementById('right-result').textContent = '✔ contextmenu isTrusted=' + e.isTrusted;
  });
  var dz = document.getElementById('drop-zone');
  dz.addEventListener('dragover', function (e) { e.preventDefault(); dz.classList.add('over'); });
  dz.addEventListener('dragleave', function () { dz.classList.remove('over'); });
  dz.addEventListener('drop', function (e) {
    e.preventDefault(); dz.classList.remove('over'); dz.classList.add('dropped');
    dz.textContent = '✔ 已放入'; document.getElementById('drop-result').textContent = '✔ drop isTrusted=' + e.isTrusted;
  });

  // button-held trace canvas
  var cv = document.getElementById('trace'), ctx = cv.getContext('2d'), drawing = false, n = 0;
  ctx.lineWidth = 2; ctx.strokeStyle = '#4a6cf7'; ctx.lineCap = 'round';
  cv.addEventListener('mousedown', function (e) {
    drawing = true; n = 0; var r = cv.getBoundingClientRect();
    ctx.clearRect(0, 0, cv.width, cv.height); ctx.beginPath(); ctx.moveTo(e.clientX - r.left, e.clientY - r.top);
  });
  cv.addEventListener('mousemove', function (e) {
    if (!drawing) return; var r = cv.getBoundingClientRect();
    ctx.lineTo(e.clientX - r.left, e.clientY - r.top); ctx.stroke(); n++;
  });
  addEventListener('mouseup', function () {
    if (drawing) { drawing = false; document.getElementById('trace-result').textContent = '轨迹点数=' + n; }
  });

  var ht = document.getElementById('hover-test');
  ht.addEventListener('mouseenter', function () { ht.classList.add('hovered'); });
  ht.addEventListener('mouseleave', function () { ht.classList.remove('hovered'); });
  document.getElementById('deep-target').addEventListener('click', function () {
    document.getElementById('deep-result').textContent = '✔ 深处按钮被真实点击';
  });
})();
</script>
</body>
</html>`
