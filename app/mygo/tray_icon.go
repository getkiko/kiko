package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// trayIconPNG draws the menu bar icon: a bold rounded "K", black on
// transparent so that macOS tints it as a template image. It is 36 pixels
// square, 18 points on a Retina display.
func trayIconPNG() []byte {
	const size, samples = 36, 4
	type segment struct{ x0, y0, x1, y1 float64 }
	// The stem, then the arms meeting it a little above the middle.
	strokes := []segment{
		{11, 7, 11, 29},
		{12.5, 19.5, 25.5, 7.5},
		{16.5, 16.5, 26, 28.5},
	}
	const radius = 2.9
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			covered := 0
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := float64(x) + (float64(sx)+0.5)/samples
					py := float64(y) + (float64(sy)+0.5)/samples
					for _, s := range strokes {
						if distanceToSegment(px, py, s.x0, s.y0, s.x1, s.y1) <= radius {
							covered++
							break
						}
					}
				}
			}
			if covered > 0 {
				img.SetNRGBA(x, y, color.NRGBA{A: uint8(covered * 255 / (samples * samples))})
			}
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}

// distanceToSegment is the distance from (px, py) to the segment from
// (x0, y0) to (x1, y1).
func distanceToSegment(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	t := 0.0
	if length := dx*dx + dy*dy; length > 0 {
		t = math.Max(0, math.Min(1, ((px-x0)*dx+(py-y0)*dy)/length))
	}
	return math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
}
