package procicon

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestFromPath(t *testing.T) {
	p := FromPath("/Applications/GoLand.app/Contents/MacOS/goland")
	if p.Name != "goland" || p.AppPath != "/Applications/GoLand.app" || p.key() != "app:/Applications/GoLand.app" {
		t.Errorf("app path: %+v", p)
	}
	if p := FromPath("/usr/bin/curl"); p.AppPath != "" || p.key() != "path:/usr/bin/curl" {
		t.Errorf("executable: %+v", p)
	}
	if p := FromPath("未知进程"); p.Path != "" || p.key() != "" {
		t.Errorf("unknown: %+v", p)
	}
	if p := FromPath(`C:\Tools\node.exe`); p.Name != "node.exe" {
		t.Errorf("windows: %+v", p)
	}
}

func icon(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// An App whose icon request failed for one process asks again through
// another of its processes, and keeps the icon after a restart.
func TestAppIconRetriesWithAnotherProcessAndPersists(t *testing.T) {
	srv := testkit.NewServer(t)
	data := icon(t)
	srv.Route("GET", "/process-monitor/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/process-monitor/10/") {
			http.Error(w, "process exited", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	e, _ := testkit.NewEnv(srv)
	e.CacheDir = t.TempDir()
	s := newStore(e)
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	first := Process{PID: 10, StartedAt: 1, Path: "/Applications/GoLand.app/Contents/MacOS/goland", AppPath: "/Applications/GoLand.app"}
	second := first
	second.PID = 11
	s.Bitmap(first)
	testkit.Until(t, tt, func() bool { return !s.entries[first.key()].pending })
	if s.Bitmap(first) != nil || s.entries[first.key()].pending {
		t.Fatal("a failed process asked again")
	}
	testkit.Until(t, tt, func() bool { return s.Bitmap(second) != nil })

	// A new store (the next launch) reads the icon from disk, the process
	// long gone.
	again := newStore(e)
	gone := Process{Path: first.Path}
	testkit.Until(t, tt, func() bool { return again.Bitmap(gone) != nil })
}

func TestDiskEvictsOldest(t *testing.T) {
	d := newDisk(t.TempDir())
	clock := time.Unix(1000, 0)
	d.now = func() time.Time { return clock }
	for i := range diskEntries + 5 {
		clock = clock.Add(time.Second)
		d.write(strconv.Itoa(i), []byte{1})
	}
	if len(d.files) != diskEntries {
		t.Fatalf("kept %d files", len(d.files))
	}
	if _, _, ok := d.read("0"); ok {
		t.Error("oldest kept")
	}
	if _, fresh, ok := d.read(strconv.Itoa(diskEntries + 4)); !ok || !fresh {
		t.Error("newest not kept fresh")
	}
	clock = clock.Add(refreshAge)
	if _, fresh, ok := d.read(strconv.Itoa(diskEntries + 4)); !ok || fresh {
		t.Error("old icon not due for a refresh")
	}
}
