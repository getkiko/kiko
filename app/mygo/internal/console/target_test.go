package console

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// remoteConsole is the console of a paired remote fake.
func remoteConsole(t *testing.T, token string, pin func(*testkit.Server) []byte) (*Console, *ui.Tester, *testkit.Server, *testkit.DirectoryShell) {
	t.Helper()
	srv := testkit.NewTLSServer(t, "tok")
	e, shell := testkit.NewRemoteEnv(srv, token, pin(srv))
	c := New(e)
	t.Cleanup(e.Server.Stop)
	tt := ui.NewTester(c.View, 1320, 860)
	return c, tt, srv, shell
}

func rightPin(s *testkit.Server) []byte { return s.Fingerprint() }

func TestRemoteConsoleShowsServer(t *testing.T) {
	c, tt, _, _ := remoteConsole(t, "tok", rightPin)
	testkit.Until(t, tt, func() bool { return c.Store.Loaded && c.Env.Server.Connected && c.Env.Server.Info != nil })
	if !tt.HasText("NAS") || !tt.HasText(strings.TrimPrefix(c.Env.Target.URL, "https://")) {
		t.Errorf("sidebar lacks the server: %q", tt.Texts())
	}
	// The overview's state card names the server, its address and state.
	host := strings.TrimPrefix(c.Env.Target.URL, "https://")
	if _, ok := tt.Find("NAS " + host + " · 已连接"); !ok {
		t.Errorf("no connected badge: %q", tt.Texts())
	}
	if c.Env.Server.Self() != "dev-self" {
		t.Errorf("self %q", c.Env.Server.Self())
	}
	saveShot(t, tt, "console-remote-badge.png")
	tt.SetDark(true)
	tt.Frame()
	saveShot(t, tt, "console-remote-badge-dark.png")
}

func TestLockNotice(t *testing.T) {
	c, tt, srv := newConsole(t)
	since := time.Now().Add(-3 * time.Second).UTC().Format(time.RFC3339)
	// This console's own operation shows nothing.
	srv.SetLock(api.LockState{Held: true, Holder: api.LockHolder{ID: "local", Name: "本机"}, Op: "保存配置", Since: since})
	testkit.Until(t, tt, func() bool { return c.Env.Server.Lock.Held })
	if tt.HasText("「本机」正在保存配置…") {
		t.Fatal("own lock shown")
	}
	srv.SetLock(api.LockState{Held: true, Holder: api.LockHolder{ID: "dev-2", Name: "NAS 面板"}, Op: "应用配置", Since: since, Queued: 1})
	testkit.Until(t, tt, func() bool {
		for _, text := range tt.Texts() {
			if strings.HasPrefix(text, "「NAS 面板」正在应用配置…（") {
				return true
			}
		}
		return false
	})
	if !tt.HasText("另有 1 个操作排队") {
		t.Errorf("queue not shown: %q", tt.Texts())
	}
	saveShot(t, tt, "console-lock.png")
	srv.SetLock(api.LockState{})
	testkit.Until(t, tt, func() bool { return !c.Env.Server.Lock.Held })
	for _, text := range tt.Texts() {
		if strings.Contains(text, "正在应用配置") {
			t.Fatalf("notice kept after release: %q", text)
		}
	}
}

func TestFingerprintMismatchStopsAndAsksToPair(t *testing.T) {
	c, tt, _, _ := remoteConsole(t, "tok", func(*testkit.Server) []byte { return make([]byte, 32) })
	testkit.UntilText(t, tt, "服务器证书已变化")
	if !c.Env.Server.NeedsPairing() {
		t.Fatal("not fatal")
	}
	saveShot(t, tt, "console-fingerprint.png")
	if err := tt.Click("重新配对"); err != nil {
		t.Fatal(err)
	}
	if c.Page() != env.PageSettings {
		t.Errorf("page %q", c.Page())
	}
}

func TestRevokedTokenAsksToPair(t *testing.T) {
	c, tt, _, _ := remoteConsole(t, "revoked", rightPin)
	testkit.UntilText(t, tt, "此设备已被吊销或令牌失效")
	// The notice replaces the overview and leads to pairing again.
	if !c.Env.Server.NeedsPairing() {
		t.Fatalf("not fatal: %q", tt.Texts())
	}
	if err := tt.Click("重新配对"); err != nil {
		t.Fatal(err)
	}
	if c.Page() != env.PageSettings {
		t.Errorf("page %q", c.Page())
	}
}

// saveShot writes the frame where CONSOLE_PREVIEW_DIR says.
func saveShot(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("CONSOLE_PREVIEW_DIR")
	if dir == "" {
		return
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
