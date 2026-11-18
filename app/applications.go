package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type installedApplication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type applicationCatalog struct {
	mu       sync.Mutex
	items    []installedApplication
	warnings []string
	loaded   time.Time
	icons    map[string][]byte
	discover func(context.Context) ([]installedApplication, []string, error)
}

func applicationID(path string) string {
	digest := sha256.Sum256([]byte(path))
	return hex.EncodeToString(digest[:16])
}

func (c *applicationCatalog) snapshot(ctx context.Context, refresh bool) ([]installedApplication, []string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && !c.loaded.IsZero() && time.Since(c.loaded) < time.Minute {
		return c.items, c.warnings, nil
	}
	items, warnings, err := c.discover(ctx)
	if err != nil {
		return nil, nil, err
	}
	if items == nil {
		items = []installedApplication{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if a == b {
			return items[i].Path < items[j].Path
		}
		return a < b
	})
	c.items, c.warnings, c.loaded = items, warnings, time.Now()
	c.icons = map[string][]byte{}
	return items, warnings, nil
}

func registerApplications(mux *routeMux) {
	catalog := &applicationCatalog{discover: discoverInstalledApplications}
	mux.get("GET /api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		items, warnings, err := catalog.snapshot(ctx, r.URL.Query().Get("refresh") == "1")
		if err != nil {
			http.Error(w, "读取已安装应用失败："+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"supported": runtime.GOOS == "darwin", "os": runtime.GOOS, "applications": items, "warnings": warnings})
	})
	mux.get("GET /api/v1/applications/{id}/icon", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		items, _, err := catalog.snapshot(ctx, false)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var app *installedApplication
		for i := range items {
			if items[i].ID == r.PathValue("id") {
				app = &items[i]
				break
			}
		}
		if app == nil {
			http.NotFound(w, r)
			return
		}
		catalog.mu.Lock()
		defer catalog.mu.Unlock()
		data := catalog.icons[app.ID]
		if len(data) == 0 {
			data, err = processBundleIcon(ctx, app.Path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if len(catalog.icons) >= 256 {
				catalog.icons = map[string][]byte{}
			}
			catalog.icons[app.ID] = data
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
	})
}
