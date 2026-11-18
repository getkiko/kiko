package console

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/supervisor"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func newConsoleWithShell(t *testing.T) (*Console, *ui.Tester, *testkit.Server, *testkit.Shell) {
	t.Helper()
	srv := testkit.NewServer(t)
	e, shell := testkit.NewEnv(srv)
	c := New(e)
	t.Cleanup(e.Server.Stop)
	tt := ui.NewTester(c.View, 1320, 860)
	testkit.Until(t, tt, func() bool { return c.Store.Loaded && e.Server.Connected })
	return c, tt, srv, shell
}

func count(requests []string, path string) int {
	n := 0
	for _, r := range requests {
		if strings.Contains(r, path) {
			n++
		}
	}
	return n
}

// A pending restart shows its countdown, attempt and last exit; "Restart now"
// asks the shell.
func TestProcessNoticeWhileRestarting(t *testing.T) {
	c, tt, _, shell := newConsoleWithShell(t)
	c.Env.Server.SetProcess(supervisor.Status{State: supervisor.Running})
	tt.Frame()
	if tt.HasText("立即重启") {
		t.Fatal("notice shown while running")
	}
	c.Env.Server.SetProcess(supervisor.Status{
		State:   supervisor.Restarting,
		Attempt: 2,
		Next:    time.Now().Add(4 * time.Second),
		Last:    &supervisor.Exit{Code: 2, Uptime: 3 * time.Second, Stderr: "panic: listen tcp: bind"},
	})
	testkit.UntilText(t, tt, "Kiko 服务意外退出，4 秒后自动重启（第 2 次）")
	if !tt.HasText("上次退出：退出码 2，运行了 3秒") {
		t.Errorf("exit line missing: %q", tt.Texts())
	}
	if tt.HasText("服务端连接已中断，实时数据已过期，正在自动重试。") {
		t.Error("the connection notice doubles the process notice")
	}
	if err := tt.Click("错误输出"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, tt, "panic: listen tcp: bind")
	if err := tt.Click("立即重启"); err != nil {
		t.Fatal(err)
	}
	if shell.RestartCount() != 1 {
		t.Errorf("%d restarts", shell.RestartCount())
	}
}

// The crash loop says the retries stopped and keeps a restart action.
func TestProcessNoticeInCrashLoop(t *testing.T) {
	c, tt, _, _ := newConsoleWithShell(t)
	c.Env.Server.SetProcess(supervisor.Status{
		State: supervisor.CrashLoop, Failures: 5,
		Last: &supervisor.Exit{Code: -1, Signal: "killed"},
	})
	testkit.UntilText(t, tt, "Kiko 服务在短时间内 5 次异常退出，已停止自动重启")
	if !tt.HasText("上次退出：被信号 killed 终止") || !tt.HasText("立即重启") {
		t.Errorf("texts %q", tt.Texts())
	}
	// A manual restart keeps the last exit while it starts.
	c.Env.Server.SetProcess(supervisor.Status{State: supervisor.Starting, Last: &supervisor.Exit{Code: 1}})
	testkit.UntilText(t, tt, "正在重启 Kiko 服务…")
	if tt.HasText("立即重启") {
		t.Error("restart offered while starting")
	}
}

// When the process runs again, the event stream reconnects at once and
// the shown page restarts its streams instead of keeping stale ones.
func TestServerBackReconnectsAndReactivates(t *testing.T) {
	c, tt, srv, _ := newConsoleWithShell(t)
	c.Navigate(env.PageLogs, nil)
	testkit.Until(t, tt, func() bool { return count(srv.Requests(), "/logs/core") >= 1 })
	events, logs := count(srv.Requests(), "/events"), count(srv.Requests(), "/logs/core")

	c.Env.Server.SetProcess(supervisor.Status{State: supervisor.Running})
	c.Env.Server.SetProcess(supervisor.Status{State: supervisor.Restarting, Attempt: 1, Next: time.Now().Add(time.Second)})
	c.Env.Server.SetProcess(supervisor.Status{State: supervisor.Running, Attempt: 1})
	testkit.Until(t, tt, func() bool {
		r := srv.Requests()
		return count(r, "/events") > events && count(r, "/logs/core") > logs
	})
	tt.Frame()
	if tt.HasText("立即重启") {
		t.Error("notice still shown once running")
	}
}
