package trajlab

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
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

// PickTolFrac is how far a requested move may sit from a recording's own
// chord length and still be served by it: ±10% — a 100px recording serves
// 90..110 after uniform rescaling.
const PickTolFrac = 0.10

// dirMatchDeg is the direction window of the preferred pool: when any
// qualifying recording points within this window of the wanted direction,
// the random draw is taken from those. Recordings outside the window still
// qualify (Rescale rotates them onto the wanted angle) but only compete
// when nothing points the right way.
const dirMatchDeg = 45

// PickResult is the outcome of matching a wanted distance/direction against
// the recording library. Traj == nil means nothing fits → synthetic.
type PickResult struct {
	File   string
	Traj   *TrajFile
	Reason string
}

// Qualifying returns the human recordings whose chord length lies within
// ±PickTolFrac of wantDist, the tolerance measured against the recording's
// own length.
func Qualifying(files []*TrajFile, wantDist float64) []*TrajFile {
	var out []*TrajFile
	for _, f := range files {
		if f.Kind != "human" {
			continue
		}
		if math.Abs(f.Dist-wantDist) <= PickTolFrac*f.Dist {
			out = append(out, f)
		}
	}
	return out
}

// Pick draws ONE qualifying recording at random — deliberately not the
// closest match, so repeated identical requests don't keep replaying the
// same shape. When any qualifying recording also points within dirMatchDeg
// of the wanted direction, the draw comes from that pool only; otherwise
// all qualifying recordings compete and Rescale rotates the winner onto
// the wanted angle.
func Pick(files []*TrajFile, wantDist, wantAngle float64) PickResult {
	qual := Qualifying(files, wantDist)
	if len(qual) == 0 {
		return PickResult{Reason: fmt.Sprintf("库里没有 %.0f~%.0fpx 的真人轨迹 → 用算法生成",
			wantDist/(1+PickTolFrac), wantDist/(1-PickTolFrac))}
	}
	pool := qual
	angled := make([]*TrajFile, 0, len(qual))
	for _, f := range qual {
		if math.Abs(wrap180(wantAngle-f.AngleDeg)) <= dirMatchDeg {
			angled = append(angled, f)
		}
	}
	if len(angled) > 0 {
		pool = angled
	}
	f := pool[rand.Intn(len(pool))]
	dd := math.Abs(f.Dist - wantDist)
	ang := math.Abs(wrap180(wantAngle - f.AngleDeg))
	return PickResult{
		File: f.File, Traj: f,
		Reason: fmt.Sprintf("真人轨迹 %s（%.0fpx@%.0f°，等比缩放 %.3f×，偏距 %.0fpx 偏向 %.0f°）",
			filepath.Base(f.File), f.Dist, f.AngleDeg, wantDist/f.Dist, dd, ang),
	}
}
