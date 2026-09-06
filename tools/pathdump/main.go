// pathdump: prints the velocity profile of a planned trajectory so the
// human-likeness (multi-peak speed, wander, tremor) can be inspected
// WITHOUT touching the real cursor.
package main

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"mousebridge/internal/mouse"
)

func main() {
	d, _ := strconv.ParseFloat(os.Args[1], 64)
	pts, subs, dur := mouse.DebugPath(mouse.Point{X: 100, Y: 400}, mouse.Point{X: 100 + d, Y: 400 + d*0.1})
	fmt.Printf("sub-movements: %d | samples: %d | duration: %v\n", subs, len(pts), dur)
	fmt.Println("per-sample speed (px/sample):")
	for i := 1; i < len(pts); i++ {
		v := math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
		bar := int(v*8 + 0.5)
		s := ""
		for j := 0; j < bar; j++ {
			s += "#"
		}
		fmt.Printf("%5.2f %s\n", v, s)
	}
}
