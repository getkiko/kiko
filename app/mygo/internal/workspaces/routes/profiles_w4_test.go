package routes

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// The device dialog's Default Outbound belongs to the profile in effect, also when
// it is not the original one or the original is gone.
func TestDeviceDialogEditsActiveProfileFinal(t *testing.T) {
	f, scope := profileFixture(t)
	work := f.profileID("工作")
	f.w.update(func(doc *model.Doc) (*model.Doc, error) { return model.SetActiveProfile(doc, "phone", work) })
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].ActiveProfile == work })
	d := findDevice(f.w.doc(), "phone")
	if err := func() error {
		f.w.editDevice(f.w.doc(), "phone")
		return nil
	}(); err != nil {
		t.Fatal(err)
	}
	if got, want := f.w.device.draft.Final, model.ProfileFinal(d, scope); got != want {
		t.Fatalf("draft %q, active profile %q", got, want)
	}
	f.w.device.draft.Final = "direct"
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.waitSaved(func(doc *model.Doc) bool { return model.ProfileFinal(doc.Route.Devices[0], scope) == "direct" })
	if got := f.saved().Route.Devices[0].Final; got != "proxy" {
		t.Fatalf("the original profile's final changed to %q", got)
	}
	// An unusable exit is refused for the active profile.
	f.w.editDevice(f.w.doc(), "phone")
	f.w.device.draft.Final = "missing"
	f.w.saveDevice(f.w.doc(), f.w.device)
	if f.w.device.validation == "" || model.ProfileFinal(f.w.doc().Route.Devices[0], scope) != "direct" {
		t.Fatalf("validation %q", f.w.device.validation)
	}
	// With the original deleted, the active profile's exit still edits.
	f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		return model.DeleteProfile(doc, "phone", model.MainProfileID)
	})
	f.waitSaved(func(doc *model.Doc) bool { return !slices.Contains(model.DeviceScopes(doc.Route.Devices[0]), "phone") })
	f.w.editDevice(f.w.doc(), "phone")
	if f.w.device.draft.Final != "direct" {
		t.Fatalf("draft %q", f.w.device.draft.Final)
	}
	f.w.device.draft.Final = "proxy"
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.waitSaved(func(doc *model.Doc) bool { return model.ProfileFinal(doc.Route.Devices[0], scope) == "proxy" })
}

// A test comes from the profiles in effect only.
func TestTestDevicesListActiveScopes(t *testing.T) {
	f, scope := profileFixture(t)
	devices := testDevices(f.w.doc())
	if !slices.Contains(devices, "phone") || slices.Contains(devices, scope) {
		t.Fatalf("devices %v", devices)
	}
	work := f.profileID("工作")
	f.w.update(func(doc *model.Doc) (*model.Doc, error) { return model.SetActiveProfile(doc, "phone", work) })
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].ActiveProfile == work })
	devices = testDevices(f.w.doc())
	if slices.Contains(devices, "phone") || !slices.Contains(devices, scope) || slices.Contains(devices, model.PreDeviceID) {
		t.Fatalf("devices %v", devices)
	}
}

// Two workspaces keep separate questions.
func TestProfileQuestionIsPerWorkspace(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	g := New(f.env, f.store)
	f.w.openProfileSwitch("phone", work.ID)
	if _, open := g.pendingProfileSwitch("phone"); open {
		t.Fatal("the question is shared")
	}
	if _, open := f.w.pendingProfileSwitch("phone"); !open {
		t.Fatal("the question is lost")
	}
}

// profileActive is the phone's profile in effect; savedActive the one the
// server stores.
func profileActive(f *fixture) string {
	return model.ActiveProfileOf(findDevice(f.w.doc(), "phone")).ID
}

func savedActive(f *fixture) string { return model.ActiveProfileOf(findDevice(f.saved(), "phone")).ID }

// doubleClick double-clicks the center of the element showing label.
func (f *fixture) doubleClick(label string) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q; texts %q", label, f.tt.Texts())
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	f.tt.ClickAt(x, y)
	f.tt.ClickAt(x, y)
}

// The sidebar switch itself, wide and narrow, with the core running: a
// double click asks, the confirmation switches.
func TestSwitchProfileFromSidebar(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		name := "wide"
		if narrow {
			name = "narrow"
		}
		t.Run(name, func(t *testing.T) {
			open := func(f *fixture) {
				f.w.showDevice("phone", false)
				if narrow {
					f.tt.SetSize(700, 820)
					f.tt.Frame()
					f.click("设备分栏")
					testkit.UntilText(t, f.tt, "iPhone")
				}
				f.tt.Frame()
			}
			confirm := func(f *fixture, target model.DeviceProfile, interrupts bool) {
				t.Helper()
				saves := f.srv.Saves()
				f.doubleClick(target.Name)
				testkit.UntilText(t, f.tt, "切换到「"+target.Name+"」？")
				// The first click showed the profile and saved nothing; the
				// release of the second keeps the question.
				f.tt.Frame()
				if f.w.owner != target.Scope || profileActive(f) == target.ID || f.srv.Saves() != saves {
					t.Fatalf("before the answer: owner %q active %q saves %d → %d", f.w.owner, profileActive(f), saves, f.srv.Saves())
				}
				if narrow && !f.w.sidebar {
					t.Fatal("the overlay closed before the answer")
				}
				if got := f.tt.HasText("「iPhone」的已有连接会中断。"); got != interrupts {
					t.Fatalf("interrupt message %v, want %v", got, interrupts)
				}
				f.click("切换")
				testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("切换到「" + target.Name + "」？") })
				f.waitSaved(func(doc *model.Doc) bool { return savedActive(f) == target.ID })
				if f.w.owner != target.Scope {
					t.Fatalf("owner %q", f.w.owner)
				}
				if _, marked := f.store.ProfileInterrupt("phone"); marked != interrupts {
					t.Fatalf("mark %v, want %v", marked, interrupts)
				}
				if narrow && f.w.sidebar {
					t.Fatal("the overlay stays after the switch")
				}
			}
			// Different exits: the confirmation says the connections go.
			f, work, _ := interruptProfileFixture(t)
			open(f)
			confirm(f, work, true)
			// An identical copy, and a stopped core, interrupt nothing.
			f, _, copied := interruptProfileFixture(t)
			open(f)
			confirm(f, copied, false)
			f, work, _ = interruptProfileFixture(t)
			f.running(false)
			open(f)
			confirm(f, work, false)
		})
	}
}

// Cancelling, Escape and a press outside keep the profile in effect and
// the preview.
func TestProfileSwitchCancelKeepsPreview(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice("phone", false)
	f.tt.Frame()
	saves := f.srv.Saves()
	title := "切换到「工作」？"
	for _, way := range []string{"cancel", "escape", "outside"} {
		f.tt.Advance(time.Second)
		f.doubleClick("工作")
		testkit.UntilText(t, f.tt, title)
		switch way {
		case "cancel":
			f.click("取消")
		case "escape":
			f.tt.Key(0, ui.KeyEscape)
		case "outside":
			f.tt.ClickAt(900, 700)
		}
		testkit.Until(t, f.tt, func() bool { return !f.tt.HasText(title) })
		if profileActive(f) != model.MainProfileID || f.w.owner != work.Scope || f.srv.Saves() != saves {
			t.Fatalf("%s: active %q owner %q saves %d → %d", way, profileActive(f), f.w.owner, saves, f.srv.Saves())
		}
	}
}

// A double click on the profile in effect shows it and asks nothing.
func TestProfileDoubleClickActiveAsksNothing(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice(work.Scope, false)
	f.tt.Frame()
	f.doubleClick("默认")
	f.tt.Frame()
	if f.w.owner != "phone" || f.w.profileAsk().state.Open || f.tt.HasText("切换到") {
		t.Fatalf("owner %q asks %v", f.w.owner, f.w.profileAsk().state.Open)
	}
}

// focusRow moves the focus with Tab to the row labeled label.
func (f *fixture) focusRow(label string) {
	f.t.Helper()
	for i := 0; i < 80; i++ {
		if f.tt.Focused(label) {
			return
		}
		f.tt.Key(0, ui.KeyTab)
		f.tt.Frame()
	}
	f.t.Fatalf("%q takes no focus", label)
}

// Enter shows the focused profile; Cmd+Enter asks, and Enter confirms.
func TestProfileSwitchByKeyboard(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice("phone", false)
	f.tt.Frame()
	f.focusRow("工作")
	saves := f.srv.Saves()
	f.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, f.tt, func() bool { return f.w.owner == work.Scope })
	f.tt.Frame()
	if f.w.profileAsk().state.Open || f.srv.Saves() != saves {
		t.Fatal("Enter asked or saved")
	}
	f.focusRow("工作")
	f.tt.Key(ui.Cmd, ui.KeyEnter)
	testkit.UntilText(t, f.tt, "切换到「工作」？")
	if profileActive(f) == work.ID {
		t.Fatal("switched before the answer")
	}
	// The confirm button takes the focus: Enter switches.
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool { return savedActive(f) == work.ID })
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("切换到「工作」？") })
	// Cmd+Enter on the profile in effect asks nothing.
	f.focusRow("工作")
	f.tt.Key(ui.Cmd, ui.KeyEnter)
	f.tt.Frame()
	if f.w.profileAsk().state.Open {
		t.Fatal("the profile in effect asks")
	}
}

// "Switch to this profile" of a profile row's menu asks beside the row; the profile in
// effect has no such item.
func TestProfileSwitchFromMenu(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice("phone", false)
	f.tt.Frame()
	if err := f.tt.RightClick("默认"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if f.tt.HasText("切换到此配置") {
		t.Fatal("the profile in effect offers a switch")
	}
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if err := f.tt.RightClick("工作"); err != nil {
		t.Fatal(err)
	}
	f.choose("切换到此配置")
	testkit.UntilText(t, f.tt, "切换到「工作」？")
	if !f.tt.HasText("「iPhone」的已有连接会中断。") || profileActive(f) == work.ID {
		t.Fatalf("texts %q active %q", f.tt.Texts(), profileActive(f))
	}
	f.click("切换")
	f.waitSaved(func(doc *model.Doc) bool { return savedActive(f) == work.ID })
	if f.w.owner != work.Scope {
		t.Fatalf("owner %q", f.w.owner)
	}
}

// A double click with a dirty draft asks about the draft first, then
// about the switch.
func TestProfileSwitchAsksAboutDirtyDraftFirst(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice("phone", false)
	f.tt.Frame()
	openGuarded(t, f, "r-phone", true)
	f.doubleClick("工作")
	f.tt.Frame()
	if !asks(f) || f.w.profileAsk().state.Open {
		t.Fatalf("draft question %v, switch question %v", asks(f), f.w.profileAsk().state.Open)
	}
	f.click("放弃更改")
	testkit.UntilText(t, f.tt, "切换到「工作」？")
	if f.w.editor != nil || profileActive(f) == work.ID {
		t.Fatalf("editor %v active %q", f.w.editor != nil, profileActive(f))
	}
}

// In the narrow overlay a press inside the confirmation keeps the overlay
// and the confirmation open.
func TestNarrowOverlayKeepsQuestionOpen(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice("phone", false)
	f.tt.SetSize(700, 820)
	f.tt.Frame()
	f.click("设备分栏")
	testkit.UntilText(t, f.tt, "iPhone")
	f.tt.Frame()
	f.click("工作")
	f.tt.Frame()
	if !f.w.sidebar || f.w.owner != work.Scope {
		t.Fatalf("a click: overlay %v owner %q", f.w.sidebar, f.w.owner)
	}
	f.tt.Advance(time.Second)
	f.doubleClick("工作")
	testkit.UntilText(t, f.tt, "切换到「工作」？")
	f.click("「iPhone」的已有连接会中断。")
	f.tt.Frame()
	if !f.w.sidebar || !f.tt.HasText("切换到「工作」？") {
		t.Fatalf("overlay %v, question %v", f.w.sidebar, f.tt.HasText("切换到「工作」？"))
	}
	if profileActive(f) == work.ID {
		t.Fatal("switched by a press inside")
	}
}

// previewDir is where the acceptance screenshots go; the tests below write
// nothing without ROUTES_PREVIEW_DIR.
func previewDir(t *testing.T) string {
	dir := os.Getenv("ROUTES_PREVIEW_DIR")
	if dir == "" {
		t.Skip("ROUTES_PREVIEW_DIR is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f *fixture) shoot(name string) {
	f.t.Helper()
	f.snapshot(name+"-light.png", false)
	f.snapshot(name+"-dark.png", true)
}

// Round 35 F4 acceptance screenshots.
func TestProfilesSnapshots(t *testing.T) {
	previewDir(t)
	// Three profiles on the phone; the third is in effect.
	h, _ := profileFixture(t)
	h.w.update(func(doc *model.Doc) (*model.Doc, error) { return model.AddProfile(doc, "phone", "游戏", "") })
	h.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].Profiles) == 3 })
	h.w.update(func(doc *model.Doc) (*model.Doc, error) {
		return model.SetActiveProfile(doc, "phone", h.profileID("工作"))
	})
	h.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].ActiveProfile == h.profileID("工作") })
	h.w.showActiveProfile("phone")
	h.tt.Frame()
	h.shoot("1-sidebar-profiles")

	f, work, _ := interruptProfileFixture(t)
	f.w.showActiveProfile("phone")
	f.tt.Frame()

	f.w.startRenameProfile("phone", work.ID)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "重命名配置")
	f.shoot("2-profile-rename-dialog")
	f.click("取消")
	testkit.Until(t, f.tt, func() bool { return f.w.profile == nil })

	f.w.requestDeleteProfile("device:phone", "phone", work.ID)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "删除配置")
	f.shoot("3-profile-delete-confirm")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()

	f.click("工作")
	testkit.Until(t, f.tt, func() bool { return f.w.owner == work.Scope })
	f.tt.Move(900, 700)
	f.tt.Frame()
	f.shoot("7-profile-preview")
	f.tt.Advance(time.Second)
	f.doubleClick("工作")
	testkit.UntilText(t, f.tt, "切换到「工作」？")
	f.shoot("4-switch-confirm")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()

	f.w.copy = newCopyDialog(f.w.doc(), copyRequest{sec: f.w.sec, owner: "other"})
	f.w.copy.selected = work.Scope
	f.tt.Frame()
	f.tt.ClickAt(700, 368)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "复制")
	f.shoot("5-copy-dialog-targets")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()

	g, _, _ := interruptProfileFixture(t)
	g.w.showDevice("phone", false)
	g.tt.SetSize(480, 820)
	g.tt.Frame()
	g.click("设备分栏")
	testkit.UntilText(t, g.tt, "iPhone")
	g.tt.Frame()
	g.shoot("6-narrow-sidebar")
	g.click("工作")
	g.tt.Frame()
	g.tt.Advance(time.Second)
	g.doubleClick("工作")
	testkit.UntilText(t, g.tt, "切换到「工作」？")
	g.shoot("8-narrow-switch-confirm")
}

// A switch left waiting by a double click is dropped once the draft
// question is answered with "Keep editing" or Escape: a later click on the
// profile, whose press outside the editor asks again, only shows it.
func TestProfileWaitingSwitchForgottenAfterKeep(t *testing.T) {
	for _, way := range []string{"keep", "escape"} {
		t.Run(way, func(t *testing.T) {
			f, work, _ := interruptProfileFixture(t)
			f.w.showDevice("phone", false)
			f.tt.Frame()
			openGuarded(t, f, "r-phone", true)
			f.doubleClick("工作")
			f.tt.Frame()
			if !asks(f) {
				t.Fatal("the draft does not ask")
			}
			if way == "keep" {
				f.click("继续编辑")
			} else {
				f.tt.Key(0, ui.KeyEscape)
			}
			testkit.Until(t, f.tt, func() bool { return !asks(f) })
			f.tt.Advance(time.Second)
			f.click("工作")
			f.tt.Frame()
			if !asks(f) {
				t.Fatal("the click does not ask about the draft")
			}
			f.click("放弃更改")
			testkit.Until(t, f.tt, func() bool { return f.w.owner == work.Scope })
			f.tt.Frame()
			if f.w.profileAsk().state.Open || f.tt.HasText("切换到「工作」？") || profileActive(f) == work.ID {
				t.Fatalf("a click asked to switch: open %v active %q", f.w.profileAsk().state.Open, profileActive(f))
			}
		})
	}
}

// A confirmation whose profile is deleted closes, and the device's
// profiles fold when it is not selected.
func TestProfileSwitchClosesWhenProfileGone(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	f.w.showDevice(model.LocalDeviceID, false)
	f.w.openProfileSwitch("phone", work.ID)
	f.tt.Frame()
	if _, open := f.w.pendingProfileSwitch("phone"); !open {
		t.Fatal("the confirmation does not open")
	}
	if !f.w.update(func(doc *model.Doc) (*model.Doc, error) { return model.DeleteProfile(doc, "phone", work.ID) }) {
		t.Fatal(f.w.err)
	}
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].Profiles) == 2 })
	f.tt.Frame()
	if _, open := f.w.pendingProfileSwitch("phone"); open || f.w.profileAsk().state.Open {
		t.Fatal("the confirmation stays open")
	}
	if f.tt.HasText("切换到「工作」？") || f.tt.HasText("副本") {
		t.Fatalf("texts %q", f.tt.Texts())
	}
}
