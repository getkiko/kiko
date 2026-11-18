package processes

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
)

// statusLabels name the process states.
var statusLabels = map[string]string{"running": "运行", "sleep": "睡眠", "sleeping": "睡眠", "idle": "空闲",
	"stopped": "已停止", "zombie": "僵尸", "blocked": "等待", "wait": "等待"}

// confirmPopover asks to quit the process beside the button anchor
// names, when that button opened it.
func (w *Workspace) confirmPopover(c *ui.Context, trigger ui.Element, anchor string) {
	if w.confirmTarget == nil || w.confirmTarget.anchor != anchor {
		return
	}
	t := w.confirmTarget
	title, body, label := "退出进程", "将向进程发送退出请求。", "退出"
	if t.force {
		title, body, label = "强制退出进程", "强制退出会立即终止进程，未保存的工作可能丢失。", "强制退出"
	}
	kit.ConfirmPopover(c, trigger, &w.confirm, kit.ConfirmOptions{
		Title: title, Message: fmt.Sprintf("%s\n%s · PID %d", body, t.process.Name, t.process.PID),
		Confirm: label, Danger: true, Run: w.signal,
	})
	if !w.confirm.Open && !w.confirm.Busy {
		w.confirmTarget = nil
	}
}

func (w *Workspace) inspector(c *ui.Context, v *view) {
	k := kit.T(c)
	p := *v.selected
	subtitle := fmt.Sprintf("PID %d", p.PID)
	if w.selection != nil && w.selection.group {
		// A group shows its own process's PID with its count.
		subtitle += fmt.Sprintf(" · %d 个进程", len(v.members))
	}
	user := p.User
	if user == "" {
		user = "—"
	}
	kit.ScrollingInspector(c, p.Name, subtitle+" · "+user, func() { w.icon(c, p, true) }, nil, func() { w.details = false }, func() {
		if v.exited {
			ui.Text(c, "进程已退出 · 显示最后选中快照").FontSize(kit.FontTable).TextColor(k.Label3)
		}
		disabled := v.exited || p.PID <= 1
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			quit := kit.Button(c, "退出", kit.ButtonOptions{Small: true, Disabled: disabled}).Grow(1)
			if quit.Clicked() && !disabled {
				w.askQuit(p, false, "quit")
			}
			w.confirmPopover(c, quit, "quit")
			force := kit.Button(c, "强制退出", kit.ButtonOptions{Small: true, Variant: kit.Danger, Disabled: disabled}).Grow(1)
			if force.Clicked() && !disabled {
				w.askQuit(p, true, "force")
			}
			w.confirmPopover(c, force, "force")
			canReveal := p.Path != "" && !v.exited
			reveal := kit.IconButton(c, kit.SymFolder, "显示进程文件位置", kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 15, Disabled: !canReveal, Hint: "在文件管理器中显示"})
			if reveal.Clicked() && canReveal {
				w.reveal(p)
			}
		})
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			kit.Button(c, "连接", kit.ButtonOptions{Small: true, Icon: kit.SymCable, Hint: "在连接中查看此进程"}).Grow(1).PopoverMenuButton(func(menu *ui.Menu) {
				if menu.Item(fmt.Sprintf("此进程（PID %d）", p.PID)).Disabled(p.PID <= 0).Chosen() {
					w.showConnections(p, true)
				}
				if menu.Item("同名全部（" + connectionName(p) + "）").Disabled(connectionName(p) == "").Chosen() {
					w.showConnections(p, false)
				}
			})
			if kit.Button(c, "用量", kit.ButtonOptions{Small: true, Icon: kit.SymActivity, Disabled: p.Path == "", Hint: "在用量中查看此进程"}).Grow(1).Clicked() {
				w.showUsage(p)
			}
			target := ruleTarget(p)
			kit.Button(c, "添加到规则…", kit.ButtonOptions{Small: true, Icon: kit.SymRoute, Disabled: !ruleactions.Available(w.store, target)}).
				Grow(1).PopoverMenuButton(func(menu *ui.Menu) { ruleactions.Items(menu, w.env, w.store, target) })
		})
		var points []sample
		if w.selection != nil {
			points = w.history[w.selection.identity]
		}
		cpu, memory := make([]*float64, len(points)), make([]*float64, len(points))
		for i, s := range points {
			cpu[i], memory[i] = s.cpu, s.memory
		}
		cpuValue := "—"
		if p.CPU != nil {
			cpuValue = FormatPercent(p.CPU, 0) + "%"
		}
		kit.DotChart(c, cpu, kit.DotChartOptions{Label: "CPU", Value: cpuValue, Tone: kit.ChartBlue, MinimumScale: 100})
		kit.DotChart(c, memory, kit.DotChartOptions{Label: "内存", Value: FormatBytes(p.Memory), Tone: kit.ChartGreen, MinimumScale: 1 << 30})

		kit.Section(c, "详细信息", nil, func() {
			io := func(rate, total *float64) string {
				r := "—"
				if rate != nil {
					r = FormatBytes(rate) + "/s"
				}
				return r + " · " + FormatBytes(total)
			}
			wakeups := "—"
			if p.Wakeups != nil {
				wakeups = fmt.Sprintf("%.1f /秒", *p.Wakeups)
			}
			started := "—"
			if p.StartedAt != 0 && w.snapshot != nil {
				started = FormatStartedAt(p.StartedAt, w.snapshot.CapturedAt)
				uptime := max(0, float64(w.snapshot.CapturedAt-p.StartedAt)/1000)
				started += " · " + FormatUptime(&uptime)
			}
			status := statusLabels[p.Status]
			switch {
			case v.exited:
				status = "已退出"
			case status == "" && p.Status != "":
				status = p.Status
			case status == "":
				status = "—"
			}
			kit.Property(c, "PID", strconv.Itoa(p.PID))
			kit.Property(c, "CPU 时间", FormatCPUTime(p.CPUTime))
			kit.Property(c, "线程", FormatCount(p.Threads))
			kit.Property(c, "常驻内存", FormatBytes(p.ResidentMemory))
			kit.Property(c, "虚拟内存", FormatBytes(p.VirtualMemory))
			kit.Property(c, "功率", FormatPower(p.Power))
			kit.Property(c, "唤醒", wakeups)
			kit.Property(c, "磁盘读取", io(p.ReadRate, p.ReadBytes))
			kit.Property(c, "磁盘写入", io(p.WriteRate, p.WriteBytes))
			kit.Property(c, "已接收", io(p.ReceiveRate, p.ReceivedBytes))
			kit.Property(c, "已发送", io(p.SendRate, p.SentBytes))
			kit.PropertyWith(c, "启动时间", func() {
				ui.Text(c, started).FontSize(kit.FontTable).SingleLine().Ellipsis("…").MinWidth(0)
			})
			kit.Property(c, "状态", status)
		})

		var parent *Process
		for i := range v.processes {
			if v.processes[i].PID == p.PPID {
				parent = &v.processes[i]
				break
			}
		}
		kit.Section(c, "父进程", nil, func() {
			if parent != nil {
				if w.related(c, *parent, strconv.Itoa(parent.PID), "").Clicked() {
					w.selectProcess(*parent, v)
				}
				return
			}
			text := "—"
			if p.PPID > 0 {
				text = fmt.Sprintf("PID %d", p.PPID)
			}
			ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label3)
		})

		var related []Process
		if w.selection != nil && w.selection.group {
			for _, m := range v.members {
				if m.PID != p.PID {
					related = append(related, m)
				}
			}
		} else {
			for _, m := range v.processes {
				if m.PPID == p.PID && m.PID != p.PID && InFilter(m, v.filter) {
					related = append(related, m)
				}
			}
		}
		slices.SortStableFunc(related, Compare(SortCPU, true))
		if len(related) > 0 {
			title := "子进程"
			if w.selection != nil && w.selection.group {
				switch v.mode {
				case ModeApp:
					title = "App 内其他进程"
				case ModeName:
					title = "同名进程"
				}
			}
			kit.Section(c, fmt.Sprintf("%s · %d", title, len(related)), nil, func() {
				for _, m := range related {
					ui.Box(c.Key(Identity(m))).Children(func() {
						cpu := "—"
						if m.CPU != nil {
							cpu = FormatPercent(m.CPU, 1) + "%"
						}
						if w.related(c, m, cpu, FormatBytes(m.Memory)).Clicked() {
							w.selectProcess(m, v)
						}
					})
				}
			})
		}
		w.code(c, "命令", p.Command, false)
		w.code(c, "路径", p.Path, true)
		if w.snapshot != nil {
			for _, warning := range w.snapshot.Warnings {
				ui.Text(c, warning).FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
			}
		}
	})
}

// related is a button for a parent, child or member process.
func (w *Workspace) related(c *ui.Context, p Process, detail, second string) ui.Element {
	k := kit.T(c)
	b := ui.ButtonBase(c).MinHeight(28).PaddingX(5).Gap(8).Radius(8).FontSize(kit.FontTable).Label(p.Name).Transition(kit.HoverMotion)
	if b.Pressed() {
		b.Background(k.Fill2)
	} else if b.Hovered() {
		b.Background(k.Fill)
	}
	b.Children(func() {
		w.icon(c, p, false)
		ui.Text(c, p.Name).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		d := ui.Text(c, detail).TextColor(k.Label3).FontFeatures("tnum").SingleLine().TextAlign(ui.End)
		if second != "" {
			d.Width(48)
			ui.Text(c, second).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Width(65).TextAlign(ui.End)
		}
	})
	return b
}

// code shows a command (or a plain path) with its directory muted and the
// executable in bold, a copy button, and folding past four lines.
func (w *Workspace) code(c *ui.Context, label, value string, plain bool) {
	k := kit.T(c)
	ui.Column(c).Gap(8).Shrink(0).Children(func() {
		ui.Row(c).Children(func() {
			ui.Text(c, label).FontSize(kit.FontTable).FontWeight(600).TextColor(k.Label3).Grow(1)
			icon := kit.SymCopy
			if w.copied == label {
				icon = kit.SymCheck
			}
			copyButton := kit.IconButton(c, icon, "复制"+label, kit.IconButtonOptions{Size: 20, IconSize: 12, Disabled: value == ""})
			if copyButton.Clicked() && value != "" {
				c.WriteClipboard(value)
				w.copied, w.copiedAt = label, time.Now()
			}
		})
		shown := value
		if shown == "" {
			shown = "—"
		}
		body := ui.Column(c).Padding(12).Radius(12).Background(k.Fill)
		if plain {
			body.Padding(0).Background(ui.Color{})
		}
		body.Children(func() {
			var text ui.Element
			if plain {
				text = ui.Text(c, shown).TextColor(ui.Hex("#86868A"))
			} else {
				dir, exe, args := CodeParts(shown, false)
				text = ui.RichText(c,
					ui.Span{Text: dir, Color: k.Label3},
					ui.Span{Text: exe, Weight: 600, Color: k.Label},
					ui.Span{Text: args},
				)
			}
			text.Font(kit.MonoFont).FontSize(11).Wrap().Selectable()
			long := !plain && (len(value) > 180 || strings.Count(value, "\n") >= 4)
			if long && !w.codeExpanded {
				text.MaxLines(4).Ellipsis("…")
			}
			if long {
				toggle := "显示更多"
				if w.codeExpanded {
					toggle = "显示更少"
				}
				b := ui.ButtonBase(c).Gap(4).Margin(8, 0, 0).FontSize(kit.FontTable).TextColor(k.Label3).Label(toggle)
				b.Children(func() {
					ui.Text(c, toggle)
					chev := kit.Icon(c, kit.SymChevronDown, 12)
					if w.codeExpanded {
						chev.Rotate(180)
					}
				})
				if b.Clicked() {
					w.codeExpanded = !w.codeExpanded
				}
			}
		})
	})
}
