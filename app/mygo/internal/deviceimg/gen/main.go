//go:build ignore

// Gen draws the built-in computer and router device images (256x256 PNG,
// transparent background) with signed distance fields and analytic anti-aliasing,
// in a flat style of neutral gray metal with soft shadows.
//
// Usage: go run gen/main.go img
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const size = 256

type rgba struct{ r, g, b, a float64 }

func hex(c uint32, a float64) rgba {
	return rgba{float64(c>>16&255) / 255, float64(c>>8&255) / 255, float64(c&255) / 255, a}
}

func mix(a, b rgba, t float64) rgba {
	t = math.Max(0, math.Min(1, t))
	return rgba{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

type canvas struct{ pix [size * size]rgba } // not premultiplied

func (c *canvas) over(x, y int, s rgba) {
	if s.a <= 0 {
		return
	}
	d := &c.pix[y*size+x]
	oa := s.a + d.a*(1-s.a)
	if oa <= 0 {
		return
	}
	d.r = (s.r*s.a + d.r*d.a*(1-s.a)) / oa
	d.g = (s.g*s.a + d.g*d.a*(1-s.a)) / oa
	d.b = (s.b*s.a + d.b*d.a*(1-s.a)) / oa
	d.a = oa
}

// sdRound is the signed distance to a rounded rectangle centered at cx,cy with half sizes hw,hh and corner radius r.
func sdRound(px, py, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(px-cx) - hw + r
	qy := math.Abs(py-cy) - hh + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

type shape func(x, y float64) float64
type fill func(x, y float64) rgba

func rr(cx, cy, hw, hh, r float64) shape {
	return func(x, y float64) float64 { return sdRound(x, y, cx, cy, hw, hh, r) }
}

func vgrad(y0, y1 float64, top, bot rgba) fill {
	return func(_, y float64) rgba { return mix(top, bot, (y-y0)/(y1-y0)) }
}

func solid(c rgba) fill { return func(_, _ float64) rgba { return c } }

func (c *canvas) draw(s shape, f fill) {
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+.5, float64(y)+.5
			cov := math.Max(0, math.Min(1, .5-s(px, py)))
			if cov == 0 {
				continue
			}
			col := f(px, py)
			col.a *= cov
			c.over(x, y, col)
		}
	}
}

// shadow draws a soft drop shadow along the outer edge of s.
func (c *canvas) shadow(s shape, dy, blur, alpha float64) {
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := s(float64(x)+.5, float64(y)+.5-dy)
			t := 1 - (d+blur*.25)/blur
			if t <= 0 {
				continue
			}
			if t > 1 {
				t = 1
			}
			c.over(x, y, rgba{.1, .12, .16, alpha * t * t * (3 - 2*t)})
		}
	}
}

func (c *canvas) save(path string) error {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i, p := range c.pix {
		img.SetNRGBA(i%size, i/size, color.NRGBA{uint8(p.r*255 + .5), uint8(p.g*255 + .5), uint8(p.b*255 + .5), uint8(p.a*255 + .5)})
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(f, img)
}

func pc() *canvas {
	c := &canvas{}
	// Base
	base := rr(128, 214, 52, 6, 6)
	c.shadow(base, 6, 16, .30)
	c.draw(base, vgrad(208, 220, hex(0xd7dae0, 1), hex(0xa9aeb8, 1)))
	// Stand neck
	neck := rr(128, 190, 12, 26, 3)
	c.draw(neck, vgrad(164, 216, hex(0xc9ccd3, 1), hex(0x9aa0ab, 1)))
	// Display housing
	body := rr(128, 100, 104, 70, 12)
	c.shadow(body, 5, 14, .22)
	c.draw(body, vgrad(30, 170, hex(0xe4e6ea, 1), hex(0xb3b8c2, 1)))
	// Bezel and screen
	c.draw(rr(128, 98, 97, 63, 8), solid(hex(0x2b2e34, 1)))
	screen := rr(128, 98, 94, 60, 6)
	c.draw(screen, func(x, y float64) rgba {
		base := mix(hex(0x6f93c9, 1), hex(0x3d5f98, 1), (y-38)/120+(x-34)/500)
		// Diagonal highlight
		h := math.Max(0, 1-math.Abs((x-y*.9)-60)/42)
		return mix(base, hex(0xffffff, 1), h*.16)
	})
	// Indicator dot on the chin
	c.draw(rr(128, 162, 3, 3, 3), solid(hex(0x8d929c, 1)))
	return c
}

func router() *canvas {
	c := &canvas{}
	// Antennas, behind the body
	for _, ax := range []float64{64, 192} {
		ant := rr(ax, 86, 6, 52, 6)
		c.shadow(ant, 3, 8, .14)
		c.draw(ant, func(x, y float64) rgba {
			return mix(hex(0x8e939d, 1), hex(0x5b6069, 1), (x-ax+6)/12)
		})
	}
	ant := rr(128, 78, 6, 44, 6)
	c.draw(ant, func(x, y float64) rgba { return mix(hex(0x8e939d, 1), hex(0x5b6069, 1), (x-122)/12) })
	// Body
	body := rr(128, 170, 108, 30, 14)
	c.shadow(body, 6, 18, .30)
	c.draw(body, vgrad(140, 200, hex(0xe6e8ec, 1), hex(0xb1b6c0, 1)))
	// Thin highlight along the top
	c.draw(rr(128, 144, 100, 1.5, 1.5), solid(hex(0xffffff, .65)))
	// Dark front panel strip and status lights
	c.draw(rr(128, 176, 92, 11, 6), solid(hex(0x2b2e34, 1)))
	leds := []uint32{0x34c759, 0x34c759, 0x34c759, 0xff9f0a}
	for i, l := range leds {
		c.draw(rr(62+float64(i)*16, 176, 3.5, 3.5, 3.5), solid(hex(l, 1)))
	}
	// Ports
	for i := 0; i < 3; i++ {
		c.draw(rr(158+float64(i)*20, 176, 7, 4, 1.5), solid(hex(0x6b717c, 1)))
	}
	return c
}

func main() {
	dir := "img"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	for name, c := range map[string]*canvas{"pc.png": pc(), "router.png": router()} {
		if err := c.save(filepath.Join(dir, name)); err != nil {
			panic(err)
		}
	}
}
