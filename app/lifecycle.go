package main

import (
	"fmt"
	"time"
)

// coreGraceTimeout is how long a stop waits for the core to exit on
// SIGTERM before killing it: the core closes within 600 ms and exits.
const coreGraceTimeout = 900 * time.Millisecond
const coreKillTimeout = 500 * time.Millisecond

// Caller holds s.mu. Process exit and system DNS restoration share one barrier.
func (s *server) coreCleanupPendingLocked() bool {
	if s.coreDone == nil {
		return false
	}
	select {
	case <-s.coreDone:
		return false
	default:
		return true
	}
}

func awaitCore(timeout time.Duration, gone func() bool) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if gone() {
			return true
		}
		select {
		case <-timer.C:
			return gone()
		case <-ticker.C:
		}
	}
}

func (s *server) finishCoreStop(target stopTarget) {
	began := time.Now()
	fail := func(message string) {
		s.mu.Lock()
		s.state.State = "failed"
		s.state.Error = message
		s.mu.Unlock()
		s.emit("runtime", s.snapshot())
	}
	if target.helperPID > 0 {
		pid := target.helperPID
		if err := privilegedStop(pid); err != nil {
			s.logs.add("[kiko] 停止特权进程失败: " + err.Error())
		}
		var aliveErr error
		gone := func() bool {
			var alive bool
			alive, aliveErr = privilegedAliveChecked(pid)
			return aliveErr == nil && !alive
		}
		if !awaitCore(coreGraceTimeout, gone) {
			if aliveErr != nil {
				fail("无法确认核心已停止: " + aliveErr.Error())
				return
			}
			s.logs.add("[kiko] 核心退出超过 0.9 秒，正在强制结束")
			if err := privilegedForceStop(pid); err != nil && !gone() {
				fail("强制停止核心失败: " + err.Error())
				return
			}
			if !awaitCore(coreKillTimeout, gone) {
				fail("停止核心超时")
				return
			}
		}
		s.logs.add(fmt.Sprintf("[kiko] 特权核心已退出（等待 %s）", time.Since(began).Round(time.Millisecond)))
		if target.stopped != nil {
			close(target.stopped)
		}
		if target.exited != nil {
			<-target.exited
		}
		s.mu.Lock()
		if s.helperPID == pid {
			s.helperPID = 0
			s.state.PID = 0
			s.state.State = "stopped"
		}
		s.mu.Unlock()
	} else if target.cmd != nil {
		cmd := target.cmd
		gone := func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cmd != cmd }
		if err := gracefulProcess(cmd); err != nil {
			_ = forceProcess(cmd)
		}
		if !awaitCore(coreGraceTimeout, gone) {
			s.logs.add("[kiko] 核心退出超过 0.9 秒，正在强制结束")
			s.mu.Lock()
			if s.cmd == cmd {
				_ = forceProcess(cmd)
			}
			s.mu.Unlock()
			if !awaitCore(coreKillTimeout, gone) {
				fail("停止核心超时")
				return
			}
		}
		s.logs.add(fmt.Sprintf("[kiko] 核心已退出（等待 %s）", time.Since(began).Round(time.Millisecond)))
	}
	if target.exited != nil {
		<-target.exited
	}
	s.mu.Lock()
	if s.cmd == nil && s.helperPID == 0 {
		s.state.State = "stopped"
	}
	s.mu.Unlock()
	s.restoreSystemDNSConfig()
	s.emit("runtime", s.snapshot())
}
