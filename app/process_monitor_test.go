package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

func TestProcessMonitorRatesAndPIDReuse(t *testing.T) {
	old := monitoredProcess{StartedAt: 100, CPUTime: ptr(2.0), ReadBytes: ptr(uint64(200)), WriteBytes: ptr(uint64(300)), ReceivedBytes: ptr(uint64(10)), SentBytes: ptr(uint64(20)), wakeupTotal: ptr(uint64(5)), energyNJ: ptr(uint64(1000000))}
	next := monitoredProcess{StartedAt: 100, CPUTime: ptr(5.0), ReadBytes: ptr(uint64(600)), WriteBytes: ptr(uint64(100)), ReceivedBytes: ptr(uint64(30)), SentBytes: ptr(uint64(80)), wakeupTotal: ptr(uint64(15)), energyNJ: ptr(uint64(7000000))}
	processRates(&next, old, 2)
	if next.CPU == nil || *next.CPU != 150 || next.ReadRate == nil || *next.ReadRate != 200 || next.WriteRate != nil || next.Power == nil || *next.Power != 3 || next.Wakeups == nil || *next.Wakeups != 5 || next.ReceiveRate == nil || *next.ReceiveRate != 10 || next.SendRate == nil || *next.SendRate != 30 {
		t.Fatalf("unexpected rates: %+v", next)
	}
	reused := monitoredProcess{StartedAt: 101, CPUTime: ptr(10.0), ReadBytes: ptr(uint64(999))}
	processRates(&reused, old, 2)
	if reused.CPU != nil || reused.ReadRate != nil {
		t.Fatal("reused PID inherited counters")
	}
}
func TestProcessMonitorIdentifiesAppAndSignedClone(t *testing.T) {
	const app = "/Applications/Google Chrome.app"
	const clone = "/private/var/folders/code_sign_clone/Google Chrome.app.bundle/Contents/MacOS/Google Chrome"
	for _, item := range []monitoredProcess{
		{Path: app + "/Contents/MacOS/Google Chrome"},
		{Path: app + "/Contents/Frameworks/Helper.app/Contents/MacOS/Helper"},
		{Path: clone, originalPath: app + "/Contents/MacOS/Google Chrome"},
	} {
		path := item.Path
		identifyProcess(&item)
		if item.AppPath != app || item.AppName != "Google Chrome" || item.Path != path {
			t.Fatalf("application identity: %+v", item)
		}
	}
}
func TestProcessMonitorPrivilegedAppOwner(t *testing.T) {
	const bundle = "/Applications/Kiko.app"
	owners := processAppOwners(bundle + "/Contents/Resources/bin/kiko-core")
	for _, path := range []string{helperpaths.Current.HelperExecutable, helperpaths.Current.CoreExecutable} {
		p := monitoredProcess{Path: path}
		identifyProcess(&p)
		name := p.Name
		identifyOwnedProcess(&p, owners)
		if p.AppPath != bundle || p.AppName != "Kiko" || p.Path != path || p.Name != name {
			t.Fatalf("privileged process App identity: %+v", p)
		}
	}
	other := monitoredProcess{Path: "/Library/PrivilegedHelperTools/another.helper"}
	identifyOwnedProcess(&other, owners)
	if other.AppPath != "" || processAppOwners("/usr/local/bin/kiko-core") != nil {
		t.Fatal("unrelated helpers acquired an App owner")
	}
}
func TestProcessMonitorSnapshotIncludesSelfAndCaches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	monitor := &processMonitor{}
	snapshot, err := monitor.sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != len(snapshot.Processes) || snapshot.Total == 0 || snapshot.CapturedAt == 0 {
		t.Fatalf("invalid snapshot: %+v", snapshot)
	}
	found := false
	for _, p := range snapshot.Processes {
		if p.PID == int32(os.Getpid()) {
			found = true
			if p.StartedAt <= 0 || p.Name == "" || p.Memory == nil || *p.Memory == 0 || p.CPUTime == nil || p.Threads == nil || *p.Threads <= 0 {
				t.Fatalf("invalid self: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("missing self")
	}
	second, err := monitor.sample(ctx)
	if err != nil || second != snapshot {
		t.Fatal("concurrent clients must share the sampling baseline")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || !strings.Contains(string(raw), `"cpu":null`) {
		t.Fatalf("unavailable metrics must be JSON null: %s %v", raw, err)
	}
}
func TestProcessMonitorRoutesAndSignalValidation(t *testing.T) {
	mux := newRouteMux(nil)
	registerProcessMonitor(mux)
	for _, pid := range []string{"0", "1", "bad"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/v1/process-monitor/"+pid+"/signal", strings.NewReader(`{"startedAt":1}`)))
		if recorder.Code != 400 {
			t.Fatalf("pid %s: %d", pid, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/process-monitor", nil))
	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot route: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestProcessMonitorSignalHelper(t *testing.T) {
	if os.Getenv("KIKO_PROCESS_MONITOR_TEST_CHILD") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
func TestProcessMonitorSignalChecksIdentityAndTerminatesChild(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessMonitorSignalHelper$")
			cmd.Env = append(os.Environ(), "KIKO_PROCESS_MONITOR_TEST_CHILD=1")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			defer func() {
				_ = cmd.Process.Kill()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
				}
			}()
			started, err := monitoredProcessStartedAt(context.Background(), int32(cmd.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			mux := newRouteMux(nil)
			registerProcessMonitor(mux)
			send := func(start int64) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				body := fmt.Sprintf(`{"startedAt":%d,"force":%t}`, start, force)
				mux.ServeHTTP(recorder, httptest.NewRequest("POST", fmt.Sprintf("/api/v1/process-monitor/%d/signal", cmd.Process.Pid), strings.NewReader(body)))
				return recorder
			}
			if response := send(started + 1); response.Code != 409 {
				t.Fatalf("identity mismatch: %d %s", response.Code, response.Body.String())
			}
			if response := send(started); response.Code != 204 {
				t.Fatalf("signal: %d %s", response.Code, response.Body.String())
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("child remained alive after signal")
			}
		})
	}
}

func TestProcessMonitorRetainsNetworkAfterSocketClosure(t *testing.T) {
	sample := func(old monitoredProcess, start int64, sockets map[string][2]uint64) monitoredProcess {
		p := monitoredProcess{PID: 123, StartedAt: start, networkSample: &processNetworkSample{sockets: sockets}}
		retainProcessNetwork(&p, old)
		processRates(&p, old, 1)
		return p
	}
	first := sample(monitoredProcess{}, 100, map[string][2]uint64{"tcp-a": {10, 1000000}, "tcp-b": {20, 100}})
	second := sample(first, 100, map[string][2]uint64{"tcp-b": {30, 200}})
	if *second.ReceivedBytes != 40 || *second.SentBytes != 1000200 || *second.SendRate != 100 {
		t.Fatalf("partial socket closure lost totals: %+v", second)
	}
	closed := sample(second, 100, nil)
	if *closed.SentBytes != 1000200 || *closed.SendRate != 0 {
		t.Fatalf("all sockets closed: %+v", closed)
	}
	reopened := sample(closed, 100, map[string][2]uint64{"tcp-a": {5, 1000}})
	if *reopened.SentBytes != 1001200 || *reopened.SendRate != 1000 {
		t.Fatalf("reopened connection lost totals: %+v", reopened)
	}
	reset := sample(reopened, 100, map[string][2]uint64{"tcp-a": {1, 10}})
	if *reset.SentBytes != 1001210 || *reset.SendRate != 10 {
		t.Fatalf("counter reset: %+v", reset)
	}
	reused := sample(reset, 101, map[string][2]uint64{"tcp-a": {2, 20}})
	if *reused.SentBytes != 20 || reused.SendRate != nil {
		t.Fatalf("PID reuse inherited traffic: %+v", reused)
	}
}
func TestProcessMonitorNetworkFailurePreservesTotalsAndBaseline(t *testing.T) {
	original := monitoredProcess{StartedAt: 100, networkSample: &processNetworkSample{sockets: map[string][2]uint64{"tcp": {20, 100}}}}
	retainProcessNetwork(&original, monitoredProcess{})
	failed := monitoredProcess{StartedAt: 100, networkUnavailable: true}
	retainProcessNetwork(&failed, original)
	processRates(&failed, original, 1)
	if *failed.SentBytes != 100 || failed.SendRate != nil {
		t.Fatalf("failed sample: %+v", failed)
	}
	restored := monitoredProcess{StartedAt: 100, networkSample: &processNetworkSample{sockets: map[string][2]uint64{"tcp": {40, 200}}}}
	retainProcessNetwork(&restored, failed)
	processRates(&restored, failed, 1)
	if *restored.SentBytes != 200 || restored.SendRate != nil {
		t.Fatalf("recovery double counted traffic or reported a rate with incomplete timing: %+v", restored)
	}
}
func TestProcessMonitorThreadCoverage(t *testing.T) {
	total, missing := summarizeProcessThreads([]monitoredProcess{{Threads: ptr(int32(12))}, {Threads: nil}, {Threads: ptr(int32(0))}, {Threads: ptr(int32(3))}})
	if total != 15 || missing != 1 {
		t.Fatalf("thread subtotal: %d unavailable processes: %d", total, missing)
	}
}
