//go:build ignore

package main

// Generates simple arrow-cursor icons for the extension.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
)

func drawIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{30, 42, 58, 255}}, image.Point{}, draw.Src)

	fg := color.RGBA{255, 255, 255, 255}
	acc := color.RGBA{74, 108, 247, 255}

	// arrow cursor polygon (scaled to unit box)
	pts := [][2]float64{
		{0.28, 0.15}, {0.28, 0.72}, {0.43, 0.58}, {0.53, 0.82}, {0.62, 0.78}, {0.52, 0.55}, {0.72, 0.55},
	}
	s := float64(size)
	px := make([]image.Point, len(pts))
	for i, p := range pts {
		px[i] = image.Point{int(math.Round(p[0] * s)), int(math.Round(p[1] * s))}
	}
	// fill polygon via even-odd scanline
	for y := 0; y < size; y++ {
		var xs []float64
		for i := 0; i < len(px); i++ {
			a, b := px[i], px[(i+1)%len(px)]
			if (a.Y <= y && b.Y > y) || (b.Y <= y && a.Y > y) {
				t := float64(y - a.Y) / float64(b.Y-a.Y)
				xs = append(xs, float64(a.X)+t*float64(b.X-a.X))
			}
		}
		for i := 0; i < len(xs); i++ {
			for j := i + 1; j < len(xs); j++ {
				if xs[i] > xs[j] {
					xs[i], xs[j] = xs[j], xs[i]
				}
			}
		}
		for k := 0; k+1 < len(xs); k += 2 {
			for x := int(math.Ceil(xs[k])); x < int(xs[k+1]); x++ {
				img.Set(x, y, fg)
			}
		}
	}
	// accent dot bottom-right
	cx, cy, r := int(0.78*s), int(0.8*s), size/7
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				x, y := cx+dx, cy+dy
				if x >= 0 && y >= 0 && x < size && y < size {
					img.Set(x, y, acc)
				}
			}
		}
	}
	return img
}

func main() {
	for _, sz := range []int{16, 32, 48, 128} {
		f, err := os.Create(os.Args[1] + "/icon" + itoa(sz) + ".png")
		if err != nil {
			panic(err)
		}
		png.Encode(f, drawIcon(sz))
		f.Close()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
