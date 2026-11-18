//go:build darwin

// Command kiko-helper is the small root-owned launchd service used by the
// macOS desktop app to start kiko-core with permission to create a utun device.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
	"golang.org/x/sys/unix"
)

var helperPaths = helperpaths.Current

var processMu sync.Mutex
var managed *exec.Cmd
var shuttingDown bool
var lastExit response

type request struct {
	Op                        string `json:"op"`
	PID                       int    `json:"pid,omitempty"`
	Config                    string `json:"config,omitempty"`
	DataDir                   string `json:"dataDir,omitempty"`
	EndpointTag               string `json:"endpointTag,omitempty"`
	DisableTailscaleUDPDirect bool   `json:"disableTailscaleUDPDirect,omitempty"`
}

type response struct {
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	ExitedPID       int    `json:"exitedPID,omitempty"`
	ExitCode        int    `json:"exitCode,omitempty"`
	Signal          string `json:"signal,omitempty"`
	OK              bool   `json:"ok"`
	PID             int    `json:"pid,omitempty"`
	Files           int    `json:"files,omitempty"`
	Bytes           int64  `json:"bytes,omitempty"`
	Error           string `json:"error,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runHelper(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runHelper(ctx context.Context) error {
	if err := cleanupOrphanedCores(ctx); err != nil {
		return err
	}
	defer os.Remove(helperPaths.Socket)
	go maintainSocketOwner()
	go maintainLogs(ctx)
	if err := serveSocket(ctx, helperPaths.Socket); err != nil {
		return err
	}
	return shutdownManagedCore()
}

// serveSocket accepts requests until ctx ends. Cleanup tools delete files
// under /var/run while the helper keeps running, so a removed or replaced
// socket is bound again and the app can still connect.
func serveSocket(ctx context.Context, path string) error {
	for {
		listener, err := bindSocket(path)
		if err != nil {
			return err
		}
		lost := make(chan struct{})
		go watchSocket(ctx, listener, path, lost)
		err = acceptConnections(listener)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-lost:
			fmt.Fprintln(os.Stderr, "helper socket was removed, listening again")
		default:
			return err
		}
	}
}

func bindSocket(path string) (*net.UnixListener, error) {
	_ = os.Remove(path)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// The path may already belong to a newer socket when this listener closes.
	listener.SetUnlinkOnClose(false)
	_ = os.Chmod(path, 0600)
	if uid, err := consoleUserID(); err == nil {
		_ = os.Chown(path, uid, -1)
	}
	return listener, nil
}

const socketCheckInterval = time.Second

// watchSocket closes the listener on shutdown, or when its socket file was
// deleted, signalling lost in the second case.
func watchSocket(ctx context.Context, listener net.Listener, path string, lost chan<- struct{}) {
	ticker := time.NewTicker(socketCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			listener.Close()
			return
		case <-ticker.C:
			// Rebind only when the file is gone. A path that names another socket
			// belongs to a newer helper (an upgrade in progress); taking it back
			// would make the two helpers fight over it.
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			close(lost)
			listener.Close()
			return
		}
	}
}

func acceptConnections(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go serve(conn)
	}
}

func maintainSocketOwner() {
	for {
		if uid, err := consoleUserID(); err == nil {
			_ = os.Chown(helperPaths.Socket, uid, -1)
			_ = os.Chmod(helperPaths.Socket, 0600)
		}
		time.Sleep(time.Second)
	}
}

func consoleUserID() (int, error) {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == 0 {
		return 0, errors.New("console user unavailable")
	}
	return int(stat.Uid), nil
}

func serve(conn net.Conn) {
	defer conn.Close()
	var req request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		writeResponse(conn, response{Error: err.Error()})
		return
	}
	var res response
	switch req.Op {
	case "start":
		res = start(req)
	case "stop":
		res = stop(req)
	case "force-stop":
		res = forceStop(req)
	case "reload":
		res = reload(req)
	case "status":
		res = status()
	case "taildrop-access":
		res = taildropAccess(req)
	case "clear-logs":
		res = clearLogs()
	default:
		res.Error = "unknown operation"
	}
	res.OK = res.Error == ""
	writeResponse(conn, res)
}

func status() response {
	processMu.Lock()
	defer processMu.Unlock()
	res := lastExit
	res.ProtocolVersion = helperpaths.ProtocolVersion
	if managed != nil && managed.Process != nil {
		res.PID = managed.Process.Pid
	}
	return res
}

func writeResponse(conn net.Conn, value response) { _ = json.NewEncoder(conn).Encode(value) }

func start(req request) response {
	processMu.Lock()
	defer processMu.Unlock()
	if shuttingDown {
		return response{Error: "helper is shutting down"}
	}
	if managed != nil && managed.Process != nil {
		return response{Error: "kiko-core is already running"}
	}
	if !isExecutable(helperPaths.CoreExecutable) {
		return response{Error: "installed kiko-core is unavailable"}
	}
	if err := validatePaths(req.Config, req.DataDir); err != nil {
		return response{Error: err.Error()}
	}
	userID, groupID, err := dataDirOwner(req.DataDir)
	if err != nil {
		return response{Error: err.Error()}
	}
	logFile, err := os.OpenFile(helperPaths.CoreLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return response{Error: err.Error()}
	}
	cmd := exec.Command(helperPaths.CoreExecutable, "run", "-c", req.Config)
	cmd.Env = tailscaleUDPEnvironment(os.Environ(), req.DisableTailscaleUDPDirect)
	cmd.Env = append(cmd.Env, "SUDO_UID="+strconv.Itoa(userID), "SUDO_GID="+strconv.Itoa(groupID))
	cmd.Dir = req.DataDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return response{Error: err.Error()}
	}
	managed = cmd
	go waitManaged(cmd, logFile)
	return response{PID: cmd.Process.Pid}
}

func waitManaged(cmd *exec.Cmd, logFile *os.File) {
	waitErr := cmd.Wait()
	_ = logFile.Close()
	processMu.Lock()
	if managed == cmd {
		lastExit = response{ExitedPID: cmd.Process.Pid, ExitCode: cmd.ProcessState.ExitCode()}
		if waitErr != nil {
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				lastExit.Signal = status.Signal().String()
			}
		}
		managed = nil
	}
	processMu.Unlock()
}

func tailscaleUDPEnvironment(current []string, disabled bool) []string {
	result := make([]string, 0, len(current)+1)
	for _, entry := range current {
		if !strings.HasPrefix(entry, "TS_DEBUG_NEVER_DIRECT_UDP=") && !strings.HasPrefix(entry, "TS_DEBUG_ALWAYS_USE_DERP=") {
			result = append(result, entry)
		}
	}
	if disabled {
		result = append(result, "TS_DEBUG_ALWAYS_USE_DERP=true")
	}
	return result
}

func stop(req request) response {
	processMu.Lock()
	defer processMu.Unlock()
	if req.PID <= 1 || managed == nil || managed.Process == nil || managed.Process.Pid != req.PID {
		return response{Error: "pid is not managed by this helper"}
	}
	if err := syscall.Kill(-req.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return response{Error: err.Error()}
	}
	return response{}
}

func forceStop(req request) response {
	processMu.Lock()
	defer processMu.Unlock()
	if req.PID <= 1 || managed == nil || managed.Process == nil || managed.Process.Pid != req.PID {
		return response{Error: "pid is not managed by this helper"}
	}
	if err := syscall.Kill(-req.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return response{Error: err.Error()}
	}
	return response{}
}

func reload(req request) response {
	processMu.Lock()
	defer processMu.Unlock()
	if req.PID <= 1 || managed == nil || managed.Process == nil || managed.Process.Pid != req.PID {
		return response{Error: "pid is not managed by this helper"}
	}
	// Signal only the managed core; its children may use different HUP semantics.
	if err := managed.Process.Signal(syscall.SIGHUP); err != nil {
		return response{Error: err.Error()}
	}
	return response{PID: req.PID}
}

func dataDirOwner(dataDir string) (int, int, error) {
	info, err := os.Stat(dataDir)
	if err != nil {
		return 0, 0, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid == 0 {
		return 0, 0, errors.New("data directory must belong to a user")
	}
	return int(owner.Uid), int(owner.Gid), nil
}

func taildropAccess(req request) response {
	req.DataDir, _ = filepath.Abs(req.DataDir)
	if err := validatePaths(req.Config, req.DataDir); err != nil {
		return response{Error: err.Error()}
	}
	directory, err := taildropDirectoryInDataDir(req.Config, req.DataDir, req.EndpointTag)
	if err != nil {
		return response{Error: err.Error()}
	}
	dataDirectory, err := openDirectoryNoFollow(req.DataDir)
	if err != nil {
		return response{Error: err.Error()}
	}
	defer dataDirectory.Close()
	var owner unix.Stat_t
	if err = unix.Fstat(int(dataDirectory.Fd()), &owner); err != nil || owner.Uid == 0 {
		return response{Error: "data directory must belong to a user"}
	}
	consoleID, err := consoleUserID()
	if err != nil || uint32(consoleID) != owner.Uid {
		return response{Error: "data directory does not belong to the console user"}
	}
	relative, _ := filepath.Rel(req.DataDir, directory)
	taildropDirectory, err := openDirectoryBelow(dataDirectory, relative)
	if err != nil {
		return response{Error: err.Error()}
	}
	defer taildropDirectory.Close()
	err = chownRootOwnedTree(taildropDirectory, int(owner.Uid), int(owner.Gid))
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{}
}

// Resolve each component through directory descriptors before changing ownership as root.
func openDirectoryNoFollow(path string) (*os.File, error) {
	root, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openDirectoryBelow(root, strings.TrimPrefix(filepath.Clean(path), "/"))
}

func openDirectoryBelow(parent *os.File, relative string) (*os.File, error) {
	current := parent
	for _, name := range strings.Split(relative, string(os.PathSeparator)) {
		fd, err := unix.Openat(int(current.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if current != parent {
			_ = current.Close()
		}
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(fd), name)
	}
	return current, nil
}

func chownRootOwnedTree(directory *os.File, userID, groupID int) error {
	var info unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &info); err != nil {
		return err
	}
	if info.Uid == 0 {
		if err := unix.Fchown(int(directory.Fd()), userID, groupID); err != nil {
			return err
		}
	}
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ELOOP) {
			continue
		}
		if err != nil {
			return err
		}
		entry := os.NewFile(uintptr(fd), name)
		if err = unix.Fstat(fd, &info); err == nil {
			switch info.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				err = chownRootOwnedTree(entry, userID, groupID)
			case unix.S_IFREG:
				if info.Uid == 0 && info.Nlink == 1 {
					err = unix.Fchown(fd, userID, groupID)
				}
			}
		}
		_ = entry.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func taildropDirectoryInDataDir(configPath, dataDir, tag string) (string, error) {
	var config struct {
		Endpoints []struct {
			Type              string `json:"type"`
			Tag               string `json:"tag"`
			TaildropDirectory string `json:"taildrop_directory"`
		} `json:"endpoints"`
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(content, &config); err != nil {
		return "", err
	}
	for _, endpoint := range config.Endpoints {
		if tag == "" || endpoint.Type != "tailscale" || endpoint.Tag != tag {
			continue
		}
		directory := os.ExpandEnv(endpoint.TaildropDirectory)
		if directory == "" {
			directory = "Taildrop"
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(dataDir, directory)
		}
		directory, err = filepath.Abs(directory)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(dataDir, directory)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return "", errors.New("Taildrop directory must stay inside the data directory")
		}
		return directory, nil
	}
	return "", fmt.Errorf("Tailscale endpoint %q not found", tag)
}

func validatePaths(config, dataDir string) error {
	dataDir, _ = filepath.Abs(dataDir)
	config, _ = filepath.Abs(config)
	if !validDataPaths(config, dataDir) {
		return errors.New("helper paths must stay inside a user's data directory")
	}
	resolvedDataDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil || resolvedDataDir != dataDir {
		return errors.New("data directory must be a real directory")
	}
	if info, err := os.Stat(config); err != nil || info.IsDir() {
		return errors.New("runtime config is unavailable")
	}
	return nil
}

func validDataPaths(config, dataDir string) bool {
	parts := strings.Split(strings.TrimPrefix(dataDir, "/Users/"), string(os.PathSeparator))
	_, numericUsernameError := strconv.Atoi(parts[0])
	expectedSuffix := filepath.FromSlash(helperPaths.UserDataSuffix)
	if !strings.HasPrefix(dataDir, "/Users/") || len(parts) < 2 || parts[0] == "" || numericUsernameError == nil || strings.Join(parts[1:], string(os.PathSeparator)) != expectedSuffix || !strings.HasPrefix(config, dataDir+string(os.PathSeparator)) {
		return false
	}
	return true
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}
