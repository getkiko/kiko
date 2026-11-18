//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const coreGracePeriod = 3 * time.Second
const coreKillPeriod = time.Second

// launchd termination must also retire the independent process group created
// for the core, before this helper loses its in-memory ownership record.
func shutdownManagedCore() error {
	processMu.Lock()
	shuttingDown = true
	cmd := managed
	processMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	exited := func() bool { processMu.Lock(); defer processMu.Unlock(); return managed != cmd }
	if waitCoreExit(context.Background(), coreGracePeriod, exited) {
		return nil
	}
	processMu.Lock()
	if managed == cmd {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	processMu.Unlock()
	if waitCoreExit(context.Background(), coreKillPeriod, exited) {
		return nil
	}
	return errors.New("timed out waiting for managed kiko-core to exit")
}

func waitCoreExit(ctx context.Context, timeout time.Duration, exited func() bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if exited() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return exited()
		case <-ticker.C:
		}
	}
}

type coreIdentity struct {
	pid, parent, uid, group int
	executable              string
}

func parseCoreIdentity(line string) (coreIdentity, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return coreIdentity{}, false
	}
	var values [4]int
	for i := range values {
		var err error
		values[i], err = strconv.Atoi(fields[i])
		if err != nil {
			return coreIdentity{}, false
		}
	}
	return coreIdentity{values[0], values[1], values[2], values[3], strings.Join(fields[4:], " ")}, true
}

func orphanCore(identity coreIdentity) bool {
	return identity.pid > 1 && identity.parent == 1 && identity.uid == os.Geteuid() && identity.group == identity.pid && identity.executable == helperPaths.CoreExecutable
}

func sameOrphan(identity coreIdentity) bool {
	out, err := exec.Command("/bin/ps", "-p", strconv.Itoa(identity.pid), "-o", "pid=,ppid=,uid=,pgid=,comm=").Output()
	if err != nil {
		return false
	}
	current, ok := parseCoreIdentity(string(out))
	return ok && current == identity && orphanCore(current)
}

// Upgrading an older helper can leave an orphan after launchd stops its parent.
// Match the exact installed executable, owner, parent and process group. Recheck
// that identity immediately before each signal; never accept a caller's PID.
func cleanupOrphanedCores(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,uid=,pgid=,comm=").Output()
	if err != nil {
		return fmt.Errorf("inspect orphan kiko-core: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		identity, ok := parseCoreIdentity(line)
		if !ok || !orphanCore(identity) || !sameOrphan(identity) {
			continue
		}
		fmt.Fprintf(os.Stderr, "stopping orphan kiko-core PID %d before helper startup\n", identity.pid)
		if err := syscall.Kill(-identity.pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		exited := func() bool { return !sameOrphan(identity) }
		if waitCoreExit(ctx, coreGracePeriod, exited) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sameOrphan(identity) {
			if err := syscall.Kill(-identity.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		if !waitCoreExit(ctx, coreKillPeriod, exited) {
			return fmt.Errorf("orphan kiko-core PID %d did not exit", identity.pid)
		}
	}
	return nil
}
