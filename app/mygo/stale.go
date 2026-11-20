package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// staleServerWait is how long a Kiko server left by an earlier run of the
// app gets to end, before it is asked to and again before it is killed.
var staleServerWait = 10 * time.Second

// clearStaleServer makes room for a new run when a Kiko server still
// answers at the app's address: one an earlier run left behind (the app
// was killed, or replaced while running) ends on its own once its parent
// is gone, or is stopped. A port held by another program stays an error.
func (s *serverSupervisor) clearStaleServer(ctx context.Context) error {
	client := &http.Client{Timeout: time.Second}
	pid, ok := serverPID(ctx, client, s.url)
	if !ok {
		return nil
	}
	if serverGone(ctx, client, s.url, staleServerWait) {
		return nil
	}
	if name, err := processName(pid); err != nil || !isServerExecutable(name) {
		return fmt.Errorf("%s 已被其他服务占用 (PID %d)", s.url, pid)
	}
	log.Printf("[kiko] stopping kiko-server %d left by an earlier run", pid)
	_ = terminateProcess(pid)
	if serverGone(ctx, client, s.url, staleServerWait) {
		return nil
	}
	_ = killProcess(pid)
	if serverGone(ctx, client, s.url, 5*time.Second) {
		return nil
	}
	return fmt.Errorf("%s 上旧的 Kiko 服务 (PID %d) 无法结束，请在“活动监视器”中退出 kiko-server 后重试", s.url, pid)
}

// serverGone waits up to wait for no server to answer at url.
func serverGone(ctx context.Context, client *http.Client, url string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if _, ok := serverPID(ctx, client, url); !ok {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
}

// isServerExecutable reports a process name or path of kiko-server.
func isServerExecutable(name string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	return base == "kiko-server" || base == "kiko-server.exe"
}
