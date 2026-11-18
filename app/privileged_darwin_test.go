//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

func TestRequireCurrentHelper(t *testing.T) {
	for _, version := range []int{0, 2, 3, 4, helperpaths.ProtocolVersion} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			previous := privilegedHelperPaths
			privilegedHelperPaths.Socket = filepath.Join(t.TempDir(), "helper.sock")
			t.Cleanup(func() { privilegedHelperPaths = previous })
			listener, err := net.Listen("unix", privilegedHelperPaths.Socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				var req helperRequest
				_ = json.NewDecoder(conn).Decode(&req)
				_ = json.NewEncoder(conn).Encode(helperResponse{OK: true, ProtocolVersion: version})
			}()
			err = requireCurrentHelper()
			if (err != nil) != (version < helperpaths.ProtocolVersion) {
				t.Fatalf("version=%d error=%v", version, err)
			}
		})
	}
}

func TestPrivilegedReloadUsesHelperOperation(t *testing.T) {
	previous := privilegedHelperPaths
	directory, err := os.MkdirTemp("/tmp", "kiko-reload-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	privilegedHelperPaths.Socket = filepath.Join(directory, "helper.sock")
	t.Cleanup(func() { privilegedHelperPaths = previous })
	listener, err := net.Listen("unix", privilegedHelperPaths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan helperRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req helperRequest
		_ = json.NewDecoder(conn).Decode(&req)
		received <- req
		_ = json.NewEncoder(conn).Encode(helperResponse{OK: true, PID: req.PID})
	}()
	if err := privilegedReload(12345); err != nil {
		t.Fatal(err)
	}
	if req := <-received; req.Op != "reload" || req.PID != 12345 {
		t.Fatalf("request=%+v", req)
	}
}

func lifecycleHelper(t *testing.T, respond func(helperRequest) helperResponse) {
	t.Helper()
	previous := privilegedHelperPaths
	dir, err := os.MkdirTemp("/tmp", "kiko-helper-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	privilegedHelperPaths.Socket = filepath.Join(dir, "helper.sock")
	privilegedHelperPaths.CoreLog = filepath.Join(dir, "core.log")
	listener, err := net.Listen("unix", privilegedHelperPaths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var req helperRequest
				if json.NewDecoder(conn).Decode(&req) == nil {
					_ = json.NewEncoder(conn).Encode(respond(req))
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-stopped; privilegedHelperPaths = previous; os.RemoveAll(dir) })
}

func TestPrivilegedStopEscalatesAfterGracePeriod(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	var forced atomic.Bool
	lifecycleHelper(t, func(req helperRequest) helperResponse {
		switch req.Op {
		case "force-stop":
			if req.PID != 12345 {
				t.Errorf("unexpected force PID %d", req.PID)
			}
			forced.Store(true)
			alive.Store(false)
		case "status":
			if alive.Load() {
				return helperResponse{OK: true, PID: 12345}
			}
		}
		return helperResponse{OK: true}
	})
	s := newGRPCTestServer(t)
	s.helperPID = 12345
	s.state = runtimeState{State: "running", PID: 12345, DesiredRunning: true}
	s.persisted.DesiredRunning = true
	began := time.Now()
	s.stop(true)
	if !forced.Load() || time.Since(began) > 5*time.Second || s.snapshot().PID != 0 || s.snapshot().State != "stopped" {
		t.Fatalf("bounded helper stop: forced=%v state=%+v time=%v", forced.Load(), s.snapshot(), time.Since(began))
	}
	t.Logf("unresponsive privileged core stopped in %v", time.Since(began))
}

func TestHelperStatusErrorPreservesProcessOwnership(t *testing.T) {
	var exited atomic.Bool
	lifecycleHelper(t, func(req helperRequest) helperResponse {
		if !exited.Load() {
			return helperResponse{Error: "temporary helper failure"}
		}
		return helperResponse{OK: true}
	})
	s := newGRPCTestServer(t)
	s.helperPID = 12345
	s.state = runtimeState{State: "running", PID: 12345}
	done := make(chan struct{})
	go func() { defer close(done); s.waitPrivileged(12345, 0) }()
	t.Cleanup(func() { exited.Store(true); <-done })
	time.Sleep(550 * time.Millisecond)
	if state := s.snapshot(); state.PID != 12345 || state.State != "running" {
		t.Fatalf("transient helper error lost live process: %+v", state)
	}
	exited.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exit watcher did not finish")
	}
}

func TestPrivilegedStopSharesConfirmedExitWithWatcher(t *testing.T) {
	var observed atomic.Bool
	var recoverStatus atomic.Bool
	lifecycleHelper(t, func(req helperRequest) helperResponse {
		if req.Op == "status" && observed.Swap(true) && !recoverStatus.Load() {
			return helperResponse{Error: "temporary helper failure after confirmed exit"}
		}
		return helperResponse{OK: true}
	})
	s := newGRPCTestServer(t)
	s.helperPID = 12345
	s.coreDone = make(chan struct{})
	s.coreStopped = make(chan struct{})
	s.state = runtimeState{State: "running", PID: 12345}
	watcherDone := s.coreDone
	go func() { defer close(watcherDone); s.waitPrivileged(12345, 0) }()
	done := make(chan struct{})
	go func() { s.stop(true); close(done) }()
	t.Cleanup(func() { recoverStatus.Store(true); <-done; <-watcherDone })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop blocked on watcher after helper already confirmed exit")
	}
}

func TestPrivilegedLogChunksPreserveLongAndSplitLines(t *testing.T) {
	var exited atomic.Bool
	lifecycleHelper(t, func(req helperRequest) helperResponse {
		if !exited.Load() {
			return helperResponse{OK: true, PID: 12345}
		}
		return helperResponse{OK: true}
	})
	want := []string{strings.Repeat("a", 70_000), strings.Repeat("b", 210_000), "last log line"}
	if err := os.WriteFile(helperLogPath(""), []byte(strings.Join(want, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newGRPCTestServer(t)
	s.helperPID = 12345
	done := make(chan struct{})
	go func() { defer close(done); s.waitPrivileged(12345, 0) }()
	t.Cleanup(func() { exited.Store(true); <-done })
	eventually(t, "all complete privileged log lines", func() bool { return len(s.logs.snapshot()) >= len(want) })
	got := s.logs.snapshot()
	if len(got) != len(want) {
		t.Fatalf("split log records: got %d want %d", len(got), len(want))
	}
	for i, line := range want {
		if !strings.HasSuffix(got[i], " "+line) {
			t.Fatalf("log %d was truncated or split: length=%d want=%d", i, len(got[i]), len(line))
		}
	}
}

// Exercise the macOS helper RPC ownership path with real isolated child processes.
// The fixture uses temporary sockets and leaves system TUN, DNS and launchd intact.
func TestPrivilegedReloadExitRecoversPreviousConfigurationOnce(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	s.stop(true)
	if err := os.WriteFile(starts, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var current *exec.Cmd
	var last helperResponse
	lifecycleHelper(t, func(req helperRequest) helperResponse {
		mu.Lock()
		defer mu.Unlock()
		res := last
		res.OK = true
		res.ProtocolVersion = helperpaths.ProtocolVersion
		switch req.Op {
		case "start":
			if current != nil {
				return helperResponse{Error: "core already running"}
			}
			cmd := exec.Command(s.singBox, "run", "-c", req.Config)
			prepareProcess(cmd)
			cmd.Dir = req.DataDir
			file, err := os.OpenFile(helperLogPath(""), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return helperResponse{Error: err.Error()}
			}
			cmd.Stdout, cmd.Stderr = file, file
			if err := cmd.Start(); err != nil {
				file.Close()
				return helperResponse{Error: err.Error()}
			}
			current = cmd
			go func() {
				cmd.Wait()
				file.Close()
				mu.Lock()
				if current == cmd {
					current = nil
					last = helperResponse{ExitedPID: cmd.Process.Pid, ExitCode: cmd.ProcessState.ExitCode()}
				}
				mu.Unlock()
			}()
			res.PID = cmd.Process.Pid
		case "reload", "stop", "force-stop":
			if current == nil || current.Process.Pid != req.PID {
				return helperResponse{Error: "unmanaged PID"}
			}
			var err error
			switch req.Op {
			case "reload":
				err = current.Process.Signal(syscall.SIGHUP)
			case "stop":
				err = gracefulProcess(current)
			case "force-stop":
				err = forceProcess(current)
			}
			if err != nil && !errors.Is(err, os.ErrProcessDone) {
				return helperResponse{Error: err.Error()}
			}
			res.PID = req.PID
		case "status":
			if current != nil {
				res.PID = current.Process.Pid
			}
		}
		return res
	})
	s.usePrivilegedHelper = true
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	defer s.stop(true)
	eventually(t, "helper fixture started", func() bool { data, _ := os.ReadFile(starts); return strings.Count(string(data), "run") == 1 })
	before := s.snapshot()
	if err := os.WriteFile(marker+".exit", []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"new"}],"route":{"final":"new"}}`)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("failed candidate: %d %s", res.Code, res.Body)
	}
	eventually(t, "one helper recovery start", func() bool { data, _ := os.ReadFile(starts); return strings.Count(string(data), "run") == 2 })
	after := s.snapshot()
	if after.State != "running" || !after.DesiredRunning || after.PID <= 1 || after.PID == before.PID {
		t.Fatalf("helper recovery: %+v", after)
	}
	if after.LastExit != "exit code=1" {
		t.Fatalf("helper exit detail: %q", after.LastExit)
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("unexpected second reload: %q", data)
	}
	t.Logf("helper recovered PID %d→%d: %s", before.PID, after.PID, fmt.Sprint(res.Body))
}
