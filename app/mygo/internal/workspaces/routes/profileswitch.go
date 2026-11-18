package routes

import (
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// showActiveProfile shows the rules of the profile in effect of a device.
func (w *Workspace) showActiveProfile(deviceID string) {
	if d := profileOwner(w.doc(), deviceID); d != nil {
		w.showDevice(model.ActiveScope(d), w.narrow)
	}
}
