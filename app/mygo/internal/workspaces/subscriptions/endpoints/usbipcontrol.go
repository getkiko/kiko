package endpoints

import (
	"context"
	"fmt"
	"regexp"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// usbipSheet lists a dynamic USB/IP server's devices and runs the
// managed `kiko-core api usbip share`.
type usbipSheet struct {
	s     *Section
	tag   string
	local localDevices
	busy  bool
}

var reStatePrefix = regexp.MustCompile(`^.*STATE_`)

func sharedName(d api.USBSharedDevice) string {
	if d.Descriptor != nil && d.Descriptor.Product != "" {
		return d.Descriptor.Product
	}
	var vendor, product int
	if d.Descriptor != nil {
		vendor, product = d.Descriptor.VendorID, d.Descriptor.ProductID
	}
	return fmt.Sprintf("%04x:%04x", vendor, product)
}

func (u *usbipSheet) toggle(sharing bool) {
	u.busy = true
	client := u.s.env.API
	tag, selected := u.tag, append([]string(nil), u.local.selected...)
	env.Go(u.s.env, func(ctx context.Context) (api.USBIPShareStatus, error) {
		if sharing {
			return client.StopUSBIPShare(ctx)
		}
		return client.StartUSBIPShare(ctx, tag, selected)
	}, func(status api.USBIPShareStatus, err error) {
		u.busy = false
		if err != nil {
			message := err.Error()
			if message == "" {
				message = "操作失败"
			}
			u.s.env.Toast(message)
			return
		}
		u.s.rt.share = &status
	})
}

func (u *usbipSheet) view(c *ui.Context, tag string) {
	k := kit.T(c)
	if u.tag != tag {
		u.tag = tag
		u.local = localDevices{}
		u.local.load(u.s)
	}
	share := u.s.rt.share
	sharing := share != nil && share.Running && share.ServiceTag == tag
	var shared []api.USBSharedDevice
	if status := u.s.rt.usbip[tag]; status != nil {
		shared = status.Devices
	}
	open := true
	kit.Dialog(c, &open, "USB/IP · "+tag, 0, func() {
		note(c, kit.Neutral, "dynamic 模式的设备由 Kiko 拉起核心的 api usbip share 导出，需要 root（Linux）或管理员（Windows）权限，且核心需带 with_usbip 构建；macOS 不支持导出。")
		if share != nil && share.Error != "" {
			note(c, kit.DangerTone, share.Error)
		}
		ui.Column(c).Gap(6).Shrink(0).Children(func() {
			subHeading(c, "本机设备", fmt.Sprint(len(u.local.devices)), func() {
				kit.IconButton(c, kit.SymRefreshCw, "刷新", kit.IconButtonOptions{Size: 28, IconSize: 15, Disabled: u.local.loading}).
					OnClick(func() { u.local.load(u.s) })
			})
			switch {
			case u.local.err != "":
				note(c, kit.WarningTone, u.local.err)
			case len(u.local.devices) == 0:
				emptyBox(c, func() { ui.Text(c, "没有可导出的设备") })
			default:
				listCard(c, func() {
					for _, dev := range u.local.devices {
						detail := dev.BusID + " · " + hexID(dev.VendorID) + ":" + hexID(dev.ProductID)
						if dev.Serial != "" {
							detail += " · " + dev.Serial
						}
						deviceRowView(c, dev, detail, containsString(u.local.selected, dev.BusID), sharing, func(on bool) { u.local.toggle(dev.BusID, on) })
					}
				})
			}
			ui.Text(c, "不勾选则导出全部设备。").FontSize(kit.FontTable).TextColor(k.Label3).PaddingX(4)
		})
		label, variant := "开始共享", kit.Primary
		if sharing {
			label, variant = "停止共享", kit.Danger
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: variant, Disabled: u.busy}).FillWidth().OnClick(func() { u.toggle(sharing) })
		ui.Column(c).Gap(6).Shrink(0).Children(func() {
			subHeading(c, "已导出", fmt.Sprint(len(shared)), nil)
			if len(shared) == 0 {
				emptyBox(c, func() { ui.Text(c, "暂无已导出的设备") })
				return
			}
			listCard(c, func() {
				for _, d := range shared {
					state := reStatePrefix.ReplaceAllString(d.State, "")
					key := d.StableID
					if key == "" {
						key = d.BusID
					}
					ui.Row(c.Key(key)).MinHeight(52).PaddingX(12).Gap(12).Children(func() {
						statusDot(c, toneOf(state, ""))
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, sharedName(d)).FontSize(14).SingleLine().Ellipsis("…")
							ui.Text(c, d.BusID).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine()
						})
						ui.Text(c, state).FontSize(kit.FontTable).TextColor(k.Label3)
					})
				}
			})
		})
	}, nil)
	if !open {
		u.s.control = control{}
		u.tag = ""
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
