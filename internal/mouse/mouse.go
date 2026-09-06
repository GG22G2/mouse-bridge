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

// Sample is one sampled path point: position in physical pixels, T in
// milliseconds since the movement started.
type Sample struct {
	X, Y, T float64
}

// HumanPathHook, when set, offers a recorded human trajectory for a move.
// Implementations return the full path already mapped onto from→to (T in ms
// since start); ok=false makes the movement fall back to the synthetic
// generator. Wired to the recording library by cmd/mouse-bridge.
var HumanPathHook func(from, to Point) (samples []Sample, source string, ok bool)

// tryHumanPath consults HumanPathHook; a nil hook or a too-short answer
// means the synthetic generator takes over.
func tryHumanPath(from, to Point) ([]Sample, string, bool) {
	if HumanPathHook == nil {
		return nil, "synthetic", false
	}
	if samps, src, ok := HumanPathHook(from, to); ok && len(samps) > 1 {
		return samps, src, true
	}
	return nil, "synthetic", false
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
	durMs := FittsMs(dist)
	durMs *= 0.8 + rand.Float64()*0.45
	dur := time.Duration(clamp(durMs, 110, 1300) * float64(time.Millisecond))

	// Choose the number of sub-movements. Waypoints sit on the travel axis
	// with a purely perpendicular offset whose SIGN is random — a fixed side
	// would stamp every movement with the same curvature signature.
	off := func(lo, hi float64) float64 {
		v := randn(lo, hi)
		if rand.Float64() < 0.5 {
			v = -v
		}
		return v
	}
	var subs []subMove
	switch {
	case dist < 70 || grip:
		subs = []subMove{{End: to, durFrac: 1}}
	case dist < 420 || rand.Float64() < 0.4:
		f := 0.72 + rand.Float64()*0.18 // first swing covers 72-90%
		o := off(1, 5)
		mid := Point{from.X + ux*dist*f + px*o, from.Y + uy*dist*f + py*o}
		subs = []subMove{
			{End: mid, durFrac: 0.55 + rand.Float64()*0.15},
			{End: to, durFrac: 1},
		}
		subs[0].durFrac = subs[0].durFrac / subs[1].durFrac // normalize below
	default:
		f1 := 0.55 + rand.Float64()*0.2
		f2 := f1 + (0.8-f1)*(0.6+rand.Float64()*0.3)
		o1 := off(2, 7)
		o2 := off(1, 4)
		mid1 := Point{from.X + ux*dist*f1 + px*o1, from.Y + uy*dist*f1 + py*o1}
		mid2 := Point{from.X + ux*dist*f2 + px*o2, from.Y + uy*dist*f2 + py*o2}
		subs = []subMove{
			{End: mid1, durFrac: 0.45 + rand.Float64()*0.1},
			{End: mid2, durFrac: 0.75},
			{End: to, durFrac: 1},
		}
	}

	// Normalize duration fractions; the first sub-movement of a long travel
	// gets the lion's share of time.
	var acc float64
	for i := range subs {
		acc += subs[i].durFrac
	}
	for i := range subs {
		subs[i].durFrac /= acc
	}
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

	// Decorate each sub-move. The lateral bow and the wander amplitude must
	// scale with the segment's ACTUAL chord length (straight-line distance
	// between its endpoints), not with its share of the total time — after
	// the clamp above the two diverge badly (a ~90px corrective segment can
	// inherit the decoration scale of a ~350px swing and draw a visible hook
	// through the slow zone).
	prev := from
	for i := range subs {
		sd := math.Hypot(subs[i].End.X-prev.X, subs[i].End.Y-prev.Y)
		prev = subs[i].End
		// Bow: the initial swing may hump up to ~12% of its chord; corrective
		// segments stay more direct (3-8%) — humans home in nearly straight.
		factor := 0.03 + rand.Float64()*0.09
		if i > 0 {
			factor = 0.02 + rand.Float64()*0.05
		}
		if rand.Float64() < 0.3 {
			subs[i].bow = (rand.Float64()*2 - 1) * 1.5
		} else {
			b := sd * factor * sign(rand.Float64()-0.5)
			subs[i].bow = clamp(b, -60, 60)
		}
		subs[i].wPh1 = rand.Float64() * math.Pi * 2
		subs[i].wF1 = 0.8 + rand.Float64()*0.9
		subs[i].wA1 = clamp(sd*0.015, 0.4, 4) * (0.5 + rand.Float64())
		subs[i].wPh2 = rand.Float64() * math.Pi * 2
		subs[i].wF2 = 2.5 + rand.Float64()*1.6
		subs[i].wA2 = clamp(sd*0.006, 0.2, 1.6)
	}
	return subs, dur
}

// FittsMs is the core duration model before randomization and clamps:
// MT = 150 + 130·log2(1 + D/100) ms.
func FittsMs(dist float64) float64 { return 150 + 130*math.Log2(1+dist/100) }

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
func emit(from Point, subs []subMove, total time.Duration) []Sample {
	out := make([]Sample, 0, 128)
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
			// The whole lateral offset (bow + wander) rides the hump
			// envelope so it vanishes at both segment ends — without that,
			// independent per-segment wander phases jump the path sideways
			// at every junction.
			wander := (sm.bow +
				sm.wA1*math.Sin(sm.wPh1+2*math.Pi*sm.wF1*u) +
				sm.wA2*math.Sin(sm.wPh2+2*math.Pi*sm.wF2*u)) * hump
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
			out = append(out, Sample{X: bx + jx, Y: by + jy, T: (t + st) * 1000})
			st += (5 + rand.Float64()*8) / 1000 // uneven 5-13ms sampling
		}
		start = sm.End
		t += subDur
	}
	last := subs[len(subs)-1].End
	out = append(out, Sample{X: last.X, Y: last.Y, T: t * 1000})
	return out
}

// ptsOf strips timestamps for callers that only need the path geometry.
func ptsOf(samps []Sample) []Point {
	out := make([]Point, len(samps))
	for i, p := range samps {
		out[i] = Point{X: p.X, Y: p.Y}
	}
	return out
}

func play(points []Sample) {
	if len(points) == 0 {
		return
	}
	for _, p := range points {
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
		time.Sleep(time.Duration(4+rand.Float64()*7) * time.Millisecond)
	}
}

// playWithPauses plays the path, occasionally hesitating like a human.
func playWithPauses(points []Sample, pauseChance float64) {
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
	return ptsOf(emit(from, subs, total)), len(subs), total
}

// DebugPathTimed is DebugPath with per-sample timestamps kept (ms since
// start). The trajectory test bench (cmd/trajlab) runs its acceptance gates
// on this. Never touches the real cursor.
func DebugPathTimed(from, to Point) ([]Sample, int, time.Duration) {
	subs, total := plan(from, to, false)
	return emit(from, subs, total), len(subs), total
}

// playTimed plays pre-sampled points on their recorded clock (T in ms since
// start) instead of a fixed per-step sleep — the pacing IS the recorded
// hand's pacing.
func playTimed(points []Sample) time.Duration {
	t0 := time.Now()
	for _, p := range points {
		deadline := t0.Add(time.Duration(p.T * float64(time.Millisecond)))
		for {
			d := time.Until(deadline)
			if d <= 0 {
				break
			}
			if d > 2*time.Millisecond {
				time.Sleep(2 * time.Millisecond)
			} else {
				time.Sleep(d)
			}
		}
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
	}
	return time.Since(t0)
}

// moveTo drives the cursor from its current position to target. Recorded
// human trajectories have first claim (when the library holds one within
// tolerance); the synthetic sub-movement generator is the fallback.
func moveTo(target Point, minDur, maxDur time.Duration, pauseChance float64) ([]Point, time.Duration, string) {
	cx, cy := win.CursorPos()
	from := Point{float64(cx), float64(cy)}
	if math.Hypot(target.X-from.X, target.Y-from.Y) < 1.5 {
		win.MoveToAbsolute(int(math.Round(target.X)), int(math.Round(target.Y)))
		return []Point{target}, 5 * time.Millisecond, "synthetic"
	}
	if samps, src, ok := tryHumanPath(from, target); ok {
		dur := playTimed(samps)
		// Land exactly on the requested pixel.
		win.MoveToAbsolute(int(math.Round(target.X)), int(math.Round(target.Y)))
		return ptsOf(samps), dur, "human:" + src
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

	samps := emit(from, subs, dur)
	playWithPauses(samps, pauseChance)
	total := dur
	pts := ptsOf(samps)

	if corr {
		cSubs, cDur := plan(aim, target, true)
		cSamps := emit(aim, cSubs, cDur)
		play(cSamps)
		pts = append(pts, ptsOf(cSamps)...)
		total += cDur
	}
	// Land exactly on the requested pixel.
	win.MoveToAbsolute(int(math.Round(target.X)), int(math.Round(target.Y)))
	return pts, total, "synthetic"
}

// Move glides the cursor to a physical desktop coordinate like a person.
// The returned source names where the path came from: "human:<file>" for a
// replayed recording, "synthetic" for the generated fallback.
func Move(x, y float64) ([]Point, time.Duration, string) {
	return moveTo(Point{x, y}, 100*time.Millisecond, 1100*time.Millisecond, 0.015)
}

// Nudge is a short corrective glide (calibration corrections).
func Nudge(x, y float64) ([]Point, time.Duration, string) {
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
	samps := emit(from, subs, dur)
	start := time.Now()
	for _, p := range samps {
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
		time.Sleep(time.Duration(5+rand.Float64()*8) * time.Millisecond)
		if rand.Float64() < 0.04 {
			time.Sleep(time.Duration(20+rand.Intn(40)) * time.Millisecond)
		}
	}
	win.MoveToAbsolute(int(math.Round(to.X)), int(math.Round(to.Y)))
	time.Sleep(time.Duration(80+rand.Intn(120)) * time.Millisecond)
	if err := win.ButtonUp(btn); err != nil {
		return ptsOf(samps), time.Since(start)
	}
	return ptsOf(samps), time.Since(start)
}
