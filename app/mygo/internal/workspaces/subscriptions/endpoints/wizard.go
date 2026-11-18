package endpoints

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

var wizardSteps = []string{"选择角色", "选择连接途径", "确认并创建"}

// wizard is the USB/IP connection wizard: role, path,
// then a preview that creates the prefilled service.
type wizard struct {
	s      *Section
	isOpen bool
	step   int
	role   string
	path   string
}

func (w *wizard) open() {
	w.isOpen, w.step, w.role, w.path = true, 0, "", ""
}

func (w *wizard) view(c *ui.Context, doc *model.Doc) {
	if !w.isOpen {
		return
	}
	k := kit.T(c)
	platform := w.s.platform(doc)
	var plan *usbIPPlan
	if w.role != "" && w.path != "" {
		p := planUsbIP(doc, w.role, w.path)
		plan = &p
	}
	serverBlocked := !schema.ServiceAvailable("usbip-server", platform)
	canNext := w.step == 0 && w.role != "" || w.step == 1 && w.path != "" || w.step > 1
	kit.Dialog(c, &w.isOpen, "USB/IP 连接向导", 600, func() {
		stepper(c, wizardSteps, w.step)
		switch {
		case w.step == 0:
			ui.Text(c, "USB/IP 分成两端：Server 在插着 USB 设备的机器上，把设备共享到网络；Client 在要使用设备的机器上，把远程设备挂载成本机 USB 设备。两端是两台不同的机器，各自用 Kiko 配置。").
				FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
			ui.Column(c).Gap(8).Role(ui.RoleRadioGroup).Label("角色").Children(func() {
				reason := ""
				if serverBlocked {
					reason = schema.PlatformLabel[platform] + "：" + schema.ServiceUnavailableReason("usbip-server", platform)
				}
				w.roleCard(c, "server", "共享本机的 USB 设备", "创建 USB/IP Server，让别的机器使用这台机器上的设备。", "在插着 USB 设备的机器上做", kit.SymUpload, serverBlocked, reason)
				w.roleCard(c, "client", "使用远程的 USB 设备", "创建 USB/IP Client，连接到另一台机器上的 Server，把它的设备挂载到本机。", "在需要用设备的机器上做", kit.SymDownload, false, "")
			})
		case w.step == 1 && w.role != "":
			question := "这台机器通过哪种网络连到 Server？"
			if w.role == "server" {
				question = "Client 通过哪种网络连到这台机器？"
			}
			ui.Text(c, question+"没有公网 IP 时选 Tailscale。").FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
			ui.Column(c).Gap(8).Role(ui.RoleRadioGroup).Label("连接途径").Children(func() {
				for _, info := range usbIPPaths {
					pathCard(c, info, w.path == info.ID, w.role, func() { w.path = info.ID })
				}
			})
		case w.step == 2 && plan != nil:
			w.preview(c, *plan)
		}
	}, func() {
		if w.step > 0 {
			kit.Button(c, "上一步", kit.ButtonOptions{}).OnClick(func() { w.step-- })
		}
		ui.Spacer(c)
		if w.step < 2 {
			kit.Button(c, "下一步", kit.ButtonOptions{Variant: kit.Primary, Disabled: !canNext}).OnClick(func() { w.step++ })
			return
		}
		kit.Button(c, "创建并继续编辑", kit.ButtonOptions{Variant: kit.Primary, Disabled: plan == nil}).OnClick(func() {
			if plan != nil {
				w.isOpen = false
				w.s.editor.createFromPlan(*plan)
			}
		})
	})
}

func (w *wizard) roleCard(c *ui.Context, role, title, subtitle, where string, icon kit.Symbol, disabled bool, reason string) {
	k := kit.T(c)
	if choiceCard(c, role, w.role == role, disabled, title, func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			ui.Box(c).Size(40, 40).Radius(12).Center().Background(k.Fill).TextColor(k.Label2).Shrink(0).Children(func() { kit.Icon(c, icon, 20) })
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, title).FontSize(16).FontWeight(600)
				ui.Text(c, subtitle).FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
				ui.Text(c, where).FontSize(kit.FontTable).TextColor(k.Label3)
				if reason != "" {
					ui.Text(c, reason).FontSize(kit.FontTable).TextColor(k.Warning).Wrap()
				}
			})
		})
	}).Clicked() && !disabled {
		w.role = role
	}
}

func (w *wizard) preview(c *ui.Context, plan usbIPPlan) {
	k := kit.T(c)
	info := pathInfo(w.path)
	var shown, blockers []guideItem
	for _, item := range sortItems(analyzeService(plan.Doc, plan.Service, w.s.rt.guideContext())) {
		// Addresses and devices are filled in the editor after creating.
		if item.Level == levelOK || item.ID == "server" || item.ID == "devices" {
			continue
		}
		shown = append(shown, item)
		if item.Level == levelError {
			blockers = append(blockers, item)
		}
	}
	f := plan.Service.Fields
	ui.Column(c).Gap(8).Shrink(0).Children(func() {
		subHeading(c, "将要创建", "", nil)
		ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Padding(12).Gap(4).Shrink(0).FontSize(14).Children(func() {
			detail := fmt.Sprintf("，端口 %s", jsv.StringOr(f["server_port"], "3240"))
			if jsv.Truthy(f["detour"]) {
				detail += "，通过 Tailscale 端点连接"
			}
			role := "Client"
			if w.role == "server" {
				role = "Server"
				detail = "，监听 " + jsv.StringOr(f["listen"], "") + ":" + jsv.StringOr(f["listen_port"], strconv.Itoa(3240))
			}
			ui.Row(c).Wrap().Children(func() {
				ui.Text(c, "USB/IP "+role+"「"+plan.Service.Tag+"」")
				ui.Text(c, detail).TextColor(k.Label3)
			})
			if plan.Endpoint != nil {
				ui.Row(c).Wrap().Children(func() {
					ui.Text(c, "Tailscale 端点")
					ui.Text(c, "（新建）").TextColor(k.Label3)
				})
			} else if w.path == pathTailscale {
				ui.Text(c, "复用已有的 Tailscale 端点").TextColor(k.Label3)
			}
			ui.Text(c, "连接方式："+info.Title).TextColor(k.Label3)
		})
	})
	ui.Column(c).Gap(8).Shrink(0).Children(func() {
		subHeading(c, "检查", "", nil)
		ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).PaddingX(12).Shrink(0).Children(func() {
			for _, item := range shown {
				checkRow(c, item, nil)
			}
		})
		message := "创建后在编辑页的「服务器地址」里填 Server 的地址。"
		if w.role == "server" {
			message = "创建后在编辑页的「设备匹配」里选择要共享的设备。"
		}
		note(c, kit.Neutral, message)
		if len(blockers) > 0 {
			note(c, kit.WarningTone, "上面标为「需要处理」的项目不会阻止创建，创建后可以在编辑页里继续调整。")
		}
	})
	ui.Column(c).Gap(8).Shrink(0).Children(func() {
		subHeading(c, "接下来", "", nil)
		ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Padding(12).Gap(4).Shrink(0).Children(func() {
			for i, line := range nextSteps(w.role, w.path) {
				ui.Text(c, strconv.Itoa(i+1)+". "+line).FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
			}
		})
		ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Padding(12).Gap(4).Shrink(0).Children(func() {
			peer, steps := "对端（Server 那台机器）需要：", info.Server
			if w.role == "server" {
				peer, steps = "对端（Client 那台机器）需要：", info.Client
			}
			ui.Text(c, peer).FontSize(kit.FontTable).TextColor(k.Label3)
			bulletList(c, steps, kit.Neutral)
		})
	})
}
