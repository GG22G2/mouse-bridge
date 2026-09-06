// Package trajlab is the trajectory lab: a browser-free test bench for the
// mouse trajectory engine. It fingerprints sampled paths (shape + timing),
// runs acceptance gates over the full distance×direction matrix, records
// REAL hand movements behind a screen overlay for later replay, and picks
// recorded-vs-synthetic trajectory sources by confidence.
package trajlab

// Sample is one timestamped path point: position in physical pixels,
// T in milliseconds since the trajectory started.
type Sample struct {
	X, Y, T float64
}
