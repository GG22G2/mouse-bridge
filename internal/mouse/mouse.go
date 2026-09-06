// Package mouse implements human-like cursor motion modeled on motor-control
// research: movements are decomposed into 2-3 sequential sub-movements
// (ballistic swing + corrective homing), each driven by a minimum-jerk
// velocity profile, with low-frequency path wander, band-limited physiological
// tremor and Fitts-law timing. Injected with SendInput so the OS cursor
// really travels the screen. No Bézier curves anywhere.
package mouse

import (
	"math"
	"math/rand"
	"time"

	"mousebridge/internal/win"
)

// Point is a desktop position in physical pixels.
type Point struct {
	X, Y float64
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return v
	}
	return v
}

// minJerk is the minimum-jerk time scaling s(u)=10u³−15u⁴+6u⁵ — the
// velocity profile human reaching movements follow (Flash & Hogan, 1985).
func minJerk(u float64) float64 {
	return u * u * u * (10 - 15*u + 6*u*u)
}

// subMove is one kinematic segment: a start-to-end glide with its own
// duration share, lateral bow, and smooth low-frequency wander.
type subMove struct {
	End     Point
	durFrac float64
	bow     float64 // perpendicular hump (px), signed
	wPh1    float64 // wander phase/frequency/amp pair 1 (~0.5-1.5 cycles)
	wF1     float64
	wA1     float64
	wPh2    float64 // higher-frequency component (~2.5-4 cycles)
	wF2     float64
	wA2     float64
}

// plan decomposes from→to into human sub-movements and a total duration
// following Fitts's law (with randomization).
func plan(from, to Point, grip bool) ([]subMove, time.Duration) {
	dx, dy := to.X-from.X, to.Y-from.Y
	dist := math.Hypot(dx, dy)
	if dist < 0.5 {
		return []subMove{{End: to, durFrac: 1}}, 30 * time.Millisecond
	}
	ux, uy := dx/dist, dy/dist // unit along the path
	px, py := -uy, ux          // unit perpendicular

	// Fitts-like duration: MT = a + b·log2(1 + D/100), randomized ±20%.
	durMs := 150 + 130*math.Log2(1+dist/100)
	durMs *= 0.8 + rand.Float64()*0.45
	dur := time.Duration(clamp(durMs, 110, 1300) * float64(time.Millisecond))

	// Choose the number of sub-movements.
	var subs []subMove
	switch {
	case dist < 70 || grip:
		subs = []subMove{{End: to, durFrac: 1}}
	case dist < 420 || rand.Float64() < 0.4:
		f := 0.72 + rand.Float64()*0.18 // first swing covers 72-90%
		mid := Point{from.X + ux*dist*f + px*randn(1, 5), from.Y + uy*dist*f + py*randn(1, 5)}
		subs = []subMove{
			{End: mid, durFrac: 0.55 + rand.Float64()*0.15},
			{End: to, durFrac: 1},
		}
		subs[0].durFrac = subs[0].durFrac / subs[1].durFrac // normalize below
	default:
		f1 := 0.55 + rand.Float64()*0.2
		f2 := f1 + (0.8-f1)*(0.6+rand.Float64()*0.3)
		mid1 := Point{from.X + ux*dist*f1 + px*randn(2, 7), from.Y + uy*dist*f1 + py*randn(2, 7)}
		mid2 := Point{from.X + ux*dist*f2 + px*randn(1, 4), from.Y + uy*dist*f2 + py*randn(1, 4)}
		subs = []subMove{
			{End: mid1, durFrac: 0.45 + rand.Float64()*0.1},
			{End: mid2, durFrac: 0.75},
			{End: to, durFrac: 1},
		}
	}

	// Normalize duration fractions and decorate each sub-move.
	var acc float64
	for i := range subs {
		acc += subs[i].durFrac
	}
	for i := range subs {
		subs[i].durFrac /= acc
		sd := dist * subs[i].durFrac // rough sub-move length for scaling
		// Bow: some sub-moves nearly straight, others up to ~12% hump.
		if rand.Float64() < 0.3 {
			subs[i].bow = randn(0, 1.5)
		} else {
			b := sd * (0.03 + rand.Float64()*0.09) * sign(rand.Float64()-0.5)
			subs[i].bow = clamp(b, -60, 60)
		}
		subs[i].wPh1 = rand.Float64() * math.Pi * 2
		subs[i].wF1 = 0.8 + rand.Float64()*0.9
		subs[i].wA1 = clamp(sd*0.015, 0.4, 4) * (0.5 + rand.Float64())
		subs[i].wPh2 = rand.Float64() * math.Pi * 2
		subs[i].wF2 = 2.5 + rand.Float64()*1.6
		subs[i].wA2 = clamp(sd*0.006, 0.2, 1.6)
	}
	// First sub-movement of a long travel gets the lion's share of time.
	if len(subs) > 1 {
		subs[0].durFrac = clamp(subs[0].durFrac, 0.5, 0.72)
		rest := 1 - subs[0].durFrac
		var restAcc float64
		for i := 1; i < len(subs); i++ {
			restAcc += subs[i].durFrac
		}
		for i := 1; i < len(subs); i++ {
			subs[i].durFrac = subs[i].durFrac / restAcc * rest
		}
	}
	return subs, dur
}

func randn(lo, hi float64) float64 { return lo + rand.Float64()*(hi-lo) }
func sign(v float64) float64 {
	if v >= 0 {
		return 1
	}
	return -1
}

// emit samples the planned path time-driven (uneven 5-13ms intervals),
// producing the physical points the cursor will visit. Contains low-frequency
// wander plus AR(1) tremor — deliberately NOT a smooth analytic curve.
func emit(from Point, subs []subMove, total time.Duration) []Point {
	out := make([]Point, 0, 128)
	trem := 0.0 // AR(1) tremor state (px)
	totalSec := total.Seconds()
	t := 0.0
	start := from
	for _, sm := range subs {
		subDur := totalSec * sm.durFrac
		st := 0.0
		for st < subDur {
			u := clamp(st/subDur, 0, 1)
			s := minJerk(u)
			bx := start.X + (sm.End.X-start.X)*s
			by := start.Y + (sm.End.Y-start.Y)*s
			hump := math.Sin(math.Pi * clamp(s, 0, 1))
			wander := sm.bow*hump +
				sm.wA1*math.Sin(sm.wPh1+2*math.Pi*sm.wF1*u) +
				sm.wA2*math.Sin(sm.wPh2+2*math.Pi*sm.wF2*u)
			// perpendicular of THIS sub-move
			sdx, sdy := sm.End.X-start.X, sm.End.Y-start.Y
			l := math.Hypot(sdx, sdy)
			var px, py float64
			if l > 0.01 {
				px, py = -sdy/l, sdx/l
			}
			trem = trem*0.72 + (rand.Float64()-0.5)*0.55
			jx := px*wander + px*trem*0.6 + (rand.Float64()-0.5)*0.3
			jy := py*wander + py*trem*0.6 + (rand.Float64()-0.5)*0.3
			out = append(out, Point{X: bx + jx, Y: by + jy})
			st += (5 + rand.Float64()*8) / 1000 // uneven 5-13ms sampling
		}
		start = sm.End
		t += subDur
		_ = t
	}
	out = append(out, subs[len(subs)-1].End)
	return out
}

func play(points []Point) {
	if len(points) == 0 {
		return
	}
	for _, p := range points {
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
		time.Sleep(time.Duration(4+rand.Float64()*7) * time.Millisecond)
	}
}

// playWithPauses plays the path, occasionally hesitating like a human.
func playWithPauses(points []Point, pauseChance float64) {
	for _, p := range points {
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
		time.Sleep(time.Duration(4+rand.Float64()*7) * time.Millisecond)
		if rand.Float64() < pauseChance {
			time.Sleep(time.Duration(15+rand.Intn(30)) * time.Millisecond)
		}
	}
}

// Plan is exported for diagnostics (tools/pathdump): plan + sample a path
// WITHOUT touching the real cursor, for inspecting the velocity profile.
func DebugPath(from, to Point) (pts []Point, subMoves int, dur time.Duration) {
	subs, total := plan(from, to, false)
	return emit(from, subs, total), len(subs), total
}

// moveTo drives the cursor from its current position to target.
func moveTo(target Point, minDur, maxDur time.Duration, pauseChance float64) ([]Point, time.Duration) {
	cx, cy := win.CursorPos()
	from := Point{float64(cx), float64(cy)}
	if math.Hypot(target.X-from.X, target.Y-from.Y) < 1.5 {
		win.MoveToAbsolute(int(math.Round(target.X)), int(math.Round(target.Y)))
		return []Point{target}, 5 * time.Millisecond
	}
	subs, dur := plan(from, target, false)
	if dur < minDur {
		dur = minDur
	}
	if dur > maxDur {
		dur = maxDur
	}

	// Occasional overshoot on long travels: aim past the target, then a
	// corrective sub-movement back (humans overshoot ~1/3 of fast reaches).
	aim := target
	corr := false
	if d := math.Hypot(target.X-from.X, target.Y-from.Y); d > 380 && rand.Float64() < 0.33 {
		ux, uy := (target.X-from.X)/d, (target.Y-from.Y)/d
		aim = Point{target.X + ux*(6+rand.Float64()*16), target.Y + uy*(6+rand.Float64()*16)}
		corr = true
		dur = time.Duration(float64(dur) * 0.9)
	}

	pts := emit(from, subs, dur)
	playWithPauses(pts, pauseChance)
	total := dur

	if corr {
		cSubs, cDur := plan(aim, target, true)
		cPts := emit(aim, cSubs, cDur)
		play(cPts)
		pts = append(pts, cPts...)
		total += cDur
	}
	// Land exactly on the requested pixel.
	win.MoveToAbsolute(int(math.Round(target.X)), int(math.Round(target.Y)))
	return pts, total
}

// Move glides the cursor to a physical desktop coordinate like a person.
func Move(x, y float64) ([]Point, time.Duration) {
	return moveTo(Point{x, y}, 100*time.Millisecond, 1100*time.Millisecond, 0.015)
}

// Nudge is a short corrective glide (calibration corrections).
func Nudge(x, y float64) ([]Point, time.Duration) {
	return moveTo(Point{x, y}, 60*time.Millisecond, 240*time.Millisecond, 0)
}

// MoveRaw teleports instantly (diagnostics only, not human-like).
func MoveRaw(x, y float64) { win.MoveToAbsolute(int(math.Round(x)), int(math.Round(y))) }

func pressFor(btn string, minMs, maxMs int) error {
	if err := win.ButtonDown(btn); err != nil {
		return err
	}
	time.Sleep(time.Duration(minMs+rand.Intn(maxMs-minMs)) * time.Millisecond)
	return win.ButtonUp(btn)
}

// Click presses and releases the button at the CURRENT cursor position.
func Click(btn string) error {
	time.Sleep(time.Duration(60+rand.Intn(90)) * time.Millisecond)
	switch btn {
	case "right":
		return pressFor("right", 45, 130)
	case "middle":
		return pressFor("middle", 45, 130)
	default:
		return pressFor("left", 45, 130)
	}
}

// Drag presses at the current position, drags along a human path to the
// target and releases. Dragging is slower and more deliberate: near-constant
// mid-phase speed, light tremor, occasional micro-pauses.
func Drag(to Point, btn string) ([]Point, time.Duration) {
	cx, cy := win.CursorPos()
	from := Point{float64(cx), float64(cy)}

	if err := win.ButtonDown(btn); err != nil {
		return nil, 0
	}
	// Grip: hold still briefly before the object starts moving.
	time.Sleep(time.Duration(120+rand.Intn(140)) * time.Millisecond)

	dist := math.Hypot(to.X-from.X, to.Y-from.Y)
	durMs := 300 + 90*math.Log2(1+dist/80)
	durMs *= 0.85 + rand.Float64()*0.3
	dur := time.Duration(clamp(durMs, 220, 2600) * float64(time.Millisecond))

	subs, _ := plan(from, to, true)
	pts := emit(from, subs, dur)
	start := time.Now()
	for _, p := range pts {
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
		time.Sleep(time.Duration(5+rand.Float64()*8) * time.Millisecond)
		if rand.Float64() < 0.04 {
			time.Sleep(time.Duration(20+rand.Intn(40)) * time.Millisecond)
		}
	}
	win.MoveToAbsolute(int(math.Round(to.X)), int(math.Round(to.Y)))
	time.Sleep(time.Duration(80+rand.Intn(120)) * time.Millisecond)
	if err := win.ButtonUp(btn); err != nil {
		return pts, time.Since(start)
	}
	return pts, time.Since(start)
}
