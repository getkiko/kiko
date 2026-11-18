// Package kit is the native console's component library: Liquid Glass
// controls (the app navigation, grouped toolbar actions, search, overlays)
// over a UniFi workspace (flat filter panes, white tables, inspectors,
// status bars).
//
// Every component takes its colors from Tokens, the design variables, and
// its sizes from the constants below, the measures of
// the console's UI standard. Pages compose these components and
// style only their own content.
package kit

import "github.com/egoist/mygo/ui"

// Tokens are the console's semantic colors, light or dark.
type Tokens struct {
	Dark bool

	Canvas        ui.Color // window and source-list background
	Surface       ui.Color // tables, groups and cards
	Surface2      ui.Color // group headers, secondary surfaces
	Elevated      ui.Color // sheets and popovers
	Hairline      ui.Color // opaque separators
	HairlineSoft  ui.Color // soft separators
	Sidebar       ui.Color // app navigation
	SidebarHover  ui.Color
	SidebarActive ui.Color

	Label  ui.Color // primary text
	Label2 ui.Color // secondary text
	Label3 ui.Color // tertiary text, headers
	Label4 ui.Color // disabled text

	Accent      ui.Color
	AccentPress ui.Color
	AccentWeak  ui.Color // selected filter fill
	RowChosen   ui.Color // the chosen row of a flat table
	AccentText  ui.Color // text in the accent color
	OnAccent    ui.Color // text on the accent

	Danger      ui.Color
	DangerWeak  ui.Color
	Success     ui.Color
	SuccessWeak ui.Color
	Warning     ui.Color
	WarningWeak ui.Color

	Fill  ui.Color // control fill
	Fill2 ui.Color // pressed control fill

	Stripe    ui.Color // alternate table rows
	RowHover  ui.Color // a hovered list row
	Glass     ui.Color // glass control groups
	GlassEdge ui.Color
	Shadow    ui.Color
	Scrim     ui.Color // behind dialogs
}

var light = Tokens{
	Canvas:        ui.Hex("#F2F2F7"),
	Surface:       ui.Hex("#FFFFFF"),
	Surface2:      ui.Hex("#F7F7FA"),
	Elevated:      ui.Hex("#FFFFFF"),
	Hairline:      ui.Hex("#C6C6C8"),
	HairlineSoft:  ui.Hex("#E5E5EA"),
	Sidebar:       ui.Hex("#E9ECF0"),
	SidebarHover:  ui.Hex("#DDE2E8"),
	SidebarActive: ui.Hex("#C9DAEE"),
	Label:         ui.Hex("#1C1C1E"),
	Label2:        ui.Hex("#4A4A4F"),
	Label3:        ui.Hex("#8A8A8F"),
	Label4:        ui.Hex("#B9B9BE"),
	Accent:        ui.Hex("#007AFF"),
	AccentPress:   ui.Hex("#0062CC"),
	AccentWeak:    ui.Hex("#E5F1FF"),
	RowChosen:     ui.Hex("#DEEEFF"),
	AccentText:    ui.Hex("#0066CC"),
	OnAccent:      ui.Hex("#FFFFFF"),
	Danger:        ui.Hex("#C0392B"),
	DangerWeak:    ui.Hex("#FDECEA"),
	Success:       ui.Hex("#15803D"),
	SuccessWeak:   ui.Hex("#DCFCE7"),
	Warning:       ui.Hex("#9B854A"),
	WarningWeak:   ui.Hex("#FBF4DF"),
	Fill:          ui.Hex("#78788020"),
	Fill2:         ui.Hex("#7878802E"),
	Stripe:        ui.Hex("#F7F7F7"),
	RowHover:      ui.Hex("#E7E7EA"),
	Glass:         ui.RGBA(255, 255, 255, 0.72),
	GlassEdge:     ui.RGBA(0, 0, 0, 0.08),
	Shadow:        ui.RGBA(0, 0, 0, 0.10),
	Scrim:         ui.RGBA(0, 0, 0, 0.22),
}

var dark = Tokens{
	Dark:          true,
	Canvas:        ui.Hex("#000000"),
	Surface:       ui.Hex("#1C1C1E"),
	Surface2:      ui.Hex("#333336"),
	Elevated:      ui.Hex("#2C2C2E"),
	Hairline:      ui.Hex("#48484A"),
	HairlineSoft:  ui.Hex("#3A3A3C"),
	Sidebar:       ui.Hex("#25272B"),
	SidebarHover:  ui.Hex("#383C42"),
	SidebarActive: ui.Hex("#243E56"),
	Label:         ui.Hex("#FFFFFF"),
	Label2:        ui.Hex("#C7C7CC"),
	Label3:        ui.Hex("#8E8E93"),
	Label4:        ui.Hex("#5A5A5F"),
	Accent:        ui.Hex("#0A84FF"),
	AccentPress:   ui.Hex("#0071E3"),
	AccentWeak:    ui.Hex("#102A43"),
	RowChosen:     ui.Hex("#102A43"),
	AccentText:    ui.Hex("#64B5FF"),
	OnAccent:      ui.Hex("#FFFFFF"),
	Danger:        ui.Hex("#FF6B6B"),
	DangerWeak:    ui.Hex("#3B1513"),
	Success:       ui.Hex("#4ADE80"),
	SuccessWeak:   ui.Hex("#052E16"),
	Warning:       ui.Hex("#E3C77C"),
	WarningWeak:   ui.Hex("#3A3016"),
	Fill:          ui.Hex("#7878805C"),
	Fill2:         ui.Hex("#78788078"),
	Stripe:        ui.Hex("#222224"),
	RowHover:      ui.Hex("#323236"),
	Glass:         ui.RGBA(44, 44, 46, 0.78),
	GlassEdge:     ui.RGBA(255, 255, 255, 0.12),
	Shadow:        ui.RGBA(0, 0, 0, 0.45),
	Scrim:         ui.RGBA(0, 0, 0, 0.45),
}

// T returns the tokens of the window's appearance.
func T(c *ui.Context) *Tokens {
	if c.Theme().Dark {
		return &dark
	}
	return &light
}

// Sizes of the console's UI standard, in DIPs.
const (
	FontBody    = 13 // settings text
	FontTable   = 12 // monitor lists
	FontCaption = 11 // secondary text and table headers
	FontToolbar = 16 // toolbar titles
	FontHeading = 22 // settings content titles
	FontSection = 13 // section headings

	ControlHeight     = 30 // buttons, inputs, selects
	SmallControl      = 26 // small buttons
	SegmentHeight     = 32 // segmented controls and search
	ToolbarGroupH     = 38 // glass groups in the toolbar
	ToolbarHeight     = 64
	RowHeight         = 32   // compact table rows and headers
	TwoLineRowH       = 48   // detailed table rows
	InlineSelectH     = 24   // selects inside tables
	TableInset        = 8    // horizontal cell padding
	TableCellInset    = 10   // padding inside each cell, either side of the column hairline
	SettingsWidth     = 1120 // settings content
	SettingsFormWidth = 720  // a single column of settings, as a subpage
	SettingsColumn    = 356  // narrowest settings column; narrower content stacks them
	SettingsGap       = 24   // between settings columns
	SettingsRowH      = 52
	FilterWidth       = 208 // default filter pane
	MonitorFilterW    = 188 // connection monitor filter pane
	InspectorWidth    = 340
	EditorWidth       = 460 // edit dialogs and the editor sidebar
	AlertWidth        = 340

	RadiusInput   = 9
	RadiusGroup   = 18 // settings groups
	RadiusNav     = 20 // app navigation
	RadiusDialog  = 26
	RadiusCapsule = 999
)

// Apply sets the window's theme from the tokens: the widgets of package ui
// then draw in the console's colors and metrics. Call it at the start of
// every frame; it follows the system's appearance.
func Apply(c *ui.Context) {
	base := c.Theme()
	k := T(c)
	t := *base
	t.Background = k.Surface
	t.Text = k.Label
	t.TextMuted = k.Label3
	t.Border = k.HairlineSoft
	t.Accent = k.Accent
	t.AccentPressed = k.AccentPress
	t.AccentHover = k.Accent.Mix(k.AccentPress, 0.4)
	t.AccentText = k.OnAccent
	t.SurfaceHover = k.RowHover
	t.Danger = k.Danger
	t.Success = k.Success
	t.Warning = k.Warning
	t.Radius = RadiusInput
	t.FontSize = FontBody
	c.SetTheme(&t)
}
