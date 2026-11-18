package endpoints

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// localDevices lists the server host's USB devices for a sheet.
type localDevices struct {
	devices  []api.USBLocalDevice
	selected []string
	err      string
	loading  bool
	run      int
}

func (l *localDevices) load(s *Section) {
	l.loading = true
	l.run++
	run := l.run
	env.Go(s.env, func(ctx context.Context) ([]api.USBLocalDevice, error) {
		return s.env.API.USBDevices(ctx)
	}, func(devices []api.USBLocalDevice, err error) {
		if run != l.run {
			return
		}
		l.loading = false
		if err != nil {
			l.err = err.Error()
			if l.err == "" {
				l.err = "无法枚举本机 USB 设备"
			}
			return
		}
		l.devices, l.err = devices, ""
	})
}

func (l *localDevices) toggle(busID string, on bool) {
	l.selected = slices.DeleteFunc(slices.Clone(l.selected), func(id string) bool { return id == busID })
	if on {
		l.selected = append(l.selected, busID)
	}
}

func deviceName(d api.USBLocalDevice) string {
	if d.Product != "" {
		return d.Product
	}
	return hexID(d.VendorID) + ":" + hexID(d.ProductID)
}

// devicesField edits a USB/IP service's devices:
// rows of bus id, vendor id, product id and serial, each row's
// conditions all matching, rows adding up.
type devicesField struct {
	pickerOpen bool
	picker     localDevices
}

var deviceKeys = []struct{ key, label, placeholder string }{
	{"bus_id", "Bus ID", "1-2"},
	{"vendor_id", "Vendor ID", "0-65535 或 0x1d6b"},
	{"product_id", "Product ID", "0-65535 或 0x0002"},
	{"serial", "序列号", ""},
}

func rowsOf(value any) []map[string]any {
	list, _ := value.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		item, _ := entry.(map[string]any)
		row := map[string]any{}
		for _, k := range deviceKeys {
			row[k.key] = jsv.StringOr(item[k.key], "")
		}
		out = append(out, row)
	}
	return out
}

func rowOf(r deviceRow) map[string]any {
	return map[string]any{"bus_id": r.BusID, "vendor_id": r.VendorID, "product_id": r.ProductID, "serial": r.Serial}
}

func sameRow(a, b map[string]any) bool {
	for _, k := range deviceKeys {
		if strings.TrimSpace(jsv.String(a[k.key])) != strings.TrimSpace(jsv.String(b[k.key])) {
			return false
		}
	}
	return true
}

// invalidID reports an id that is not an integer in 0–65535.
func invalidID(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	n, err := strconv.ParseFloat(trimmed, 64)
	return err != nil || n != float64(int(n)) || n < 0 || n > 65535
}

func toAny(rows []map[string]any) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

func (d *devicesField) view(c *ui.Context, s *Section, service *model.Service) {
	k := kit.T(c)
	required := service.Type == "usbip-server"
	rows := rowsOf(service.Fields["devices"])
	set := func(next []map[string]any) { service.Fields["devices"] = toAny(next) }
	ui.Column(c.Key("devices")).Gap(8).Margin(0, 0, 8).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, "设备匹配 devices").FontSize(kit.FontBody).FontWeight(500)
			if required {
				ui.Text(c, "*").TextColor(k.Danger)
			}
			ui.Spacer(c)
			kit.Button(c, "从本机选择", kit.ButtonOptions{Small: true, Icon: kit.SymUsb}).OnClick(func() {
				d.pickerOpen = true
				d.picker.selected = nil
				d.picker.load(s)
			})
			kit.Button(c, "添加设备", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() {
				set(append(rows, map[string]any{"bus_id": "", "vendor_id": "", "product_id": "", "serial": ""}))
			})
		})
		ui.Text(c, "同一行的条件同时满足才匹配，多行之间取并集；每行至少填写一项。").FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
		if len(rows) == 0 {
			message := "未添加设备，将导入服务端导出的全部设备"
			if required {
				message = "尚未添加要导出的设备"
			}
			ui.Box(c).Radius(kit.RadiusInput).Border(1, k.Hairline).Background(k.Surface).Padding(14, 12).Shrink(0).Children(func() {
				ui.Text(c, message).FontSize(kit.FontBody).TextColor(k.Label3).TextAlign(ui.Center).FillWidth()
			})
		}
		for i, row := range rows {
			ui.Row(c.Key(i)).Gap(8).AlignItems(ui.End).Radius(kit.RadiusInput).Border(1, k.HairlineSoft).Background(k.Surface).Padding(8).Shrink(0).Children(func() {
				ui.Grid(c).Columns(4).Gap(8).Grow(1).MinWidth(0).Children(func() {
					for _, key := range deviceKeys {
						ui.Column(c.Key(key.key)).Gap(2).MinWidth(0).Children(func() {
							ui.Text(c, key.label).FontSize(kit.FontTable).TextColor(k.Label3)
							value := jsv.String(row[key.key])
							input := kit.Input(c, "v", &value, key.placeholder).Label(fmt.Sprintf("第 %d 项 %s", i+1, key.label))
							if (key.key == "vendor_id" || key.key == "product_id") && invalidID(value) {
								input.TextColor(k.Danger)
							}
							if input.Changed() {
								next := rowsOf(service.Fields["devices"])
								next[i][key.key] = value
								set(next)
							}
						})
					}
				})
				kit.IconButton(c, kit.SymTrash2, "删除设备", kit.IconButtonOptions{Danger: true}).OnClick(func() {
					next := rowsOf(service.Fields["devices"])
					set(slices.Delete(next, i, i+1))
				})
			})
		}
	})
	d.pickerView(c, s, rows, func(added []map[string]any) { set(append(rowsOf(service.Fields["devices"]), added...)) })
}

// pickerView chooses devices of the server's host (LocalDevicePicker).
func (d *devicesField) pickerView(c *ui.Context, s *Section, existing []map[string]any, add func([]map[string]any)) {
	if !d.pickerOpen {
		return
	}
	k := kit.T(c)
	p := &d.picker
	added := func(dev api.USBLocalDevice) bool {
		row := rowOf(usbDeviceToRow(dev))
		return slices.ContainsFunc(existing, func(r map[string]any) bool { return sameRow(r, row) })
	}
	var rows []map[string]any
	for _, dev := range p.devices {
		if slices.Contains(p.selected, dev.BusID) && !added(dev) {
			rows = append(rows, rowOf(usbDeviceToRow(dev)))
		}
	}
	kit.Dialog(c, &d.pickerOpen, "从本机选择设备", 0, func() {
		note(c, kit.Neutral, "列出的是运行 Kiko 服务端的这台机器上的 USB 设备。导出设备需要 root（Linux）或管理员（Windows）权限，且核心需带 with_usbip 构建。")
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("本机设备 %d", len(p.devices))).FontSize(kit.FontBody).TextColor(k.Label3).Grow(1)
			kit.IconButton(c, kit.SymRefreshCw, "刷新", kit.IconButtonOptions{Disabled: p.loading, Size: 28, IconSize: 15}).OnClick(func() { p.load(s) })
		})
		switch {
		case p.err != "":
			note(c, kit.WarningTone, p.err)
		case len(p.devices) == 0:
			message := "没有可导出的设备"
			if p.loading {
				message = "正在读取…"
			}
			emptyBox(c, func() { ui.Text(c, message) })
		default:
			listCard(c, func() {
				for _, dev := range p.devices {
					already := added(dev)
					detail := dev.BusID + " · " + hexID(dev.VendorID) + ":" + hexID(dev.ProductID)
					if dev.Serial != "" {
						detail += " · " + dev.Serial
					}
					if already {
						detail += " · 已添加"
					}
					deviceRowView(c, dev, detail, slices.Contains(p.selected, dev.BusID), already, func(on bool) { p.toggle(dev.BusID, on) })
				}
			})
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.pickerOpen = false })
		label := "添加"
		if len(rows) > 0 {
			label = fmt.Sprintf("添加 (%d)", len(rows))
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: len(rows) == 0}).OnClick(func() {
			add(rows)
			d.pickerOpen = false
		})
	})
}

// deviceRowView is a checkable device row.
func deviceRowView(c *ui.Context, dev api.USBLocalDevice, detail string, checked, disabled bool, toggle func(bool)) {
	k := kit.T(c)
	on := checked
	row := ui.Row(c.Key(dev.BusID)).MinHeight(52).PaddingX(12).Gap(12).Shrink(0)
	if disabled {
		row.Opacity(0.5)
	}
	row.Children(func() {
		box := kit.Checkbox(c, &on, "").Disabled(disabled).Label(deviceName(dev))
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, deviceName(dev)).FontSize(14).SingleLine().Ellipsis("…")
			ui.Text(c, detail).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Ellipsis("…")
		})
		if box.Changed() && !disabled {
			toggle(on)
		}
	})
}
