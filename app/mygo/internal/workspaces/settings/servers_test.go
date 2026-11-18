package settings

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// withDirectory gives the fixture's console a server directory holding
// the local server and a paired NAS.
func withDirectory(f *fixture) *testkit.DirectoryShell {
	shell := &testkit.DirectoryShell{Shell: &testkit.Shell{}, Directory: testkit.NewDirectory()}
	shell.Directory.Add("nas", "NAS", "https://192.168.1.2:9443", api.FormatFingerprint(api.Fingerprint([]byte("nas"))))
	f.env.Shell = shell
	f.env.Target.ID = servers.LocalID
	return shell
}

func (f *fixture) untilCall(shell *testkit.DirectoryShell, call string) {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return slices.Contains(shell.Calls(), call) })
}

func TestServersPage(t *testing.T) {
	f := newFixture(t, nil)
	shell := withDirectory(f)
	testkit.UntilText(t, f.tt, "本机和 1 台远程服务器，每台在独立窗口中打开")
	f.click("本机和 1 台远程服务器，每台在独立窗口中打开")
	testkit.UntilText(t, f.tt, "服务器列表")
	for _, text := range []string{"本机", "NAS", "当前窗口", "启动时恢复上次打开的窗口"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q: %q", text, f.tt.Texts())
		}
	}
	detail := serverDetail(shell.Servers()[1], time.Now())
	if !strings.Contains(detail, "192.168.1.2:9443 · 指纹 ") || !strings.Contains(detail, "最近连接 5 分钟前") {
		t.Errorf("detail %q", detail)
	}
	f.save("settings-servers.png")

	f.click("打开 NAS")
	f.untilCall(shell, "open nas")

	// Rename through the menu.
	f.click("NAS 操作")
	f.click("重命名")
	testkit.UntilText(t, f.tt, "重命名服务器")
	f.w.servers.rename.name = "书房 NAS"
	f.click("保存")
	f.untilCall(shell, "rename nas 书房 NAS")
	testkit.UntilText(t, f.tt, "书房 NAS")

	// Remove asks first, anchored to the menu button.
	f.click("书房 NAS 操作")
	f.click("移除")
	testkit.UntilText(t, f.tt, "移除「书房 NAS」？")
	f.click("移除")
	f.untilCall(shell, "remove nas")
	testkit.Until(t, f.tt, func() bool { return len(shell.Servers()) == 1 })

	f.clickRowControl("启动时恢复上次打开的窗口", 16)
	testkit.Until(t, f.tt, func() bool { return shell.RestoreWindows() })
}

func TestAddServerDialog(t *testing.T) {
	f := newFixture(t, nil)
	shell := withDirectory(f)
	f.w.Open("servers:add")
	testkit.UntilText(t, f.tt, "添加服务器")
	fp := api.FormatFingerprint(api.Fingerprint([]byte("x")))
	f.w.servers.pair.uri = "kiko-pair://10.0.0.5:9443?fp=" + fp + "&code=ABCDEFGH&name=office"
	f.save("settings-add-server.png")
	f.click("配对")
	testkit.Until(t, f.tt, func() bool {
		return slices.ContainsFunc(shell.Calls(), func(c string) bool { return strings.HasPrefix(c, "add ") })
	})
	f.untilCall(shell, "open srv2")
	if f.w.servers.pair.shown {
		t.Error("dialog kept open")
	}

	// By hand: the certificate's fingerprint is shown before pairing.
	f.w.Open("servers:add")
	f.tt.Frame()
	f.click("手动填写")
	f.w.servers.pair.addr, f.w.servers.pair.code = "nas.lan", "WXYZ-1234"
	f.click("配对")
	testkit.UntilText(t, f.tt, "请核对服务器证书指纹")
	if !f.tt.HasText(api.GroupFingerprint(shell.Pin)) {
		t.Errorf("fingerprint not shown: %q", f.tt.Texts())
	}
	f.save("settings-add-server-fingerprint.png")
	f.click("指纹一致，配对")
	f.untilCall(shell, "manual nas.lan WXYZ-1234")
}

func TestRepairDialog(t *testing.T) {
	f := newFixture(t, nil)
	shell := withDirectory(f)
	f.w.Open("servers:repair:nas")
	testkit.UntilText(t, f.tt, "重新配对「NAS」")
	f.click("手动填写")
	f.w.servers.pair.code = "ABCD-EFGH"
	f.click("配对")
	// The certificate differs from the pinned one: compare first.
	testkit.UntilText(t, f.tt, "服务器证书已变化")
	f.click("指纹一致，配对")
	f.untilCall(shell, "repair nas ABCD-EFGH")
}

func TestRemoteAccessPage(t *testing.T) {
	f := newFixture(t, nil)
	withDirectory(f)
	testkit.UntilText(t, f.tt, "已关闭 · 仅本机可以访问")
	f.click("远程访问")
	testkit.UntilText(t, f.tt, "允许远程访问")
	if f.tt.HasText("配对新设备") {
		t.Error("pairing offered while off")
	}
	if !f.tt.HasText("MacBook") || !f.tt.HasText("吊销") {
		t.Errorf("devices missing: %q", f.tt.Texts())
	}
	f.clickRowControl("允许远程访问", 16)
	testkit.Until(t, f.tt, func() bool { return f.srv.Remote().Enabled })
	testkit.UntilText(t, f.tt, "运行中 · :9443")
	f.click("生成配对码")
	testkit.UntilText(t, f.tt, "ABCD-EFGH")
	// The code expires five minutes after the fake server issued it; the
	// countdown reads the wall clock, so allow a loaded machine a few seconds.
	if !slices.ContainsFunc(f.tt.Texts(), func(text string) bool {
		return text == "剩余 5:00" || strings.HasPrefix(text, "剩余 4:5")
	}) {
		t.Errorf("countdown: %q", f.tt.Texts())
	}
	pin, _ := api.ParseFingerprint(f.srv.Remote().Fingerprint)
	if !f.tt.HasText(api.GroupFingerprint(pin)) {
		t.Errorf("fingerprint missing: %q", f.tt.Texts())
	}
	f.save("settings-remote.png")
	f.click("复制配对字符串")

	f.click("吊销 MacBook")
	testkit.UntilText(t, f.tt, "吊销「MacBook」？")
	f.save("settings-remote-revoke.png")
	f.click("吊销设备")
	testkit.Until(t, f.tt, func() bool { return len(f.srv.Devices()) == 0 })
	testkit.UntilText(t, f.tt, "尚无已配对设备")
}

// A remote server's settings hide what belongs to this computer.
func TestRemoteHidesLocalFeatures(t *testing.T) {
	f := newFixture(t, nil)
	f.env.Target.Remote, f.env.Target.Name = true, "NAS"
	f.tt.Frame()
	if f.tt.HasText("打开日志目录") || f.tt.HasText("TUN 特权助手") {
		t.Errorf("local features shown: %q", f.tt.Texts())
	}
	if !f.tt.HasText("清理历史日志") {
		t.Errorf("logs group gone: %q", f.tt.Texts())
	}
}
