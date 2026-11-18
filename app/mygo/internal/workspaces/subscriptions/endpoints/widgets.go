package endpoints

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// Small pieces shared by the endpoint sheets: status dots and rows,
// detail sections and the guide's checklist, collapsibles and path
// choices.

func toneColor(k *kit.Tokens, t stateTone) ui.Color {
	switch t {
	case toneOK:
		return k.Success
	case toneWarn:
		return k.Warning
	case toneError:
		return k.Danger
	}
	return k.Label4
}

// statusDot is an 8 DIP dot in a state's color.
func statusDot(c *ui.Context, t stateTone) ui.Element {
	return ui.Box(c).Size(8, 8).Radius(4).Background(toneColor(kit.T(c), t)).Shrink(0)
}

// stateMark is Tailscale's state glyph beside its words, in place of the
// dot: the logo's dots in the state's color, as the Overview card draws it.
func stateMark(c *ui.Context, t stateTone) ui.Element {
	return kit.TailscaleMark(c, 14, toneColor(kit.T(c), t))
}

// stateButton is a hero card's runtime state at its bottom right, a
// dot and the state's words, taking the click from the card around it;
// it reports a click.
func stateButton(c *ui.Context, line stateLine, label string) bool {
	k := kit.T(c)
	b := ui.ButtonBase(c).Height(19).Gap(5).PaddingX(6).Radius(6).AlignItems(ui.Center).
		Shrink(1).MinWidth(0).Label(label).Transition(kit.HoverMotion)
	switch {
	case b.Pressed():
		b.Background(k.Fill2)
	case b.Hovered():
		b.Background(k.Fill)
	}
	b.Children(func() {
		statusDot(c, line.tone)
		ui.Text(c, line.text).TextColor(k.Label2).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
	return b.Clicked()
}

// peerAvatars are up to three online devices' initials, overlapping, over
// how many devices are online, at the right of the Overview Tailscale card.
func peerAvatars(c *ui.Context, peers []tailnetPeer) {
	k := kit.T(c)
	tints := [][2]ui.Color{{k.AccentWeak, k.Accent}, {k.SuccessWeak, k.Success}, {k.WarningWeak, k.Warning}}
	ui.Column(c).Gap(4).AlignItems(ui.End).AlignSelf(ui.End).Shrink(0).Children(func() {
		// Each avatar overlaps the one before it by 8 DIPs.
		shown := peers[:min(len(peers), 3)]
		ui.Box(c).Size(float32(26+18*(len(shown)-1)), 26).Shrink(0).Children(func() {
			for i, peer := range shown {
				initial := "?"
				if r := []rune(strings.TrimSpace(peer.Name)); len(r) > 0 {
					initial = strings.ToUpper(string(r[0]))
				}
				ui.Box(c.Key(i)).Absolute().Top(0).Left(float32(18*i)).Size(26, 26).Radius(13).Border(2, k.Surface).
					Background(tints[i][0]).Center().Children(func() {
					ui.Text(c, initial).FontSize(kit.FontCaption).FontWeight(700).TextColor(tints[i][1]).SingleLine()
				})
			}
		})
		ui.Text(c, strconv.Itoa(len(peers))+" 台设备").FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine()
	})
}

// heroGrid lays out the hero cards of ids in kit.HeroColumns columns,
// their cells stretched to the same height in each row, then tail's
// cell, an AddCard, the whole row without cards. With move a card can be
// dragged onto another: move(id, target) then reorders them.
func heroGrid(c *ui.Context, key string, ids []string, move func(id, target string), card func(i int), tail func() ui.Element) ui.Element {
	k := kit.T(c)
	columns := kit.HeroColumns(c)
	return ui.Grid(c.Key(key)).Columns(columns).Gap(12).FillWidth().Shrink(0).Children(func() {
		for i, id := range ids {
			// A one-column grid lets the card fill the cell.
			cell := ui.Grid(c.Key(id)).Columns(1).MinWidth(0).Radius(kit.HeroRadius + 2)
			if move != nil {
				cell.Drag(dragItem{id})
				if cell.Dragging() {
					cell.Opacity(0.45)
				}
				if over, ok := ui.DragOver[dragItem](cell); ok && over.id != id {
					cell.Border(2, k.Accent.Alpha(0.55))
				}
				if dropped, ok := ui.Drop[dragItem](cell); ok && dropped.id != id {
					move(dropped.id, id)
				}
			}
			cell.Children(func() { card(i) })
		}
		if tail != nil {
			if add := tail(); len(ids) == 0 && columns > 1 {
				add.ColumnSpan(columns)
			}
		}
	})
}

// infoRow is a label on the left and its value on the right.
func infoRow(c *ui.Context, label string, value func()) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(label)).MinHeight(40).Gap(12).PaddingY(6).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label3).Shrink(0)
		ui.Row(c).Grow(1).MinWidth(0).Justify(ui.End).Gap(8).FontSize(kit.FontBody).Children(value)
	})
}

// infoText is an infoRow of selectable text.
func infoText(c *ui.Context, label, value string) ui.Element {
	return infoRow(c, label, func() {
		ui.Text(c, value).Selectable().Wrap().TextAlign(ui.End).FontFeatures("tnum").MinWidth(0)
	})
}

// card is a rounded group of rows.
func card(c *ui.Context, fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c).Radius(16).Background(k.Surface).Border(1, k.HairlineSoft).PaddingX(14).Clip().
		Dividers(1, k.HairlineSoft).Shrink(0).Children(fn)
}

// listCard is a rounded list whose rows pad themselves.
func listCard(c *ui.Context, fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c).Radius(18).Background(k.Surface).Border(1, k.HairlineSoft).Clip().
		Dividers(1, k.HairlineSoft).Shrink(0).Children(fn)
}

// sectionTitle is a section's small uppercase title with an action.
func detailSection(c *ui.Context, title string, action func(), fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c.Key(title)).Gap(8).Shrink(0).Children(func() {
		ui.Row(c).MinHeight(20).Children(func() {
			ui.Text(c, title).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label3).Grow(1)
			if action != nil {
				action()
			}
		})
		fn()
	})
}

// subHeading is a title with a count and an action.
func subHeading(c *ui.Context, title, count string, action func()) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(title)).MinHeight(32).Gap(8).PaddingX(4).Shrink(0).Children(func() {
		ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label)
		if count != "" {
			ui.Text(c, count).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
		}
		ui.Spacer(c)
		if action != nil {
			action()
		}
	})
}

// emptyBox is the dashed placeholder of an empty list.
func emptyBox(c *ui.Context, fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c).Radius(18).Border(1, k.Hairline).Background(k.Surface).Padding(28, 16).Gap(4).
		AlignItems(ui.Center).FontSize(kit.FontBody).TextColor(k.Label3).Shrink(0).Children(fn)
}

// note is a short message.
func note(c *ui.Context, tone kit.Tone, message string) ui.Element {
	return kit.Notice(c, tone, "", message, nil)
}

// bulletList lists lines with colored bullets.
func bulletList(c *ui.Context, items []string, tone kit.Tone) ui.Element {
	k := kit.T(c)
	mark := k.Label3
	switch tone {
	case kit.SuccessTone:
		mark = k.Success
	case kit.WarningTone:
		mark = k.Warning
	}
	return ui.Column(c).Gap(4).Shrink(0).Children(func() {
		for i, line := range items {
			ui.Row(c.Key(i)).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(mark).Margin(7, 0, 0).Shrink(0)
				ui.Text(c, line).FontSize(kit.FontBody).TextColor(k.Label2).Wrap().Grow(1).MinWidth(0)
			})
		}
	})
}

var levelIcons = map[guideLevel]kit.Symbol{levelOK: kit.SymCircleCheck, levelInfo: kit.SymInfo, levelWarn: kit.SymTriangleAlert, levelError: kit.SymCircleX}
var levelLabels = map[guideLevel]string{levelOK: "通过", levelInfo: "提示", levelWarn: "注意", levelError: "需要处理"}

func levelColor(k *kit.Tokens, l guideLevel) ui.Color {
	switch l {
	case levelOK:
		return k.Success
	case levelWarn:
		return k.Warning
	case levelError:
		return k.Danger
	}
	return k.Label3
}

// copyCommand is a monospaced command with a copy button.
func copyCommand(c *ui.Context, command string, copied func()) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(command)).Gap(8).Radius(kit.RadiusInput).Background(k.Fill).Padding(6, 6, 6, 12).Margin(6, 0, 0).Shrink(0).Children(func() {
		ui.Text(c, command).Font(kit.MonoFont).FontSize(kit.FontTable).Selectable().Wrap().Grow(1).MinWidth(0)
		kit.IconButton(c, kit.SymCopy, "复制命令", kit.IconButtonOptions{Size: 28, IconSize: 14}).OnClick(func() {
			c.WriteClipboard(command)
			if copied != nil {
				copied()
			}
		})
	})
}

// checkRow is a checklist item.
func checkRow(c *ui.Context, item guideItem, copied func()) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(item.ID)).Gap(10).PaddingY(8).AlignItems(ui.Start).Label(levelLabels[item.Level] + "：" + item.Title).Shrink(0).Children(func() {
		kit.Icon(c, levelIcons[item.Level], 17).TextColor(levelColor(k, item.Level)).Margin(1, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, item.Title).FontSize(kit.FontBody).TextColor(k.Label).Wrap()
			if item.Detail != "" {
				ui.Text(c, item.Detail).FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
			}
			if item.Command != "" {
				copyCommand(c, item.Command, copied)
			}
		})
	})
}

// collapsible is a titled block that opens on click, open while *open.
func collapsible(c *ui.Context, title string, badge func(), open *bool, fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c.Key(title)).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Clip().Shrink(0).Children(func() {
		head := ui.ButtonBase(c).MinHeight(40).PaddingX(12).Gap(8).FillWidth().Justify(ui.Start).Label(title).Expanded(*open).Transition(kit.HoverMotion)
		if head.Pressed() {
			head.Background(k.Fill)
		}
		head.Children(func() {
			ui.Text(c, title).FontSize(kit.FontBody).FontWeight(500).Grow(1).MinWidth(0).Wrap()
			if badge != nil {
				badge()
			}
			chev := kit.Icon(c, kit.SymChevronDown, 16).TextColor(k.Label3)
			if *open {
				chev.Rotate(180)
			}
		})
		if head.Clicked() {
			*open = !*open
		}
		if *open {
			ui.Column(c).Padding(0, 12, 12).Gap(6).FontSize(kit.FontBody).TextColor(k.Label2).Children(fn)
		}
	})
}

// stepper is the wizard's three steps.
func stepper(c *ui.Context, steps []string, current int) ui.Element {
	k := kit.T(c)
	return ui.Row(c).Gap(8).Label("向导步骤").Shrink(0).Children(func() {
		for i, label := range steps {
			ui.Row(c.Key(i)).Gap(8).Grow(1).Basis(0).MinWidth(0).Children(func() {
				dot := ui.Box(c).Size(24, 24).Radius(12).Center().FontSize(kit.FontTable).FontWeight(600).Shrink(0)
				switch {
				case i < current:
					dot.Background(k.Success).TextColor(k.OnAccent).Children(func() { kit.Icon(c, kit.SymCheck, 13) })
				case i == current:
					dot.Background(k.Accent).TextColor(k.OnAccent).Children(func() { ui.Text(c, strconv.Itoa(i+1)) })
				default:
					dot.Background(k.Fill).TextColor(k.Label3).Children(func() { ui.Text(c, strconv.Itoa(i+1)) })
				}
				text := ui.Text(c, label).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).TextColor(k.Label3)
				if i == current {
					text.FontWeight(600).TextColor(k.Label)
				}
				if i < len(steps)-1 {
					ui.Box(c).Height(1).MinWidth(12).Grow(1).Background(k.HairlineSoft)
				}
			})
		}
	})
}

// choiceCard is a radio card: a mark, a title and its content.
func choiceCard(c *ui.Context, key any, selected, disabled bool, label string, fn func()) ui.Element {
	k := kit.T(c)
	b := ui.ButtonBase(c.Key(key)).FillWidth().Padding(12).Gap(6).Radius(16).Border(1, k.HairlineSoft).Background(k.Surface).
		Justify(ui.Start).AlignItems(ui.Start).Label(label).Disabled(disabled).Role(ui.RoleRadio).Selected(selected).Transition(kit.HoverMotion)
	b.Column()
	switch {
	case selected:
		b.Border(1, k.Accent).Background(k.Surface.Mix(k.AccentWeak, 0.6))
	case disabled:
		b.Opacity(0.45)
	case b.Pressed():
		b.Background(k.Fill2)
	case b.Hovered():
		b.Background(k.Fill)
	}
	return b.Children(fn)
}

// radioMark is a choice's round mark.
func radioMark(c *ui.Context, selected bool) ui.Element {
	k := kit.T(c)
	mark := ui.Box(c).Size(18, 18).Radius(9).Center().Border(1, k.Hairline).Shrink(0)
	if selected {
		mark.Border(1, k.Accent).Background(k.Accent).TextColor(k.OnAccent).Children(func() { kit.Icon(c, kit.SymCheck, 12) })
	}
	return mark
}

// pathCard is a connection path's choice; compact shows the summary
// only, else the scenario, pros and cons and the side's steps.
func pathCard(c *ui.Context, info usbIPPath, selected bool, role string, onSelect func()) {
	k := kit.T(c)
	if choiceCard(c, info.ID, selected, false, info.Title, func() {
		ui.Row(c).Gap(8).Children(func() {
			radioMark(c, selected)
			ui.Text(c, info.Title).FontSize(15).FontWeight(600).Grow(1)
		})
		ui.Text(c, info.Summary).FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
		ui.Column(c).Gap(8).Margin(4, 0, 0).Children(func() {
			ui.Text(c, "适合："+info.Scenario).FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
			bulletList(c, info.Pros, kit.SuccessTone)
			bulletList(c, info.Cons, kit.WarningTone)
			if role != "" {
				side := "使用设备的那台机器上要做："
				steps := info.Client
				if role == "server" {
					side, steps = "共享设备的那台机器上要做：", info.Server
				}
				ui.Text(c, side).FontSize(kit.FontTable).TextColor(k.Label3)
				bulletList(c, steps, kit.Neutral)
			}
		})
	}).Clicked() {
		onSelect()
	}
}
