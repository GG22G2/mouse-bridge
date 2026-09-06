package trajlab

import (
	"math"
	"path/filepath"
	"sync"
	"time"
)

// Library is the daemon-side view of the recording directory. It reloads
// lazily (at most every 10s) so freshly recorded trajectories are picked up
// without a daemon restart.
type Library struct {
	dir    string
	mu     sync.Mutex
	files  []*TrajFile
	loaded time.Time
}

func NewLibrary(dir string) *Library { return &Library{dir: dir} }

func (l *Library) Dir() string { return l.dir }

func (l *Library) Size() int { return len(l.snapshot()) }

func (l *Library) snapshot() []*TrajFile {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.files == nil || time.Since(l.loaded) > 10*time.Second {
		if files, err := LoadAll(l.dir); err == nil {
			l.files = files
		}
		l.loaded = time.Now()
	}
	return l.files
}

// HumanPath maps one qualifying recording onto a wanted from→to move: the
// whole path is translated, rotated and uniformly scaled so its endpoint
// lands exactly on the target. Point count and per-point timestamps are
// untouched, so the recorded hand dynamics play back as-is. ok=false means
// nothing in the library serves this distance — the caller falls back to
// the synthetic generator.
func (l *Library) HumanPath(fromX, fromY, toX, toY float64) ([]Sample, string, bool) {
	dx, dy := toX-fromX, toY-fromY
	dist := math.Hypot(dx, dy)
	ang := math.Atan2(dy, dx) * 180 / math.Pi
	if ang < 0 {
		ang += 360
	}
	r := Pick(l.snapshot(), dist, ang)
	if r.Traj == nil {
		return nil, "", false
	}
	return Rescale(r.Traj, [2]float64{fromX, fromY}, dist, ang), filepath.Base(r.File), true
}
