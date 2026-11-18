package processes

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
)

// icon shows a process's icon from the console's shared store at 16
// DIPs, or 40 when large, falling back to a symbol: JS for Node, a
// terminal for shells, a window for Apps, a gear for the rest.
func (w *Workspace) icon(c *ui.Context, p Process, large bool) ui.Element {
	o := procicon.Options{Size: 16, Radius: 4, Look: procicon.Tinted}
	if large {
		o.Size, o.Radius = 40, 9
	}
	return procicon.View(c, w.icons, procicon.Process{PID: p.PID, StartedAt: p.StartedAt, Path: p.Path, Name: p.Name, AppPath: p.AppPath}, o)
}
