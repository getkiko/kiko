// Package deviceimg provides the built-in device images (Apple devices from appledb,
// the computer and router drawn in-house) and compresses and validates custom device
// images. The images are 256px WebP or PNG, which MyGo's ui.DecodeBitmap decodes directly.
package deviceimg

import (
	"embed"
	_ "embed"
)

//go:embed img/*
var files embed.FS

// SourcesManifest records the built-in images' sources, retrieval date and license.
//
//go:embed SOURCES.md
var SourcesManifest string

// keys are the built-in image keys, in the order the UI shows them.
var keys = []string{"iphone", "ipad", "watch", "mac", "appletv", "homepod", "airpods", "vision", "pc", "router"}

// extensions lists the possible file extensions, in order of preference.
var extensions = []string{".webp", ".png"}

// Keys returns a copy of all built-in image keys.
func Keys() []string { return append([]string(nil), keys...) }

// Builtin returns the light-mode image of key and whether it exists.
func Builtin(key string) ([]byte, bool) {
	if !valid(key) {
		return nil, false
	}
	return read(key)
}

// BuiltinDark returns the dark-mode image of key, falling back to the light one without a _dark variant.
func BuiltinDark(key string) ([]byte, bool) {
	if !valid(key) {
		return nil, false
	}
	if b, ok := read(key + "_dark"); ok {
		return b, true
	}
	return read(key)
}

// HasDark reports whether key has a dedicated dark variant.
func HasDark(key string) bool {
	if !valid(key) {
		return false
	}
	_, ok := read(key + "_dark")
	return ok
}

func valid(key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

func read(name string) ([]byte, bool) {
	for _, ext := range extensions {
		if b, err := files.ReadFile("img/" + name + ext); err == nil {
			return b, true
		}
	}
	return nil, false
}
