package main

// A browser that goes through one outbound: the core serves an HTTP proxy
// on a loopback port whose connections all take that outbound
// (StartOutboundProxy), and a browser window uses it, the built-in one
// (kiko-browser, a WKWebView window on macOS 14 and later) or Chrome with
// a profile of its own. Each outbound and engine has one session, which
// holds the proxy while its browser runs; opened again, the browser shows
// the page in its window.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Browser engines.
const (
	browserBuiltIn = "webview"
	browserChrome  = "chrome"
)

// browserHome is the page a browser opens without one asked for: where it
// shows the address and the place the outbound reaches the web from.
const browserHome = "https://ipinfo.io/"

// browserReadyTimeout bounds the wait for the core's proxy and for the
// built-in browser's window.
const browserReadyTimeout = 15 * time.Second

// browserSession is a browser of an outbound and the proxy it uses.
type browserSession struct {
	outbound, engine string
	// stop ends the proxy; done closes when the browser has exited.
	stop context.CancelFunc
	done chan struct{}
	cmd  *exec.Cmd
	// input takes the built-in browser's commands, a JSON line each.
	input io.WriteCloser
	// executable and args start Chrome again for another window.
	executable string
	args       func(target string) []string
}

func (b *browserSession) running() bool {
	select {
	case <-b.done:
		return false
	default:
		return true
	}
}

// browsers are the server's browser sessions.
type browsers struct {
	mu       sync.Mutex
	sessions map[string]*browserSession
	closing  bool
}

// browserOpenRequest is POST /api/v1/browser/open.
type browserOpenRequest struct {
	// Outbound is the outbound's tag in the running core.
	Outbound string `json:"outbound"`
	// Engine is "webview", the built-in browser, or "chrome".
	Engine string `json:"engine"`
	// URL is the page to open, the home page when empty.
	URL string `json:"url,omitempty"`
}

// browserOpenResult names the browser that shows the page.
type browserOpenResult struct {
	Browser string `json:"browser"`
	URL     string `json:"url"`
}

func (s *server) handleBrowserOpen(w http.ResponseWriter, r *http.Request) {
	if requestClient(r).Remote {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能在本机打开浏览器"})
		return
	}
	var in browserOpenRequest
	if !decode(w, r, &in) {
		return
	}
	result, err := s.openBrowser(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// browserTarget is the page to open: an http or https URL, https when the
// scheme is left out, the home page when empty.
func browserTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return browserHome, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("请输入 HTTP 或 HTTPS 网页地址")
	}
	return u.String(), nil
}

func browserName(engine string) string {
	if engine == browserChrome {
		return "Chrome"
	}
	return "内置浏览器"
}

// openBrowser shows the page in the outbound's browser of the engine,
// starting it with the core's proxy when it does not run.
func (s *server) openBrowser(ctx context.Context, in browserOpenRequest) (browserOpenResult, error) {
	outbound := strings.TrimSpace(in.Outbound)
	if outbound == "" {
		return browserOpenResult{}, errors.New("请选择出站")
	}
	engine := in.Engine
	if engine == "" {
		engine = browserBuiltIn
	}
	if engine != browserBuiltIn && engine != browserChrome {
		return browserOpenResult{}, errors.New("未知的浏览器类型")
	}
	target, err := browserTarget(in.URL)
	if err != nil {
		return browserOpenResult{}, err
	}
	result := browserOpenResult{Browser: browserName(engine), URL: target}
	key := engine + "\x00" + outbound

	s.browsers.mu.Lock()
	defer s.browsers.mu.Unlock()
	if s.browsers.closing {
		return browserOpenResult{}, errors.New("应用正在退出")
	}
	if session := s.browsers.sessions[key]; session != nil && session.running() {
		return result, session.show(target)
	}
	var executable string
	if engine == browserBuiltIn {
		executable, err = findBuiltInBrowser(s.singBox)
	} else {
		executable, err = findChrome()
	}
	if err != nil {
		return browserOpenResult{}, err
	}
	proxyCtx, stop := context.WithCancel(context.Background())
	address, err := s.startOutboundProxy(proxyCtx, ctx, outbound)
	if err != nil {
		stop()
		return browserOpenResult{}, err
	}
	session := &browserSession{outbound: outbound, engine: engine, stop: stop, done: make(chan struct{}), executable: executable}
	if engine == browserBuiltIn {
		session.cmd, session.input, err = startBuiltInBrowser(executable, "http://"+address, target, outbound)
	} else {
		profile := filepath.Join(s.dataDir, "browser", "chrome-"+browserProfileID(outbound))
		session.args = func(target string) []string { return chromeArgs(profile, address, target) }
		session.cmd = exec.Command(executable, session.args(target)...)
		err = session.cmd.Start()
		if err != nil {
			err = fmt.Errorf("启动 Chrome 失败：%w", err)
		}
	}
	if err != nil {
		stop()
		return browserOpenResult{}, err
	}
	if s.browsers.sessions == nil {
		s.browsers.sessions = map[string]*browserSession{}
	}
	s.browsers.sessions[key] = session
	go func() {
		_ = session.cmd.Wait()
		stop()
		close(session.done)
		s.browsers.mu.Lock()
		if s.browsers.sessions[key] == session {
			delete(s.browsers.sessions, key)
		}
		s.browsers.mu.Unlock()
	}()
	return result, nil
}

// show opens target in the session's browser: the built-in window loads
// it, Chrome opens another window of its instance.
func (b *browserSession) show(target string) error {
	if b.input != nil {
		if err := json.NewEncoder(b.input).Encode(map[string]string{"url": target}); err != nil {
			return errors.New("内置浏览器已关闭，请重新打开")
		}
		return nil
	}
	// Chrome hands the window to the instance of the profile and exits.
	command := exec.Command(b.executable, b.args(target)...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("打开 Chrome 窗口失败：%w", err)
	}
	go func() { _ = command.Wait() }()
	return nil
}

// closeBrowsers ends every browser and its proxy, as the server exits.
func (s *server) closeBrowsers() {
	s.browsers.mu.Lock()
	s.browsers.closing = true
	sessions := s.browsers.sessions
	s.browsers.sessions = nil
	s.browsers.mu.Unlock()
	for _, session := range sessions {
		if session.input != nil {
			// The built-in browser ends at the end of its input.
			_ = session.input.Close()
		} else if session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
		session.stop()
	}
	for _, session := range sessions {
		select {
		case <-session.done:
		case <-time.After(2 * time.Second):
			if session.cmd.Process != nil {
				_ = session.cmd.Process.Kill()
			}
		}
	}
}

// startOutboundProxy has the core serve a proxy through outbound, until
// ctx ends, and returns its address once it listens, waiting no longer
// than wait allows. When the core restarts the proxy follows it on the
// same port, so the browser keeps working.
func (s *server) startOutboundProxy(ctx, wait context.Context, outbound string) (string, error) {
	type result struct {
		address string
		err     error
	}
	first := make(chan result, 1)
	go func() {
		var port uint32
		reported := false
		report := func(r result) {
			if !reported {
				reported = true
				first <- r
			}
		}
		for ctx.Err() == nil {
			client := s.grpcClient()
			if client == nil {
				report(result{err: errors.New("核心未运行，请先启动")})
			} else if stream, err := client.StartOutboundProxy(ctx, &daemon.OutboundProxyRequest{Outbound: outbound, Port: port}); err != nil {
				report(result{err: outboundProxyError(err, outbound)})
			} else {
				for {
					event, err := stream.Recv()
					if err != nil {
						report(result{err: outboundProxyError(err, outbound)})
						break
					}
					if event.Address == "" {
						report(result{err: outboundProxyError(errors.New(event.Error), outbound)})
						continue
					}
					if _, p, err := net.SplitHostPort(event.Address); err == nil {
						if n, err := strconv.ParseUint(p, 10, 16); err == nil {
							port = uint32(n)
						}
					}
					report(result{address: event.Address})
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	timeout := time.NewTimer(browserReadyTimeout)
	defer timeout.Stop()
	select {
	case r := <-first:
		return r.address, r.err
	case <-timeout.C:
		return "", errors.New("等待核心代理超时")
	case <-wait.Done():
		return "", wait.Err()
	}
}

// outboundProxyError says why the core does not serve the outbound's
// proxy.
func outboundProxyError(err error, outbound string) error {
	message := status.Convert(err).Message()
	switch {
	case status.Code(err) == codes.Unimplemented:
		return errors.New("当前核心不支持按出站打开浏览器，请更新 kiko-core")
	case strings.HasPrefix(message, "outbound not found"):
		return fmt.Errorf("出站「%s」不在正在运行的配置中，请先应用配置", outbound)
	case message == "core is not running":
		return errors.New("核心未运行，请先启动")
	}
	return fmt.Errorf("核心代理不可用：%s", message)
}

// browserProfileID names an outbound's browser data: its own cookies and
// logins, kept from one session to the next.
func browserProfileID(outbound string) string {
	sum := sha256.Sum256([]byte("kiko-browser:" + outbound))
	return hex.EncodeToString(sum[:8])
}

// browserProfileUUID is browserProfileID as the UUID the built-in browser
// names its website data store by.
func browserProfileUUID(outbound string) string {
	sum := sha256.Sum256([]byte("kiko-browser:" + outbound))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// findBuiltInBrowser finds kiko-browser beside the core or the server.
func findBuiltInBrowser(core string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("内置浏览器目前只支持 macOS，请使用 Chrome 打开")
	}
	var dirs []string
	if core != "" {
		dirs = append(dirs, filepath.Dir(core))
	}
	if executable, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		dirs = append(dirs, filepath.Dir(executable))
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, "kiko-browser")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("缺少内置浏览器组件，请重新安装 Kiko")
}

// startBuiltInBrowser starts kiko-browser on proxy and waits for its
// window, or for the reason it cannot show one.
func startBuiltInBrowser(executable, proxy, target, outbound string) (*exec.Cmd, io.WriteCloser, error) {
	command := exec.Command(executable, proxy, target, browserProfileUUID(outbound), outbound)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, nil, err
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		return nil, nil, fmt.Errorf("启动内置浏览器失败：%w", err)
	}
	ready := make(chan string, 1)
	go func() {
		lines := bufio.NewReader(output)
		line, _ := lines.ReadString('\n')
		ready <- strings.TrimSpace(line)
		// Keep reading so that the browser never blocks on its output.
		_, _ = io.Copy(io.Discard, lines)
	}()
	failure := "内置浏览器初始化失败"
	select {
	case line := <-ready:
		if line == "READY" {
			return command, input, nil
		}
		if message := strings.TrimPrefix(line, "ERROR "); message != line && message != "" {
			failure = message
		}
	case <-time.After(browserReadyTimeout):
		failure = "内置浏览器初始化超时，请重试"
	}
	_ = input.Close()
	_ = command.Process.Kill()
	_ = command.Wait()
	return nil, nil, errors.New(failure)
}

// findChrome finds Google Chrome, or on Windows Microsoft Edge without it.
func findChrome() (string, error) {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
	case "windows":
		roots := []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")}
		for _, path := range []string{"Google/Chrome/Application/chrome.exe", "Microsoft/Edge/Application/msedge.exe"} {
			for _, root := range roots {
				if root != "" {
					candidates = append(candidates, filepath.Join(root, path))
				}
			}
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("没有找到 Google Chrome，请安装后再试")
}

// chromeArgs start Chrome on the profile through the proxy at address:
// nothing bypasses it, not even loopback, QUIC is off so that every
// connection takes the proxy, and WebRTC sends no UDP around it.
func chromeArgs(profile, address, target string) []string {
	return []string{
		"--user-data-dir=" + profile, "--proxy-server=http://" + address, "--proxy-bypass-list=<-loopback>",
		"--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--no-first-run", "--no-default-browser-check", "--new-window", target,
	}
}
