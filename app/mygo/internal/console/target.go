package console

import (
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// lockNotice shows another device holding the server's write gate —
// "X is <operation>... (N s)" — and a save waiting for it.
func (c *Console) lockNotice(ctx *ui.Context) bool {
	server := c.Env.Server
	waiting := c.Store.LockWait
	if !server.LockedByOther() && waiting == "" {
		return false
	}
	title := waiting
	if server.LockedByOther() {
		lock := server.Lock
		holder := lock.Holder.Name
		if holder == "" {
			holder = "其他设备"
		}
		op := lock.Op
		if op == "" {
			op = "操作"
		}
		title = fmt.Sprintf("「%s」正在%s…", holder, op)
		if since, err := time.Parse(time.RFC3339, lock.Since); err == nil {
			title += fmt.Sprintf("（%d 秒）", int(max(time.Since(since), 0).Seconds()))
			ctx.After(time.Second - time.Since(since)%time.Second)
		}
	}
	message := ""
	if server.LockedByOther() {
		message = waiting
		if server.Lock.Queued > 0 {
			if message != "" {
				message += "；"
			}
			message += fmt.Sprintf("另有 %d 个操作排队", server.Lock.Queued)
		}
	}
	ui.Box(ctx).Padding(6, 12, 4).Shrink(0).Role(ui.RoleStatus).Children(func() {
		kit.Notice(ctx, kit.AccentTone, title, message, nil)
	})
	return true
}

// fatalNotice reports a remote server this device can no longer reach
// without pairing again: its certificate changed, or its token was
// revoked. The event stream stopped retrying.
func (c *Console) fatalNotice(ctx *ui.Context) bool {
	failure := c.Env.Server.Failure
	if !api.Fatal(failure) {
		return false
	}
	tone, title, message := kit.WarningTone, "此设备已被吊销或令牌失效", "需要在服务器上生成新的配对码，然后重新配对。"
	var fp *api.FingerprintError
	if errors.As(failure, &fp) {
		tone, title = kit.DangerTone, "服务器证书已变化"
		message = "为安全起见已停止连接（当前指纹 " + api.ShortFingerprint(fp.Got) + "，配对时为 " + api.ShortFingerprint(fp.Want) +
			"）。如果服务器重新生成了证书，请核对后重新配对。"
	}
	ui.Box(ctx).Padding(6, 12, 4).Shrink(0).Role(ui.RoleStatus).Children(func() {
		kit.Notice(ctx, tone, title, message, func() {
			_, ok := c.Env.Directory()
			if c.Env.Target.ID != "" && ok {
				kit.Button(ctx, "重新配对", kit.ButtonOptions{Small: true, Variant: kit.Primary}).OnClick(func() {
					c.Navigate(env.PageSettings, "servers:repair:"+c.Env.Target.ID)
				})
			}
		})
	})
	return true
}
