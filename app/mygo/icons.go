package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// iconCache finds the icons of applications for the process monitor and
// the process conditions of rules: a macOS app bundle's .icns, named by
// CFBundleIconFile in Info.plist or the first in Resources, as PNG bytes
// the native UI decodes. Executables without a bundle have none.
type iconCache struct {
	mu    sync.Mutex
	cache map[string][]byte
}

func newIconCache() *iconCache {
	return &iconCache{cache: map[string][]byte{}}
}

// png returns the PNG icon of an application or executable path, nil when
// it has none. Results are cached, including misses.
func (c *iconCache) png(path string) []byte {
	if path == "" {
		return nil
	}
	c.mu.Lock()
	if cached, ok := c.cache[path]; ok {
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	located := locateIcon(path)
	c.mu.Lock()
	c.cache[path] = located
	c.mu.Unlock()
	return located
}

// bundleOf returns the .app bundle containing path, or path itself.
func bundleOf(path string) string {
	if index := strings.Index(path, ".app/"); index >= 0 {
		return path[:index+len(".app")]
	}
	return strings.TrimSuffix(path, "/")
}

// locateIcon finds an icon for an executable or bundle path.
func locateIcon(path string) []byte {
	bundle := bundleOf(path)
	if !strings.HasSuffix(bundle, ".app") {
		return nil
	}
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	if data, err := os.ReadFile(plist); err == nil {
		if name := plistIconName(string(data)); name != "" {
			candidate := filepath.Join(bundle, "Contents", "Resources", name)
			if !strings.HasSuffix(candidate, ".icns") {
				candidate += ".icns"
			}
			if icon := readIcon(candidate); icon != nil {
				return icon
			}
		}
	}
	resources := filepath.Join(bundle, "Contents", "Resources")
	entries, err := os.ReadDir(resources)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".icns") {
			if icon := readIcon(filepath.Join(resources, entry.Name())); icon != nil {
				return icon
			}
		}
	}
	return nil
}

// plistIconName extracts CFBundleIconFile without pulling in a plist
// dependency.
func plistIconName(plist string) string {
	const key = "<key>CFBundleIconFile</key>"
	index := strings.Index(plist, key)
	if index < 0 {
		return ""
	}
	rest := plist[index+len(key):]
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return ""
	}
	rest = rest[open+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// readIcon reads an .icns file and returns one of its PNG images.
func readIcon(path string) []byte {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 8<<20 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return icnsPNG(data)
}

// icnsPreference orders the PNG images of an .icns file: large enough for
// a sharp list icon at 2x, small enough to decode quickly.
var icnsPreference = []string{"ic07", "ic12", "ic13", "ic08", "ic14", "ic09", "ic10", "ic11"}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// icnsPNG returns the preferred PNG image stored in an .icns file, nil if
// it holds none (older icons store only packed bitmaps).
func icnsPNG(data []byte) []byte {
	if len(data) < 8 || string(data[:4]) != "icns" {
		return nil
	}
	images := map[string][]byte{}
	for offset := 8; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		size := int(binary.BigEndian.Uint32(data[offset+4 : offset+8]))
		if size < 8 || offset+size > len(data) {
			break
		}
		body := data[offset+8 : offset+size]
		if bytes.HasPrefix(body, pngSignature) {
			images[kind] = body
		}
		offset += size
	}
	for _, kind := range icnsPreference {
		if image, ok := images[kind]; ok {
			return image
		}
	}
	for _, image := range images {
		return image
	}
	return nil
}
