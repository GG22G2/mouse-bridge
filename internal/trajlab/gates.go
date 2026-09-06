package trajlab

import (
	"fmt"
	"math"
	"time"

	"mousebridge/internal/mouse"
)

// AnalysisDistances covers the working range of a 1920×1080 desktop
// (corner-to-corner ≈ 2203px), with extra density in the common 100..800px
// band where most real pointing happens.
var AnalysisDistances = []float64{10, 25, 50, 75, 100, 150, 200, 250, 300, 350, 400, 450, 500, 600, 700, 800, 1000, 1200, 1400, 1600, 1800, 2000, 2200}

// AnalysisAngles is the 8-direction wind rose: horizontal, vertical, both diagonals.
var AnalysisAngles = []float64{0, 45, 90, 135, 180, 225, 270, 315}

// Gate is one acceptance check applied to a single generated path.
type Gate struct {
	Name   string
	Pass   bool
	Detail string
}

// CellReport aggregates one (distance, direction) cell of the matrix.
type CellReport struct {
	Dist       float64        `json:"dist"`
	Angle      float64        `json:"angle"`
	Reps       int            `json:"reps"`
	FailReps   int            `json:"fail_reps"`
	FailCounts map[string]int `json:"fail_counts,omitempty"`

	ArcChordMin, ArcChordMax   float64 `json:"arc_chord_min_max"`
	MaxSegLateralRatio         float64 `json:"max_seg_lateral_ratio"`
	PeakRatioMin, PeakRatioMax float64 `json:"peak_ratio_min_max"`
	T2PMin, T2PMax             float64 `json:"t2p_min_max"`
	DurRatioMin, DurRatioMax   float64 `json:"dur_ratio_min_max"`
	MeanAsym                   float64 `json:"mean_asymmetry"`
	FracRightBulge             float64 `json:"frac_right_bulge"`
	MaxJumpPxS                 float64 `json:"max_jump_pxs"`
}

// MatrixReport is the full acceptance run.
type MatrixReport struct {
	GeneratedAt string         `json:"generated_at"`
	RepsPerCell int            `json:"reps_per_cell"`
	Paths       int            `json:"total_paths"`
	FailPaths   int            `json:"total_failed_paths"`
	Cells       []CellReport   `json:"cells"`
	FailByGate  map[string]int `json:"fail_by_gate,omitempty"`
	Verdict     string         `json:"verdict"`
}

// RunMatrix generates and gates dist×angle×reps paths. Pure compute — the
// real cursor is never touched.
func RunMatrix(distances, angles []float64, reps int) *MatrixReport {
	rep := &MatrixReport{
		GeneratedAt: time.Now().Format(time.RFC3339),
		RepsPerCell: reps,
		FailByGate:  map[string]int{},
	}
	for _, d := range distances {
		for _, a := range angles {
			cell := runCell(d, a, reps)
			rep.Cells = append(rep.Cells, cell)
			rep.Paths += reps
			rep.FailPaths += cell.FailReps
			for g, c := range cell.FailCounts {
				rep.FailByGate[g] += c
			}
		}
	}
	rep.Verdict = "PASS"
	if rep.FailPaths > 0 {
		rep.Verdict = "FAIL"
	}
	return rep
}

func runCell(dist, angle float64, reps int) CellReport {
	cell := CellReport{
		Dist: dist, Angle: angle, Reps: reps,
		ArcChordMin: math.Inf(1), ArcChordMax: math.Inf(-1),
		PeakRatioMin: math.Inf(1), PeakRatioMax: math.Inf(-1),
		T2PMin: math.Inf(1), T2PMax: math.Inf(-1),
		DurRatioMin: math.Inf(1), DurRatioMax: math.Inf(-1),
		FailCounts: map[string]int{},
	}
	rad := angle * math.Pi / 180
	from := mouse.Point{X: 960, Y: 540}
	to := mouse.Point{X: 960 + dist*math.Cos(rad), Y: 540 + dist*math.Sin(rad)}
	tgt := [2]float64{to.X, to.Y}

	var asymSum float64
	right := 0
	for i := 0; i < reps; i++ {
		samps, subs, dur := mouse.DebugPathTimed(from, to)
		pts := make([]Sample, len(samps))
		for j, s := range samps {
			pts[j] = Sample{X: s.X, Y: s.Y, T: s.T}
		}
		m := ComputeMetrics(pts, &tgt)
		failed := false
		for _, g := range ApplyGates(m, subs, dur, dist) {
			if !g.Pass {
				cell.FailCounts[g.Name]++
				failed = true
			}
		}
		if failed {
			cell.FailReps++
		}
		pr := 0.0
		if m.MeanSpeed > 0 {
			pr = m.PeakSpeed / m.MeanSpeed
		}
		cell.ArcChordMin = math.Min(cell.ArcChordMin, m.ArcChord)
		cell.ArcChordMax = math.Max(cell.ArcChordMax, m.ArcChord)
		cell.MaxSegLateralRatio = math.Max(cell.MaxSegLateralRatio, maxSegRatio(m))
		cell.PeakRatioMin = math.Min(cell.PeakRatioMin, pr)
		cell.PeakRatioMax = math.Max(cell.PeakRatioMax, pr)
		cell.T2PMin = math.Min(cell.T2PMin, m.TimeToPeak)
		cell.T2PMax = math.Max(cell.T2PMax, m.TimeToPeak)
		dr := durRatio(dur, dist)
		cell.DurRatioMin = math.Min(cell.DurRatioMin, dr)
		cell.DurRatioMax = math.Max(cell.DurRatioMax, dr)
		cell.MaxJumpPxS = math.Max(cell.MaxJumpPxS, m.MaxJumpPxS)
		asymSum += m.Asymmetry
		if m.Asymmetry > 0.002 {
			right++
		}
	}
	cell.MeanAsym = asymSum / float64(reps)
	cell.FracRightBulge = float64(right) / float64(reps)

	// aggregate curvature-sign balance gates (per cell, not per path).
	// The bands scale with this cell's rep count: small samples get wide
	// bands so sampling noise doesn't trip them, while a systematic
	// one-sided bulge (the machine signature this gate hunts) still fails
	// hard at any sample size.
	asymLim, fracBand := signBalanceLimits(reps)
	if math.Abs(cell.MeanAsym) > asymLim {
		cell.FailCounts["sign_balance"]++
		cell.FailReps++
	}
	if cell.FracRightBulge < 0.5-fracBand || cell.FracRightBulge > 0.5+fracBand {
		cell.FailCounts["sign_balance"]++
		cell.FailReps++
	}
	if len(cell.FailCounts) == 0 {
		cell.FailCounts = nil
	}
	return cell
}

func maxSegRatio(m Metrics) float64 {
	w := 0.0
	for _, s := range m.Segments {
		if s.LateralRatio > w {
			w = s.LateralRatio
		}
	}
	return w
}

// signBalanceLimits returns the acceptance band for curvature-sign balance
// at a given rep count: |mean asymmetry| ≤ asymLim and the right-bulge
// fraction within 0.5 ± fracBand. Calibrated to the measured per-path
// asymmetry std (~0.045, worst case for short single-segment moves).
func signBalanceLimits(reps int) (asymLim, fracBand float64) {
	n := float64(reps)
	if n < 1 {
		n = 1
	}
	const perPathStd = 0.045
	return math.Max(0.008, 4.0*perPathStd/math.Sqrt(n)), 4.0 * math.Sqrt(0.25/n)
}

func durRatio(dur time.Duration, dist float64) float64 {
	base := mouse.FittsMs(dist)
	if base <= 0 {
		return 1
	}
	ms := dur.Seconds() * 1000
	if ms > 1100 {
		ms = 1100 // Move() clamps here
	}
	if ms < 100 {
		ms = 100
	}
	return ms / base
}

// ApplyGates checks one generated path. The gates encode what "a plausible
// human reach" means; they are the acceptance criteria for the generator.
func ApplyGates(m Metrics, plannedSubs int, dur time.Duration, dist float64) []Gate {
	var g []Gate

	// G1 total detour: humans overshoot straight lines by a few percent.
	hi := 1.18
	if dist < 100 {
		hi = 1.40 // short corrective moves curve relatively more
	}
	g = append(g, Gate{"arc_chord", m.ArcChord >= 1.00 && m.ArcChord <= hi,
		fmt.Sprintf("弧/弦=%.3f (期望 1.00..%.2f)", m.ArcChord, hi)})

	// G2 per-sub-movement bow: no segment may bulge far off its chord —
	// this is the gate that catches a "hook" drawn through the slow zone.
	bad := 0.0
	for _, s := range m.Segments {
		if s.MaxLateral > math.Max(0.18*s.Chord, 4) {
			bad = s.MaxLateral
		}
	}
	g = append(g, Gate{"seg_lateral", bad == 0, fmt.Sprintf("最大段侧偏=%.1fpx", bad)})

	// G3 the speed peak must happen inside the movement, early-to-mid.
	// Exempt for micro-moves (<25px): at ~130px/s peak, the 8-12Hz
	// physiological tremor (±20px/s) dominates the speed series and the
	// peak position is noise, not motor control.
	if dist >= 25 {
		g = append(g, Gate{"t2p", m.TimeToPeak >= 0.15 && m.TimeToPeak <= 0.60,
			fmt.Sprintf("峰速时刻=%.2f", m.TimeToPeak)})
	}

	// G4 duration follows Fitts (±randomization already inside the plan).
	r := durRatio(dur, dist)
	g = append(g, Gate{"duration", r >= 0.75 && r <= 1.30,
		fmt.Sprintf("时长/Fitts=%.2f", r)})

	// G5 sub-movement count per distance band.
	loS, hiS := 1, 1
	if dist >= 70 && dist < 420 {
		loS, hiS = 2, 2
	} else if dist >= 420 {
		loS, hiS = 2, 3
	}
	g = append(g, Gate{"submoves", plannedSubs >= loS && plannedSubs <= hiS,
		fmt.Sprintf("段数=%d (期望 %d..%d)", plannedSubs, loS, hiS)})

	// G6 the planned structure must be visible in the sampled path too.
	// The detector can legitimately miss a tiny corrective segment whose
	// speed never rises above 30% of the global peak — one segment of
	// slack, no more.
	g = append(g, Gate{"seg_detect", len(m.Segments) >= plannedSubs-1 && len(m.Segments) <= plannedSubs,
		fmt.Sprintf("检出段=%d 计划段=%d", len(m.Segments), plannedSubs)})

	// G7 no meaningful backtracking (overshoot lives in Move(), not plan).
	g = append(g, Gate{"backtrack", m.Backtrack <= 0.015,
		fmt.Sprintf("回退占比=%.3f", m.Backtrack)})

	// G8 land exactly on target.
	g = append(g, Gate{"end_err", m.EndErr <= 0.5,
		fmt.Sprintf("终点偏差=%.2fpx", m.EndErr)})

	// G9 no teleport-scale discontinuities between consecutive samples.
	// (Real fast flicks reach ~9000px/s at the peak; a step above 15000px/s
	// is an artifact, not motion.)
	g = append(g, Gate{"jump", m.MaxJumpPxS <= 15000,
		fmt.Sprintf("单步峰值=%.0fpx/s", m.MaxJumpPxS)})

	// G10 peak-to-mean speed ratio in a plausible band (a swing covering
	// ~90% of the distance in half the time peaks at ~3.4× mean).
	pr := 0.0
	if m.MeanSpeed > 0 {
		pr = m.PeakSpeed / m.MeanSpeed
	}
	g = append(g, Gate{"peak_ratio", pr >= 1.2 && pr <= 3.8,
		fmt.Sprintf("峰/均=%.2f", pr)})

	return g
}
