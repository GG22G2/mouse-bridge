package trajlab

import "math"

// RecCfg tunes the motion-onset / motion-end detection.
type RecCfg struct {
	OnsetPxS  float64 // cursor speed that counts as "the hand started moving"
	OnsetMs   float64 // speed must stay above OnsetPxS this long to start
	EndPxS    float64 // speed below which the hand counts as "at rest"
	EndMs     float64 // rest must persist this long to stop the take...
	ResumeMs  float64 // ...unless the hand starts moving again within this grace window
	MaxWaitMs float64 // give up if no motion onset within this window
	MaxMoveMs float64 // hard cap on one recording once motion started
}

// DefaultRecCfg returns the standard detection thresholds: a deliberate
// reach starts well above 60px/s, micro-tremor stays far below 25px/s.
func DefaultRecCfg() RecCfg {
	return RecCfg{
		OnsetPxS: 60, OnsetMs: 40,
		EndPxS: 25, EndMs: 250, ResumeMs: 600,
		MaxWaitMs: 30000, MaxMoveMs: 15000,
	}
}

// Recorder is the pure state machine behind the recording command: feed it
// timestamped cursor samples and it segments 静止→移动→静止, trimming the
// static lead-in and tail so only the actual movement becomes the stored
// trajectory. It is free of any OS calls, so it is unit-testable.
type Recorder struct {
	cfg                 RecCfg
	samples             []Sample
	state               string  // wait | move | rest | done | timeout
	highSince, lowSince float64 // -1 = not currently sustained
	highIdx             int     // first sample of the sustained-high run
	onsetIdx            int
	restSince           float64
}

func NewRecorder(cfg RecCfg) *Recorder {
	return &Recorder{cfg: cfg, state: "wait", highSince: -1, lowSince: -1}
}

func (r *Recorder) State() string { return r.state }

// TimedOut reports whether the take ended by timeout instead of a clean
// stop (no movement happened, or the hand never settled).
func (r *Recorder) TimedOut() bool { return r.state == "timeout" }

// Feed consumes one cursor sample (tMs = milliseconds since the take began).
func (r *Recorder) Feed(x, y, tMs float64) {
	r.samples = append(r.samples, Sample{X: x, Y: y, T: tMs})
	n := len(r.samples)
	if n < 2 {
		return
	}
	prev := r.samples[n-2]
	dt := tMs - prev.T
	if dt <= 0 {
		dt = 0.5
	}
	speed := math.Hypot(x-prev.X, y-prev.Y) / (dt / 1000)

	switch r.state {
	case "wait":
		if speed >= r.cfg.OnsetPxS {
			if r.highSince < 0 {
				r.highSince = tMs
				r.highIdx = n - 1 // first sample of the sustained-high run
			}
			if tMs-r.highSince >= r.cfg.OnsetMs {
				r.state = "move"
				r.onsetIdx = r.highIdx
				r.lowSince = -1
			}
		} else {
			r.highSince = -1
		}
		if tMs-r.samples[0].T > r.cfg.MaxWaitMs {
			r.state = "timeout"
		}
	case "move":
		if speed < r.cfg.EndPxS {
			if r.lowSince < 0 {
				r.lowSince = tMs
			}
			if tMs-r.lowSince >= r.cfg.EndMs {
				r.state = "rest"
				r.restSince = r.lowSince
			}
		} else {
			r.lowSince = -1
		}
		if tMs-r.samples[r.onsetIdx].T > r.cfg.MaxMoveMs {
			r.state = "timeout"
		}
	case "rest":
		// grace window: a brief hesitation mid-reach resumes the take
		if speed >= r.cfg.OnsetPxS && tMs-r.restSince < r.cfg.ResumeMs {
			r.state = "move"
			r.lowSince = -1
			return
		}
		if tMs-r.restSince >= r.cfg.ResumeMs {
			r.state = "done"
		}
	}
}

// Trimmed returns the recorded movement with the static lead-in and tail
// removed and timestamps rebased to 0. Only valid once State() == "done".
func (r *Recorder) Trimmed() []Sample {
	if r.state != "done" {
		return nil
	}
	lo := r.onsetIdx - 1
	if lo < 0 {
		lo = 0
	}
	// Cut the tail where the cursor actually settled (within 0.8px of its
	// final position), so the slow below-threshold deceleration creep stays
	// part of the trajectory instead of being mistaken for rest.
	hi := len(r.samples) - 1
	final := r.samples[hi]
	for hi > r.onsetIdx {
		dx, dy := r.samples[hi].X-final.X, r.samples[hi].Y-final.Y
		if dx*dx+dy*dy > 0.64 {
			break
		}
		hi--
	}
	if hi <= lo {
		return nil
	}
	out := make([]Sample, hi-lo+1)
	t0 := r.samples[lo].T
	for i := lo; i <= hi; i++ {
		out[i-lo] = Sample{X: r.samples[i].X, Y: r.samples[i].Y, T: r.samples[i].T - t0}
	}
	return out
}
