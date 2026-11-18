package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// bundleIconCache converts each App bundle's icon once; conversions run
// one at a time. A failure is kept for bundleIconRetry together with the
// stamp of the bundle's icon sources, so a bundle that cannot convert does
// not hold every icon request behind it again, while an icon that appears
// later (an installation finishing, an update) is converted right away.
type bundleIconCache struct {
	mu     sync.Mutex
	icons  map[string][]byte
	failed map[string]bundleIconFailure
}

type bundleIconFailure struct {
	at    time.Time
	stamp string
}

const bundleIconRetry = time.Minute

func newBundleIconCache() *bundleIconCache {
	return &bundleIconCache{icons: map[string][]byte{}, failed: map[string]bundleIconFailure{}}
}

// bundleIconStamp describes the files an App icon is read from: the
// bundle (or executable), its Info.plist, the Resources directory, whose
// modification time changes when an icon file is added, removed or
// renamed, the asset catalog and the .icns files, which an installer may
// still be writing in place. Missing entries are part of the stamp.
func bundleIconStamp(appPath string) string {
	resources := filepath.Join(appPath, "Contents", "Resources")
	paths := []string{
		appPath,
		filepath.Join(appPath, "Contents", "Info.plist"),
		resources,
		filepath.Join(resources, "Assets.car"),
	}
	icons, _ := filepath.Glob(filepath.Join(resources, "*.icns"))
	paths = append(paths, icons...)
	var b strings.Builder
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			b.WriteString("-;")
			continue
		}
		fmt.Fprintf(&b, "%d:%d:%o;", info.ModTime().UnixNano(), info.Size(), info.Mode())
	}
	return b.String()
}

func (c *bundleIconCache) icon(ctx context.Context, appPath string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if data, ok := c.icons[appPath]; ok {
		return data, nil
	}
	stamp := bundleIconStamp(appPath)
	if failure, ok := c.failed[appPath]; ok && failure.stamp == stamp && time.Since(failure.at) < bundleIconRetry {
		return nil, errors.New("bundle icon unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := processBundleIcon(ctx, appPath)
	if err == nil && len(data) == 0 {
		err = errors.New("bundle icon unavailable")
	}
	if err != nil {
		// A canceled request says nothing of the bundle.
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if len(c.failed) >= 256 {
				clear(c.failed)
			}
			c.failed[appPath] = bundleIconFailure{at: time.Now(), stamp: stamp}
		}
		return nil, err
	}
	delete(c.failed, appPath)
	if len(c.icons) >= 256 {
		clear(c.icons)
	}
	c.icons[appPath] = data
	return data, nil
}

func writeProcessIcon(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// registerProcessPathIcon serves the icon of the App holding an
// executable path, running or not (the Usage page lists processes long gone, and a
// remote console cannot read this host's bundles). Only existing
// executable files are accepted; the icon comes from their App bundle.
func registerProcessPathIcon(mux *routeMux, icons *bundleIconCache, owners map[string]string) {
	mux.get("GET /api/v1/process-icon", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if !existingExecutable(path) {
			w.WriteHeader(404)
			return
		}
		item := monitoredProcess{Path: path}
		identifyProcess(&item)
		identifyOwnedProcess(&item, owners)
		if item.AppPath == "" {
			w.WriteHeader(404)
			return
		}
		data, err := icons.icon(r.Context(), item.AppPath)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		writeProcessIcon(w, data)
	})
}

// existingExecutable reports whether path is the clean absolute path of
// an existing executable file.
func existingExecutable(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode().Perm()&0o111 != 0
}
