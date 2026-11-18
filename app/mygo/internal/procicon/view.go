package procicon

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// Look is how a process without an icon shows its kind.
type Look int

const (
	// Plain draws the kind's symbol in Label3 on the Fill (Connections, Usage).
	Plain Look = iota
	// Tinted colors it as the kind's own icons are: a green Node badge,
	// a dark terminal (Processes).
	Tinted
)

// Options size an icon. Radius defaults to 22% of Size.
type Options struct {
	Size   float32
	Radius float32
	Look   Look
}

var terminalNames = []string{"zsh", "bash", "sh", "cmd.exe", "powershell.exe"}

// View shows p's icon from s, or a symbol for its kind while there is
// none: JS for Node, a terminal for shells, a window for Apps, a gear for
// the rest, a network symbol without a process. UI thread.
func View(c *ui.Context, s *Store, p Process, o Options) ui.Element {
	size, radius := o.Size, o.Radius
	if radius == 0 {
		radius = size * 0.22
	}
	if s != nil {
		if bitmap := s.Bitmap(p); bitmap != nil {
			return ui.Image(c, bitmap).Size(size, size).Radius(radius).Shrink(0)
		}
	}
	return Glyph(c, p, o)
}

// Glyph is View's symbol for p's kind; a network symbol, as Connections draws
// for a connection without a process, when nothing identifies one (Usage's
// "Unknown process", package names, —).
func Glyph(c *ui.Context, p Process, o Options) ui.Element {
	k := kit.T(c)
	p = p.withBundle()
	size, radius := o.Size, o.Radius
	if radius == 0 {
		radius = size * 0.22
	}
	if p.Path == "" && p.PID == 0 && p.AppPath == "" {
		box := ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(k.Label3)
		box.Children(func() { kit.Icon(c, kit.SymNetwork, size*0.6) })
		return box
	}
	box := ui.Box(c).Size(size, size).Radius(radius).Center().Shrink(0).Background(k.Fill).TextColor(k.Label3)
	node := p.Name == "node" || p.Name == "node.exe"
	terminal := slices.Contains(terminalNames, p.Name)
	if o.Look == Plain {
		box.Children(func() {
			switch {
			case node:
				ui.Text(c, "JS").FontSize(size * 0.42).FontWeight(700)
			case terminal:
				kit.Icon(c, kit.SymTerminal, size*0.55)
			case p.AppPath != "":
				kit.Icon(c, kit.SymAppWindow, size*0.55)
			default:
				kit.Icon(c, kit.SymSettings, size*0.5)
			}
		})
		return box
	}
	// Tinted keeps the Processes proportions: 16 DIPs in rows, 40 in the
	// inspector.
	large := size >= 32
	symbol := func(s kit.Symbol, scale float32) {
		box.Children(func() {
			if large {
				kit.Icon(c, s, size*0.7*scale)
			} else {
				kit.Icon(c, s, size*0.875*scale)
			}
		})
	}
	switch {
	case node:
		box.Background(ui.Hex("#5FA04E")).TextColor(ui.Hex("#FFFFFF")).Children(func() {
			b, font := size*0.75, size*0.4375
			if large {
				b, font = size*0.7, size*0.375
			}
			ui.Box(c).Size(b, b).Radius(b/4).Border(1, ui.Hex("#FFFFFF")).Center().Children(func() {
				ui.Text(c, "JS").FontSize(font).FontWeight(700)
			})
		})
	case terminal:
		box.Background(ui.Hex("#232323")).TextColor(ui.Hex("#DDDDDD"))
		symbol(kit.SymTerminal, 1)
	case p.AppPath != "":
		symbol(kit.SymAppWindow, 1)
	default:
		if !k.Dark {
			box.Background(ui.Hex("#E2E2E2")).TextColor(ui.Hex("#89898D"))
		}
		symbol(kit.SymSettings, 0.8)
	}
	return box
}
