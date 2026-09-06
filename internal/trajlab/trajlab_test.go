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
	// 500@0 wanted → prefer exact direction & distance
	got := Pick(files, 500, 0, 30)
	if got.Traj == nil || math.Abs(got.Traj.Dist-500) > 3 || math.Abs(got.Traj.AngleDeg-0) > 1 {
		t.Fatalf("picked %+v, want 500@0", got)
	}
	if got.Confidence < 0.9 {
		t.Fatalf("confidence = %.2f, want high", got.Confidence)
	}
	// different direction preferred when wanted
	got = Pick(files, 500, 90, 30)
	if got.Traj == nil || math.Abs(got.Traj.AngleDeg-90) > 1 {
		t.Fatalf("picked %+v, want 500@90", got)
	}
	// out of tolerance → synthetic fallback
	got = Pick(files, 2000, 0, 30)
	if got.Traj != nil || got.Confidence != 0 {
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
