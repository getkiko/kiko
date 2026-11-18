package kit

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// Workspace is a page's root: a column filling its area on the white
// workspace surface.
func Workspace(c *ui.Context, fn func()) ui.Element {
	return ui.Column(c).Fill().MinWidth(0).MinHeight(0).Background(T(c).Surface).Children(fn)
}

// Toolbar is a page's 64 DIP toolbar: a 16 DIP title with an optional
// subtitle (statistics), then the actions fn builds, pushed to the right.
// The toolbar's empty space drags the window.
func Toolbar(c *ui.Context, title, subtitle string, fn func()) ui.Element {
	k := T(c)
	return ui.Row(c).Height(ToolbarHeight).PaddingX(20).Gap(10).Shrink(0).DragWindow().Children(func() {
		ui.Column(c).Gap(1).Shrink(0).Children(func() {
			ui.Text(c, title).FontSize(FontToolbar).FontWeight(600).SingleLine().TextColor(k.Label)
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(FontCaption).SingleLine().TextColor(k.Label3).FontFeatures("tnum")
			}
		})
		ui.Spacer(c)
		if fn != nil {
			fn()
		}
	})
}

// SectionHeading is a 13 DIP group title with a count and actions on its
// right, at least 52 DIPs high. Its empty space drags the window.
func SectionHeading(c *ui.Context, title, count string, actions func()) ui.Element {
	k := T(c)
	return ui.Row(c).MinHeight(52).Gap(8).Shrink(0).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(FontSection).FontWeight(600).TextColor(k.Label).SingleLine()
		if count != "" {
			ui.Text(c, count).FontSize(FontCaption).TextColor(k.Label3).SingleLine().FontFeatures("tnum")
		}
		ui.Spacer(c)
		if actions != nil {
			ui.Row(c).Gap(6).Children(actions)
		}
	})
}

// Separator is a hairline across its row or column.
func Separator(c *ui.Context) ui.Element {
	return ui.Box(c).Height(1).FillWidth().Background(T(c).HairlineSoft).Shrink(0)
}

// VSeparator is a vertical hairline.
func VSeparator(c *ui.Context) ui.Element {
	return ui.Box(c).Width(1).FillHeight().Background(T(c).HairlineSoft).Shrink(0)
}

// FilterVariant chooses a filter pane's look.
type FilterVariant int

const (
	// FilterDefault has groups that fold, with square check marks, as the
	// DNS servers' filters.
	FilterDefault FilterVariant = iota
	// FilterConnection has open groups, compact options and counts, as the
	// connection monitor's.
	FilterConnection
)

// FilterPane is the flat filter pane of a monitor: a solid column with a
// hairline on its right, a header with "Clear" when something is filtered,
// and the groups fn builds, scrolling.
func FilterPane(c *ui.Context, variant FilterVariant, filtered bool, onClear func(), fn func()) ui.Element {
	return SizedFilterPane(c, variant, 0, filtered, onClear, fn)
}

// FilterPaneWidth is the default width of a filter pane of variant.
func FilterPaneWidth(c *ui.Context, variant FilterVariant) float32 {
	if variant != FilterConnection {
		return FilterWidth
	}
	if w, _ := c.Size(); w <= 1200 {
		return 166
	}
	return MonitorFilterW
}

// SizedFilterPane is a FilterPane width DIPs wide; 0 is the default width.
func SizedFilterPane(c *ui.Context, variant FilterVariant, width float32, filtered bool, onClear func(), fn func()) ui.Element {
	k := T(c)
	if width <= 0 {
		width = FilterPaneWidth(c, variant)
	}
	return ui.Column(c).Width(width).Shrink(0).Background(k.Surface2).BorderWidth(0, 1, 0, 0).
		BorderColor(k.HairlineSoft).Label("筛选").Children(func() {
		ui.Row(c).Height(36).PaddingX(14).Shrink(0).Children(func() {
			ui.Text(c, "筛选").FontSize(FontCaption).FontWeight(700).TextColor(k.Label3)
			ui.Spacer(c)
			if filtered && onClear != nil {
				clear := ui.ButtonBase(c).Label("清除筛选").TextColor(k.AccentText).FontSize(FontCaption)
				clear.Children(func() { ui.Text(c, "清除") }).OnClick(onClear)
			}
		})
		ui.Scroll(c).Grow(1).Padding(0, 11, 20).Gap(18).Children(fn)
	})
}

// FilterGroup is a titled group of filter options. Connection groups stay
// open, scrolling with their pane (FacetGroup folds long ones); default
// groups fold under a 36 DIP title row, open while *open.
func FilterGroup(c *ui.Context, variant FilterVariant, title string, open *bool, fn func()) ui.Element {
	k := T(c)
	if variant == FilterConnection {
		return ui.Column(c).Gap(2).Shrink(0).Children(func() {
			ui.Text(c, title).FontSize(10).FontWeight(700).TextColor(k.Label3).Padding(0, 3, 4)
			ui.Column(c).Gap(1).Children(fn)
		})
	}
	return ui.Column(c).Shrink(0).Children(func() {
		head := ui.ButtonBase(c).Height(36).PaddingX(3).Gap(6).FillWidth().Justify(ui.Start).Label(title)
		head.Children(func() {
			ui.Text(c, title).FontSize(FontBody).FontWeight(600).TextColor(k.Label).Grow(1)
			chev := Icon(c, SymChevronDown, 13).TextColor(k.Label3)
			if open != nil && !*open {
				chev.Rotate(-90)
			}
		})
		if head.Clicked() && open != nil {
			*open = !*open
		}
		if open == nil || *open {
			ui.Column(c).Gap(1).Children(fn)
		}
	})
}

// filterWeight is the font weight of a filter option's label.
func filterWeight(variant FilterVariant, selected bool) int {
	if selected && variant == FilterConnection {
		return 600
	}
	return 400
}

// FilterOption is an option of a filter group: an optional icon, a label
// and a count; selected options show a weak accent fill and accent text.
// Read Clicked on the element it returns.
func FilterOption(c *ui.Context, variant FilterVariant, selected bool, icon func(), label string, count int) ui.Element {
	k := T(c)
	o := ui.ButtonBase(c).MinHeight(26).PaddingX(7).Gap(7).Radius(7).FontSize(FontTable).Label(label).
		Transition(HoverMotion)
	if variant == FilterDefault {
		o.MinHeight(28)
	}
	switch {
	case selected && variant == FilterConnection:
		o.Background(k.AccentWeak).TextColor(k.AccentText).FontWeight(600)
	case o.Hovered():
		o.Background(k.Fill)
	}
	if !selected || variant != FilterConnection {
		o.TextColor(k.Label)
	}
	o.Children(func() {
		if variant == FilterDefault {
			mark := ui.Box(c).Size(14, 14).Radius(3.5).Center().Border(1, k.Hairline).Shrink(0)
			if selected {
				mark.Background(k.Accent).Border(0, k.Accent).TextColor(k.OnAccent).Children(func() { Icon(c, SymCheck, 10) })
			}
		}
		if icon != nil {
			icon()
		}
		Clipped(c, label, FontTable, filterWeight(variant, selected)).Grow(1)
		if count >= 0 {
			ui.Text(c, fmt.Sprint(count)).FontSize(10).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
		}
	})
	return o
}

// Inspector is the in-place detail panel on the right of a table: an
// object title with a subtitle, local actions, a close button, and the
// sections fn builds, scrolling. onClose may be nil.
func Inspector(c *ui.Context, title, subtitle string, icon func(), actions func(), onClose func(), fn func()) ui.Element {
	return inspector(c, false, title, subtitle, icon, actions, onClose, fn)
}

// ScrollingInspector is Inspector with its header scrolling away with the
// details, as the first part of them.
func ScrollingInspector(c *ui.Context, title, subtitle string, icon func(), actions func(), onClose func(), fn func()) ui.Element {
	return inspector(c, true, title, subtitle, icon, actions, onClose, fn)
}

func inspector(c *ui.Context, scrolling bool, title, subtitle string, icon func(), actions func(), onClose func(), fn func()) ui.Element {
	k := T(c)
	header := func() {
		row := ui.Row(c).MinHeight(52).Padding(10, 12, 10, 16).Gap(10).Shrink(0)
		// A header scrolling with the details reads as their first part,
		// without a line under it.
		if !scrolling {
			row.BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft)
		}
		row.Children(func() {
			if icon != nil {
				icon()
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, title).FontSize(FontBody).FontWeight(600).MaxLines(2).Selectable()
				if subtitle != "" {
					ui.Text(c, subtitle).FontSize(FontCaption).TextColor(k.Label3).MaxLines(2).Selectable()
				}
			})
			if actions != nil {
				actions()
			}
			if onClose != nil {
				IconButton(c, SymX, "关闭详情", IconButtonOptions{Size: 26, IconSize: 14}).OnClick(onClose)
			}
		})
	}
	return ui.Column(c).Width(InspectorWidth).Shrink(0).Background(k.Surface).BorderWidth(0, 0, 0, 1).
		BorderColor(k.HairlineSoft).Label("详情").Children(func() {
		if !scrolling {
			header()
			ui.Scroll(c).Grow(1).Padding(8, 16, 20).Gap(14).Children(fn)
			return
		}
		ui.Scroll(c).Grow(1).Children(func() {
			header()
			ui.Column(c).Padding(8, 16, 20).Gap(14).Shrink(0).Children(fn)
		})
	})
}

// Section is a titled group of an inspector or a form.
func Section(c *ui.Context, title string, trailing func(), fn func()) ui.Element {
	k := T(c)
	return ui.Column(c).Gap(2).Shrink(0).Children(func() {
		if title != "" || trailing != nil {
			ui.Row(c).MinHeight(24).Children(func() {
				ui.Text(c, title).FontSize(FontCaption).FontWeight(700).TextColor(k.Label3).Grow(1)
				if trailing != nil {
					trailing()
				}
			})
		}
		fn()
	})
}

// Property is a compact key-value row of an inspector; the value wraps and
// can be selected and copied.
func Property(c *ui.Context, label, value string) ui.Element {
	return PropertyWith(c, label, func() {
		ui.Text(c, value).FontSize(FontTable).Selectable().Grow(1).MinWidth(0).Wrap()
	})
}

// PropertyWith is Property with a value of its own.
func PropertyWith(c *ui.Context, label string, value func()) ui.Element {
	k := T(c)
	return ui.Row(c).MinHeight(24).Gap(10).AlignItems(ui.Start).PaddingY(3).Children(func() {
		ui.Text(c, label).FontSize(FontTable).TextColor(k.Label3).Width(92).Shrink(0)
		ui.Row(c).Grow(1).MinWidth(0).Gap(6).Children(value)
	})
}

// LinkProperty is Property with a value that leads elsewhere: accent text
// that runs open when clicked; hint names it for assistive technology.
func LinkProperty(c *ui.Context, label, value, hint string, open func()) ui.Element {
	k := T(c)
	return PropertyWith(c, label, func() {
		link := ui.ButtonBase(c).Grow(1).MinWidth(0).Justify(ui.Start).Label(hint).TextColor(k.AccentText).FontSize(FontTable)
		if link.Hovered() {
			link.Opacity(0.75)
		}
		link.Children(func() { ui.Text(c, value).Grow(1).MinWidth(0).Wrap() })
		if link.Clicked() {
			open()
		}
	})
}

// Disclosure is a section that folds under its title, open while *open.
func Disclosure(c *ui.Context, title string, open *bool, fn func()) ui.Element {
	k := T(c)
	return ui.Column(c).Shrink(0).Children(func() {
		head := ui.ButtonBase(c).Height(26).Gap(4).FillWidth().Justify(ui.Start).Label(title)
		head.Children(func() {
			chev := Icon(c, SymChevronRight, 12).TextColor(k.Label3)
			if *open {
				chev.Rotate(90)
			}
			ui.Text(c, title).FontSize(FontCaption).FontWeight(700).TextColor(k.Label3)
		})
		if head.Clicked() {
			*open = !*open
		}
		if *open {
			ui.Column(c).Gap(2).PaddingStart(16).Children(fn)
		}
	})
}

// CodeBlock shows raw text in the monospaced font, selectable.
func CodeBlock(c *ui.Context, text string) ui.Element {
	k := T(c)
	return ui.Box(c).Padding(8, 10).Radius(8).Background(k.Surface2).Children(func() {
		ui.Text(c, text).Font(MonoFont).FontSize(11).Selectable().Wrap().TextColor(k.Label2)
	})
}

// EmptyState fills its area: an icon, a short title, a description and an
// optional action, centered.
func EmptyState(c *ui.Context, icon Symbol, title, description string, action func()) ui.Element {
	k := T(c)
	return ui.Column(c).Grow(1).Fill().Center().Gap(8).Padding(24).Children(func() {
		if icon != "" {
			Icon(c, icon, 28).TextColor(k.Label4).Margin(0, 0, 4)
		}
		ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(k.Label2).TextAlign(ui.Center)
		if description != "" {
			ui.Text(c, description).FontSize(FontBody).TextColor(k.Label3).TextAlign(ui.Center).MaxWidth(360)
		}
		if action != nil {
			ui.Row(c).Margin(6, 0, 0).Children(action)
		}
	})
}

// Notice is a short message beside what it concerns, with a semantic icon.
func Notice(c *ui.Context, tone Tone, title, message string, action func()) ui.Element {
	k := T(c)
	fg, bg := tone.colors(k)
	icon := SymInfo
	if tone == WarningTone || tone == DangerTone {
		icon = SymTriangleAlert
	}
	return ui.Row(c).Padding(10, 12).Gap(10).Radius(10).Background(bg).AlignItems(ui.Start).Shrink(0).Children(func() {
		Icon(c, icon, 15).TextColor(fg).Margin(1, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			if title != "" {
				ui.Text(c, title).FontSize(FontBody).FontWeight(600).TextColor(k.Label)
			}
			if message != "" {
				ui.Text(c, message).FontSize(FontTable).TextColor(k.Label2).Selectable().Wrap()
			}
		})
		if action != nil {
			action()
		}
	})
}

// StatusBar is the footer under a table: statistics on the left, center
// text and pagination on the right.
func StatusBar(c *ui.Context, left, center string, right func()) ui.Element {
	k := T(c)
	return ui.Row(c).Height(32).PaddingX(16).Gap(12).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
		FontSize(FontCaption).TextColor(k.Label3).Children(func() {
		ui.Text(c, left).SingleLine().FontFeatures("tnum").Grow(1).Basis(0)
		ui.Text(c, center).SingleLine().FontFeatures("tnum")
		ui.Row(c).Grow(1).Basis(0).Justify(ui.End).Children(func() {
			if right != nil {
				right()
			}
		})
	})
}

// Pagination moves *page (0-based) among pages.
func Pagination(c *ui.Context, page *int, pages int) {
	k := T(c)
	if pages < 1 {
		pages = 1
	}
	*page = min(max(*page, 0), pages-1)
	ui.Row(c).Gap(4).Children(func() {
		IconButton(c, SymChevronLeft, "上一页", IconButtonOptions{Size: 22, IconSize: 13, Disabled: *page == 0}).OnClick(func() {
			*page = max(*page-1, 0)
		})
		ui.Text(c, fmt.Sprintf("%d / %d", *page+1, pages)).FontFeatures("tnum").TextColor(k.Label2)
		IconButton(c, SymChevronRight, "下一页", IconButtonOptions{Size: 22, IconSize: 13, Disabled: *page >= pages-1}).OnClick(func() {
			*page = min(*page+1, pages-1)
		})
	})
}
