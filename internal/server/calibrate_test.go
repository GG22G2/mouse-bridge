package server

import (
	"math"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mousebridge/internal/win"
)

// startFakeExt dials the daemon WS and plays a real extension: hello with
// caps, then answers `measure` HONESTLY — it reads the real Windows cursor
// and reports the css coordinate under the page's TRUE geometry:
//
//	physX = (cssX + border) * osScale
//	physY = (cssY + chromeUI) * osScale   (+ window origin)
//
// border/chromeUI/window origin are the scenario constants, so a scenario can
// describe a left-aligned viewport (side panel docked right) or a centered
// one, and the fake never lies about where the cursor landed.
func startFakeExt(t *testing.T, url string, originX, originY, border, chromeUI, osScale float64) {
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("fake ext dial: %v", err)
	}
	ws.WriteJSON(map[string]any{
		"type": "hello", "version": "1.1.6", "ext_id": "fake-ext",
		"caps": []string{"ping", "get_state", "locate", "measure", "measure_page", "set_zoom", "set_window"},
	})
	go func() {
		defer ws.Close()
		events := 0.0
		for {
			var msg map[string]any
			if err := ws.ReadJSON(&msg); err != nil {
				return
			}
			if msg["cmd"] == "measure" {
				events++
				cx, cy := win.CursorPos()
				cssX := float64(cx)/osScale - border - originX
				cssY := float64(cy)/osScale - chromeUI - originY
				ws.WriteJSON(map[string]any{
					"id": msg["id"], "ok": true,
					"data": map[string]any{"x": cssX, "y": cssY, "age_ms": 5, "events_seen": events, "ok": true},
				})
				continue
			}
			if id, ok := msg["id"].(string); ok {
				ws.WriteJSON(map[string]any{"id": id, "ok": true, "data": map[string]any{}})
			}
		}
	}()
}

// TestFitAffineSidePanel reproduces the reported failure: side panel docked
// right, page LEFT-ALIGNED in the window. The centered seed guess is then off
// by ~193 css px, which tripped the old ±150px guard ("wrong window or the
// page jumped underneath us"). The new algorithm must absorb it via the first
// honest readback and still produce an exact map.
func TestFitAffineSidePanel(t *testing.T) {
	const (
		originX, originY = 0.0, 0.0 // maximized window at 0,0
		border           = 2.0      // window border only — page is flush left
		chromeUI         = 96.0     // tab strip + address bar
		osScale          = 1.25
	)
	s := New()
	go s.Run("127.0.0.1:19387")
	time.Sleep(300 * time.Millisecond)
	startFakeExt(t, "ws://127.0.0.1:19387/ws", originX, originY, border, chromeUI, osScale)
	time.Sleep(200 * time.Millisecond)

	loc := &locateResult{ServedBy: "fake-ext"}
	loc.Tab = map[string]any{"id": 1.0, "url": "http://test/"}
	loc.Metrics.Dpr = osScale
	loc.Metrics.InnerW = 1146 // viewport narrowed by the side panel
	loc.Metrics.InnerH = 754
	loc.Win.Left, loc.Win.Top, loc.Win.Width, loc.Win.Height = originX, originY, 1536, 864
	loc.Zoom = 1

	f, err := s.fitAffineCalibration(loc)
	if err != nil {
		t.Fatalf("calibration failed with side panel open: %v", err)
	}
	px, py := f.physFromCss(100, 100)
	wantX, wantY := (100+border+originX)*osScale, (100+chromeUI+originY)*osScale
	if math.Abs(px-wantX) > 6 || math.Abs(py-wantY) > 6 {
		t.Errorf("physFromCss(100,100) = (%.1f,%.1f), want (%.1f,%.1f)", px, py, wantX, wantY)
	}
	if f.MaxResidual > 2.5 {
		t.Errorf("residual %.2f too high", f.MaxResidual)
	}
	t.Logf("side-panel fit ok: sx=%.4f sy=%.4f residual=%.2f phys(100,100)=(%.1f,%.1f)", f.SX, f.SY, f.MaxResidual, px, py)
}

// TestFitAffineCentered guards the plain geometry: no panel, page effectively
// centered, window not at the screen origin.
func TestFitAffineCentered(t *testing.T) {
	const (
		originX, originY = 100.0, 50.0
		border           = 2.0
		chromeUI         = 96.0
		osScale          = 1.25
	)
	s := New()
	go s.Run("127.0.0.1:19388")
	time.Sleep(300 * time.Millisecond)
	startFakeExt(t, "ws://127.0.0.1:19388/ws", originX, originY, border, chromeUI, osScale)
	time.Sleep(200 * time.Millisecond)

	loc := &locateResult{ServedBy: "fake-ext"}
	loc.Tab = map[string]any{"id": 1.0, "url": "http://test/"}
	loc.Metrics.Dpr = osScale
	loc.Metrics.InnerW = 1532
	loc.Metrics.InnerH = 754
	loc.Win.Left, loc.Win.Top, loc.Win.Width, loc.Win.Height = originX, originY, 1536, 864
	loc.Zoom = 1

	f, err := s.fitAffineCalibration(loc)
	if err != nil {
		t.Fatalf("calibration failed on plain geometry: %v", err)
	}
	px, py := f.physFromCss(700, 300)
	wantX, wantY := (700+border+originX)*osScale, (300+chromeUI+originY)*osScale
	if math.Abs(px-wantX) > 6 || math.Abs(py-wantY) > 6 {
		t.Errorf("physFromCss(700,300) = (%.1f,%.1f), want (%.1f,%.1f)", px, py, wantX, wantY)
	}
	t.Logf("centered fit ok: residual=%.2f", f.MaxResidual)
}
