package routes

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// update saves the document fn returns for the current one.
func (w *Workspace) update(fn func(doc *model.Doc) (*model.Doc, error)) bool {
	return w.save(func(doc *model.Doc) error {
		next, err := fn(doc)
		return replace(doc, next, err)
	})
}

// profileDialog names a new profile or renames one.
type profileDialog struct {
	open     bool
	deviceID string
	// profileID is the profile renamed; "" for a new one.
	profileID string
	name      string
}

// profileDelete is the confirmation of deleting a profile, beside the row
// button it came from (from).
type profileDelete struct {
	state             kit.ConfirmState
	deviceID, profile string
	from              string
	anchor            ui.Element
	anchored          bool
}

// newProfileName is the default name of a new profile of d.
func newProfileName(d *model.RouteDevice) string { return model.UniqueProfileName(d, "新配置") }

// startNewProfile opens the dialog naming a new profile of a device.
func (w *Workspace) startNewProfile(deviceID string) {
	w.leaveEditor(func() {
		if d := profileOwner(w.doc(), deviceID); d != nil {
			w.err = ""
			w.profile = &profileDialog{open: true, deviceID: d.ID, name: newProfileName(d)}
		}
	})
}

// startRenameProfile opens the dialog renaming a profile.
func (w *Workspace) startRenameProfile(deviceID, profileID string) {
	w.leaveEditor(func() {
		d := profileOwner(w.doc(), deviceID)
		if d == nil {
			return
		}
		for _, p := range model.DeviceProfiles(d) {
			if p.ID == profileID {
				w.err = ""
				w.profile = &profileDialog{open: true, deviceID: d.ID, profileID: p.ID, name: p.Name}
			}
		}
	})
}

// profileByName finds a device's profile by its (unique) name.
func profileByName(doc *model.Doc, deviceID, name string) (model.DeviceProfile, bool) {
	d := profileOwner(doc, deviceID)
	if d == nil {
		return model.DeviceProfile{}, false
	}
	for _, p := range model.DeviceProfiles(d) {
		if p.Name == name {
			return p, true
		}
	}
	return model.DeviceProfile{}, false
}

// addProfile adds a profile (a copy of copyFrom's scope when set) and
// shows it; the profile in effect stays. The editor is left already.
func (w *Workspace) addProfile(deviceID, name, copyFrom string) bool {
	if !w.update(func(doc *model.Doc) (*model.Doc, error) { return model.AddProfile(doc, deviceID, name, copyFrom) }) {
		return false
	}
	name = strings.TrimSpace(name)
	if p, ok := profileByName(w.doc(), deviceID, name); ok {
		w.showDevice(p.Scope, w.narrow)
	}
	return true
}

// duplicateProfile copies a profile under a new name and shows the copy.
func (w *Workspace) duplicateProfile(deviceID, profileID string) {
	w.leaveEditor(func() {
		d := profileOwner(w.doc(), deviceID)
		if d == nil {
			return
		}
		for _, p := range model.DeviceProfiles(d) {
			if p.ID == profileID {
				w.addProfile(d.ID, model.UniqueProfileName(d, p.Name+" 副本"), p.Scope)
			}
		}
	})
}

// requestDeleteProfile asks to delete a profile beside the button from.
func (w *Workspace) requestDeleteProfile(from, deviceID, profileID string) {
	w.leaveEditor(func() {
		w.profileDelete = profileDelete{deviceID: deviceID, profile: profileID, from: from}
		w.profileDelete.state.Open = true
	})
}

// profileDeleteView is the confirmation of deleting a profile, at the
// button it came from when the sidebar built it this frame.
func (w *Workspace) profileDeleteView(c *ui.Context, doc *model.Doc) {
	del := &w.profileDelete
	if !del.state.Open {
		del.anchored = false
		return
	}
	d := profileOwner(doc, del.deviceID)
	if d == nil || !del.anchored {
		del.state.Open = false
		return
	}
	name := ""
	for _, p := range model.DeviceProfiles(d) {
		if p.ID == del.profile {
			name = p.Name
		}
	}
	deviceID, profileID := del.deviceID, del.profile
	kit.ConfirmPopover(c, del.anchor, &del.state, kit.ConfirmOptions{
		Title: "删除配置「" + name + "」？", Message: "该配置的规则与分组会一并删除。", Confirm: "删除", Danger: true,
		Run: func(done func(error)) {
			if w.update(func(doc *model.Doc) (*model.Doc, error) { return model.DeleteProfile(doc, deviceID, profileID) }) {
				done(nil)
				return
			}
			msg := w.err
			w.err = ""
			done(errors.New(msg))
		},
	})
	del.anchored = false
}

// profileDialogView is the dialog naming a new profile or renaming one.
func (w *Workspace) profileDialogView(c *ui.Context, doc *model.Doc) {
	d := w.profile
	title := "新建配置"
	if d.profileID != "" {
		title = "重命名配置"
	}
	disabled := strings.TrimSpace(d.name) == ""
	save := func() {
		if disabled {
			return
		}
		if d.profileID != "" {
			if w.update(func(doc *model.Doc) (*model.Doc, error) {
				return model.RenameProfile(doc, d.deviceID, d.profileID, d.name)
			}) {
				d.open = false
			}
			return
		}
		if w.addProfile(d.deviceID, d.name, "") {
			d.open = false
		}
	}
	kit.Dialog(c, &d.open, title, 0, func() {
		dialogRow(c, "名称", func() {
			kit.Input(c, "profile-name", &d.name, "配置名称").Label("配置名称")
		})
		if w.err != "" {
			kit.Notice(c, kit.DangerTone, "", w.err, nil)
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.open = false })
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: disabled}).OnClick(save)
	})
	if w.profile == d && !d.open {
		w.profile = nil
		w.err = ""
	}
}

// profileMenu fills the menu of a profile row, or of a device row for the
// profile it shows (menuProfile): new, rename, copy and delete, the last
// only while the device keeps another. from names the button the delete
// confirmation anchors to, the device's More.
func (w *Workspace) profileMenu(c *ui.Context, m *ui.Menu, d *model.RouteDevice, p model.DeviceProfile, from string) {
	if m.Item("新建配置…").Chosen() {
		w.startNewProfile(d.ID)
	}
	if m.Item("重命名配置「" + p.Name + "」…").Chosen() {
		w.startRenameProfile(d.ID, p.ID)
	}
	if m.Item("复制配置「" + p.Name + "」").Chosen() {
		w.duplicateProfile(d.ID, p.ID)
	}
	if len(model.DeviceProfiles(d)) < 2 {
		return
	}
	m.Separator()
	if m.Item("删除配置「" + p.Name + "」…").Chosen() {
		w.requestDeleteProfile(from, d.ID, p.ID)
	}
}

// noteDeleteAnchor records button as the anchor of the delete confirmation
// when it is the one the confirmation came from.
func (w *Workspace) noteDeleteAnchor(from string, button ui.Element) {
	if w.profileDelete.state.Open && w.profileDelete.from == from {
		w.profileDelete.anchor, w.profileDelete.anchored = button, true
	}
	if w.imageRemove.state.Open && w.imageRemove.from == from {
		w.imageRemove.anchor, w.imageRemove.anchored = button, true
	}
}
