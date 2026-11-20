package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Hero card measures, those of the Components page design: a compact
// card about 104 DIPs high, rounded 18, its content inset 13/15/12 with
// 5 DIP lines, a glow of 84 DIPs whose center sits 16 DIPs inside the
// top right corner, and a background trend 34 DIPs high along the
// bottom.
const (
	HeroMinHeight = 104
	HeroRadius    = 18
	heroGlow      = 84
	heroGlowAt    = -26
	heroSparkH    = 34
	heroMoreSize  = 26
	// heroMoreInset keeps the header (a wide card's content) clear of
	// the more button, 11 DIPs inside the corner, by 4 DIPs.
	heroMoreInset = 11 + heroMoreSize + 4 - 15
)

// CardSurface styles e as a dashboard card: the surface color, rounded
// radius, a soft shadow and, in dark mode, where the shadow does not read
// on the black canvas, a soft border. The Overview cards and HeroCard share
// it.
func CardSurface(c *ui.Context, e ui.Element, radius float32) ui.Element {
	k := T(c)
	e.Radius(radius).Background(k.Surface).Shadow(0, 1, 3, 0, k.Shadow)
	if k.Dark {
		e.Border(1, k.HairlineSoft)
	}
	return e
}

// HeroCardOptions describe a hero card (HeroCard).
type HeroCardOptions struct {
	// Label names the card's button, which opens the entity's editor:
	// "Edit <name>".
	Label string
	// Glow is the type's color (TypeLook) of the glow in the top right
	// corner; the zero color draws none.
	Glow ui.Color
	// Muted shows a disabled entity: the header and the main value in
	// Label3, at full opacity.
	Muted bool
	// Span is how many columns of its grid the card spans, 1 when 0.
	Span int
	// MinHeight is the card's least height, HeroMinHeight when 0.
	MinHeight float32
	// StrongHeader makes the header the card's title: 16 DIP semibold in
	// Label (Label3 when Muted), its tags keeping their own size.
	StrongHeader bool
	// HeaderEnd builds the end of the header, right aligned, as a type
	// tag. The more button takes its place while it shows, rather than a
	// space kept for it at the end of every header.
	HeaderEnd func()
	// Header builds the first line, a row of 12 DIP Label2 text (HeroDot,
	// HeroInitial or a flag, HeroName, a Spacer, HeroTag). Main builds the
	// main value's row in Label (HeroValue, HeroMono, HeroLatency). Footer
	// builds the bottom line, a row of 11 DIP Label3 text kept at the
	// bottom.
	Header, Main, Footer func()
	// Content, when set, builds the card's content in place of Header,
	// Main and Footer, in a column with 5 DIP gaps, kept clear of the
	// more button: a wide card lays out its own row.
	Content func()
	// Spark is a trend drawn faintly along the bottom, under the content
	// (Sparkline), in SparkColor; fewer than two values draw none.
	Spark      []float64
	SparkColor ui.Color
	// OnOpen runs as the card is clicked.
	OnOpen func()
	// Menu lists the more menu's actions; with OnEnabledChange the menu
	// starts with "Enable" or "Disable", by Enabled. Without both the card has no
	// more button.
	Menu            []EntityMenuItem
	Enabled         bool
	OnEnabledChange func(enabled bool)
	// KeepMore keeps the more button shown, as while a confirmation
	// anchored to it is open.
	KeepMore bool
	// ContextMenu gives the card a context menu listing what the more
	// menu lists, a secondary click on the card opening it.
	ContextMenu bool
	// Focus moves the keyboard focus to the card (FocusMore to its more
	// button) this frame, as an editor closes.
	Focus, FocusMore bool
}

// HeroCard is a compact card putting one fact of an entity first: a
// header line, a large main value and a bottom line, over a glow of the
// type's color in the top right corner and, optionally, a faint trend
// along the bottom. The whole card is one button (Label, OnOpen); its
// more button ("More actions", 26 DIPs) shows in the top right corner while
// the card is hovered, its menu is open, it has the keyboard focus or
// KeepMore. Cards in a grid row stretch to the same height.
//
// It returns the more button, mounted while hidden, for anchoring
// confirmations started from the menu; the zero Element without a menu.
func HeroCard(c *ui.Context, key any, o HeroCardOptions) ui.Element {
	k := T(c)
	minHeight := o.MinHeight
	if minHeight <= 0 {
		minHeight = HeroMinHeight
	}
	card := CardSurface(c, ui.Column(c.Key(key)), HeroRadius).MinWidth(0).MinHeight(minHeight).
		AlignItems(ui.Stretch).Clip().Transition(HoverMotion)
	if o.Span > 1 {
		card.ColumnSpan(o.Span)
	}
	// Hovered, the shadow deepens (a second, wider one) or, in dark mode,
	// the border.
	hovered := card.Hovered()
	if hovered {
		if k.Dark {
			card.Border(1, k.Hairline)
		} else {
			card.Shadow(0, 2, 10, 0, k.Shadow)
		}
	}
	menuOpen := ui.Local(card, "menu", func() bool { return false })
	// moreFocused is whether the more button had the keyboard focus last
	// frame, which shows it, read before the button builds.
	moreFocused := ui.Local(card, "more-focused", func() bool { return false })
	head, value := k.Label2, k.Label
	if o.Muted {
		head, value = k.Label3, k.Label3
	}
	hasMore := o.OnEnabledChange != nil || len(o.Menu) > 0
	var more ui.Element
	card.Children(func() {
		if o.Glow != (ui.Color{}) {
			glow := float32(0.14)
			if k.Dark {
				glow = 0.18
			}
			ui.Box(c).Absolute().Top(heroGlowAt).Right(heroGlowAt).Size(heroGlow, heroGlow).Radius(heroGlow / 2).
				Background(o.Glow.Alpha(glow)).PassThrough()
		}
		if len(o.Spark) >= 2 {
			Sparkline(c, o.Spark, o.SparkColor, SparklineOptions{Height: heroSparkH}).
				Absolute().Left(0).Right(0).Bottom(0).PassThrough()
		}
		body := ui.ButtonBase(c).Column().Grow(1).MinWidth(0).Padding(13, 15, 12).Gap(5).Radius(HeroRadius).
			AlignItems(ui.Stretch).Justify(ui.Start).Label(o.Label)
		if hasMore && o.Content != nil {
			body.PaddingEnd(15 + heroMoreInset)
		}
		if body.Pressed() {
			body.Opacity(0.75)
		}
		if o.Focus {
			body.Focus()
		}
		body.Children(func() {
			if o.Content != nil {
				o.Content()
				return
			}
			if o.Header != nil {
				header := ui.Row(c).Gap(8).MinHeight(22).MinWidth(0).AlignItems(ui.Center).FontSize(FontTable).
					TextColor(head)
				if o.StrongHeader {
					header.FontSize(16).FontWeight(600).TextColor(value).MinHeight(24)
				}
				if hasMore && o.HeaderEnd == nil {
					header.PaddingEnd(heroMoreInset)
				}
				header.Children(func() {
					o.Header()
					if o.HeaderEnd == nil {
						return
					}
					ui.Spacer(c)
					end := ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).MinWidth(0).Transition(HoverMotion)
					if hasMore && (hovered || *menuOpen || o.KeepMore || o.FocusMore || *moreFocused) {
						end.Opacity(0)
					}
					end.Children(o.HeaderEnd)
				})
			}
			if o.Main != nil {
				ui.Row(c).Gap(6).MinWidth(0).AlignItems(ui.Center).TextColor(value).Children(o.Main)
			}
			if o.Footer != nil {
				ui.Spacer(c)
				ui.Row(c).Gap(6).MinHeight(19).MinWidth(0).AlignItems(ui.Center).FontSize(FontCaption).
					TextColor(k.Label3).Children(o.Footer)
			}
		})
		if body.Clicked() && o.OnOpen != nil {
			o.OnOpen()
		}
		if !hasMore {
			return
		}
		// The more button sits in the top right corner, past the header's
		// end (heroMoreInset).
		holder := ui.Box(c).Absolute().Top(11).Right(11).Radius(RadiusCapsule)
		holder.Children(func() {
			more = IconButton(c, SymEllipsis, "更多操作", IconButtonOptions{Size: heroMoreSize, IconSize: 15})
		})
		if o.FocusMore {
			more.Focus()
		}
		if more.Clicked() {
			*menuOpen = !*menuOpen
		}
		// It comes in growing from a little smaller, as it fades in, and
		// leaves faster, in frames that only paint. Fade 1 is gone.
		*moreFocused = more.FocusVisible()
		if hovered || *menuOpen || o.KeepMore || o.FocusMore || more.FocusVisible() {
			holder.AnimatePose(ui.Pose{Scale: 1}, heroMoreIn)
		} else {
			holder.AnimatePose(ui.Pose{Y: -2, Scale: 0.8, Fade: 1}, heroMoreOut)
		}
		entityMenu(c, more, menuOpen, o.Enabled, o.OnEnabledChange, o.Menu)
	})
	if hasMore && o.ContextMenu {
		// The menu builds here, where the item chosen runs: after the more
		// button, its items' anchor.
		card.ContextMenu(func(m *ui.Menu) { entityContextMenu(m, more, o.Enabled, o.OnEnabledChange, o.Menu) })
	}
	return more
}

// heroMoreIn and heroMoreOut are how a hero card's more button shows
// and hides: entering decelerates, leaving accelerates.
var (
	heroMoreIn  = ui.Animation{Duration: 160 * time.Millisecond, Ease: ui.CubicBezier(0.2, 0.8, 0.2, 1)}
	heroMoreOut = ui.Animation{Duration: 110 * time.Millisecond, Ease: ui.EaseIn}
)

// HeroColumns is how many hero cards fit a row at the window's width:
// one under 640 DIPs, two under 900, three under 1200, else four.
func HeroColumns(c *ui.Context) int {
	w, _ := c.Size()
	switch {
	case w >= 1200:
		return 4
	case w >= 900:
		return 3
	case w >= 640:
		return 2
	}
	return 1
}

// AddCard is the last cell of a hero card grid: a dashed outline as
// large as a card reading "＋ label" ("＋ Add inbound"), which a section's
// add action opens. label names the button. Read Clicked on it.
func AddCard(c *ui.Context, label string) ui.Element { return AddCardHeight(c, label, HeroMinHeight) }

// AddCardHeight is AddCard as high as cards of the least height h.
func AddCardHeight(c *ui.Context, label string, h float32) ui.Element {
	k := T(c)
	b := ui.ButtonBase(c).MinWidth(0).MinHeight(h).Radius(HeroRadius).Border(1.5, k.Label4).
		BorderStyle(ui.BorderDashed).Center().Label(label).Transition(HoverMotion)
	switch {
	case b.Pressed():
		b.Background(k.Fill2)
	case b.Hovered():
		b.Background(k.Fill.Alpha(0.6))
	}
	b.Children(func() {
		ui.Text(c, "＋ "+label).FontSize(FontBody).TextColor(k.Label3).SingleLine().Ellipsis("…").MinWidth(0)
	})
	return b
}

// HeroDot is the 7 DIP state dot of a hero card's header: green while
// on, faint otherwise.
func HeroDot(c *ui.Context, on bool) ui.Element {
	color := T(c).Label4
	if on {
		color = systemColor(c, sysGreen)
	}
	return ui.Box(c).Size(7, 7).Radius(3.5).Background(color).Shrink(0)
}

// HeroInitial is the 22 DIP tile of a name's first character in a hero
// card's header.
func HeroInitial(c *ui.Context, text string) ui.Element {
	k := T(c)
	tile := ui.Box(c).Size(22, 22).Radius(6).Center().Background(k.Fill).Shrink(0)
	tile.Children(func() {
		ui.Text(c, text).FontSize(FontCaption).FontWeight(700).TextColor(k.Label).SingleLine()
	})
	return tile
}

// HeroName is the entity's name in a hero card's header, in the header's
// color, shortened to fit.
func HeroName(c *ui.Context, name string) ui.Element {
	return ui.Text(c, name).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
}

// HeroTag is the outlined type tag at the right of a hero card's header,
// as "MIXED".
func HeroTag(c *ui.Context, text string) {
	k := T(c)
	CapsuleTag(c, text, k.Label2, ui.Color{}, k.Label4)
}

// HeroTags are a hero card's header tags, "链" for an item dialing
// through a proxy chain before its protocol tag (when not "").
func HeroTags(c *ui.Context, chained bool, protocol string) {
	ui.Row(c).Gap(4).Shrink(0).Children(func() {
		if chained {
			HeroTag(c, "链")
		}
		if protocol != "" {
			HeroTag(c, protocol)
		}
	})
}

// HeroValue is a hero card's main value, 22 DIPs semibold with tabular
// figures, as a port; HeroMono is an address, 18 DIPs monospaced. Both
// take the main row's color and shorten to fit.
func HeroValue(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).FontSize(22).FontWeight(600).FontFeatures("tnum").LetterSpacing(-0.2).
		SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
}

// HeroMono is HeroValue for addresses.
func HeroMono(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).Font(MonoFont).FontSize(18).FontWeight(600).SingleLine().Ellipsis("…").
		Shrink(1).MinWidth(0)
}

// HeroLatency is a latency tag on a hero card, 19 DIPs high: text in fg
// on bg, as nodeview.LatencyTag gives them.
func HeroLatency(c *ui.Context, text string, fg, bg ui.Color) ui.Element {
	tag := ui.Row(c).Height(19).PaddingX(6).Radius(6).AlignItems(ui.Center).Background(bg).Shrink(0)
	tag.Children(func() {
		ui.Text(c, text).FontSize(FontCaption).FontWeight(700).FontFeatures("tnum").TextColor(fg).SingleLine()
	})
	return tag
}
