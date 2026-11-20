package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
)

// faviconSide is the largest side of a kept icon, twice the largest the
// pages draw.
const faviconSide = 64

// faviconLink is an icon a page links.
type faviconLink struct {
	href  string
	size  int // its largest declared side, 0 when unknown
	apple bool
}

// faviconLinks are the icons page links, resolved against base, best
// first: those nearest faviconSide at or above it, then smaller ones, an
// unknown size counting as 32. SVG icons are left out: the pages cannot
// draw them.
func faviconLinks(page []byte, base *url.URL) []string {
	var links []faviconLink
	tokens := html.NewTokenizer(bytes.NewReader(page))
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := tokens.TagName()
		if string(name) == "body" {
			break
		}
		if string(name) != "link" || !hasAttr {
			continue
		}
		var rel, href, sizes, typ string
		for {
			key, value, more := tokens.TagAttr()
			switch string(key) {
			case "rel":
				rel = strings.ToLower(string(value))
			case "href":
				href = strings.TrimSpace(string(value))
			case "sizes":
				sizes = strings.ToLower(string(value))
			case "type":
				typ = strings.ToLower(string(value))
			}
			if !more {
				break
			}
		}
		fields := strings.Fields(rel)
		icon, apple := false, false
		for _, f := range fields {
			switch f {
			case "icon":
				icon = true
			case "apple-touch-icon", "apple-touch-icon-precomposed":
				icon, apple = true, true
			}
		}
		if !icon || href == "" || strings.Contains(typ, "svg") || strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".svg") ||
			strings.HasPrefix(href, "data:image/svg") {
			continue
		}
		link := faviconLink{href: href, apple: apple}
		for _, size := range strings.Fields(sizes) {
			if w, _, ok := strings.Cut(size, "x"); ok {
				if n, err := strconv.Atoi(w); err == nil && n > link.size {
					link.size = n
				}
			}
		}
		if link.size == 0 && apple {
			link.size = 180
		}
		links = append(links, link)
	}
	score := func(l faviconLink) int {
		size := l.size
		if size == 0 {
			size = 32
		}
		if size >= faviconSide {
			return size - faviconSide
		}
		return 10000 + faviconSide - size
	}
	sort.SliceStable(links, func(i, j int) bool { return score(links[i]) < score(links[j]) })
	var out []string
	for _, l := range links {
		if strings.HasPrefix(l.href, "data:") {
			out = append(out, l.href)
			continue
		}
		ref, err := url.Parse(l.href)
		if err != nil || base == nil {
			continue
		}
		if u := base.ResolveReference(ref); faviconPublic(u) {
			out = append(out, u.String())
		}
	}
	return out
}

// decodeDataURL is the bytes of a base64 or plain data URL.
func decodeDataURL(raw string) ([]byte, error) {
	meta, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !ok {
		return nil, errors.New("bad data URL")
	}
	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(data)
	}
	text, err := url.PathUnescape(data)
	return []byte(text), err
}

// faviconPNG is an icon as a PNG at most faviconSide on a side: PNG, JPEG,
// GIF, WebP and ICO (its largest image, PNG or BMP) are read.
func faviconPNG(data []byte) ([]byte, error) {
	var img image.Image
	var err error
	if len(data) >= 4 && binary.LittleEndian.Uint32(data[:4]) == 0x00010000 {
		img, err = decodeICO(data)
	} else if err = checkImageSize(data); err == nil {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return nil, errors.New("empty image")
	}
	if b.Dx() > faviconSide || b.Dy() > faviconSide {
		w, h := faviconSide, faviconSide
		if b.Dx() > b.Dy() {
			h = max(1, b.Dy()*faviconSide/b.Dx())
		} else if b.Dy() > b.Dx() {
			w = max(1, b.Dx()*faviconSide/b.Dy())
		}
		scaled := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, b, draw.Src, nil)
		img = scaled
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// faviconMaxSide bounds the side of an image decoded: a small file may
// declare a huge one, whose pixels would not fit in memory.
const faviconMaxSide = 1024

// checkImageSize reads an image's header and refuses one larger than
// faviconMaxSide on a side.
func checkImageSize(data []byte) error {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > faviconMaxSide || config.Height > faviconMaxSide {
		return errors.New("image too large")
	}
	return nil
}

// decodeICO decodes the largest image of an ICO file: a PNG, or a BMP of
// 32 bits (with alpha, or the AND mask without it) or 24 bits.
func decodeICO(data []byte) (image.Image, error) {
	if len(data) < 6 {
		return nil, errors.New("short ICO")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	type entry struct{ side, offset, size int }
	var entries []entry
	for i := 0; i < count && 6+16*(i+1) <= len(data); i++ {
		e := data[6+16*i:]
		side := int(e[0])
		if side == 0 {
			side = 256
		}
		size, offset := int(binary.LittleEndian.Uint32(e[8:12])), int(binary.LittleEndian.Uint32(e[12:16]))
		if offset > 0 && size > 0 && offset+size <= len(data) {
			entries = append(entries, entry{side, offset, size})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].side > entries[j].side })
	var last error = errors.New("no image in ICO")
	for _, e := range entries {
		raw := data[e.offset : e.offset+e.size]
		var img image.Image
		var err error
		if bytes.HasPrefix(raw, []byte("\x89PNG")) {
			if err = checkImageSize(raw); err == nil {
				img, err = png.Decode(bytes.NewReader(raw))
			}
		} else {
			img, err = decodeICOBitmap(raw)
		}
		if err == nil {
			return img, nil
		}
		last = err
	}
	return nil, last
}

// decodeICOBitmap decodes an ICO's BMP image: a BITMAPINFOHEADER, bottom-up
// rows of the color bitmap, then the AND mask, both padded to 4 bytes; the
// header's height counts both.
func decodeICOBitmap(raw []byte) (image.Image, error) {
	if len(raw) < 40 {
		return nil, errors.New("short bitmap")
	}
	header := int(binary.LittleEndian.Uint32(raw[0:4]))
	w := int(int32(binary.LittleEndian.Uint32(raw[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(raw[8:12]))) / 2
	bits := int(binary.LittleEndian.Uint16(raw[14:16]))
	if header < 40 || header > len(raw) || w <= 0 || h <= 0 || w > 512 || h > 512 || (bits != 32 && bits != 24) {
		return nil, errors.New("unsupported bitmap")
	}
	stride := (w*bits/8 + 3) &^ 3
	maskStride := ((w+7)/8 + 3) &^ 3
	pixels := raw[header:]
	if len(pixels) < stride*h {
		return nil, errors.New("short bitmap")
	}
	mask := pixels[stride*h:]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := 0; y < h; y++ {
		row := pixels[(h-1-y)*stride:]
		for x := 0; x < w; x++ {
			p := row[x*bits/8:]
			c := color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			if bits == 32 {
				c.A = p[3]
				if c.A != 0 {
					anyAlpha = true
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	// Without alpha the AND mask tells the transparent pixels.
	if bits == 24 || !anyAlpha {
		for y := 0; y < h && len(mask) >= maskStride*h; y++ {
			row := mask[(h-1-y)*maskStride:]
			for x := 0; x < w; x++ {
				c := img.NRGBAAt(x, y)
				c.A = 255
				if row[x/8]&(0x80>>(x%8)) != 0 {
					c.A = 0
				}
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return img, nil
}
