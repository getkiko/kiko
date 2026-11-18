package kit

import "github.com/egoist/mygo/ui"

// Preferences is a settings page's scrolling body: a light gray window
// surface with the content centered, at most SettingsWidth wide.
func Preferences(c *ui.Context, fn func()) ui.Element {
	return preferences(c, SettingsWidth, fn)
}

// PreferencesForm is Preferences for a single column of settings, such as
// a subpage, at most SettingsFormWidth wide.
func PreferencesForm(c *ui.Context, fn func()) ui.Element {
	return preferences(c, SettingsFormWidth, fn)
}

func preferences(c *ui.Context, width float32, fn func()) ui.Element {
	k := T(c)
	return ui.Scroll(c).Grow(1).MinHeight(0).Background(k.Canvas).Padding(0, 28, 40).Children(func() {
		// The top margin drags the window, as a toolbar does.
		ui.Box(c).Height(28).FillWidth().Shrink(0).DragWindow()
		ui.Row(c).Justify(ui.Center).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).MaxWidth(width).Children(fn)
		})
	})
}

// SettingsColumns lays settings groups out in columns from left to right:
// side by side while each column gets SettingsColumn DIPs, and stacked in
// order below that.
func SettingsColumns(c *ui.Context, columns ...func()) ui.Element {
	return ui.Row(c).Wrap().GapX(SettingsGap).AlignItems(ui.Start).Children(func() {
		for _, fn := range columns {
			ui.Column(c).Grow(1).Basis(SettingsColumn).MinWidth(0).Children(fn)
		}
	})
}

// PaneTitle is a settings page's 22 DIP title with a description and a
// trailing control.
func PaneTitle(c *ui.Context, title, description string, trailing func()) ui.Element {
	k := T(c)
	return ui.Row(c).Gap(16).Margin(0, 0, 26).Shrink(0).DragWindow().Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
			ui.Text(c, title).FontSize(FontHeading).FontWeight(600)
			if description != "" {
				ui.Text(c, description).FontSize(FontTable).TextColor(k.Label2).MaxWidth(440)
			}
		})
		if trailing != nil {
			trailing()
		}
	})
}

// SettingsGroup is a rounded group of settings rows with a title above and
// a footer note below. Rows are separated by hairlines.
func SettingsGroup(c *ui.Context, title, footer string, fn func()) ui.Element {
	k := T(c)
	return ui.Column(c).Margin(0, 0, 25).Shrink(0).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(FontBody).FontWeight(600).Margin(0, 14, 9)
		}
		ui.Column(c).Radius(RadiusGroup).Background(k.Surface).Border(1, k.Label.Alpha(0.04)).Clip().
			PaddingX(16).Dividers(1, k.HairlineSoft).Children(fn)
		if footer != "" {
			ui.Text(c, footer).FontSize(FontCaption).TextColor(k.Label3).Margin(7, 14, 0)
		}
	})
}

// SettingsRow is a row of a settings group: a label with a description on
// the left, the control on the right, at least 52 DIPs high.
func SettingsRow(c *ui.Context, label, description string, control func()) ui.Element {
	k := T(c)
	return ui.Row(c).MinHeight(SettingsRowH).PaddingY(10).Gap(14).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(FontBody)
			if description != "" {
				ui.Text(c, description).FontSize(FontCaption).TextColor(k.Label3).Wrap()
			}
		})
		if control != nil {
			ui.Row(c).Gap(8).Shrink(0).Children(control)
		}
	})
}
