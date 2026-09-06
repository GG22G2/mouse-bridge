package mouse

import "testing"

func TestTryHumanPath(t *testing.T) {
	from, to := Point{100, 100}, Point{300, 100}

	// no hook wired → synthetic
	if _, src, ok := tryHumanPath(from, to); ok || src != "synthetic" {
		t.Fatalf("no hook: ok=%v src=%s, want synthetic", ok, src)
	}

	// hook supplies a path → used verbatim
	HumanPathHook = func(f, t2 Point) ([]Sample, string, bool) {
		return []Sample{{X: f.X, Y: f.Y}, {X: t2.X, Y: t2.Y, T: 500}}, "d0200_a000_001.json", true
	}
	samps, src, ok := tryHumanPath(from, to)
	if !ok || src != "d0200_a000_001.json" || len(samps) != 2 {
		t.Fatalf("hook path rejected: ok=%v src=%s n=%d", ok, src, len(samps))
	}

	// hook declines → synthetic
	HumanPathHook = func(f, t2 Point) ([]Sample, string, bool) { return nil, "", false }
	if _, src, ok := tryHumanPath(from, to); ok || src != "synthetic" {
		t.Fatalf("declining hook: ok=%v src=%s, want synthetic", ok, src)
	}

	// garbage (single point) → synthetic
	HumanPathHook = func(f, t2 Point) ([]Sample, string, bool) {
		return []Sample{{X: 1, Y: 1}}, "x.json", true
	}
	if _, _, ok := tryHumanPath(from, to); ok {
		t.Fatal("1-point path accepted")
	}
	HumanPathHook = nil
}
