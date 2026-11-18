package usage

import (
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/srcname"
)

// sourceNames names the source filter's addresses from the route devices
// of the current document and the host's addresses; rebuilt when either
// changes, so a renamed device shows at once.
func (w *Workspace) sourceNames() *srcname.Names {
	if w.store == nil {
		return nil
	}
	if w.names == nil || w.namesVersion != w.store.Version() || !slices.Equal(w.namesLocal, w.store.LocalAddresses) {
		w.names = srcname.FromDoc(w.store.Doc(), w.store.LocalAddresses)
		w.namesVersion = w.store.Version()
		w.namesLocal = slices.Clone(w.store.LocalAddresses)
	}
	return w.names
}
