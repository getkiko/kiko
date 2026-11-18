package kit

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"
)

// hintDelay is how long the pointer rests on an element, beyond MyGo's own
// tooltip delay, before its hint shows: about 1.5 seconds in all, so hints
// do not trail the pointer across lists and toolbars.
const hintDelay = 900 * time.Millisecond

// Hint shows text by e after the pointer has rested on it for about 1.5
// seconds, or at once as the keyboard focus comes to it, in a small
// light tip below it. Use it only where e does not say itself what it
// does or means: icon-only buttons, status symbols and notes found nowhere
// else. Text an ellipsis ends shows whole with FullText.
func Hint(c *ui.Context, e ui.Element, text string) ui.Element {
	if text == "" {
		return e
	}
	hint(c, e, text, ui.Local(e, "kit-hint", func() time.Time { return time.Time{} }))
	return e
}

// hint is Hint, with since when its tip came to show.
func hint(c *ui.Context, e ui.Element, text string, since *time.Time) {
	pending := false
	ui.TooltipBase(c, e, func(tip ui.Element) {
		pending = true
		now := c.Now()
		if since.IsZero() {
			*since = now
		}
		if wait := hintDelay - now.Sub(*since); wait > 0 && !e.Focused() {
			c.After(wait)
			return
		}
		k := T(c)
		tip.AttachTo(e, ui.AnchorBottom, ui.AnchorTop).Margin(6, 0, 0, 0).MaxWidth(280).
			Padding(4, 8).Radius(6).Background(k.Elevated).Border(0.5, k.Hairline).
			Shadow(0, 2, 8, 0, k.Shadow).TextColor(k.Label2).FontSize(FontCaption)
		tip.Children(func() { ui.Text(c, text) })
	})
	if !pending {
		*since = time.Time{}
	}
}

// ShortcutLabel is how the platform writes a shortcut of the command
// modifier (ui.Cmd) and key: ⌘= on macOS, Ctrl+= elsewhere.
func ShortcutLabel(key string) string {
	if runtime.GOOS == "darwin" {
		return "⌘" + key
	}
	return "Ctrl+" + key
}
