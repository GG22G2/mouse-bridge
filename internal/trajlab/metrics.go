package trajlab

import "math"

// Segment is one detected sub-movement: the run of samples between two
// velocity minima (or the path ends). Detection is blind — the same
// segmenter runs on generated and human-recorded paths.
type Segment struct {
	StartIdx, EndIdx int
	Chord            float64 // straight-line length of the segment (px)
	ArcLen           float64 // traveled length along the segment (px)
	MaxLateral       float64 // max deviation from the segment's chord line (px)
	LateralRatio     float64 // MaxLateral / Chord
	SideSign         int     // sign of the widest deviation along the perpendicular
	// axis q = (-uy, ux): +1 means the segment bulges on the clockwise side
	// of its travel direction (screen coords, y grows downward).
}

// Metrics is the shape/timing fingerprint of one sampled trajectory.
type Metrics struct {
	DurationMs float64
	Chord      float64 // straight-line distance first→last sample (px)
	ArcLen     float64 // traveled length (px)
	ArcChord   float64 // ArcLen / Chord — how much the path detours
	PeakSpeed  float64 // px/s
	MeanSpeed  float64 // ArcLen / duration
	TimeToPeak float64 // 0..1, when the speed peak happens
	Segments   []Segment
	Backtrack  float64 // fraction of samples moving backward along the chord
	Asymmetry  float64 // signed bulge area / chord²: + = bulges on the
	// clockwise side of travel (screen coords). Balanced generation → ≈0.
	MaxJumpPxS float64 // fastest single-sample step (px/s)
	EndErr     float64 // distance from last sample to the intended target (px)
	Smoothness float64 // log dimensionless jerk; lower = smoother
	NSamples   int
}

// ComputeMetrics fingerprints a sampled trajectory. target (start→end goal)
// is optional; when given, EndErr is how far the last sample landed from it.
func ComputeMetrics(pts []Sample, target *[2]float64) Metrics {
	n := len(pts)
	m := Metrics{NSamples: n}
	if n < 3 {
		return m
	}
	x0, y0 := pts[0].X, pts[0].Y
	x1, y1 := pts[n-1].X, pts[n-1].Y
	m.Chord = math.Hypot(x1-x0, y1-y0)
	for i := 1; i < n; i++ {
		m.ArcLen += math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
	if m.Chord > 0 {
		m.ArcChord = m.ArcLen / m.Chord
	}
	m.DurationMs = pts[n-1].T - pts[0].T
	if m.DurationMs > 0 {
		m.MeanSpeed = m.ArcLen / (m.DurationMs / 1000)
	}
	if target != nil {
		m.EndErr = math.Hypot(x1-target[0], y1-target[1])
	}

	// per-sample speeds (px/s) + jump check (dt floored at 1ms so adjacent
	// samples sharing a rounded timestamp can't fake infinite speed)
	raw := make([]float64, n)
	for i := 1; i < n; i++ {
		dt := pts[i].T - pts[i-1].T
		if dt < 1 {
			dt = 1
		}
		v := math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y) / (dt / 1000)
		raw[i] = v
		if v > m.MaxJumpPxS {
			m.MaxJumpPxS = v
		}
	}
	raw[0] = raw[1]
	sm := smooth3(smooth3(raw))
	peakIdx := 1
	for i := 2; i < n-1; i++ {
		if sm[i] > sm[peakIdx] {
			peakIdx = i
		}
	}
	m.PeakSpeed = sm[peakIdx]
	if m.DurationMs > 0 {
		m.TimeToPeak = (pts[peakIdx].T - pts[0].T) / m.DurationMs
	}

	// sub-movement boundaries: the start of every below-threshold speed run
	// that is followed by faster motion again (so the head ramp and the
	// final deceleration don't count, but junction dips do). Boundary
	// detection uses a deeper-smoothed copy — sub-movement junctions are
	// wide, deep dips; sampling noise is shallow.
	smB := smooth3(smooth3(smooth3(sm)))
	bounds := findBoundaries(pts, smB, m.DurationMs)
	prev := 0
	for _, b := range append(bounds, n-1) {
		seg := segmentOf(pts, prev, b)
		if seg.ArcLen >= 2 || b == n-1 {
			m.Segments = append(m.Segments, seg)
		}
		prev = b
	}

	// signed bulge area (shoelace closed back to start), normalized.
	// Negated so positive = bulge on the clockwise side, matching SideSign.
	var area float64
	for i := 0; i < n-1; i++ {
		area += pts[i].X*pts[i+1].Y - pts[i+1].X*pts[i].Y
	}
	area += pts[n-1].X*y0 - x0*pts[n-1].Y
	if m.Chord > 1 {
		m.Asymmetry = -(area / 2) / (m.Chord * m.Chord)
	}

	// backtrack along the main axis
	if m.Chord > 0 {
		ux, uy := (x1-x0)/m.Chord, (y1-y0)/m.Chord
		prevP := 0.0
		back := 0
		for i := 1; i < n; i++ {
			p := (pts[i].X-x0)*ux + (pts[i].Y-y0)*uy
			if p < prevP-0.5 {
				back++
			}
			prevP = p
		}
		m.Backtrack = float64(back) / float64(n-1)
	}

	m.Smoothness = logDimlessJerk(pts)
	return m
}

// findBoundaries returns sample indices where a sub-movement ends: the first
// sample of each run of speeds below 30% of the peak that later rises above
// it again. Runs touching the path start (acceleration ramp) or end (final
// deceleration) are not boundaries.
func findBoundaries(pts []Sample, sm []float64, durMs float64) []int {
	n := len(pts)
	pk := max(sm)
	if pk <= 0 {
		return nil
	}
	thr := 0.30 * pk
	var bounds []int
	lastT := -1000.0
	inRun := false
	for i := 1; i < n; i++ {
		below := sm[i] < thr
		if below && !inRun {
			inRun = true
			// does motion rise above the threshold again after this run?
			rises := false
			for j := i + 1; j < n; j++ {
				if sm[j] >= thr {
					rises = true
					break
				}
			}
			tt := pts[i].T - pts[0].T
			if rises && tt >= 60 && tt <= durMs-60 && tt-lastT >= 30 {
				bounds = append(bounds, i)
				lastT = tt
			}
		} else if !below {
			inRun = false
		}
	}
	return bounds
}

func segmentOf(pts []Sample, a, b int) Segment {
	seg := Segment{StartIdx: a, EndIdx: b}
	if b <= a {
		return seg
	}
	ax, ay := pts[a].X, pts[a].Y
	bx, by := pts[b].X, pts[b].Y
	seg.Chord = math.Hypot(bx-ax, by-ay)
	for i := a + 1; i <= b; i++ {
		seg.ArcLen += math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
	if seg.Chord > 0.5 && b > a {
		ux, uy := (bx-ax)/seg.Chord, (by-ay)/seg.Chord
		qx, qy := -uy, ux // perpendicular: positive side = clockwise of travel
		worst, wsign := 0.0, 1
		for i := a; i <= b; i++ {
			d := (pts[i].X-ax)*qx + (pts[i].Y-ay)*qy
			if ad := math.Abs(d); ad > worst {
				worst, wsign = ad, signOf(d)
			}
		}
		seg.MaxLateral = worst
		seg.LateralRatio = worst / seg.Chord
		seg.SideSign = wsign
	}
	return seg
}

// logDimlessJerk estimates smoothness as the log of a dimensionless jerk
// measure (positions are resampled to a uniform 8ms grid and lightly
// smoothed first, so sub-pixel jitter doesn't dominate the derivatives).
func logDimlessJerk(pts []Sample) float64 {
	T := pts[len(pts)-1].T - pts[0].T
	if T < 60 {
		return 0
	}
	var D float64
	for i := 1; i < len(pts); i++ {
		D += math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
	if D < 10 {
		return 0
	}
	dt := 8.0
	n := int(T / dt)
	if n < 8 {
		return 0
	}
	xs := make([]float64, n+1)
	ys := make([]float64, n+1)
	k := 0
	for i := 0; i < n; i++ {
		tt := float64(i) * dt
		for k < len(pts)-2 && pts[k+1].T-pts[0].T < tt {
			k++
		}
		ta := pts[k].T - pts[0].T
		tb := pts[k+1].T - pts[0].T
		f := 0.0
		if tb > ta {
			f = (tt - ta) / (tb - ta)
		}
		xs[i] = pts[k].X + (pts[k+1].X-pts[k].X)*f
		ys[i] = pts[k].Y + (pts[k+1].Y-pts[k].Y)*f
	}
	xs[n], ys[n] = pts[len(pts)-1].X, pts[len(pts)-1].Y
	xs = smooth3(xs)
	xs = smooth3(xs)
	ys = smooth3(ys)
	ys = smooth3(ys)
	central := func(v []float64) []float64 {
		out := make([]float64, len(v)-2)
		for i := 1; i < len(v)-1; i++ {
			out[i-1] = (v[i+1] - v[i-1]) / (2 * dt)
		}
		return out
	}
	vx, vy := central(xs), central(ys)
	ax, ay := central(vx), central(vy)
	jx, jy := central(ax), central(ay)
	var sj float64
	for i := range jx {
		sj += jx[i]*jx[i] + jy[i]*jy[i]
	}
	rms := math.Sqrt(sj / float64(2*len(jx)))
	return math.Log(math.Pow(T/1000, 2.5) / D * rms)
}

func smooth3(v []float64) []float64 {
	if len(v) < 3 {
		return v
	}
	out := make([]float64, len(v))
	out[0], out[len(out)-1] = v[0], v[len(v)-1]
	for i := 1; i < len(v)-1; i++ {
		out[i] = (v[i-1] + 2*v[i] + v[i+1]) / 4
	}
	return out
}

func max(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

func signOf(v float64) int {
	if v >= 0 {
		return 1
	}
	return -1
}
