package main

import (
	"image"
	"path/filepath"
	"strings"
)

// windowsAppName names a Windows App by its version resource's file
// description or product name, else by its executable's name.
func windowsAppName(path string, version map[string]string) string {
	for _, field := range []string{"FileDescription", "ProductName"} {
		if name := strings.TrimSpace(version[field]); name != "" {
			return name
		}
	}
	base := path[strings.LastIndexAny(path, `\/`)+1:]
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".exe") {
		base = base[:len(base)-len(ext)]
	}
	return base
}

// windowsPathWithin reports whether a Windows path lies in dir, ignoring
// case and the separator's spelling.
func windowsPathWithin(path, dir string) bool {
	clean := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(s, "/", `\`), `\`))
	}
	path, dir = clean(path), clean(dir)
	return dir != "" && strings.HasPrefix(path, dir+`\`)
}

// iconImage is an icon's BGRA colour as an image: its own alpha, or for
// icons without one, opaque where the mask is black.
func iconImage(color, mask []byte, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	hasAlpha := false
	for i := 3; i < len(color); i += 4 {
		if color[i] != 0 {
			hasAlpha = true
			break
		}
	}
	for i := 0; i+3 < len(color) && i+3 < len(img.Pix); i += 4 {
		a := color[i+3]
		if !hasAlpha {
			a = 255
			if len(mask) > i && mask[i] != 0 {
				a = 0
			}
		}
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = color[i+2], color[i+1], color[i], a
	}
	return img
}
