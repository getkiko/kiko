package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// ButtonVariant is a button's emphasis.
type ButtonVariant int

const (
	// Secondary is a filled neutral capsule, the default.
	Secondary ButtonVariant = iota
	// Primary is the accent capsule of a window's main action.
	Primary
	// TextButton is accent text without a fill.
	TextButton
	// Danger is a destructive action.
	Danger
)

// ButtonOptions style a button.
type ButtonOptions struct {
	Variant  ButtonVariant
	Small    bool
	Icon     Symbol
	Disabled bool
	// Hint explains the button on a long hover (see Hint).
	Hint string
}

// Button is a flat capsule button, 30 DIPs high (26 when Small). Read
// Clicked on the element it returns.
func Button(c *ui.Context, label string, o ButtonOptions) ui.Element {
	k := T(c)
	height := float32(ControlHeight)
	if o.Small {
		height = SmallControl
	}
	b := ui.ButtonBase(c).Height(height).PaddingX(14).Gap(6).Radius(RadiusCapsule).
		Justify(ui.Center).FontSize(FontBody).FontWeight(500).Shrink(0).Disabled(o.Disabled)
	if o.Small {
		b.PaddingX(10).FontSize(FontTable)
	}
	var bg, fg ui.Color
	switch o.Variant {
	case Primary:
		bg, fg = k.Accent, k.OnAccent
		if b.Pressed() {
			bg = k.AccentPress
		} else if b.Hovered() {
			bg = k.Accent.Mix(k.AccentPress, 0.35)
		}
		b.FontWeight(600)
	case TextButton:
		fg = k.AccentText
		if b.Pressed() {
			bg = k.Fill2
		} else if b.Hovered() {
			bg = k.Fill
		}
		b.PaddingX(8)
	case Danger:
		bg, fg = k.DangerWeak, k.Danger
		if b.Pressed() || b.Hovered() {
			bg = k.DangerWeak.Mix(k.Danger, 0.15)
		}
	default:
		bg, fg = k.Fill, k.Label
		if b.Pressed() {
			bg = k.Fill2
		} else if b.Hovered() {
			bg = k.Fill.Mix(k.Fill2, 0.6)
		}
	}
	if o.Disabled {
		b.Opacity(0.45)
	}
	b.Background(bg).TextColor(fg).Label(label).Transition(HoverMotion)
	Hint(c, b, o.Hint)
	b.Children(func() {
		if o.Icon != "" {
			Icon(c, o.Icon, 15)
		}
		if label != "" {
			ui.Text(c, label).SingleLine()
		}
	})
	return b
}

// IconButtonOptions style an icon button.
type IconButtonOptions struct {
	// Active shows a pressed toggle: accent text on a weak accent fill.
	Active   bool
	Danger   bool
	Disabled bool
	// Size is the button's side, 30 by default.
	Size float32
	// IconSize is the symbol's size, 16 by default.
	IconSize float32
	// Hint replaces label as the hint shown on a long hover.
	Hint string
	// Busy shows the work the button started under way: the spinner
	// (SpinnerIn) in the symbol's place and color, the button taking no
	// clicks (BusyCover) while it stays as readable as it was, unfaded.
	// With less motion it keeps its symbol, in Label on the Fill.
	Busy bool
	// BusyHint replaces the hint while Busy, as "Testing".
	BusyHint string
}

// IconButton is a round button showing only a symbol. label names it for
// assistive technology and shows as its hint (see Hint).
func IconButton(c *ui.Context, s Symbol, label string, o IconButtonOptions) ui.Element {
	k := T(c)
	size := o.Size
	if size == 0 {
		size = ControlHeight
	}
	iconSize := o.IconSize
	if iconSize == 0 {
		iconSize = 16
	}
	hint := o.Hint
	if hint == "" {
		hint = label
	}
	fg := k.Label2
	if o.Active {
		fg = k.AccentText
	}
	if o.Danger {
		fg = k.Danger
	}
	if o.Busy {
		if o.BusyHint != "" {
			hint = o.BusyHint
		}
		box := ui.Box(c).Size(size, size).Radius(RadiusCapsule).Center().Shrink(0).TextColor(fg)
		if o.Active {
			box.Background(k.AccentWeak)
		}
		Hint(c, box, hint)
		var cover ui.Element
		still := c.Preferences().ReduceMotion
		if still && !o.Active {
			box.Background(k.Fill).TextColor(k.Label)
		}
		box.Children(func() {
			if still {
				Icon(c, s, iconSize)
			} else {
				SpinnerIn(c, iconSize, fg)
			}
			cover = BusyCover(c, label)
		})
		return cover
	}
	b := ui.ButtonBase(c).Size(size, size).Radius(RadiusCapsule).Center().Shrink(0).
		Label(label).Disabled(o.Disabled)
	Hint(c, b, hint)
	var bg ui.Color
	switch {
	case o.Active:
		bg = k.AccentWeak
	case b.Pressed():
		bg = k.Fill2
	case b.Hovered():
		bg = k.Fill
	}
	if o.Disabled {
		b.Opacity(0.4)
	}
	b.Background(bg).TextColor(fg).Transition(HoverMotion).Children(func() { Icon(c, s, iconSize) })
	return b
}

// Glass styles e as a Liquid Glass surface: translucent fill, a fine edge
// and a soft shadow. Under reduced transparency it is solid.
func Glass(c *ui.Context, e ui.Element) ui.Element {
	k := T(c)
	fill := k.Glass
	if c.Preferences().HighContrast {
		fill = k.Elevated
	}
	return e.Background(fill).Border(0.5, k.GlassEdge).
		Shadow(0, 1, 3, 0, k.Shadow).Shadow(0, 6, 18, 0, k.Shadow.Alpha(0.05))
}

// ToolbarGroup groups related toolbar actions on one capsule of glass,
// about 38 DIPs high.
func ToolbarGroup(c *ui.Context, fn func()) ui.Element {
	g := ui.Row(c).Height(ToolbarGroupH).Padding(0, 4).Gap(2).Radius(RadiusCapsule).Shrink(0)
	return Glass(c, g).Children(fn)
}

// Segment is one choice of a segmented control.
type Segment struct {
	Label string
	// Count shows after the label, as "Active 21".
	Count string
	Icon  Symbol
	// Name names a segment showing no Label, as one of an icon and a
	// count, for assistive technology and its hint.
	Name string
}

// segmentMotion is how the selected indicator of a segmented control slides
// and its labels fade: 220 ms ease-in-out
// (cubic-bezier(0.77, 0, 0.175, 1)). While the desktop asks for less
// motion, MyGo puts the indicator at its place at once and only fades the
// colors.
var segmentMotion = ui.Animation{Duration: 220 * time.Millisecond, Ease: ui.CubicBezier(0.77, 0, 0.175, 1)}

// Segmented is a capsule segmented control choosing *selected, 32 DIPs
// high. The selected indicator slides between segments and the labels fade
// to their color. Changed reports a new choice on the element it returns.
func Segmented(c *ui.Context, key any, selected *int, segments ...Segment) ui.Element {
	return segmented(c, key, selected, false, segments)
}

// SegmentedFill is Segmented filling its container's width, the
// segments sharing it equally while their labels fit, a long label
// keeping its width in a narrow container.
func SegmentedFill(c *ui.Context, key any, selected *int, segments ...Segment) ui.Element {
	return segmented(c, key, selected, true, segments).FillWidth()
}

func segmented(c *ui.Context, key any, selected *int, fill bool, segments []Segment) ui.Element {
	k := T(c)
	parts := ui.SegmentedBase(c.Key(key), selected, len(segments))
	track := parts.Track
	// Where each segment showed in the frame before, from the track's
	// origin: the indicator is painted behind the segments at a place
	// between two of them.
	trackBox := track.LayoutBounds()
	boxes := make([]ui.Rect, len(segments))
	placed := trackBox.W > 0
	track.Height(SegmentHeight).Padding(2).Radius(RadiusCapsule).Background(k.Fill).Shrink(0).Children(func() {
		for i, s := range segments {
			seg := parts.Segment(i).Height(SegmentHeight - 4).PaddingX(12).Gap(5).Radius(RadiusCapsule).
				Center().FontSize(FontTable)
			if fill {
				seg.Grow(1).Basis(0)
			}
			if s.Name != "" {
				Hint(c, seg.Label(s.Name), s.Name)
			}
			if b := seg.LayoutBounds(); b.W > 0 && placed {
				boxes[i] = ui.Rect{X: b.X - trackBox.X, Y: b.Y - trackBox.Y, W: b.W, H: b.H}
			} else {
				placed = false
			}
			fg := k.Label2
			if i == *selected {
				fg = k.Label
				seg.FontWeight(600)
			} else if seg.Hovered() {
				fg = k.Label
			}
			seg.TextColor(seg.AnimateColor("fg", fg, segmentMotion))
			seg.Children(func() {
				if s.Icon != "" {
					Icon(c, s.Icon, 14)
				}
				if s.Label != "" {
					ui.Text(c, s.Label).SingleLine()
				}
				if s.Count != "" {
					ui.Text(c, s.Count).SingleLine().FontSize(FontCaption).TextColor(k.Label3).FontFeatures("tnum")
				}
			})
			if !placed && i == *selected {
				// Before the first layout, the segment draws its own fill.
				seg.Background(k.Surface).Shadow(0, 1, 2, 0, k.Shadow)
			}
		}
	})
	if placed && len(segments) > 0 {
		at := track.AnimateTo("indicator", float32(*selected), segmentMotion)
		box := indicatorBox(boxes, at)
		surface, shadow := k.Surface, k.Shadow
		track.Draw(func(p *ui.Painter, r ui.Rect) {
			b := ui.Rect{X: r.X + box.X, Y: r.Y + box.Y, W: box.W, H: box.H}
			radius := b.H / 2
			p.Shadow(b, radius, 0, 1, 2, 0, shadow)
			p.Fill(b, surface, radius)
		})
	}
	return track
}

// indicatorBox is the indicator's box at a fractional segment index, between
// the boxes of the segments either side.
func indicatorBox(boxes []ui.Rect, at float32) ui.Rect {
	last := len(boxes) - 1
	if at <= 0 {
		return boxes[0]
	}
	if at >= float32(last) {
		return boxes[last]
	}
	i := int(at)
	t := at - float32(i)
	a, b := boxes[i], boxes[i+1]
	lerp := func(x, y float32) float32 { return x + (y-x)*t }
	return ui.Rect{X: lerp(a.X, b.X), Y: lerp(a.Y, b.Y), W: lerp(a.W, b.W), H: lerp(a.H, b.H)}
}

// SearchOptions shape a search field.
type SearchOptions struct {
	Placeholder string
	// Toolbar makes it 38 DIPs high, for a toolbar.
	Toolbar bool
	Width   float32
}

// Search is a capsule search field editing *query; Escape and its clear
// button empty it (Escape in an empty field goes on to what is around it). Changed reports edits on the element it returns, the
// text input.
func Search(c *ui.Context, key any, query *string, o SearchOptions) ui.Element {
	k := T(c)
	height := float32(SegmentHeight)
	if o.Toolbar {
		height = ToolbarGroupH
	}
	placeholder := o.Placeholder
	if placeholder == "" {
		placeholder = "搜索"
	}
	box := ui.Row(c.Key(key)).Height(height).PaddingX(11).Gap(6).Radius(RadiusCapsule).Shrink(1).
		Background(k.Fill).TextColor(k.Label3).Cursor(ui.CursorText)
	if o.Width > 0 {
		box.Width(o.Width).MinWidth(120)
	}
	if o.Toolbar {
		Glass(c, box)
	}
	var input ui.Element
	box.Children(func() {
		Icon(c, SymSearch, 15)
		input = ui.TextInputBase(c, query).Grow(1).MinWidth(0).FontSize(FontBody).
			TextColor(k.Label).Placeholder(placeholder).SingleLine().Label(placeholder)
		// An empty field leaves Escape to what is around it, as a dialog
		// or a page returning.
		noteTextFocus(c, input)
		if *query != "" && input.Shortcut(0, ui.KeyEscape) {
			*query = ""
			input.SetChanged()
		}
		if *query != "" {
			clear := ui.ButtonBase(c).Size(18, 18).Radius(RadiusCapsule).Center().Background(k.Fill2).
				TextColor(k.Surface).Label("清除搜索").Children(func() { Icon(c, SymX, 11) })
			if clear.Clicked() {
				*query = ""
				input.SetChanged()
			}
		}
	})
	return input
}

// Option is a choice of a Select.
type Option struct {
	Value string
	Label string
	// Detail shows muted after the label in the popup.
	Detail string
}

// SelectOptions shape a select.
type SelectOptions struct {
	// Inline is the compact select of a table cell: 24 DIPs, 12 DIP text,
	// no fill until hovered.
	Inline bool
	// Toolbar is a select in a toolbar group: on the group's glass, no
	// fill until hovered, as the group's buttons.
	Toolbar     bool
	Width       float32
	Placeholder string
	Disabled    bool
	Label       string
}

// Select is a drop-down choosing *value among options. Changed reports a
// new choice on the trigger it returns.
func Select(c *ui.Context, key any, value *string, options []Option, o SelectOptions) ui.Element {
	k := T(c)
	sel := ui.SelectBase(c.Key(key), value)
	trigger := sel.Trigger
	height, font := float32(ControlHeight), float32(FontBody)
	if o.Inline {
		height, font = InlineSelectH, FontTable
	}
	trigger.Height(height).PaddingX(10).Gap(6).Radius(RadiusInput).FontSize(font).TextColor(k.Label).
		Disabled(o.Disabled).MinWidth(0).Transition(HoverMotion)
	if o.Width > 0 {
		trigger.Width(o.Width)
	}
	if o.Label != "" {
		trigger.Label(o.Label)
	}
	switch {
	case o.Inline && trigger.Hovered():
		trigger.Background(k.Fill).PaddingX(6)
	case o.Inline:
		trigger.PaddingX(6)
	case o.Toolbar:
		trigger.Radius(RadiusCapsule)
		if trigger.Hovered() {
			trigger.Background(k.Fill)
		}
	default:
		trigger.Background(k.Fill)
	}
	if o.Disabled {
		trigger.Opacity(0.5)
	}
	current := o.Placeholder
	muted := true
	for _, option := range options {
		if option.Value == *value {
			current, muted = option.Label, false
		}
	}
	trigger.Children(func() {
		text := ui.Text(c, current).SingleLine().Grow(1).MinWidth(0)
		if muted {
			text.TextColor(k.Label3)
		}
		Icon(c, SymChevronsUpDown, font).TextColor(k.Label3)
	})
	sel.Popup(func(panel ui.Element) {
		Popup(c, panel).Padding(5).MinWidth(160)
		AttachPopover(c, panel, trigger, ui.AnchorBottomStart, ui.AnchorTopStart, 4, 360)
		PopoverBody(c, 0, func() {
			for _, option := range options {
				item := sel.Item(option.Value).Height(28).PaddingX(10).Gap(8).Radius(7).FontSize(FontBody)
				if item.Highlighted() {
					item.Background(k.Accent).TextColor(k.OnAccent)
				}
				item.Children(func() {
					mark := Icon(c, SymCheck, 13)
					if option.Value != *value {
						mark.Invisible()
					}
					ui.Text(c, option.Label).SingleLine().Grow(1)
					if option.Detail != "" {
						d := ui.Text(c, option.Detail).SingleLine().FontSize(FontCaption)
						if !item.Highlighted() {
							d.TextColor(k.Label3)
						}
					}
				})
			}
		})
	})
	return trigger
}

// Popup styles a floating panel: menus, popovers and select popups.
func Popup(c *ui.Context, panel ui.Element) ui.Element {
	k := T(c)
	return panel.Radius(12).Background(k.Elevated).Border(0.5, k.GlassEdge).
		Shadow(0, 10, 32, 0, k.Shadow.Alpha(0.18)).Shadow(0, 1, 3, 0, k.Shadow)
}

// PopoverRoom is the most height a popover attached to anchor gets in the
// window: the larger room above or below the anchor, less gap and an 8px
// window margin. A popover is laid out at its own size and only flips
// side, so its MaxHeight takes this room and its list scrolls in it.
func PopoverRoom(c *ui.Context, anchor ui.Element, gap float32) float32 {
	_, h := c.Size()
	b := anchor.Bounds()
	if b.H <= 0 {
		return max(h-16, 120)
	}
	return max(max(b.Y, h-b.Y-b.H)-gap-8, 120)
}

// Tone colors badges and status dots.
type Tone int

const (
	Neutral Tone = iota
	AccentTone
	SuccessTone
	WarningTone
	DangerTone
)

func (t Tone) colors(k *Tokens) (fg, bg ui.Color) {
	switch t {
	case AccentTone:
		return k.AccentText, k.AccentWeak
	case SuccessTone:
		return k.Success, k.SuccessWeak
	case WarningTone:
		return k.Warning, k.WarningWeak
	case DangerTone:
		return k.Danger, k.DangerWeak
	}
	return k.Label2, k.Fill
}

// Badge is a short text in a pill, as a count or a protocol.
func Badge(c *ui.Context, text string, tone Tone) ui.Element {
	fg, bg := tone.colors(T(c))
	if rowOnAccent {
		fg, bg = T(c).OnAccent, T(c).OnAccent.Alpha(0.2)
	}
	return ui.Row(c).Height(16).PaddingX(6).Radius(8).Background(bg).TextColor(fg).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(10).FontWeight(600).SingleLine().FontFeatures("tnum")
	})
}

// Status is a colored dot with neutral text, as "Running".
func Status(c *ui.Context, text string, tone Tone) ui.Element {
	k := T(c)
	dot, _ := tone.colors(k)
	if tone == Neutral {
		dot = k.Label4
	}
	return ui.Row(c).Gap(6).Shrink(0).Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(dot)
		ui.Text(c, text).SingleLine().TextColor(k.Label2)
	})
}

// Tag is a chip, with a remove button when onRemove is set.
func Tag(c *ui.Context, text string, onRemove func()) ui.Element {
	k := T(c)
	return ui.Row(c).Height(22).PaddingX(8).Gap(4).Radius(7).Background(k.Fill).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(FontTable).SingleLine()
		if onRemove != nil {
			x := ui.ButtonBase(c).Size(14, 14).Radius(7).Center().Label("移除 " + text).TextColor(k.Label3).Transition(HoverMotion)
			if x.Hovered() {
				x.Background(k.Fill2)
			}
			x.Children(func() { Icon(c, SymX, 10) }).OnClick(onRemove)
		}
	})
}

// Switch is a 32×20 switch bound to *on; Changed on the element.
func Switch(c *ui.Context, on *bool, label string) ui.Element {
	k := T(c)
	sw := ui.SwitchBase(c, on).Size(32, 20).Radius(10).Padding(2).Shrink(0).Label(label)
	track := k.Fill2
	if *on {
		track = k.Accent
	}
	switchMotion := ui.Animation{Duration: 180 * time.Millisecond, Ease: segmentMotion.Ease}
	sw.Background(sw.AnimateColor("track", track, switchMotion))
	var target float32
	if *on {
		target = 12
	}
	x := sw.AnimateTo("knob", target, switchMotion)
	sw.Children(func() {
		ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#FFFFFF")).Shadow(0, 1, 2, 0, k.Shadow).Translate(x, 0)
	})
	return sw
}

// Checkbox is a square check box with a label, bound to *checked.
func Checkbox(c *ui.Context, checked *bool, label string) ui.Element {
	k := T(c)
	box := ui.CheckboxBase(c, checked).Gap(8).Label(label).Shrink(0)
	box.Children(func() {
		mark := ui.Box(c).Size(16, 16).Radius(4).Center().Border(1, k.Hairline)
		if *checked {
			mark.Background(k.Accent).Border(0, k.Accent).TextColor(k.OnAccent)
		}
		mark.Children(func() {
			if *checked {
				Icon(c, SymCheck, 12)
			}
		})
		if label != "" {
			ui.Text(c, label).FontSize(FontBody)
		}
	})
	return box
}

// Input is a 30 DIP text field with a 9 DIP radius, bound to *value.
func Input(c *ui.Context, key any, value *string, placeholder string) ui.Element {
	k := T(c)
	var input ui.Element
	wrap := ui.Row(c.Key(key)).Height(ControlHeight).PaddingX(10).Radius(RadiusInput).Background(k.Fill).
		Cursor(ui.CursorText).MinWidth(0)
	wrap.Children(func() {
		input = ui.TextInputBase(c, value).Grow(1).MinWidth(0).FontSize(FontBody).SingleLine().
			Placeholder(placeholder).TextColor(k.Label)
		if placeholder != "" {
			input.Label(placeholder)
		}
	})
	if input.Focused() {
		wrap.Border(1.5, k.Accent.Alpha(0.6))
	}
	noteTextFocus(c, input)
	return input
}

// TextArea is a multi-line field bound to *value.
func TextArea(c *ui.Context, key any, value *string, placeholder string, mono bool) ui.Element {
	k := T(c)
	area := ui.TextAreaBase(c.Key(key), value).Padding(8, 10).Radius(RadiusInput).Background(k.Fill).
		FontSize(FontTable).Placeholder(placeholder).MinHeight(90).TextColor(k.Label)
	if mono {
		area.Font(MonoFont)
	}
	if area.Focused() {
		area.Border(1.5, k.Accent.Alpha(0.6))
	}
	noteTextFocus(c, area)
	return area
}

// MonoFont is the family of raw messages and code.
const MonoFont = "monospace"
