package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type persistFake struct {
	mitmapi.UnimplementedHTTPCaptureServer
	deleted atomic.Pointer[mitmapi.DeleteRecordRequest]
	purged  atomic.Bool
	purgeIs error
}

func (f *persistFake) DeleteRecord(_ context.Context, in *mitmapi.DeleteRecordRequest) (*mitmapi.CaptureStatus, error) {
	f.deleted.Store(in)
	if in.Id == "missing" {
		return nil, status.Error(codes.NotFound, "record not found")
	}
	return &mitmapi.CaptureStatus{Instance: "i", PersistEnabled: true, PersistRecords: 3, PersistBytes: 4096, PersistWritten: 9}, nil
}

func (f *persistFake) PurgePersisted(context.Context, *emptypb.Empty) (*mitmapi.CaptureStatus, error) {
	f.purged.Store(true)
	if f.purgeIs != nil {
		return nil, f.purgeIs
	}
	return &mitmapi.CaptureStatus{Instance: "i", PersistEnabled: true}, nil
}

func TestHTTPCaptureDeleteRecordRoute(t *testing.T) {
	fake := &persistFake{}
	s := startPersistFake(t, fake)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/http/records/{id}", s.deleteHTTPRecord)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/http/records/42?instance=abc", nil))
	var body struct {
		Status httpStatusJSON `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Status.PersistRecords != 3 || body.Status.PersistBytes != 4096 || body.Status.PersistWritten != "9" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got := fake.deleted.Load(); got == nil || got.Id != "42" || got.Instance != "abc" {
		t.Fatalf("request %+v", got)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/http/records/missing", nil))
	if w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// No core connection answers 503.
	bare := newGRPCTestServer(t)
	w = httptest.NewRecorder()
	bare.deleteHTTPRecord(w, httptest.NewRequest("DELETE", "/api/v1/http/records/1", nil))
	if w.Code != 503 {
		t.Fatalf("%d", w.Code)
	}
}

func startPersistFake(t *testing.T, fake *persistFake) *server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer()
	t.Cleanup(rpcServer.Stop)
	mitmapi.RegisterHTTPCaptureServer(rpcServer, fake)
	go rpcServer.Serve(listener)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := newGRPCTestServer(t)
	s.rpc.httpCapture = mitmapi.NewHTTPCaptureClient(conn)
	return s
}

func TestHTTPCapturePurgeClearsDirectoryWithoutCore(t *testing.T) {
	s := newGRPCTestServer(t)
	dir := s.httpCapturePersistDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"seg-000000000001.cap", "seg-000000000002.cap"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.purgeHTTPPersisted(w, httptest.NewRequest("POST", "/api/v1/http/persist/purge", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%v", entries)
	}
	// Repeating on a missing directory succeeds.
	w = httptest.NewRecorder()
	s.purgeHTTPPersisted(w, httptest.NewRequest("POST", "/api/v1/http/persist/purge", nil))
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
}

func TestHTTPCapturePurgeThroughCoreAndDirectory(t *testing.T) {
	for name, purgeErr := range map[string]error{
		"ok":          nil,
		"unavailable": status.Error(codes.FailedPrecondition, "HTTPS 解密未运行"),
	} {
		t.Run(name, func(t *testing.T) {
			fake := &persistFake{purgeIs: purgeErr}
			s := startPersistFake(t, fake)
			dir := s.httpCapturePersistDir()
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, "seg-000000000001.cap"), []byte("x"), 0o600)
			w := httptest.NewRecorder()
			s.purgeHTTPPersisted(w, httptest.NewRequest("POST", "/api/v1/http/persist/purge", nil))
			if w.Code != 200 || !fake.purged.Load() {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("%v", entries)
			}
		})
	}
	fake := &persistFake{purgeIs: status.Error(codes.Internal, "stored files could not be removed")}
	s := startPersistFake(t, fake)
	w := httptest.NewRecorder()
	s.purgeHTTPPersisted(w, httptest.NewRequest("POST", "/api/v1/http/persist/purge", nil))
	if w.Code == 200 {
		t.Fatalf("core failure was hidden: %s", w.Body.String())
	}
}

// capture.persist gets the directory at run time on a core that knows it and
// is removed, with the rest of capture kept, on a core that does not.
func TestInjectRuntimePersistPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	run := func(script string) (map[string]any, string, string) {
		dataDir := t.TempDir()
		s := &server{singBox: writeScript(t, script), dataDir: dataDir, logs: newLogBuffer(t.TempDir())}
		s.rpc.probeOnce.Do(func() {})
		out, err := s.injectRuntime(map[string]any{"mitm": map[string]any{
			"enabled": true,
			"capture": map[string]any{"enabled": true, "persist": map[string]any{"enabled": true, "body": true}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			MITM map[string]any `json:"mitm"`
		}
		if err := json.Unmarshal(out, &config); err != nil {
			t.Fatal(err)
		}
		return config.MITM, strings.Join(s.logs.snapshot(), "\n"), dataDir
	}
	older, log, _ := run("if grep -q persist \"$3\"; then exit 1; fi\nexit 0\n")
	capture, _ := older["capture"].(map[string]any)
	if capture == nil || capture["enabled"] != true {
		t.Fatalf("capture lost: %v", older)
	}
	if _, ok := capture["persist"]; ok || !strings.Contains(log, "抓取存储") {
		t.Fatalf("persist kept on an older core: %v log %q", capture, log)
	}
	current, log, dataDir := run("exit 0\n")
	capture, _ = current["capture"].(map[string]any)
	persist, _ := capture["persist"].(map[string]any)
	if persist == nil || persist["path"] != filepath.Join(dataDir, "http-capture") || persist["body"] != true {
		t.Fatalf("persist %v", capture)
	}
	if strings.Contains(log, dataDir) {
		t.Fatalf("path in the log: %q", log)
	}
	// Without capture support the whole section goes.
	none, _, _ := run("if grep -q capture \"$3\"; then exit 1; fi\nexit 0\n")
	if _, ok := none["capture"]; ok {
		t.Fatalf("%v", none)
	}
}
