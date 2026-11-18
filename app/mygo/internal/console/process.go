package console

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/supervisor"
)

// processNotice reports the server process while it is not running: a
// restart pending with its countdown and attempt, a restart in progress,
// or the crash loop that stopped the retries, each with the last exit and
// a "Restart now" action. It reports whether it showed, the connection notice
// then being redundant.
func (c *Console) processNotice(ctx *ui.Context) bool {
	server := c.Env.Server
	if !server.HasProcess {
		return false
	}
	status := server.Process
	tone, title := kit.WarningTone, ""
	switch status.State {
	case supervisor.Restarting:
		wait := time.Until(status.Next)
		title = fmt.Sprintf("Kiko 服务意外退出，%d 秒后自动重启（第 %d 次）", int(math.Ceil(max(wait, 0).Seconds())), status.Attempt)
		// The countdown ticks on the next whole second.
		ctx.After(max(wait%time.Second, 50*time.Millisecond))
	case supervisor.Starting:
		if status.Attempt == 0 && status.Last == nil {
			return false // the first start, before the window shows content
		}
		tone, title = kit.AccentTone, "正在重启 Kiko 服务…"
		if status.Attempt > 0 {
			title = fmt.Sprintf("正在重启 Kiko 服务（第 %d 次）…", status.Attempt)
		}
	case supervisor.CrashLoop:
		tone = kit.DangerTone
		title = fmt.Sprintf("Kiko 服务在短时间内 %d 次异常退出，已停止自动重启", status.Failures)
	default:
		return false
	}
	ui.Column(ctx).Padding(6, 12, 4).Gap(6).Shrink(0).Role(ui.RoleStatus).Children(func() {
		kit.Notice(ctx, tone, title, exitSummary(status.Last), func() {
			ui.Row(ctx).Gap(6).Shrink(0).Children(func() {
				if status.Last != nil && status.Last.Stderr != "" {
					label := "错误输出"
					if c.showStderr {
						label = "收起输出"
					}
					kit.Button(ctx, label, kit.ButtonOptions{Small: true}).OnClick(func() { c.showStderr = !c.showStderr })
				}
				if restarter, ok := c.Env.Shell.(env.ServerRestarter); ok && status.State != supervisor.Starting {
					kit.Button(ctx, "立即重启", kit.ButtonOptions{Small: true, Variant: kit.Primary}).OnClick(restarter.RestartServer)
				}
			})
		})
		if c.showStderr && status.Last != nil && status.Last.Stderr != "" {
			kit.CodeBlock(ctx, status.Last.Stderr).MaxHeight(180)
		}
	})
	return true
}

// exitSummary is the last exit in a line: its code or signal, how long the
// process had run and the error.
func exitSummary(exit *supervisor.Exit) string {
	if exit == nil {
		return ""
	}
	var reason string
	switch {
	case exit.Signal != "":
		reason = "被信号 " + exit.Signal + " 终止"
	case exit.Code >= 0:
		reason = fmt.Sprintf("退出码 %d", exit.Code)
	default:
		reason = "启动失败"
	}
	if exit.Uptime > 0 {
		reason += "，运行了 " + kit.Uptime(exit.Uptime.Seconds())
	}
	if exit.Err != "" && exit.Code < 0 {
		reason += "：" + exit.Err
	}
	return "上次退出：" + reason
}
