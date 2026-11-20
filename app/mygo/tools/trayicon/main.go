// Command trayicon cuts the K of the logo out as the menu bar's template
// image: black where the K is, transparent elsewhere, fitted into a
// square. Run it again when the logo changes:
//
//	go run ./tools/trayicon -in resources/icon.png -out resources/tray-icon.png
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

func main() {
	in := flag.String("in", "resources/icon.png", "logo, the K on a plain background")
	out := flag.String("out", "resources/tray-icon.png", "template image")
	size := flag.Int("size", 36, "side in pixels")
	pad := flag.Float64("pad", 2, "margin in pixels")
	flag.Parse()
	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	src, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	b := src.Bounds()
	// The background is the corners' colour.
	br, bg, bb, _ := src.At(b.Min.X+8, b.Min.Y+8).RGBA()
	w, h := b.Dx(), b.Dy()
	alpha := make([]float64, w*h)
	minX, minY, maxX, maxY := w, h, 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			d := math.Sqrt(sq(float64(r)-float64(br))+sq(float64(g)-float64(bg))+sq(float64(bl)-float64(bb))) / 257
			a := math.Max(0, math.Min(1, (d-60)/60))
			alpha[y*w+x] = a
			if a > 0.5 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	bw, bh := float64(maxX-minX+1), float64(maxY-minY+1)
	inner := float64(*size) - 2**pad
	scale := inner / math.Max(bw, bh)
	ox := (float64(*size) - bw*scale) / 2
	oy := (float64(*size) - bh*scale) / 2
	dst := image.NewNRGBA(image.Rect(0, 0, *size, *size))
	for y := 0; y < *size; y++ {
		for x := 0; x < *size; x++ {
			// Average the source pixels this one covers.
			sx0, sy0 := (float64(x)-ox)/scale+float64(minX), (float64(y)-oy)/scale+float64(minY)
			sx1, sy1 := sx0+1/scale, sy0+1/scale
			sum, n := 0.0, 0.0
			for sy := int(math.Floor(sy0)); sy < int(math.Ceil(sy1)); sy++ {
				for sx := int(math.Floor(sx0)); sx < int(math.Ceil(sx1)); sx++ {
					if sx >= 0 && sy >= 0 && sx < w && sy < h {
						sum += alpha[sy*w+sx]
					}
					n++
				}
			}
			if n > 0 && sum > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{A: uint8(math.Round(255 * sum / n))})
			}
		}
	}
	o, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer o.Close()
	if err := png.Encode(o, dst); err != nil {
		log.Fatal(err)
	}
}

func sq(v float64) float64 { return v * v }
