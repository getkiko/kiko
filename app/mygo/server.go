package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	"github.com/getkiko/kiko/app/mygo/internal/supervisor"
)

const serverPort = 9099

// serverSupervisor runs the kiko-server child process and restarts it
// when it exits on its own (internal/supervisor holds the policy). Each run
// stops through the coordinated shutdown handshake: an authenticated
// loopback request plus stdin EOF, with a force-kill fallback after the
// timeout.
type serverSupervisor struct {
	paths *Paths
	url   string
	token string

	sup *supervisor.Supervisor
	// onChange, set before start, receives each status change.
	onChange func(supervisor.Status)
}

func newServerSupervisor(paths *Paths) (*serverSupervisor, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	s := &serverSupervisor{
		paths: paths,
		url:   paths.serverURL(),
		token: hex.EncodeToString(raw),
	}
	s.sup = supervisor.New(s.launch, supervisor.RealClock, supervisor.Options{}, func(status supervisor.Status) {
		if status.State == supervisor.Restarting || status.State == supervisor.CrashLoop {
			log.Printf("[kiko] server exited unexpectedly (%s), state: %s", describeExit(status.Last), status.State)
		}
		if s.onChange != nil {
			s.onChange(status)
		}
	})
	return s, nil
}

// start launches kiko-server and waits until it serves; a failure here is
// fatal to the app and not retried.
func (s *serverSupervisor) start() error { return s.sup.Start() }

// restart replaces the running server or leaves a crash loop, as the
// console's "Restart now".
func (s *serverSupervisor) restart() { s.sup.Restart() }

// stop shuts the server down for good, as the app quits; nothing restarts
// it, not even a restart the user asked for meanwhile. It is safe to call
// more than once.
func (s *serverSupervisor) stop() { s.sup.Shutdown() }

func (s *serverSupervisor) status() supervisor.Status { return s.sup.Status() }

// launch starts one run of kiko-server and waits until it answers with
// its own PID, refusing a port already occupied by an unrelated service.
func (s *serverSupervisor) launch(ctx context.Context) (supervisor.Process, error) {
	kiko := s.paths.binaryPath("kiko-server")
	core := s.paths.binaryPath("kiko-core")
	for _, file := range []string{kiko, core} {
		if _, err := os.Stat(file); err != nil {
			return nil, fmt.Errorf("缺少打包的二进制文件: %s", file)
		}
	}
	if err := os.MkdirAll(s.paths.dataDir(), 0o700); err != nil {
		return nil, err
	}
	if err := s.clearStaleServer(ctx); err != nil {
		return nil, err
	}

	args := []string{
		"--core", core,
		"--listen", hostPort(s.url),
		"--data-dir", s.paths.dataDir(),
	}
	if mygo.App.IsPackaged() && runtimeIsDarwin() {
		args = append(args, "--privileged-helper")
	}

	cmd := exec.Command(kiko, args...)
	cmd.Dir = s.paths.dataDir()
	env := binaryEnv(dirOf(core))
	cmd.Env = append(env, "KIKO_DESKTOP_SHUTDOWN_TOKEN="+s.token)
	p, err := startServerProcess(cmd, s)
	if err != nil {
		return nil, err
	}
	if err := s.waitReady(ctx, p); err != nil {
		p.Stop()
		select {
		case <-p.exited:
			// It ended on its own: its exit says why.
			if !errors.Is(err, context.Canceled) {
				return nil, &supervisor.ExitError{Exit: p.exit, Err: err}
			}
		default:
		}
		return nil, err
	}
	return p, nil
}

// waitReady polls /api/v1/info until the run p answers with its PID.
func (s *serverSupervisor) waitReady(ctx context.Context, p *serverProcess) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case <-p.exited:
			return errors.New("kiko-server 启动后立即退出")
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("kiko-server 未能在 %s 上启动", s.url)
			}
			return ctx.Err()
		default:
		}
		if pid, ok := serverPID(ctx, client, s.url); ok {
			if pid != p.cmd.Process.Pid {
				return fmt.Errorf("%s 已被其他服务占用 (PID %d)", s.url, pid)
			}
			return nil
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
		case <-p.exited:
		}
	}
}

// serverPID asks the server at url for its PID.
func serverPID(ctx context.Context, client *http.Client, url string) (int, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/info", nil)
	if err != nil {
		return 0, false
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if response.StatusCode != http.StatusOK {
		return 0, false
	}
	var info struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(body, &info) != nil || info.PID == 0 {
		return 0, false
	}
	return info.PID, true
}

// stderrTailLines is how much of a run's standard error its exit keeps.
const stderrTailLines = 20

// serverProcess is one run of kiko-server.
type serverProcess struct {
	owner *serverSupervisor
	cmd   *exec.Cmd
	stdin io.WriteCloser

	tailMu sync.Mutex
	tail   []string

	exited   chan struct{} // closed once reaped
	exit     supervisor.Exit
	stopOnce sync.Once
}

// startServerProcess starts cmd, forwarding its output to the log and
// reaping it once both pipes are drained.
func startServerProcess(cmd *exec.Cmd, owner *serverSupervisor) (*serverProcess, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &serverProcess{owner: owner, cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() {
		defer pipes.Done()
		logPipe(stdout, "[kiko] ", nil)
	}()
	go func() {
		defer pipes.Done()
		logPipe(stderr, "[kiko] ", p.keepTail)
	}()
	go func() {
		// Wait closes the pipes, so it follows their readers.
		pipes.Wait()
		waitErr := cmd.Wait()
		_ = stdin.Close()
		p.exit = exitOf(cmd, waitErr)
		p.tailMu.Lock()
		p.exit.Stderr = strings.Join(p.tail, "\n")
		p.tailMu.Unlock()
		close(p.exited)
	}()
	return p, nil
}

func (p *serverProcess) keepTail(line string) {
	p.tailMu.Lock()
	defer p.tailMu.Unlock()
	p.tail = append(p.tail, line)
	if len(p.tail) > stderrTailLines {
		p.tail = p.tail[len(p.tail)-stderrTailLines:]
	}
}

// Wait returns once the run has exited and been reaped.
func (p *serverProcess) Wait() supervisor.Exit {
	<-p.exited
	return p.exit
}

// Stop asks the server to clean up through the authenticated loopback
// endpoint, then closes stdin, waits, and force-kills a process tree that
// outstays the timeout.
func (p *serverProcess) Stop() {
	p.stopOnce.Do(func() {
		select {
		case <-p.exited:
			return
		default:
		}
		body := bytes.NewReader([]byte("{}"))
		request, err := http.NewRequest(http.MethodPost, p.owner.url+"/api/v1/shutdown", body)
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Kiko-Shutdown-Token", p.owner.token)
			client := &http.Client{Timeout: 2 * time.Second}
			if response, err := client.Do(request); err == nil {
				response.Body.Close()
			} else {
				log.Printf("[kiko] quit request failed: %v", err)
			}
		}
		// stdin EOF is the second cleanup signal; the server may already
		// be gone.
		_ = p.stdin.Close()
		select {
		case <-p.exited:
			return
		case <-time.After(60 * time.Second):
		}
		killTree(p.cmd)
		<-p.exited
	})
}

// exitOf describes how cmd ended.
func exitOf(cmd *exec.Cmd, waitErr error) supervisor.Exit {
	exit := supervisor.Exit{Code: -1}
	if waitErr != nil {
		exit.Err = waitErr.Error()
	}
	state := cmd.ProcessState
	if state == nil {
		return exit
	}
	exit.Code = state.ExitCode()
	if status, ok := state.Sys().(interface {
		Signaled() bool
		Signal() syscall.Signal
	}); ok && status.Signaled() {
		exit.Signal = status.Signal().String()
	}
	return exit
}

// describeExit is a one-line account of how a run ended.
func describeExit(exit *supervisor.Exit) string {
	if exit == nil {
		return "未知原因"
	}
	switch {
	case exit.Signal != "":
		return "信号 " + exit.Signal
	case exit.Code >= 0:
		return fmt.Sprintf("退出码 %d", exit.Code)
	case exit.Err != "":
		return exit.Err
	}
	return "未知原因"
}

func dirOf(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[:index]
	}
	return "."
}

// logPipe forwards the child's output to ours so it lands in Console/stderr
// for desktop diagnostics, handing each line to keep when it is not nil.
func logPipe(reader io.Reader, prefix string, keep func(string)) {
	scanner := newLineScanner(reader)
	for scanner.Scan() {
		log.Println(prefix + scanner.Text())
		if keep != nil {
			keep(scanner.Text())
		}
	}
	// A line beyond the scanner's limit ends the scan: drain the rest so
	// the child never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, reader)
}

// hostPort extracts the listen address from the local server URL.
func hostPort(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
}
