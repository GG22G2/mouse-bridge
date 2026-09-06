// Mouse Bridge — content script.
// Tracks real mousemove positions (used by the daemon's feedback
// calibration) and resolves/locates elements for the background worker.

(() => {
  if (window.__mouseBridgeInstalled) return;
  window.__mouseBridgeInstalled = true;

  // ---- real cursor tracking ----
  const lastMouse = { x: 0, y: 0, t: -1e9 };
  window.addEventListener(
    'mousemove',
    (e) => {
      lastMouse.x = e.clientX;
      lastMouse.y = e.clientY;
      lastMouse.t = performance.now();
      window.__mbEventCount = (window.__mbEventCount || 0) + 1;
    },
    { capture: true, passive: true }
  );

  // ---- selector resolution ----

  function isVisible(el) {
    const r = el.getBoundingClientRect();
    const st = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none';
  }

  function resolveElement(selector) {
    const sel = String(selector);
    if (sel.startsWith('xpath=')) {
      return document.evaluate(sel.slice(6), document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
    }
    if ((sel.startsWith('/') || sel.startsWith('(')) && !sel.startsWith('//[@')) {
      return document.evaluate(sel, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
    }
    if (sel.startsWith('text=')) {
      return findByText(sel.slice(5));
    }
    return document.querySelector(sel);
  }

  // Deepest visible element whose trimmed text contains the needle.
  function findByText(needle) {
    const want = needle.trim().toLowerCase();
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
    let best = null;
    let bestLen = Infinity;
    while (walker.nextNode()) {
      const el = walker.currentNode;
      if (!isVisible(el)) continue;
      const ownText = Array.from(el.childNodes)
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => n.textContent)
        .join(' ')
        .trim()
        .toLowerCase();
      if (ownText.includes(want) && ownText.length < bestLen) {
        best = el;
        bestLen = ownText.length;
      }
    }
    return best;
  }

  // Pick the point a real user would hit: center first, then a small grid
  // of fallbacks; verify with elementFromPoint that the element would
  // actually receive the click.
  function pickPoint(el) {
    const r = el.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const fracs = [0, -0.25, 0.25];
    const candidates = [];
    for (const fy of fracs) {
      for (const fx of fracs) {
        const x = r.left + r.width * (0.5 + fx);
        const y = r.top + r.height * (0.5 + fy);
        const cx = Math.min(Math.max(x, 2), vw - 2);
        const cy = Math.min(Math.max(y, 2), vh - 2);
        if (candidates.some((c) => c.x === cx && c.y === cy)) continue;
        candidates.push({ x: cx, y: cy });
      }
    }
    candidates.sort((a, b) => dist2(a) - dist2(b));
    function dist2(c) {
      const dx = c.x - (r.left + r.width / 2);
      const dy = c.y - (r.top + r.height / 2);
      return dx * dx + dy * dy;
    }
    for (const c of candidates) {
      const hit = document.elementFromPoint(c.x, c.y);
      if (hit && (hit === el || el.contains(hit) || hit.contains(el))) {
        return { point: c, occluded: false, occluder: null };
      } else if (hit && !c.occluder) {
        var occluder = hit;
      }
    }
    return { point: candidates[0], occluded: true, occluder: occluder || null };
  }

  function elementInfo(el) {
    const info = {
      tag: el.tagName ? el.tagName.toLowerCase() : '?',
      id: el.id || null,
      classes: el.classList ? Array.from(el.classList).slice(0, 4) : [],
      text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 80),
    };
    if (el.getAttribute) {
      info.role = el.getAttribute('role');
      info.href = el.getAttribute('href') || null;
      info.name = el.getAttribute('name') || null;
      info.type = el.getAttribute('type') || null;
    }
    return info;
  }

  const settle = (ms) => new Promise((r) => setTimeout(r, ms));

  async function locate(selector) {
    let el = resolveElement(selector);
    if (!el) throw new Error('element not found: ' + selector);
    if (el.nodeType !== 1) el = el.parentElement;
    if (!isVisible(el)) {
      // Scroll it into view and re-check before giving up.
      try { el.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' }); } catch {}
      await settle(300);
      if (!isVisible(el)) throw new Error('element is not visible (zero size or hidden): ' + selector);
    }

    // Always center the target, then let layout/scroll settle.
    try { el.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' }); } catch {}
    await settle(260);
    if (document.visibilityState === 'visible') {
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    }

    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) throw new Error('element has zero size: ' + selector);
    if (r.right < 0 || r.bottom < 0 || r.left > innerWidth || r.top > innerHeight) {
      throw new Error('element outside viewport after scroll: ' + selector);
    }

    const pick = pickPoint(el);
    const occ = pick.occluder;
    return {
      ok: true,
      css: { x: +pick.point.x.toFixed(2), y: +pick.point.y.toFixed(2) },
      rect: { x: r.left, y: r.top, w: r.width, h: r.height },
      occluded: pick.occluded,
      element: elementInfo(el),
      occluder: occ ? elementInfo(occ) : null,
      metrics: {
        dpr: window.devicePixelRatio,
        innerW: window.innerWidth,
        innerH: window.innerHeight,
        outerW: window.outerWidth,
        outerH: window.outerHeight,
        screenX: window.screenX,
        screenY: window.screenY,
        screenW: screen.width,
        screenH: screen.height,
      },
    };
  }

  // ---- message handling ----

  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    (async () => {
      switch (msg && msg.type) {
        case 'ping_cs':
          sendResponse({ ok: true });
          break;
        case 'locate':
          try {
            sendResponse(await locate(msg.selector));
          } catch (e) {
            sendResponse({ ok: false, error: String((e && e.message) || e) });
          }
          break;
        case 'measure': {
          const age = performance.now() - lastMouse.t;
          sendResponse({
            ok: true,
            fresh: age >= 0 && age < 2000,
            x: lastMouse.x,
            y: lastMouse.y,
            age_ms: Math.round(age),
            dpr: window.devicePixelRatio,
            href: location.href.slice(0, 60),
            events_seen: window.__mbEventCount || 0,
          });
          break;
        }
        default:
          sendResponse({ ok: false, error: 'unknown message type' });
      }
    })();
    return true; // async response
  });
})();
