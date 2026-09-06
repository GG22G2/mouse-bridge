package server

// Element-driven operations: ask the extension to locate a CSS element, then
// convert viewport CSS coordinates into physical desktop pixels.
//
// The conversion is NOT modeled from first principles — window rects, chrome
// heights, DWM borders, DPI and zoom multipliers are all estimates that drift
// with every Windows/browser update. Instead the mapping is MEASURED: the
// daemon sends the real cursor to a few known physical points inside the page,
// reads back the page's own mousemove clientX/Y for each, and fits the affine
// map  css = s·phys + t  per axis. Everything a model would have to guess —
// OS display scaling, browser zoom, title bar/toolbox height, invisible
// borders, window position and size — is absorbed into the fitted s and t.
//
// The fit is cached per window geometry (win id + rect + zoom + dpr); any
// move/resize/zoom/DPI change produces a different key and forces a fresh
// ~0.7s fit. Because every operation predicts from the fitted parameters
// (never from the previous operation's residual), errors cannot accumulate.

import (
	"fmt"
	"math"
	"time"

	"mousebridge/internal/mouse"
	"mousebridge/internal/win"
)

type cssPt struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type cssRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type locateResult struct {
	Tab      map[string]any `json:"tab"`
	Css      cssPt          `json:"css"`
	Rect     cssRect        `json:"rect"`
	Occluded bool           `json:"occluded"`
	Element  map[string]any `json:"element"`
	Metrics  struct {
		Dpr              float64 `json:"dpr"`
		InnerW           float64 `json:"innerW"`
		InnerH           float64 `json:"innerH"`
		OuterW           float64 `json:"outerW"`
		OuterH           float64 `json:"outerH"`
		ScreenX          float64 `json:"screenX"`
		ScreenY          float64 `json:"screenY"`
		ScreenW          float64 `json:"screenW"`
		ScreenH          float64 `json:"screenH"`
		VisualViewportOk bool    `json:"vvOk"`
	} `json:"metrics"`
	Win struct {
		Id     int     `json:"id"`
		Left   float64 `json:"left"`
		Top    float64 `json:"top"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		State  string  `json:"state"`
	} `json:"win"`
	Zoom float64 `json:"zoom"`
}

// physFromCss converts a viewport CSS point into physical desktop pixels via
// the first-principles model. This is ONLY used as the rough map that places
// calibration probes inside the viewport (±150px accuracy is plenty there)
// and as the blind fallback when allow_blind is requested without a fit.
//
//	osScale     = devicePixelRatio / browserZoom          (OS display scale)
//	contentDip  = window origin (DIP) + chrome offsets    (DIP space)
//	physical    = (contentDip + css*zoom) * osScale
func (l *locateResult) osScale() float64 {
	z := l.Zoom
	if z <= 0 {
		z = 1
	}
	s := l.Metrics.Dpr / z
	if s <= 0 {
		s = 1
	}
	return s
}

func (l *locateResult) physTarget(css cssPt) (float64, float64) {
	z := l.Zoom
	if z <= 0 {
		z = 1
	}
	s := l.osScale()
	contentLeft := l.Win.Left + (l.Win.Width-l.Metrics.InnerW*z)/2
	contentTop := l.Win.Top + (l.Win.Height - l.Metrics.InnerH*z)
	return (contentLeft + css.X*z) * s, (contentTop + css.Y*z) * s
}

// calibKey identifies the geometry an affine fit is valid for. Chrome height
// and borders are NOT in the key on purpose: they are absorbed by the fitted
// offsets. Everything that genuinely changes the mapping is: window identity,
// window rect, browser zoom, devicePixelRatio (OS scale × zoom) and the
// viewport size (toggling a bookmarks bar changes innerH without moving the
// outer rect).
func (l *locateResult) calibKey() string {
	return fmt.Sprintf("win%d|z%.4f|dpr%.4f|%.0f,%.0f,%.0f,%.0f|%.0fx%.0f",
		l.Win.Id, l.Zoom, l.Metrics.Dpr, l.Win.Left, l.Win.Top, l.Win.Width, l.Win.Height,
		l.Metrics.InnerW, l.Metrics.InnerH)
}

// locateOnPage asks the extension to resolve + measure the element,
// activating its tab first so real mouse input lands on it.
func (s *Server) locateOnPage(args map[string]any) (*locateResult, map[string]any, error) {
	resp, err := s.callExt("locate", map[string]any{
		"selector": args["selector"],
		"tab_id":   args["tab_id"],
		"url":      args["url"],
	}, s.extTimeout)
	if err != nil {
		return nil, nil, err
	}
	if ok, _ := resp["ok"].(bool); !ok {
		msg, _ := resp["error"].(string)
		if msg == "" {
			msg = "extension locate failed"
		}
		return nil, nil, fmt.Errorf("%s", msg)
	}
	raw, _ := resp["data"].(map[string]any)
	if raw == nil {
		return nil, nil, fmt.Errorf("extension returned no data")
	}
	// Re-marshal into typed struct.
	blob, _ := jsonMarshal(raw)
	var loc locateResult
	if err := jsonUnmarshal(blob, &loc); err != nil {
		return nil, nil, fmt.Errorf("bad locate payload: %v", err)
	}
	return &loc, raw, nil
}

// ---- measured affine calibration -----------------------------------------

// probePair is one (physical sent, css observed) measurement.
type probePair struct {
	P [2]float64
	C [2]float64
}

// affineFit is the measured viewport-css ↔ physical-desktop map:
//
//	cssX = SX*physX + TX,  cssY = SY*physY + TY
//
// SX/SY are measured css-per-physical ratios (they must come out at
// 1/devicePixelRatio, which cross-checks that we probed the right window);
// TX/TY absorb window position, chrome height and every other offset the
// old model had to guess.
type affineFit struct {
	SX, SY      float64
	TX, TY      float64
	Pairs       []probePair
	MaxResidual float64 // worst |predicted−observed| css px across the probe set
	FittedAt    time.Time
}

func (f *affineFit) physFromCss(x, y float64) (float64, float64) {
	return (x - f.TX) / f.SX, (y - f.TY) / f.SY
}

func (f *affineFit) cssFromPhys(x, y float64) (float64, float64) {
	return f.SX*x + f.TX, f.SY*y + f.TY
}

// fitAxis least-squares fits c = s·p + t for one axis. The slope is measured
// only when the probes are far enough apart to condition it; otherwise it
// falls back to the exact theoretical ratio 1/dpr and just the offset is
// fitted (tiny viewports, or probes accidentally aligned on one axis).
func fitAxis(pairs []probePair, idx int, dpr float64) (s, t float64) {
	fallback := 1.0
	if dpr > 0 {
		fallback = 1 / dpr
	}
	if len(pairs) == 0 {
		return fallback, 0
	}
	if len(pairs) == 1 {
		return fallback, pairs[0].C[idx] - fallback*pairs[0].P[idx]
	}
	var mp, mc float64
	for _, pr := range pairs {
		mp += pr.P[idx]
		mc += pr.C[idx]
	}
	mp /= float64(len(pairs))
	mc /= float64(len(pairs))
	var spp, spc float64
	for _, pr := range pairs {
		dp, dc := pr.P[idx]-mp, pr.C[idx]-mc
		spp += dp * dp
		spc += dp * dc
	}
	if spp < 120*120 || spc <= 0 {
		return fallback, mc - fallback*mp
	}
	s = spc / spp
	if s <= 0 || s > 2*fallback {
		return fallback, mc - fallback*mp
	}
	return s, mc - s*mp
}

func fitAffine(pairs []probePair, dpr float64) *affineFit {
	f := &affineFit{Pairs: pairs, FittedAt: time.Now()}
	f.SX, f.TX = fitAxis(pairs, 0, dpr)
	f.SY, f.TY = fitAxis(pairs, 1, dpr)
	return f
}

// measureTab asks the located tab where its last real mousemove was.
func (s *Server) measureTab(loc *locateResult) (x, y, ageMs, events float64, ok bool) {
	tabId, _ := loc.Tab["id"].(float64)
	resp, err := s.callExt("measure", map[string]any{"tab_id": int(tabId)}, 4*time.Second)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return 0, 0, 0, 0, false
	}
	x, _ = data["x"].(float64)
	y, _ = data["y"].(float64)
	ageMs, _ = data["age_ms"].(float64)
	events, _ = data["events_seen"].(float64)
	return x, y, ageMs, events, true
}

// probeCssAt is the strict readback protocol: teleport the cursor to a
// physical point with a small wake jiggle (so events fire even if the cursor
// already sat there), then wait for the page to emit a FRESH mousemove —
// confirmed by its event counter, not by hoping a timeout was long enough —
// and return the css position the page observed. Coalesced/stale events can
// never be mistaken for the landing: the counter only moves when a new event
// arrived, and the last event after the jiggle IS the landing.
//
// ok=false means the page never saw the cursor at all: the point is covered
// by browser UI (infobar, find bar, restore bubble) or belongs to another
// window — the daemon refuses to click blind in that case.
func (s *Server) probeCssAt(loc *locateResult, px, py float64) (cssPt, bool) {
	_, _, _, ev0, ok := s.measureTab(loc)
	if !ok {
		return cssPt{}, false
	}
	mouse.MoveRaw(px+3, py) // wake: force a transition even from an identical position
	time.Sleep(35 * time.Millisecond)
	mouse.MoveRaw(px, py) // land exactly on the probe point
	deadline := time.Now().Add(700 * time.Millisecond)
	for {
		time.Sleep(45 * time.Millisecond)
		x, y, age, ev, ok := s.measureTab(loc)
		if ok && ev > ev0 && age < 400 {
			time.Sleep(30 * time.Millisecond) // let the landing event supersede the wake event
			if x2, y2, age2, _, ok2 := s.measureTab(loc); ok2 && age2 < 400 {
				return cssPt{X: x2, Y: y2}, true
			}
			return cssPt{X: x, Y: y}, true
		}
		if time.Now().After(deadline) {
			return cssPt{}, false
		}
	}
}

// fitAffineCalibration probes 2 distant corner points + 1 random verification
// point and fits the per-axis affine map. Every inconsistency aborts with an
// error instead of storing a bad fit: no mousemove feedback, observed css too
// far from the aimed css (wrong window), measured slope contradicting
// 1/devicePixelRatio, or an unstable residual across probes.
func (s *Server) fitAffineCalibration(loc *locateResult) (*affineFit, error) {
	dpr := loc.Metrics.Dpr
	if dpr <= 0 {
		dpr = 1
	}
	vw, vh := loc.Metrics.InnerW, loc.Metrics.InnerH
	if vw < 120 || vh < 120 {
		return nil, fmt.Errorf("viewport too small to calibrate (%.0fx%.0f css)", vw, vh)
	}
	aims := []cssPt{
		{X: vw * 0.20, Y: vh * 0.28},
		{X: vw * 0.80, Y: vh * 0.74},
		{X: vw * (0.42 + randFrac()*0.16), Y: vh * (0.38 + randFrac()*0.20)},
	}
	var pairs []probePair
	for i, aim := range aims {
		px, py := loc.physTarget(aim) // rough map only places the probe; readback does the rest
		c, ok := s.probeCssAt(loc, px, py)
		if !ok {
			return nil, fmt.Errorf("probe %d/%d got no mousemove from the page — cursor is not over the live viewport (covered by browser UI? another window on top?)", i+1, len(aims))
		}
		if math.Abs(c.X-aim.X) > 150 || math.Abs(c.Y-aim.Y) > 150 {
			return nil, fmt.Errorf("probe %d: page saw css (%.0f,%.0f) but the probe aimed at (%.0f,%.0f) — wrong window or the page jumped underneath us", i+1, c.X, c.Y, aim.X, aim.Y)
		}
		pairs = append(pairs, probePair{P: [2]float64{px, py}, C: [2]float64{c.X, c.Y}})
		time.Sleep(50 * time.Millisecond)
	}

	// Fit from the two distant pairs, then let the held-out random point
	// grade the fit; a poor grade pulls it into a 3-point least squares.
	f := fitAffine(pairs[:2], dpr)
	gx, gy := f.cssFromPhys(pairs[2].P[0], pairs[2].P[1])
	if math.Hypot(gx-pairs[2].C[0], gy-pairs[2].C[1]) > 1.0 {
		f = fitAffine(pairs, dpr)
	}
	f.MaxResidual = 0
	for _, pr := range pairs {
		gx, gy := f.cssFromPhys(pr.P[0], pr.P[1])
		f.MaxResidual = math.Max(f.MaxResidual, math.Hypot(gx-pr.C[0], gy-pr.C[1]))
	}

	// css-per-physical must equal 1/devicePixelRatio; disagreeing means the
	// probed window is not the located one (or the geometry changed mid-fit).
	for _, ax := range []struct {
		s  float64
		nm string
	}{{f.SX, "x"}, {f.SY, "y"}} {
		if math.Abs(ax.s-1/dpr) > (1/dpr)*0.04 {
			return nil, fmt.Errorf("measured %s-axis scale %.4f contradicts devicePixelRatio %.3f (expected %.4f) — probed the wrong window?", ax.nm, ax.s, dpr, 1/dpr)
		}
	}
	if f.MaxResidual > 2.5 {
		return nil, fmt.Errorf("calibration unstable (%.2f css px residual across probes) — is the window moving or the page reloading?", f.MaxResidual)
	}
	return f, nil
}

func (s *Server) storeAffine(loc *locateResult, f *affineFit) {
	s.calibMu.Lock()
	defer s.calibMu.Unlock()
	s.affine[loc.calibKey()] = f
}

func (s *Server) cachedAffine(loc *locateResult) *affineFit {
	s.calibMu.Lock()
	defer s.calibMu.Unlock()
	return s.affine[loc.calibKey()]
}

// affineForOp returns the fit for this window geometry, fitting a fresh one
// (3 probe pairs, ~0.7s of visible cursor motion) on a cache miss. The info
// map travels into the response as "calibration".
func (s *Server) affineForOp(loc *locateResult) (*affineFit, map[string]any) {
	if f := s.cachedAffine(loc); f != nil {
		return f, map[string]any{
			"mode": "affine", "cached": true, "probes": len(f.Pairs),
			"fit_residual_px": f.MaxResidual,
			"sx": f.SX, "sy": f.SY, "tx": f.TX, "ty": f.TY,
		}
	}
	fresh, err := s.fitAffineCalibration(loc)
	if err != nil {
		return nil, map[string]any{"mode": "affine", "cached": false, "error": err.Error()}
	}
	s.storeAffine(loc, fresh)
	return fresh, map[string]any{
		"mode": "affine", "cached": false, "probes": len(fresh.Pairs),
		"fit_residual_px": fresh.MaxResidual,
		"sx": fresh.SX, "sy": fresh.SY, "tx": fresh.TX, "ty": fresh.TY,
	}
}

// verifyLanding does a strict readback at the CURRENT cursor position (which
// must already be the intended target) and returns the css point the page
// observed plus its distance to the element target. The jiggle inside
// probeCssAt doubles as the liveness test: if the landing point is covered by
// browser UI, the page never sees it and ok=false.
func (s *Server) verifyLanding(loc *locateResult) (cssPt, bool, float64) {
	time.Sleep(140 * time.Millisecond) // let the trajectory's final coalesced events flush
	cx, cy := win.CursorPos()
	obs, ok := s.probeCssAt(loc, float64(cx), float64(cy))
	if !ok {
		return cssPt{}, false, 0
	}
	return obs, true, math.Hypot(obs.X-loc.Css.X, obs.Y-loc.Css.Y)
}

// cssToPhys uses the measured map when available; the first-principles model
// otherwise (blind mode only).
func cssToPhys(f *affineFit, loc *locateResult, c cssPt) (float64, float64) {
	if f != nil {
		return f.physFromCss(c.X, c.Y)
	}
	return loc.physTarget(c)
}

func errStr(v any) string {
	s, _ := v.(string)
	return s
}

func randFrac() float64 { return float64(randInt(0, 1000)) / 1000 }

// locateForMouse locates the element, guarantees the browser window is
// actually foreground (chrome.windows' focused:true is best-effort under
// the Windows foreground lock), and — when the window had to be raised —
// re-locates so metrics are fresh after the page became visible (an
// occluded tab can carry a pending, uncommitted browser zoom).
func (s *Server) locateForMouse(args map[string]any) (*locateResult, map[string]any, error) {
	loc, raw, err := s.locateOnPage(args)
	if err != nil {
		return nil, nil, err
	}
	if args["allow_unfocused"] == true {
		return loc, raw, nil
	}
	tabTitle, _ := loc.Tab["title"].(string)
	hwnd := s.findBrowserHwnd(loc, tabTitle)
	if hwnd == 0 {
		return nil, nil, fmt.Errorf("cannot find the browser window (rect %dx%d@%d,%d physical); is it minimized or on another desktop?",
			int(loc.Win.Width*loc.osScale()), int(loc.Win.Height*loc.osScale()), int(loc.Win.Left*loc.osScale()), int(loc.Win.Top*loc.osScale()))
	}
	if !win.IsForeground(hwnd) {
		if !win.SetForegroundReliably(hwnd) {
			return nil, nil, fmt.Errorf("could not bring the browser window to the foreground; close overlapping windows or pass allow_unfocused:true to override")
		}
		time.Sleep(300 * time.Millisecond)
		// Re-locate: dpr/zoom may have been captured while the tab was occluded.
		return s.locateOnPage(args)
	}
	return loc, raw, nil
}

func (s *Server) findBrowserHwnd(loc *locateResult, tabTitle string) uintptr {
	scale := loc.osScale()
	return win.FindChromeWindowNear(
		int(loc.Win.Left*scale), int(loc.Win.Top*scale),
		int(loc.Win.Width*scale), int(loc.Win.Height*scale), 6, tabTitle)
}

// doCalibrate forces a fresh affine fit for the window showing the selected
// element (args: selector required, url/tab_id optional) and reports the
// measured parameters.
func (s *Server) doCalibrate(args map[string]any) map[string]any {
	loc, _, err := s.locateOnPage(args)
	if err != nil {
		return errRes("locate failed: " + err.Error())
	}
	f, err := s.fitAffineCalibration(loc)
	if err != nil {
		return errRes(err.Error())
	}
	s.storeAffine(loc, f)
	return map[string]any{
		"ok": true,
		"fit": map[string]any{
			"sx": f.SX, "sy": f.SY, "tx": f.TX, "ty": f.TY,
			"probes":         len(f.Pairs),
			"max_residual_px": f.MaxResidual,
		},
		"key": loc.calibKey(),
		"theory": map[string]any{
			"sx":  1 / loc.Metrics.Dpr,
			"note": "css px per physical px must equal 1/devicePixelRatio",
		},
	}
}

func (s *Server) doLocate(args map[string]any) map[string]any {
	loc, raw, err := s.locateOnPage(args)
	if err != nil {
		return errRes("locate failed: " + err.Error())
	}
	px, py := loc.physTarget(loc.Css)
	rawPx, rawPy := px, py
	f := s.cachedAffine(loc)
	if f != nil {
		px, py = f.physFromCss(loc.Css.X, loc.Css.Y)
	}
	calib := map[string]any{"mode": "affine", "fitted": f != nil}
	if f != nil {
		calib["sx"], calib["sy"], calib["tx"], calib["ty"] = f.SX, f.SY, f.TX, f.TY
		calib["fit_residual_px"] = f.MaxResidual
	}
	return map[string]any{
		"ok":  true,
		"tab": loc.Tab,
		"element": map[string]any{
			"info":     loc.Element,
			"css":      map[string]any{"x": loc.Css.X, "y": loc.Css.Y},
			"rect":     map[string]any{"x": loc.Rect.X, "y": loc.Rect.Y, "w": loc.Rect.W, "h": loc.Rect.H},
			"occluded": loc.Occluded,
		},
		"screen": map[string]any{
			"x":     int(math.Round(px)),
			"y":     int(math.Round(py)),
			"raw_x": int(math.Round(rawPx)),
			"raw_y": int(math.Round(rawPy)),
		},
		"conversion": map[string]any{
			"device_pixel_ratio": loc.Metrics.Dpr,
			"browser_zoom":       loc.Zoom,
			"os_scale":           loc.osScale(),
			"window":             map[string]any{"id": loc.Win.Id, "left": loc.Win.Left, "top": loc.Win.Top, "width": loc.Win.Width, "height": loc.Win.Height, "state": loc.Win.State},
			"calibration":        calib,
		},
		"raw": raw,
	}
}

// doElement runs the full chain for move_to_element / click_element:
// locate → measured affine map → human-like move → strict readback verify →
// (on drift: fresh fit + exactly one re-move, never an accumulator) → click.
func (s *Server) doElement(args map[string]any, mode string) map[string]any {
	loc, _, err := s.locateForMouse(args)
	if err != nil {
		return errRes(err.Error())
	}
	if loc.Occluded && args["force"] != true {
		tag, _ := loc.Element["occluder_tag"].(string)
		return errRes(fmt.Sprintf("element at (%.0f,%.0f) is covered by another element (%s); adjust selector or pass force:true", loc.Css.X, loc.Css.Y, tag))
	}
	blind := args["allow_blind"] == true
	fit, calib := s.affineForOp(loc)
	if fit == nil && !blind {
		return map[string]any{"ok": false, "error": "calibration failed: " + errStr(calib["error"]), "calibration": calib}
	}

	attempt := func(f *affineFit, l *locateResult) map[string]any {
		px, py := cssToPhys(f, l, l.Css)
		if res := s.doMove(px, py); res["ok"] != true {
			return res
		}
		return nil
	}

	// No fit and blind: trust the first-principles model, no feedback.
	verified := fit == nil
	if fit == nil {
		if res := attempt(nil, loc); res != nil {
			return res
		}
	} else {
		if res := attempt(fit, loc); res != nil {
			return res
		}
		for round := 0; round < 2 && !verified; round++ {
			if round == 1 {
				// Between locate and move the layout may have shifted (late
				// infobar, popup): re-locate with fresh metrics, retry once.
				loc2, _, err2 := s.locateOnPage(args)
				if err2 != nil {
					calib["retry_error"] = err2.Error()
					break
				}
				loc = loc2
				fit, calib = s.affineForOp(loc)
				if fit == nil {
					if blind {
						break
					}
					return map[string]any{"ok": false, "error": "calibration failed: " + errStr(calib["error"]), "calibration": calib}
				}
				if res := attempt(fit, loc); res != nil {
					return res
				}
			}
			_, ok, resid := s.verifyLanding(loc)
			if !ok {
				continue // page never saw the cursor — covered by browser UI; retry with fresh locate
			}
			calib["landing_residual_px"] = resid
			if resid <= 1.5 {
				verified = true
				break
			}
			// The landing drifted beyond the readback floor: the cached map is
			// stale despite its key. Fit a fresh one from measurements and
			// re-move ONCE — deliberately not an iterative accumulator.
			f2, e2 := s.fitAffineCalibration(loc)
			if e2 != nil {
				calib["refit_error"] = e2.Error()
				break
			}
			s.storeAffine(loc, f2)
			fit = f2
			calib["refitted"] = true
			calib["cached"] = false
			if res := attempt(f2, loc); res != nil {
				return res
			}
			if _, ok2, resid2 := s.verifyLanding(loc); ok2 {
				calib["landing_residual_px"] = resid2
				verified = resid2 <= 1.5 || blind
			}
			break
		}
	}
	if !verified && !blind {
		reason := errStr(calib["refit_error"])
		if reason == "" {
			reason = errStr(calib["retry_error"])
		}
		if reason == "" {
			if r, has := calib["landing_residual_px"].(float64); has {
				reason = fmt.Sprintf("residual %.1f css px after refit", r)
			} else {
				reason = "page emitted no mousemove at the target"
			}
		}
		return map[string]any{"ok": false,
			"error":       "landing point not verified on the live page: " + reason + " — dismiss any browser popup over the page, or pass allow_blind:true to override",
			"calibration": calib}
	}

	cx, cy := win.CursorPos()
	out := map[string]any{
		"ok":   true,
		"mode": mode,
		"tab":  loc.Tab,
		"element": map[string]any{
			"info":     loc.Element,
			"css":      map[string]any{"x": loc.Css.X, "y": loc.Css.Y},
			"rect":     map[string]any{"x": loc.Rect.X, "y": loc.Rect.Y, "w": loc.Rect.W, "h": loc.Rect.H},
			"occluded": loc.Occluded,
		},
		"screen_target": map[string]any{"x": cx, "y": cy},
		"conversion": map[string]any{
			"device_pixel_ratio": loc.Metrics.Dpr,
			"browser_zoom":       loc.Zoom,
			"os_scale":           loc.osScale(),
		},
		"calibration": calib,
	}

	if mode == "click" {
		btn := strOr(args, "button", "left")
		if btn != "left" && btn != "right" && btn != "middle" {
			return errRes("button must be left|right|middle")
		}
		time.Sleep(time.Duration(90+randInt(0, 90)) * time.Millisecond)
		if err := mouse.Click(btn); err != nil {
			out["ok"] = false
			out["error"] = "click failed: " + err.Error()
			return out
		}
		out["clicked"] = true
		out["button"] = btn
	}
	return out
}

// dragViaElements supports drag with selector endpoints:
// {from_selector, to_selector, button?}
func (s *Server) dragViaElements(args map[string]any) map[string]any {
	fromSel, _ := args["from_selector"].(string)
	toSel, _ := args["to_selector"].(string)
	if fromSel == "" || toSel == "" {
		return errRes("drag_element needs from_selector and to_selector (or plain drag with x,y,to_x,to_y)")
	}
	btn := strOr(args, "button", "left")

	from, _, err := s.locateForMouse(map[string]any{"selector": fromSel, "tab_id": args["tab_id"], "url": args["url"], "allow_unfocused": args["allow_unfocused"]})
	if err != nil {
		return errRes("from_selector: " + err.Error())
	}
	if from.Occluded && args["force"] != true {
		return errRes("from_selector element is occluded; pass force:true to override")
	}
	to, _, err := s.locateOnPage(map[string]any{"selector": toSel, "tab_id": args["tab_id"], "url": args["url"]})
	if err != nil {
		return errRes("to_selector: " + err.Error())
	}
	if to.Occluded && args["force"] != true {
		return errRes("to_selector element is occluded; pass force:true to override")
	}

	fit, calib := s.affineForOp(from)
	if fit == nil && args["allow_blind"] != true {
		return map[string]any{"ok": false, "error": "calibration failed: " + errStr(calib["error"]), "calibration": calib}
	}

	fpx, fpy := cssToPhys(fit, from, from.Css)
	if res := s.doMove(fpx, fpy); res["ok"] != true {
		return res
	}
	if fit != nil {
		if _, ok, r := s.verifyLanding(from); ok && r > 1.5 {
			if f2, e2 := s.fitAffineCalibration(from); e2 == nil {
				s.storeAffine(from, f2)
				fit = f2
				calib["refitted"] = true
				if res := s.doMove(f2.physFromCss(from.Css.X, from.Css.Y)); res["ok"] != true {
					return res
				}
			}
		} else if !ok && args["allow_blind"] != true {
			return map[string]any{"ok": false, "error": "drag source not verified on the live page: page emitted no mousemove at the source — dismiss any browser popup over the page, or pass allow_blind:true", "calibration": calib}
		}
	}
	time.Sleep(60 * time.Millisecond)

	// Endpoint: the same window's fit when both elements share it (the usual
	// case), the first-principles model when `to` lives in another window.
	tx, ty := to.physTarget(to.Css)
	if fit != nil && to.calibKey() == from.calibKey() {
		tx, ty = fit.physFromCss(to.Css.X, to.Css.Y)
	}

	pts, dur := mouse.Drag(mouse.Point{X: tx, Y: ty}, btn)
	ex, ey := win.CursorPos()
	return map[string]any{
		"ok":          true,
		"dragged":     true,
		"button":      btn,
		"from":        map[string]any{"selector": fromSel, "css": from.Css, "element": from.Element},
		"to":          map[string]any{"selector": toSel, "css": to.Css, "element": to.Element, "landed": map[string]any{"x": ex, "y": ey}},
		"duration_ms": ms(dur),
		"path_points": len(pts),
		"calibration": calib,
	}
}
