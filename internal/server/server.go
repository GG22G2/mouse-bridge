// Package server exposes the agent-facing HTTP API and the WebSocket the
// browser extension connects to, and orchestrates element-driven mouse ops.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"mousebridge/internal/mouse"
	"mousebridge/internal/win"
)

const Version = "1.2.2"

// Server owns the extension connections and serializes mouse operations.
type Server struct {
	mu       sync.Mutex
	extConns map[*extConn]struct{}
	lastUsed *extConn // default route for hint-less commands (freshest hello / last served)

	pendingReqs map[string]chan map[string]any

	opMu sync.Mutex // one mouse op at a time

	affine  map[string]*affineFit // geometry key -> measured css↔physical affine map
	calibMu sync.Mutex

	start time.Time

	extTimeout time.Duration
}

type extConn struct {
	ws      *websocket.Conn
	sendMu  sync.Mutex
	id      string
	browser string // edge|chrome|"" (pre-brand clients) — same unpacked id runs in both
	version string
	caps    map[string]bool // capabilities declared in hello (nil = pre-caps client)
	alive   bool
	since   time.Time // connect time (candidate ordering)
}

// identity is the unique per-browser connection key: unpacked installs share
// one extension id across Chrome and Edge, the brand disambiguates.
func (c *extConn) identity() string { return c.id + "@" + c.browser }

// pending response channel for a request id
type pending struct {
	ch chan map[string]any
}

func New() *Server {
	return &Server{
		extConns:    make(map[*extConn]struct{}),
		pendingReqs: make(map[string]chan map[string]any),
		affine:      make(map[string]*affineFit),
		start:       time.Now(),
		extTimeout:  12 * time.Second,
	}
}

// Run starts the HTTP server (blocking).
func (s *Server) Run(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/command", s.handleCommand)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/shutdown", s.handleShutdown)
	mux.HandleFunc("/test", handleTestPage)
	mux.HandleFunc("/crx/updates.xml", s.handleCrxManifest)
	mux.HandleFunc("/crx/mouse-bridge.crx", s.handleCrxFile)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, "mouse-bridge daemon. POST /command, GET /status, GET /test, WS /ws")
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Printf("[http] listening on %s", addr)
	return srv.ListenAndServe()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	x, y := win.CursorPos()
	vx, vy, vw, vh := win.VirtualDesktop()
	ftitle, fclass := win.ForegroundWindow()
	writeJSON(w, map[string]any{
		"ok":                  true,
		"tool":                "mouse-bridge",
		"version":             Version,
		"extension_connected": s.extensionConnected(),
		"extension_version":   s.extensionVersion(),
		"extensions":          s.extensionsInfo(),
		"cursor":              map[string]any{"x": x, "y": y},
		"virtual_desktop":     map[string]any{"x": vx, "y": vy, "w": vw, "h": vh},
		"dpi":                 map[string]any{"system_dpi": win.SystemDpi(), "scale": float64(win.SystemDpi()) / 96.0, "per_monitor_v2": true},
		"foreground_window":   map[string]any{"title": ftitle, "class": fclass},
		"uptime_s":            int(time.Since(s.start).Seconds()),
		"pid":                 pidSelf(),
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "bye": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		win.TimeEndPeriod()
		exitProcess(0)
	}()
}

type commandReq struct {
	Action  string         `json:"action"`
	Args    map[string]any `json:"args"`
	Session string         `json:"session"`
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, map[string]any{"ok": false, "error": "POST only"})
		return
	}
	var req commandReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "bad json: " + err.Error()})
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	res := s.dispatch(req.Action, req.Args)
	writeJSON(w, res)
}

func (s *Server) dispatch(action string, args map[string]any) map[string]any {
	if action == "status" {
		x, y := win.CursorPos()
		return map[string]any{"ok": true, "extension_connected": s.extensionConnected(), "cursor": map[string]any{"x": x, "y": y}, "version": Version}
	}
	if action == "reset_calibration" {
		s.calibMu.Lock()
		n := len(s.affine)
		s.affine = make(map[string]*affineFit)
		s.calibMu.Unlock()
		return map[string]any{"ok": true, "cleared": n}
	}

	// Everything below touches the physical mouse or the extension; require
	// the extension for element ops, serialize mouse movement.
	needExt := strings.HasSuffix(action, "_element") || action == "calibrate" || action == "move_css"
	if needExt && !s.extensionConnected() {
		return map[string]any{"ok": false, "error": "browser extension not connected. Load the Mouse Bridge extension in Chrome (it reconnects within ~30s)."}
	}

	if !s.opMu.TryLock() {
		return map[string]any{"ok": false, "error": "another mouse operation is in progress"}
	}
	defer s.opMu.Unlock()

	switch action {
	case "move":
		x, y, ok := num2(args, "x", "y")
		if !ok {
			return errRes("move needs numeric x,y (physical desktop pixels)")
		}
		return s.doMove(x, y)
	case "click":
		btn := strOr(args, "button", "left")
		if btn != "left" && btn != "right" && btn != "middle" {
			return errRes("button must be left|right|middle")
		}
		if x, y, ok := num2(args, "x", "y"); ok {
			if res := s.doMove(x, y); res["ok"] != true {
				return res
			}
		}
		start := time.Now()
		if err := mouse.Click(btn); err != nil {
			return errRes("click failed: " + err.Error())
		}
		cx, cy := win.CursorPos()
		return map[string]any{"ok": true, "clicked": true, "button": btn, "at": map[string]any{"x": cx, "y": cy}, "duration_ms": ms(time.Since(start))}
	case "wheel":
		dy := numOr(args, "dy", 3)
		if x, y, ok := num2(args, "x", "y"); ok {
			if res := s.doMove(x, y); res["ok"] != true {
				return res
			}
		}
		if err := win.Wheel(-int(dy)); err != nil {
			return errRes("wheel failed: " + err.Error())
		}
		return map[string]any{"ok": true, "scrolled": dy}
	case "drag":
		// from/to as physical coords, or from_selector/to_selector via extension
		if fx, fy, ok := num2(args, "x", "y"); !ok {
			return s.dragViaElements(args)
		} else {
			tx, ty, ok2 := num2(args, "to_x", "to_y")
			if !ok2 {
				return errRes("drag needs x,y and to_x,to_y (physical pixels)")
			}
			return s.doDrag(fx, fy, tx, ty, strOr(args, "button", "left"))
		}
	case "locate_element":
		return s.doLocate(args)
	case "move_to_element":
		return s.doElement(args, "move")
	case "click_element":
		return s.doElement(args, "click")
	case "drag_element":
		return s.dragViaElements(args)
	case "calibrate":
		return s.doCalibrate(args)
	case "move_css":
		return s.doMoveCss(args)
	case "get_calibration":
		s.calibMu.Lock()
		defer s.calibMu.Unlock()
		out := map[string]any{}
		for k, f := range s.affine {
			out[k] = map[string]any{
				"sx": f.SX, "sy": f.SY, "tx": f.TX, "ty": f.TY,
				"probes":         len(f.Pairs),
				"max_residual_px": f.MaxResidual,
				"age_s":          int(time.Since(f.FittedAt).Seconds()),
			}
		}
		return map[string]any{"ok": true, "affine_calibration": out}
	case "measure":
		// Debug: raw content-script cursor feedback (freshness + css pos).
		resp, err := s.callExt("measure", args, s.extTimeout)
		if err != nil {
			return errRes("measure: " + err.Error())
		}
		return resp
	case "set_window":
		if !s.extensionConnected() {
			return errRes("browser extension not connected")
		}
		resp, err := s.callExt("set_window", args, s.extTimeout)
		if err != nil {
			return errRes("set_window: " + err.Error())
		}
		return resp
	case "set_zoom":
		z, ok := args["zoom"].(float64)
		if !ok {
			return errRes("set_zoom needs numeric zoom (e.g. 1.5)")
		}
		resp, err := s.callExt("set_zoom", map[string]any{
			"zoom":   z,
			"tab_id": args["tab_id"],
			"ext_id": args["ext_id"],
		}, s.extTimeout)
		if err != nil {
			return errRes("set_zoom: " + err.Error())
		}
		return resp
	case "move_raw":
		x, y, ok := num2(args, "x", "y")
		if !ok {
			return errRes("move_raw needs numeric x,y")
		}
		mouse.MoveRaw(x, y)
		cx, cy := win.CursorPos()
		return map[string]any{"ok": true, "cursor": map[string]any{"x": cx, "y": cy}, "note": "raw teleport, not human-like"}
	default:
		return errRes("unknown action: " + action + ". Known: status, move, click, wheel, drag, locate_element, move_to_element, click_element, drag_element, calibrate, move_css, reset_calibration, get_calibration, move_raw")
	}
}

func errRes(msg string) map[string]any   { return map[string]any{"ok": false, "error": msg} }
func ms(d time.Duration) int64           { return d.Milliseconds() }
func strOr(m map[string]any, k, def string) string {
	if v, ok := m[k].(string); ok && v != "" {
		return v
	}
	return def
}
func numOr(m map[string]any, k string, def float64) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return def
}
func num2(m map[string]any, k1, k2 string) (float64, float64, bool) {
	a, oka := m[k1].(float64)
	b, okb := m[k2].(float64)
	if !oka || !okb {
		return 0, 0, false
	}
	return a, b, true
}

func (s *Server) doMove(x, y float64) map[string]any {
	vx, vy, vw, vh := win.VirtualDesktop()
	if x < float64(vx) || y < float64(vy) || x > float64(vx+vw) || y > float64(vy+vh) {
		return errRes(fmt.Sprintf("target (%.0f,%.0f) outside virtual desktop %dx%d at (%d,%d)", x, y, vw, vh, vx, vy))
	}
	cx, cy := win.CursorPos()
	pts, dur := mouse.Move(x, y)
	ex, ey := win.CursorPos()
	return map[string]any{
		"ok":          true,
		"from":        map[string]any{"x": cx, "y": cy},
		"to":          map[string]any{"x": ex, "y": ey},
		"duration_ms": ms(dur),
		"path_points": len(pts),
	}
}

func (s *Server) doDrag(fx, fy, tx, ty float64, btn string) map[string]any {
	if res := s.doMove(fx, fy); res["ok"] != true {
		return res
	}
	time.Sleep(80 * time.Millisecond)
	pts, dur := mouse.Drag(Point2(tx, ty), btn)
	ex, ey := win.CursorPos()
	return map[string]any{
		"ok":          true,
		"dragged":     true,
		"from":        map[string]any{"x": fx, "y": fy},
		"to":          map[string]any{"x": ex, "y": ey},
		"button":      btn,
		"duration_ms": ms(dur),
		"path_points": len(pts),
	}
}

// Point2 adapts floats into mouse.Point.
func Point2(x, y float64) mouse.Point { return mouse.Point{X: x, Y: y} }
