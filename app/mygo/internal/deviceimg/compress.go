package deviceimg

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"

	_ "image/gif"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// MaxSide is the longest side of a custom image, in pixels.
	MaxSide = 128
	// TargetBytes is the size compression aims for; MaxBytes is the hard limit.
	TargetBytes = 20 * 1024
	MaxBytes    = 24 * 1024

	maxInputBytes  = 32 << 20
	maxInputSide   = 8192
	maxInputPixels = 36_000_000
	dataPrefix     = "data:image/jpeg;base64,"
)

var (
	ErrNotImage = errors.New("deviceimg: not a supported image")
	ErrTooLarge = errors.New("deviceimg: image exceeds size limit")
	ErrBadData  = errors.New("deviceimg: invalid data URL")
)

// Compress decodes a JPEG, PNG, GIF or WebP image, crops it to a centered square,
// scales it to at most MaxSide pixels, fills transparent areas with white and
// encodes it as JPEG, lowering the size and quality step by step until it fits
// in TargetBytes.
func Compress(src []byte) (string, error) {
	if len(src) == 0 || len(src) > maxInputBytes {
		return "", ErrNotImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", ErrNotImage
	}
	if cfg.Width > maxInputSide || cfg.Height > maxInputSide || cfg.Width*cfg.Height > maxInputPixels {
		return "", ErrTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return "", ErrNotImage
	}
	sq := squareCrop(img)
	side := sq.Bounds().Dx()
	if side > MaxSide {
		side = MaxSide
	}
	var out []byte
	for _, s := range []int{side, side * 7 / 8, side * 3 / 4, side * 5 / 8, side / 2} {
		if s < 16 {
			s = 16
		}
		scaled := scale(sq, s)
		for _, q := range []int{85, 75, 65, 55, 45, 35, 25} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: q}); err != nil {
				return "", err
			}
			out = buf.Bytes()
			if len(out) <= TargetBytes {
				return dataPrefix + base64.StdEncoding.EncodeToString(out), nil
			}
		}
	}
	if len(out) > MaxBytes {
		return "", ErrTooLarge
	}
	return dataPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// squareCrop returns the centered square of img as RGBA on a white background.
func squareCrop(img image.Image) *image.RGBA {
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, image.Point{X: x0, Y: y0}, draw.Over)
	return dst
}

func scale(src *image.RGBA, side int) *image.RGBA {
	if src.Bounds().Dx() == side {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// Decode parses a data URL (image/jpeg|png|webp|gif; base64) and returns the
// image bytes. It returns ErrTooLarge above MaxBytes.
func Decode(dataURL string) ([]byte, error) {
	rest, ok := strings.CutPrefix(dataURL, "data:image/")
	if !ok {
		return nil, ErrBadData
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, ErrBadData
	}
	kind, enc, _ := strings.Cut(meta, ";")
	switch kind {
	case "jpeg", "png", "webp", "gif":
	default:
		return nil, ErrBadData
	}
	if enc != "base64" {
		return nil, ErrBadData
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > MaxBytes+3 {
		return nil, ErrTooLarge
	}
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadData, err)
	}
	if len(b) > MaxBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Validate checks a data URL: its format, its size (at most MaxBytes) and that the image decodes.
func Validate(dataURL string) error {
	b, err := Decode(dataURL)
	if err != nil {
		return err
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil {
		return ErrNotImage
	}
	return nil
}
