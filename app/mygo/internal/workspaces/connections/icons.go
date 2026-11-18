package connections

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
)

// iconCache shows process icons from the console's shared store
// (procicon), which Processes and Usage use too. UI thread.
type iconCache struct {
	store *procicon.Store
}

func newIconCache(e *env.Env) *iconCache { return &iconCache{store: procicon.For(e)} }

// iconProcess is what identifies a process's icon.
type iconProcess struct {
	PID       int
	StartedAt int64
	Path      string
	Name      string
	AppPath   string
}

// processIcon shows a process's icon at size DIPs, or a glyph for its kind.
func (ic *iconCache) processIcon(c *ui.Context, p iconProcess, size float32) {
	procicon.View(c, ic.store, procicon.Process{PID: p.PID, StartedAt: p.StartedAt, Path: p.Path, Name: p.Name, AppPath: p.AppPath},
		procicon.Options{Size: size})
}
