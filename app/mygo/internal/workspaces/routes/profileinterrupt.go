package routes

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// profileAsk is the confirmation of switching a device to another of its
// profiles: the device and profile it is open for.
type profileAsk struct {
	state          kit.ConfirmState
	device, target string
	// waiting is the profile (device/profile) whose switch waits on the
	// editor's question "Discard changes?".
	waiting string
}

func (w *Workspace) profileAsk() *profileAsk {
	if w.profileQ == nil {
		w.profileQ = &profileAsk{}
	}
	return w.profileQ
}

// profileTarget finds the device (or the local scope) and the scope of its
// profile profileID.
func profileTarget(doc *model.Doc, deviceID, profileID string) (*model.RouteDevice, model.DeviceProfile, bool) {
	for _, d := range model.ProfileDevices(doc) {
		if d.ID != deviceID {
			continue
		}
		for _, p := range model.DeviceProfiles(d) {
			if p.ID == profileID {
				return d, p, true
			}
		}
	}
	return nil, model.DeviceProfile{}, false
}

// asksToInterruptProfile reports whether switching device deviceID to its
// profile profileID asks about the device's existing connections: the core
// runs, the profile is another than the one in effect, and the exits of its
// rules and its default exit differ in order from those of the scope the
// connections match now (the one last applied; a copy of that profile asks
// nothing, nor does a switch back to it).
func (w *Workspace) asksToInterruptProfile(doc *model.Doc, deviceID, profileID string) bool {
	server := w.env.Server
	if !server.HasRuntime || !server.Runtime.Running() {
		return false
	}
	d, target, ok := profileTarget(doc, deviceID, profileID)
	if !ok || target.Scope == model.ActiveScope(d) {
		return false
	}
	base := w.store.ConnectionScope(doc, d)
	return base != target.Scope && store.ExitSequencesDiffer(doc, base, target.Scope)
}

// requestProfileSwitch asks to switch device deviceID to its profile
// profileID, once the rule editor is left (a dirty draft asks first). The
// sidebar shows the confirmation beside the profile's row; a profile in
// effect asks nothing.
func (w *Workspace) requestProfileSwitch(deviceID, profileID string) {
	a := w.profileAsk()
	a.waiting = deviceID + "/" + profileID
	w.leaveEditor(func() {
		a.waiting = ""
		d, target, ok := profileTarget(w.doc(), deviceID, profileID)
		if !ok || model.ActiveProfileOf(d).ID == profileID {
			return
		}
		if w.owner != target.Scope {
			w.showDevice(target.Scope, false)
		}
		w.openProfileSwitch(deviceID, profileID)
	})
}

// switchWaits reports whether the switch to profileID of device deviceID
// asked to wait on the editor's question "Discard changes?", which still shows.
func (w *Workspace) switchWaits(deviceID, profileID string) bool {
	e := w.editor
	return w.profileAsk().waiting == deviceID+"/"+profileID && e != nil && e.open && e.guard.Pending()
}

// openProfileSwitch opens the confirmation of the switch.
func (w *Workspace) openProfileSwitch(deviceID, profileID string) {
	a := w.profileAsk()
	a.device, a.target = deviceID, profileID
	a.state = kit.ConfirmState{Open: true}
}

// pendingProfileSwitch is the profile the open confirmation is about for
// the device. A confirmation whose device or profile is gone closes.
func (w *Workspace) pendingProfileSwitch(deviceID string) (profileID string, open bool) {
	a := w.profileAsk()
	if !a.state.Open || a.device != deviceID {
		return a.target, false
	}
	if _, _, ok := profileTarget(w.doc(), a.device, a.target); !ok {
		a.state.Open = false
		return a.target, false
	}
	return a.target, true
}

// forgetWaitingSwitch drops the switch waiting on the editor's question
// once that question no longer shows (answered "Keep editing", Escape, or the
// editor closed), so a later click on the profile only shows it. Called
// at the start of each frame, before the presses of the frame ask again.
func (w *Workspace) forgetWaitingSwitch() {
	if a := w.profileQ; a != nil && a.waiting != "" {
		if e := w.editor; e == nil || !e.open || !e.guard.Pending() {
			a.waiting = ""
		}
	}
}

// applyProfileSwitch makes profile profileID of device deviceID the one in
// effect and records the answer: interrupt marks the device's old
// connections for the next live apply; a switch that asked nothing drops a
// mark an earlier switch left (back to the profile the connections match,
// or to one with the same exits).
func (w *Workspace) applyProfileSwitch(deviceID, profileID string, asked, interrupt bool) bool {
	doc := w.doc()
	d, target, ok := profileTarget(doc, deviceID, profileID)
	if !ok {
		return false
	}
	base := w.store.ConnectionScope(doc, d)
	_, marked := w.store.ProfileInterrupt(deviceID)
	if !w.save(func(doc *model.Doc) error {
		next, err := model.SetActiveProfile(doc, deviceID, profileID)
		return replace(doc, next, err)
	}) {
		return false
	}
	switch {
	case asked:
		w.store.SetProfileInterrupt(deviceID, base, target.Scope, interrupt)
	case marked:
		w.store.SetProfileInterrupt(deviceID, base, target.Scope, false)
	}
	return true
}

// profileSwitchPopover confirms, beside anchor, the switch of the device
// to the profile. When the switch interrupts the device's connections
// (asksToInterruptProfile) it says so, and confirming records that; saving
// only records it, and the Overview apply closes the connections.
func (w *Workspace) profileSwitchPopover(c *ui.Context, anchor ui.Element, deviceID, profileID string) {
	a := w.profileAsk()
	if a.device != deviceID || a.target != profileID {
		return
	}
	doc := w.doc()
	d, target, ok := profileTarget(doc, deviceID, profileID)
	if !ok {
		a.state.Open = false
		return
	}
	asks := w.asksToInterruptProfile(doc, deviceID, profileID)
	message := ""
	if asks {
		message = "「" + d.Name + "」的已有连接会中断。"
	}
	kit.ConfirmPopover(c, anchor, &a.state, kit.ConfirmOptions{
		Title:   "切换到「" + target.Name + "」？",
		Message: message,
		Confirm: "切换",
		Run: func(done func(error)) {
			if w.applyProfileSwitch(deviceID, profileID, asks, asks) {
				w.showActiveProfile(deviceID)
				done(nil)
				return
			}
			done(errString(w.err))
		},
	})
}
