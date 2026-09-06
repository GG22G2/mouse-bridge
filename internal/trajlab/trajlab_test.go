package trajlab

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"
)

func minJerk(u float64) float64 { return u * u * u * (10 - 15*u + 6*u*u) }

// straightConst samples a constant-speed straight line.
func straightConst(x0, y0, x1, y1, ms float64, n int) []Sample {
	pts := make([]Sample, n)
	for i := 0; i < n; i++ {
		u := float64(i) / float64(n-1)
		pts[i] = Sample{X: x0 + (x1-x0)*u, Y: y0 + (y1-y0)*u, T: ms * u}
	}
	return pts
}

func TestMetricsStraightLine(t *testing.T) {
	// perfectly straight path with a min-jerk speed profile
	n := 101
	pts := make([]Sample, n)
	for i := 0; i < n; i++ {
		u := float64(i) / float64(n-1)
		pts[i] = Sample{X: 100 + 500*minJerk(u), Y: 100, T: 500 * u}
	}
	m := ComputeMetrics(pts, &([2]float64{600, 100}))
	if math.Abs(m.ArcChord-1) > 1e-9 {
		t.Fatalf("arc/chord = %f, want 1", m.ArcChord)
	}
	if math.Abs(m.Asymmetry) > 1e-9 {
		t.Fatalf("asymmetry = %f, want 0", m.Asymmetry)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(m.Segments))
	}
	if m.Backtrack != 0 {
		t.Fatalf("backtrack = %f, want 0", m.Backtrack)
	}
	if m.EndErr > 1e-9 {
		t.Fatalf("end err = %f", m.EndErr)
	}
	if m.TimeToPeak < 0.4 || m.TimeToPeak > 0.6 {
		t.Fatalf("t2p = %f", m.TimeToPeak)
	}
}

func TestMetricsBulgeSignAndLateral(t *testing.T) {
	// travel +x, bulge toward +y (screen down = clockwise side) → asym > 0
	n := 200
	pts := make([]Sample, n)
	for i := 0; i < n; i++ {
		u := float64(i) / float64(n-1)
		pts[i] = Sample{X: 100 + 500*u, Y: 300 + 40*math.Sin(math.Pi*u), T: 500 * u}
	}
	m := ComputeMetrics(pts, &([2]float64{600, 300}))
	if m.Asymmetry <= 0.01 {
		t.Fatalf("asymmetry = %f, want > 0.01 (clockwise bulge)", m.Asymmetry)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(m.Segments))
	}
	seg := m.Segments[0]
	if math.Abs(seg.MaxLateral-40) > 2 {
		t.Fatalf("max lateral = %f, want ~40", seg.MaxLateral)
	}
	if seg.SideSign != 1 {
		t.Fatalf("side sign = %d, want +1", seg.SideSign)
	}
}

func TestMetricsTwoSubmovements(t *testing.T) {
	// fast swing (350px in 350ms) + slow corrective min-jerk (200px in 350ms)
	pts := []Sample{}
	x := 100.0
	for i := 0; i <= 175; i++ { // 2ms steps
		pts = append(pts, Sample{X: x + 2.0, Y: 100, T: float64((i + 1) * 2)})
		x += 2
	}
	x0 := x
	for i := 0; i <= 175; i++ {
		u := float64(i) / 175
		pts = append(pts, Sample{X: x0 + 200*minJerk(u), Y: 100, T: 350 + 350*u})
	}
	m := ComputeMetrics(pts, &([2]float64{x0 + 200, 100}))
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
}

func synthRecorderFeed(rec *Recorder, path []Sample, t0ms float64) {
	t := t0ms
	for _, p := range path {
		rec.Feed(p.X, p.Y, t+p.T)
	}
}

func TestRecorderTrimStatic(t *testing.T) {
	rec := NewRecorder(DefaultRecCfg())
	// 300ms rest at start
	for i := 0; i <= 150; i++ {
		rec.Feed(100, 100, float64(i*2))
	}
	// 400ms min-jerk move 100→500 (x), y fixed — a realistic smooth landing
	move := make([]Sample, 201)
	for i := 0; i <= 200; i++ {
		u := float64(i) / 200
		move[i] = Sample{X: 100 + 400*minJerk(u), Y: 100, T: 400 * u}
	}
	for i, p := range move {
		if i == 0 {
			continue // first sample duplicates the rest position
		}
		rec.Feed(p.X, p.Y, 300+p.T)
	}
	// 800ms rest at the end (must outlast the 600ms resume grace)
	for i := 0; i <= 400; i++ {
		rec.Feed(500, 100, 700+float64(i*2))
	}
	if rec.State() != "done" {
		t.Fatalf("state = %s, want done", rec.State())
	}
	if rec.TimedOut() {
		t.Fatal("unexpected timeout")
	}
	trim := rec.Trimmed()
	if len(trim) < 10 {
		t.Fatalf("trimmed too short: %d", len(trim))
	}
	dur := trim[len(trim)-1].T - trim[0].T
	if dur < 330 || dur > 450 {
		t.Fatalf("trimmed duration = %.0fms, want ~400", dur)
	}
	tgt := [2]float64{500, 100}
	m := ComputeMetrics(trim, &tgt)
	if m.EndErr > 2 {
		t.Fatalf("end err = %.2f", m.EndErr)
	}
	if m.Chord < 380 || m.Chord > 420 {
		t.Fatalf("chord = %.0f, want ~400", m.Chord)
	}
}

func TestRecorderMidHesitation(t *testing.T) {
	rec := NewRecorder(DefaultRecCfg())
	for i := 0; i <= 100; i++ {
		rec.Feed(0, 0, float64(i*2))
	}
	// move 150ms, hesitate 300ms (below end-speed but within resume grace),
	// move again 150ms, rest.
	seg1 := straightConst(0, 0, 200, 0, 150, 51)
	synthRecorderFeed(rec, seg1[1:], 200)
	for i := 0; i <= 150; i++ {
		rec.Feed(200, 0, 350+float64(i*2))
	}
	seg2 := straightConst(200, 0, 400, 0, 150, 51)
	synthRecorderFeed(rec, seg2[1:], 650)
	// 800ms rest (outlasts the resume grace)
	for i := 0; i <= 400; i++ {
		rec.Feed(400, 0, 800+float64(i*2))
	}
	if rec.State() != "done" {
		t.Fatalf("state = %s, want done (hesitation should resume)", rec.State())
	}
	trim := rec.Trimmed()
	dur := trim[len(trim)-1].T - trim[0].T
	if dur < 550 || dur > 750 {
		t.Fatalf("trimmed duration = %.0fms, want ~650 (both takes + hesitation)", dur)
	}
}

func TestStoreSaveLoadPick(t *testing.T) {
	dir := t.TempDir()
	mk := func(dist, angle float64) *TrajFile {
		rad := angle * math.Pi / 180
		pts := straightConst(500, 400, 500+dist*math.Cos(rad), 400+dist*math.Sin(rad), 400, 60)
		return &TrajFile{
			Kind: "human", Dist: dist, AngleDeg: angle,
			Start: [2]float64{pts[0].X, pts[0].Y}, End: [2]float64{pts[len(pts)-1].X, pts[len(pts)-1].Y},
			Points: pts,
		}
	}
	for _, p := range []struct {
		dist, angle float64
	}{{500, 0}, {500, 90}, {600, 0}, {502, 0}} {
		if _, err := Save(dir, mk(p.dist, p.angle)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("loaded %d files, want 4", len(files))
	}
	// ±10% of the recording's own length qualifies: 500@0, 500@90, 502@0 all
	// serve a 500px want; the 600px one does not (100 > 60).
	if q := Qualifying(files, 500); len(q) != 3 {
		t.Fatalf("Qualifying(500) = %d files, want 3", len(q))
	}
	if q := Qualifying(files, 2000); len(q) != 0 {
		t.Fatalf("Qualifying(2000) = %d files, want 0", len(q))
	}
	// wanted direction filters the random pool when it can
	got := Pick(files, 500, 0)
	if got.Traj == nil || math.Abs(got.Traj.AngleDeg-0) > 45 {
		t.Fatalf("picked %+v, want a ~0° recording", got)
	}
	got = Pick(files, 500, 90)
	if got.Traj == nil || math.Abs(got.Traj.AngleDeg-90) > 1 {
		t.Fatalf("picked %+v, want 500@90", got)
	}
	// out of tolerance → synthetic fallback
	got = Pick(files, 2000, 0)
	if got.Traj != nil {
		t.Fatalf("expected synthetic fallback, got %+v", got)
	}
	// sequence naming: same (dist,angle) must not collide
	p1, err := Save(dir, mk(700, 45))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Save(dir, mk(700, 45))
	if err != nil || p1 == p2 {
		t.Fatalf("sequence naming collided: %s vs %s (%v)", p1, p2, err)
	}
	if filepath.Dir(p1) != dir {
		t.Fatalf("unexpected dir %s", filepath.Dir(p1))
	}
}

func TestRescaleRotateStretch(t *testing.T) {
	pts := straightConst(100, 200, 600, 200, 400, 80) // 500px @0°
	tf := &TrajFile{
		Kind: "human", Dist: 500, AngleDeg: 0,
		Start: [2]float64{100, 200}, End: [2]float64{600, 200}, Points: pts,
	}
	out := Rescale(tf, [2]float64{50, 60}, 750, 90)
	if math.Abs(out[0].X-50) > 1e-6 || math.Abs(out[0].Y-60) > 1e-6 {
		t.Fatalf("start = %v,%v", out[0].X, out[0].Y)
	}
	last := out[len(out)-1]
	if math.Abs(last.X-50) > 1 || math.Abs(last.Y-810) > 1 {
		t.Fatalf("end = %.1f,%.1f, want 50,810 (750px @90°)", last.X, last.Y)
	}
	// timestamps survive
	if out[len(out)-1].T != 400 {
		t.Fatalf("last T = %f, want 400", out[len(out)-1].T)
	}
	// shape survives: arc/chord of the rescaled path stays 1
	var arc float64
	for i := 1; i < len(out); i++ {
		arc += math.Hypot(out[i].X-out[i-1].X, out[i].Y-out[i-1].Y)
	}
	if math.Abs(arc-750) > 3 {
		t.Fatalf("arc = %.1f, want ~750", arc)
	}
}

// TestGeneratorPassesGates is THE regression test for the trajectory
// generator: a sample of the acceptance matrix must pass all gates.
func TestGeneratorPassesGates(t *testing.T) {
	dists := []float64{50, 200, 500, 800, 1500}
	angles := []float64{0, 90}
	rep := RunMatrix(dists, angles, 32)
	if rep.FailPaths != 0 {
		for _, c := range rep.Cells {
			if c.FailReps > 0 {
				t.Errorf("cell %.0fpx@%.0f°: %d/%d failed: %v",
					c.Dist, c.Angle, c.FailReps, c.Reps, c.FailCounts)
			}
		}
		t.Fatalf("generator failed %d/%d paths", rep.FailPaths, rep.Paths)
	}
}

// TestNoDirectionalSignature guards the curvature-sign balance across a
// larger sample: systematic left/right bulges are a machine signature.
func TestNoDirectionalSignature(t *testing.T) {
	reps := 40
	rep := RunMatrix([]float64{300, 800}, []float64{0, 45, 90, 135}, reps)
	asymLim, fracBand := signBalanceLimits(reps)
	for _, c := range rep.Cells {
		if math.Abs(c.MeanAsym) > asymLim {
			t.Errorf("%.0fpx@%.0f°: mean asymmetry %.4f (systematic bulge)", c.Dist, c.Angle, c.MeanAsym)
		}
		if c.FracRightBulge < 0.5-fracBand || c.FracRightBulge > 0.5+fracBand {
			t.Errorf("%.0fpx@%.0f°: right-bulge fraction %.2f (one-sided)", c.Dist, c.Angle, c.FracRightBulge)
		}
	}
}

// keep rand import used even if tests above change
var _ = rand.Float64

// TestRecorderStaircaseOnset guards the injected-motion case: SendInput
// playback moves the cursor in ~4-11ms steps, so a 1ms poller sees bursts of
// huge single-sample speeds separated by zero-speed samples. Onset must
// still fire (no-gap sustained logic), and end detection must not fire
// mid-motion.
func TestRecorderStaircaseOnset(t *testing.T) {
	rec := NewRecorder(DefaultRecCfg())
	tms := 0.0
	feed := func(x, y float64) { rec.Feed(x, y, tms); tms += 1 }
	for i := 0; i < 200; i++ { // 200ms rest at origin
		feed(0, 0)
	}
	for i := 0; i < 400; i++ { // 400ms of staircase motion: +10px every 8ms
		if i%8 == 7 {
			feed(float64((i+1)/8)*10, 0)
		} else {
			feed(float64((i+1)/8)*10, 0) // position holds between injected steps
		}
	}
	finalX := float64(400 / 8 * 10)
	for i := 0; i < 900; i++ { // 900ms rest
		feed(finalX, 0)
	}
	if rec.State() != "done" {
		t.Fatalf("state = %s, want done", rec.State())
	}
	trim := rec.Trimmed()
	if trim == nil {
		t.Fatal("no trimmed data")
	}
	dur := trim[len(trim)-1].T - trim[0].T
	if dur < 340 || dur > 460 {
		t.Fatalf("trimmed duration = %.0fms, want ~400", dur)
	}
	tgt := [2]float64{finalX, 0}
	m := ComputeMetrics(trim, &tgt)
	if m.EndErr > 2 {
		t.Fatalf("end err = %.2f", m.EndErr)
	}
}

// mkFile builds a minimal in-memory recording: straight line, min-jerk-ish
// timing is unnecessary here — only chord/angle/points matter for picking.
func mkFile(dist, angle float64) *TrajFile {
	rad := angle * math.Pi / 180
	pts := straightConst(500, 400, 500+dist*math.Cos(rad), 400+dist*math.Sin(rad), dist, 60)
	return &TrajFile{
		Kind: "human", Dist: dist, AngleDeg: angle,
		Start:  [2]float64{pts[0].X, pts[0].Y},
		End:    [2]float64{pts[len(pts)-1].X, pts[len(pts)-1].Y},
		Points: pts,
	}
}

func TestPickToleranceBoundaries(t *testing.T) {
	files := []*TrajFile{mkFile(100, 0), mkFile(105, 0)}
	// a 100px recording serves exactly 90..110 (±10% of its own length)
	if q := Qualifying(files, 110); len(q) != 2 {
		t.Fatalf("want 110 within both recordings, got %d", len(q))
	}
	if q := Qualifying(files, 110.1); len(q) != 1 {
		t.Fatalf("want 110.1 outside the 100px recording, got %d", len(q))
	}
	if q := Qualifying(files, 90); len(q) != 1 {
		t.Fatalf("want 90 within the 100px recording only, got %d", len(q))
	}
	if q := Qualifying(files, 89.9); len(q) != 0 {
		t.Fatalf("want 89.9 outside both recordings, got %d", len(q))
	}
}

func TestPickRandomAmongQualifying(t *testing.T) {
	// need 105: both the 100px and the 105px recordings qualify — the draw
	// must be random, not an exact-match shortcut. Over 60 draws both files
	// must appear (P(miss) = 2·2⁻⁶⁰).
	files := []*TrajFile{mkFile(100, 0), mkFile(105, 0)}
	seen := map[float64]bool{}
	for i := 0; i < 60; i++ {
		got := Pick(files, 105, 0)
		if got.Traj == nil {
			t.Fatal("no pick despite qualifying recordings")
		}
		seen[got.Traj.Dist] = true
	}
	if !seen[100] || !seen[105] {
		t.Fatalf("random draw never used both candidates: %v", seen)
	}
}

func TestPickPrefersDirection(t *testing.T) {
	files := []*TrajFile{mkFile(500, 0), mkFile(500, 270)}
	// a rightward want must always draw from the rightward recording
	for i := 0; i < 40; i++ {
		if got := Pick(files, 500, 0); math.Abs(got.Traj.AngleDeg) > 1 {
			t.Fatalf("pick %d: got %.0f°, want 0° pool only", i, got.Traj.AngleDeg)
		}
	}
	// 180° matches nothing within 45° — pool opens up, both must appear
	seen := map[float64]bool{}
	for i := 0; i < 60; i++ {
		seen[Pick(files, 500, 180).Traj.AngleDeg] = true
	}
	if !seen[0] || !seen[270] {
		t.Fatalf("no direction match should open the whole pool: %v", seen)
	}
}

func TestRescaleRealRecordings(t *testing.T) {
	files, err := LoadAll("../../trajectories")
	if err != nil || len(files) == 0 {
		t.Skip("repo trajectory library not present")
	}
	for _, f := range files {
		// the stored Dist/AngleDeg must really be the chord/angle of the
		// points — Rescale relies on that to land the endpoint exactly
		chord := math.Hypot(f.End[0]-f.Start[0], f.End[1]-f.Start[1])
		if math.Abs(chord-f.Dist) > 0.5*f.Dist/100 {
			t.Errorf("%s: stored dist %.1f vs point chord %.1f", f.File, f.Dist, chord)
			continue
		}
		newDist := f.Dist * 1.07
		newAng := wrap180(f.AngleDeg + 30)
		rad := newAng * math.Pi / 180
		start := [2]float64{300, 300}
		wantEnd := [2]float64{300 + newDist*math.Cos(rad), 300 + newDist*math.Sin(rad)}
		pts := Rescale(f, start, newDist, newAng)
		if len(pts) != len(f.Points) {
			t.Errorf("%s: rescale changed point count %d → %d", f.File, len(f.Points), len(pts))
			continue
		}
		last := pts[len(pts)-1]
		if math.Hypot(last.X-wantEnd[0], last.Y-wantEnd[1]) > 0.01 {
			t.Errorf("%s: rescaled endpoint (%.2f,%.2f), want (%.2f,%.2f)",
				f.File, last.X, last.Y, wantEnd[0], wantEnd[1])
		}
		for i := range pts {
			if pts[i].T != f.Points[i].T {
				t.Errorf("%s: rescale moved timestamps", f.File)
				break
			}
		}
	}
}
