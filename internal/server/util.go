package server

import (
	"encoding/json"
	"math/rand"
)

func jsonMarshal(v any) ([]byte, error)  { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func randInt(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rand.Intn(hi-lo)
}
