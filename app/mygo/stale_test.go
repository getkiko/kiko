package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func infoServer(t *testing.T, pid int, answering *atomic.Bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !answering.Load() || r.URL.Path != "/api/v1/info" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"pid":%d}`, pid)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// A server left by an earlier run that ends on its own makes room.
func TestClearStaleServerWaitsForItToEnd(t *testing.T) {
	staleServerWait = 2 * time.Second
	t.Cleanup(func() { staleServerWait = 10 * time.Second })
	var answering atomic.Bool
	answering.Store(true)
	ts := infoServer(t, 4242, &answering)
	time.AfterFunc(300*time.Millisecond, func() { answering.Store(false) })
	if err := (&serverSupervisor{url: ts.URL}).clearStaleServer(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A port held by another program is not touched and stays an error.
func TestClearStaleServerLeavesOtherPrograms(t *testing.T) {
	staleServerWait = 300 * time.Millisecond
	t.Cleanup(func() { staleServerWait = 10 * time.Second })
	var answering atomic.Bool
	answering.Store(true)
	ts := infoServer(t, os.Getpid(), &answering)
	err := (&serverSupervisor{url: ts.URL}).clearStaleServer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已被其他服务占用") {
		t.Fatalf("error %v", err)
	}
}

func TestIsServerExecutable(t *testing.T) {
	for name, want := range map[string]bool{
		"/Applications/Kiko.app/Contents/Resources/bin/kiko-server\n": true,
		"kiko-server.exe": true, "Kiko-Server.EXE": true, "kiko-core": false, "zsh": false, "": false,
	} {
		if got := isServerExecutable(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}
