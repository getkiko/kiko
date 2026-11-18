package routes

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func scanFixture(t *testing.T, state string, cancels *atomic.Int32) *fixture {
	devices := []api.ScanDevice{
		{IP: "192.168.1.20", Name: "iPhone", Type: "phone", MAC: "aa:bb:cc:00:00:01", Sources: []string{"arp"}},
		{IP: "192.168.1.77", Name: "iPhone-moved", Type: "phone", MAC: "00:11:22:33:44:55", Sources: []string{"arp"}},
		{IP: "192.168.1.30", Name: "Mia 的 iPad", Type: "tablet", Model: "iPad Pro", MAC: "da:11:22:33:44:66", MACRandom: true, Image: "ipad", Sources: []string{"mdns"}},
		{IP: "192.168.1.40", Type: "tv", Sources: []string{"ssdp"}},
		{IP: "192.168.1.2", Name: "本机", Type: "computer", Self: true, Sources: []string{"self"}},
		{IP: "10.0.0.9", Type: "unknown", Sources: []string{"ping"}},
	}
	return newFixtureDoc(t, pendingDoc(), func(f *fixture) {
		f.srv.Route("POST", "/lan-scan", testkit.JSON(map[string]string{"id": "s1"}))
		f.srv.Route("GET", "/lan-scan", testkit.JSON(api.ScanSnapshot{ID: "s1", State: state, Step: 2, Steps: 5, Cursor: 6, Devices: devices}))
		f.srv.Route("POST", "/lan-scan/cancel", func(w http.ResponseWriter, r *http.Request) {
			cancels.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	})
}

func TestScanDialogAddsSelected(t *testing.T) {
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	d := f.w.scan
	for _, want := range []string{"已添加", "本机", "MAC 未识别", "随机 MAC", "192.168.1.30 · 平板 · iPad Pro", "添加所选 3 台"} {
		if !f.tt.HasText(want) {
			t.Errorf("missing %q: %q", want, f.tt.Texts())
		}
	}
	if d.selected["192.168.1.20"] || d.selected["192.168.1.2"] || !d.selected["192.168.1.30"] || !d.selected["192.168.1.40"] {
		t.Fatalf("selected %v", d.selected)
	}
	// The known device seen at another address has it pending.
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].PendingAddresses) == 1 })
	f.tt.Frame()
	f.w.addScanned(d)
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices) == 4 })
	ipad := f.saved().Route.Devices[1]
	if ipad.Name != "Mia 的 iPad" || ipad.MAC != "" || ipad.ImageKey != "ipad" || ipad.Icon != "phone" ||
		len(ipad.Addresses) != 1 || ipad.Addresses[0] != "192.168.1.30/32" {
		t.Fatalf("device %+v", ipad)
	}
	if tv := f.saved().Route.Devices[2]; tv.Name != "192.168.1.40" || tv.MAC != "" || tv.Icon != "server" {
		t.Fatalf("device %+v", tv)
	}
	if f.w.scan != nil && f.w.scan.open {
		t.Fatal("dialog stays open")
	}
}

func TestScanCancelAndRemote(t *testing.T) {
	var cancels atomic.Int32
	f := scanFixture(t, api.ScanRunning, &cancels)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "扫描中 2 / 5")
	f.click("取消扫描")
	testkit.Until(t, f.tt, func() bool { return cancels.Load() == 1 })
	f.tt.Frame()
	if f.w.scan.running || !f.tt.HasText("Mia 的 iPad") || !f.tt.HasText("添加所选 3 台") {
		t.Fatalf("texts %q", f.tt.Texts())
	}

	g := scanFixture(t, api.ScanDone, new(atomic.Int32))
	g.env.Target.Remote = true
	g.tt.Frame()
	if err := g.tt.Click("扫描局域网"); err == nil {
		t.Fatal("scan offered for a remote server")
	}
}

// A random MAC changes, so it is not saved with the added device; a MAC
// that conflicts with the device at the address is marked and the other
// hosts are still added.
func TestScanAddSkipsFailedHostsAndRandomMAC(t *testing.T) {
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	for _, want := range []string{"MAC 不同", "已添加"} {
		if !f.tt.HasText(want) {
			t.Errorf("missing %q: %q", want, f.tt.Texts())
		}
	}
	d := f.w.scan
	if d.selected["192.168.1.20"] {
		t.Fatal("a conflict starts ticked")
	}
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].PendingAddresses) == 1 })
	f.tt.Frame()
	d.selected["192.168.1.20"] = true
	f.w.addScanned(d)
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices) == 4 })
	if !d.open || d.failed["192.168.1.20"] == "" || len(d.failed) != 1 {
		t.Fatalf("open %v failed %v", d.open, d.failed)
	}
	f.tt.Frame()
	if !f.tt.HasText(d.failed["192.168.1.20"]) {
		t.Errorf("failure not shown: %q", f.tt.Texts())
	}
	for _, dev := range f.saved().Route.Devices {
		if model.IsRandomMAC(dev.MAC) {
			t.Errorf("random MAC saved: %+v", dev)
		}
	}
	// The added hosts are unticked and listed as added.
	if d.selected["192.168.1.30"] || !d.selected["192.168.1.20"] {
		t.Errorf("selected %v", d.selected)
	}
}

func TestCleanScanText(t *testing.T) {
	long := strings.Repeat("长", 100)
	cases := map[string]string{
		"iPhone\x00\x07 15\n": "iPhone 15",
		"bad\xffutf8":         "badutf8",
		"  spaced  ":          "spaced",
		long:                  strings.Repeat("长", 64),
	}
	for in, want := range cases {
		if got := cleanScanText(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if got := scanName(api.ScanDevice{IP: "10.0.0.1", Name: "\x01\x02", Model: "iPad\x1b"}); got != "iPad" {
		t.Errorf("scanName %q", got)
	}
	if got := scanName(api.ScanDevice{IP: "10.0.0.1", Name: "\x01"}); got != "10.0.0.1" {
		t.Errorf("scanName %q", got)
	}
}

// A 409 (a scan of this window is still running on the server) cancels
// that scan and starts again once.
func TestScanRestartAfterConflict(t *testing.T) {
	var cancelled []string
	var mu sync.Mutex
	starts := 0
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	f.srv.Route("POST", "/lan-scan", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts++
		n := starts
		mu.Unlock()
		if n == 1 {
			http.Error(w, "扫描进行中", http.StatusConflict)
			return
		}
		testkit.JSON(map[string]string{"id": "s1"})(w, r)
	})
	f.srv.Route("POST", "/lan-scan/cancel", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cancelled = append(cancelled, r.URL.Query().Get("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	old := "old"
	f.w.lastScan.Store(&old)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 || len(cancelled) != 1 || cancelled[0] != "old" {
		t.Fatalf("starts %d cancelled %v", starts, cancelled)
	}
	if f.w.scan.err != "" {
		t.Fatalf("err %q", f.w.scan.err)
	}
}

// Closing the dialog while the start request is in flight still cancels
// the scan the server then starts.
func TestScanCancelledWhileStarting(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var cancelled []string
	f := scanFixture(t, api.ScanRunning, new(atomic.Int32))
	f.srv.Route("POST", "/lan-scan", func(w http.ResponseWriter, r *http.Request) {
		<-gate
		testkit.JSON(map[string]string{"id": "s7"})(w, r)
	})
	f.srv.Route("POST", "/lan-scan/cancel", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cancelled = append(cancelled, r.URL.Query().Get("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	f.w.cancelScan(f.w.scan)
	close(gate)
	testkit.Until(t, f.tt, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(cancelled) == 1 && cancelled[0] == "s7"
	})
}

// busyFixture answers the first start with 409 carrying body, then starts
// scan s1; it records the ids cancelled and the number of starts.
func busyFixture(t *testing.T, body string, cancelled *[]string, starts *int, mu *sync.Mutex) *fixture {
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	f.srv.Route("POST", "/lan-scan", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*starts++
		n := *starts
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(body))
			return
		}
		testkit.JSON(map[string]string{"id": "s1"})(w, r)
	})
	f.srv.Route("POST", "/lan-scan/cancel", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*cancelled = append(*cancelled, r.URL.Query().Get("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return f
}

// After a restart the window knows no scan of its own: the 409 names the
// orphan, which is cancelled before the retry.
func TestScanConflictCancelsServerNamedOrphan(t *testing.T) {
	var mu sync.Mutex
	var cancelled []string
	starts := 0
	f := busyFixture(t, `{"error":"已有扫描在进行","id":"orphan"}`, &cancelled, &starts, &mu)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 || len(cancelled) != 1 || cancelled[0] != "orphan" || f.w.scan.err != "" {
		t.Fatalf("starts %d cancelled %v err %q", starts, cancelled, f.w.scan.err)
	}
}

// The id the server names wins over this window's last scan, so a dialog
// closed and reopened at once cancels the scan that is really running.
func TestScanConflictPrefersServerID(t *testing.T) {
	var mu sync.Mutex
	var cancelled []string
	starts := 0
	f := busyFixture(t, `{"error":"已有扫描在进行","id":"live"}`, &cancelled, &starts, &mu)
	stale := "stale"
	f.w.lastScan.Store(&stale)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 || len(cancelled) != 1 || cancelled[0] != "live" {
		t.Fatalf("starts %d cancelled %v", starts, cancelled)
	}
}

// An older server answers 409 without an id: the last scan is the fallback.
func TestScanConflictWithoutIDFallsBackToLast(t *testing.T) {
	var mu sync.Mutex
	var cancelled []string
	starts := 0
	f := busyFixture(t, "扫描进行中", &cancelled, &starts, &mu)
	last := "mine"
	f.w.lastScan.Store(&last)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 || len(cancelled) != 1 || cancelled[0] != "mine" {
		t.Fatalf("starts %d cancelled %v", starts, cancelled)
	}
}

// "Cancel scan" polls once more before it cancels, so hosts found since the
// last poll are kept.
func TestScanCancelTakesFinalPoll(t *testing.T) {
	var mu sync.Mutex
	var events []string
	f := scanFixture(t, api.ScanRunning, new(atomic.Int32))
	f.srv.Route("GET", "/lan-scan", func(w http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("since")
		mu.Lock()
		events = append(events, "poll:"+since)
		mu.Unlock()
		if since == "0" {
			testkit.JSON(api.ScanSnapshot{ID: "s1", State: api.ScanRunning, Step: 1, Steps: 5, Cursor: 1,
				Devices: []api.ScanDevice{{IP: "192.168.1.40", Name: "Early", Type: "tv", Sources: []string{"ssdp"}}}})(w, r)
			return
		}
		testkit.JSON(api.ScanSnapshot{ID: "s1", State: api.ScanRunning, Step: 2, Steps: 5, Cursor: 2,
			Devices: []api.ScanDevice{{IP: "192.168.1.41", Name: "Late", Type: "tv", Sources: []string{"ssdp"}}}})(w, r)
	})
	f.srv.Route("POST", "/lan-scan/cancel", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, "cancel")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Early")
	f.click("取消扫描")
	testkit.UntilText(t, f.tt, "Late")
	testkit.Until(t, f.tt, func() bool { return !f.w.scan.running })
	mu.Lock()
	defer mu.Unlock()
	cancelAt, finalAt := -1, -1
	for i, e := range events {
		if e == "cancel" && cancelAt < 0 {
			cancelAt = i
		}
		if e == "poll:1" && finalAt < 0 {
			finalAt = i
		}
	}
	if finalAt < 0 || cancelAt < finalAt {
		t.Fatalf("events %v", events)
	}
}

// A conflicting host (its address belongs to a device with another MAC)
// cannot be ticked, and the status is computed once per document version
// and scan revision.
func TestScanConflictRowLockedAndStatusCached(t *testing.T) {
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	d := f.w.scan
	host := api.ScanDevice{IP: "192.168.1.20", MAC: "aa:bb:cc:00:00:01"}
	known, conflict := f.w.hostStatus(d, f.w.doc(), host)
	if known || !conflict {
		t.Fatalf("known %v conflict %v", known, conflict)
	}
	d.status.conflict["marker"] = true
	f.w.hostStatus(d, f.w.doc(), host)
	if !d.status.conflict["marker"] {
		t.Error("status recomputed without a change")
	}
	d.rev++
	f.w.hostStatus(d, f.w.doc(), host)
	if d.status.conflict["marker"] {
		t.Error("status kept after the scan changed")
	}
	// Ticking is refused: the checkbox is disabled and the choice stays off.
	d.selected["192.168.1.20"] = false
	f.tt.Frame()
	if d.selected["192.168.1.20"] {
		t.Error("conflict row ticked")
	}
}
