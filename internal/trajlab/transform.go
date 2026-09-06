package trajlab

import "math"

// Rescale maps a recorded trajectory onto a new start point, distance and
// direction: translate to the origin, rotate, uniformly scale the chord,
// then translate to the new start. Timestamps are kept, so playback speed
// scales together with the geometry and the dynamics of the original
// movement survive the transfer.
func Rescale(t *TrajFile, newStart [2]float64, newDist, newAngle float64) []Sample {
	scale := 1.0
	if t.Dist > 1 {
		scale = newDist / t.Dist
	}
	rot := (newAngle - t.AngleDeg) * math.Pi / 180
	cos, sin := math.Cos(rot), math.Sin(rot)
	out := make([]Sample, len(t.Points))
	for i, p := range t.Points {
		dx, dy := p.X-t.Start[0], p.Y-t.Start[1]
		out[i] = Sample{
			X: newStart[0] + (dx*cos-dy*sin)*scale,
			Y: newStart[1] + (dx*sin+dy*cos)*scale,
			T: p.T,
		}
	}
	return out
}

func wrap180(a float64) float64 {
	a = math.Mod(a, 360)
	if a > 180 {
		a -= 360
	}
	if a < -180 {
		a += 360
	}
	return a
}

// AngleOf returns the direction first→last sample in degrees, 0..360,
// 0 = right, 90 = down (screen coords).
func AngleOf(pts []Sample) float64 {
	if len(pts) < 2 {
		return 0
	}
	a := math.Atan2(pts[len(pts)-1].Y-pts[0].Y, pts[len(pts)-1].X-pts[0].X) * 180 / math.Pi
	if a < 0 {
		a += 360
	}
	return a
}
