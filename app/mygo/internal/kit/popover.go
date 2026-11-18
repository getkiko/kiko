package kit

import "github.com/egoist/mygo/ui"

// AttachPopover places panel, the root of a PopoverBase, below anchor or,
// where there is no room below, above it, gap DIPs apart: its self point
// on the anchor's at point, as AttachTo (the at and self of a popover
// below its anchor, as ui.AnchorBottomEnd and ui.AnchorTopEnd). MyGo
// flips such a popover to the other side but does not move it along that
// side, so its height is held to the room the window leaves on the larger
// side (PopoverRoom) and, when positive, to limit: it then lies whole in
// the window, its content scrolling in a ui.Scroll or a PopoverBody.
// Across, MyGo keeps it in the window.
func AttachPopover(c *ui.Context, panel, anchor ui.Element, at, self ui.Anchor, gap, limit float32) ui.Element {
	room := PopoverRoom(c, anchor, gap)
	if limit > 0 {
		room = min(room, limit)
	}
	return panel.AttachTo(anchor, at, self).Margin(gap, 0, 0, 0).MaxHeight(room)
}

// PopoverBody is the content of a popover AttachPopover places, gap DIPs
// apart: a column that scrolls when the popover is held shorter than it.
func PopoverBody(c *ui.Context, gap float32, fn func()) ui.Element {
	return ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(gap).Children(fn)
}
