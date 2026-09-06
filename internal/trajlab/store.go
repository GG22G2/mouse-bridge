package trajlab

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TrajFile is the on-disk format: one JSON file per trajectory, stored in a
// fixed directory so the folder can be copied between machines as-is.
type TrajFile struct {
	Version    int        `json:"version"`
	Kind       string     `json:"kind"` // "human" | "synthetic"
	Dist       float64    `json:"dist"` // chord length start→end (px)
	AngleDeg   float64    `json:"angle_deg"`
	Start      [2]float64 `json:"start"`
	End        [2]float64 `json:"end"`
	ScreenW    int        `json:"screen_w"`
	ScreenH    int        `json:"screen_h"`
	DurationMs float64    `json:"duration_ms"`
	PeakSpeed  float64    `json:"peak_speed"`
	ArcChord   float64    `json:"arc_chord"`
	RecordedAt string     `json:"recorded_at"`
	Notes      string     `json:"notes,omitempty"`
	Points     []Sample   `json:"points"`

	File string `json:"-"` // set by LoadAll
}

// DefaultDir is the fixed trajectory library directory. The path is stable
// so a library recorded on one machine can be dropped onto another.
func DefaultDir() string {
	if d := os.Getenv("MOUSEBRIDGE_TRAJ_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".mouse-bridge", "trajectories")
}

// Save writes one trajectory as its own JSON file named
// d<dist>_a<angle>_<seq>.json, so (distance, direction) groups sort together.
func Save(dir string, t *TrajFile) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if t.Version == 0 {
		t.Version = 1
	}
	if t.RecordedAt == "" {
		t.RecordedAt = time.Now().Format(time.RFC3339)
	}
	d := int(math.Round(t.Dist))
	a := ((int(math.Round(t.AngleDeg)) % 360) + 360) % 360
	base := filepath.Join(dir, fmt.Sprintf("d%04d_a%03d", d, a))
	for n := 1; n < 10000; n++ {
		p := fmt.Sprintf("%s_%03d.json", base, n)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			b, err := json.MarshalIndent(t, "", " ")
			if err != nil {
				return "", err
			}
			t.File = p
			return p, os.WriteFile(p, b, 0o644)
		}
	}
	return "", fmt.Errorf("no free sequence slot under %s", base)
}

// LoadAll reads every *.json trajectory in dir (bad files are skipped).
func LoadAll(dir string) ([]*TrajFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*TrajFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var t TrajFile
		if json.Unmarshal(b, &t) != nil || len(t.Points) < 3 || t.Dist <= 0 {
			continue
		}
		t.File = p
		out = append(out, &t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dist != out[j].Dist {
			return out[i].Dist < out[j].Dist
		}
		return out[i].AngleDeg < out[j].AngleDeg
	})
	return out, nil
}

// PickResult is the confidence-based source choice: prefer a REAL human
// recording whose distance is close enough; only fall back to the synthetic
// generator when nothing fits.
type PickResult struct {
	File       string
	Traj       *TrajFile
	Confidence float64 // 1.0 exact human match … 0.0 nothing fits (use synthetic)
	Reason     string
}

// Pick selects the best human recording for a wanted distance/direction.
// tolPx is the distance tolerance (e.g. 30: a real 500px recording serves
// 470..530 after Rescale). Any recorded direction works — the trajectory is
// rotated onto the wanted angle — but a matching direction scores better.
func Pick(files []*TrajFile, wantDist, wantAngle, tolPx float64) PickResult {
	if tolPx <= 0 {
		tolPx = 30
	}
	var best *TrajFile
	bestScore := math.Inf(1)
	for _, f := range files {
		if f.Kind != "human" {
			continue
		}
		dd := math.Abs(f.Dist - wantDist)
		if dd > tolPx {
			continue
		}
		ang := math.Abs(wrap180(wantAngle - f.AngleDeg))
		score := dd/tolPx + ang/45 // lower is better
		if score < bestScore {
			bestScore, best = score, f
		}
	}
	if best == nil {
		return PickResult{Confidence: 0, Reason: "库里没有距离匹配的真人轨迹 → 用算法生成"}
	}
	dd := math.Abs(best.Dist - wantDist)
	ang := math.Abs(wrap180(wantAngle - best.AngleDeg))
	conf := 1 - 0.5*(dd/tolPx) - 0.2*math.Min(1, ang/45)
	if conf < 0.3 {
		conf = 0.3
	}
	return PickResult{
		File: best.File, Traj: best, Confidence: conf,
		Reason: fmt.Sprintf("真人轨迹 偏距 %.0fpx 偏向 %.0f°", dd, ang),
	}
}
