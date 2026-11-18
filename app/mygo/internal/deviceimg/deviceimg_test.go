package deviceimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
	"testing"

	_ "golang.org/x/image/webp"
)

func TestBuiltinAll(t *testing.T) {
	total := 0
	for _, k := range Keys() {
		b, ok := Builtin(k)
		if !ok {
			t.Fatalf("missing %s", k)
		}
		d, ok := BuiltinDark(k)
		if !ok {
			t.Fatalf("missing dark %s", k)
		}
		total += len(b)
		if HasDark(k) {
			total += len(d)
		}
		for _, data := range [][]byte{b, d} {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s decode: %v", k, err)
			}
			if cfg.Width > 256 || cfg.Height > 256 {
				t.Fatalf("%s size %dx%d", k, cfg.Width, cfg.Height)
			}
			if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
				t.Fatalf("%s full decode: %v", k, err)
			}
		}
	}
	if total > 1<<20 {
		t.Fatalf("total %d bytes exceeds 1 MiB", total)
	}
	if _, ok := Builtin("nope"); ok {
		t.Fatal("unknown key must fail")
	}
	if len(Keys()) != 10 || strings.TrimSpace(SourcesManifest) == "" {
		t.Fatal("keys or manifest")
	}
}

func enc(t *testing.T, img image.Image, f string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	switch f {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func check(t *testing.T, du string) image.Image {
	t.Helper()
	if !strings.HasPrefix(du, "data:image/jpeg;base64,") {
		t.Fatalf("prefix %q", du[:30])
	}
	b, err := Decode(du)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > TargetBytes {
		t.Fatalf("size %d", len(b))
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r := img.Bounds()
	if r.Dx() != r.Dy() || r.Dx() > MaxSide {
		t.Fatalf("dims %v", r)
	}
	if err := Validate(du); err != nil {
		t.Fatal(err)
	}
	return img
}

func TestCompressFormatsAndSizes(t *testing.T) {
	for _, f := range []string{"png", "jpeg", "gif"} {
		for _, d := range [][2]int{{16, 16}, {100, 60}, {60, 300}, {512, 512}, {1500, 900}} {
			du, err := Compress(enc(t, gradient(d[0], d[1]), f))
			if err != nil {
				t.Fatalf("%s %v: %v", f, d, err)
			}
			img := check(t, du)
			if d[0] >= 128 && d[1] >= 128 && img.Bounds().Dx() != 128 {
				t.Fatalf("%s %v expected 128 got %d", f, d, img.Bounds().Dx())
			}
		}
	}
}

func TestCompressWebP(t *testing.T) {
	b, _ := Builtin("iphone")
	du, err := Compress(b)
	if err != nil {
		t.Fatal(err)
	}
	check(t, du)
}

func TestCompressNoiseStaysWithinLimit(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Intn(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	du, err := Compress(enc(t, img, "png"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Decode(du)
	if len(b) > MaxBytes {
		t.Fatalf("size %d", len(b))
	}
}

func TestCompressTransparentGetsWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	du, err := Compress(enc(t, img, "png"))
	if err != nil {
		t.Fatal(err)
	}
	out := check(t, du)
	r, g, b, _ := out.At(64, 64).RGBA()
	if r>>8 < 245 || g>>8 < 245 || b>>8 < 245 {
		t.Fatalf("expected white, got %d %d %d", r>>8, g>>8, b>>8)
	}
}

func TestCompressRejects(t *testing.T) {
	if _, err := Compress([]byte("not an image")); err != ErrNotImage {
		t.Fatalf("got %v", err)
	}
	if _, err := Compress(nil); err != ErrNotImage {
		t.Fatalf("got %v", err)
	}
	// A PNG header declaring a huge size is rejected before decoding.
	huge := image.NewGray(image.Rect(0, 0, 20000, 2))
	if _, err := Compress(enc(t, huge, "png")); err != ErrTooLarge {
		t.Fatalf("got %v", err)
	}
}

// Images over 8192 pixels a side or 36 million pixels are rejected before decoding.
func TestCompressInputLimits(t *testing.T) {
	if maxInputSide != 8192 || maxInputPixels != 36_000_000 {
		t.Fatalf("limits %d %d", maxInputSide, maxInputPixels)
	}
	wide := image.NewGray(image.Rect(0, 0, 8193, 2))
	if _, err := Compress(enc(t, wide, "png")); err != ErrTooLarge {
		t.Fatalf("side: %v", err)
	}
	many := image.NewGray(image.Rect(0, 0, 7000, 6000)) // 42 million pixels, each side within the limit
	if _, err := Compress(enc(t, many, "png")); err != ErrTooLarge {
		t.Fatalf("pixels: %v", err)
	}
}

func TestDecodeValidation(t *testing.T) {
	for _, s := range []string{"", "http://x", "data:text/plain;base64,AAAA", "data:image/jpeg,AAAA", "data:image/jpeg;base64,!!!", "data:image/svg+xml;base64,AAAA"} {
		if _, err := Decode(s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
	big := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxBytes+1))
	if _, err := Decode(big); err != ErrTooLarge {
		t.Fatalf("got %v", err)
	}
	junk := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("hello"))
	if err := Validate(junk); err != ErrNotImage {
		t.Fatalf("got %v", err)
	}
}
