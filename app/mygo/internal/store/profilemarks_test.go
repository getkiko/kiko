package store_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// withDevice adds the device phone with one traffic rule to the first
// outbound and the default exit of the second, plus the profiles "Work" (blank:
// only the default exit) and "Copy" (a copy of the first). It returns the
// scope of each.
func (h *harness) withDevice() (work, copied string) {
	h.t.Helper()
	exit := h.s.Doc().Outbounds[0].ID
	err := h.s.Update(func(doc *model.Doc) {
		doc.Route.Devices = append(doc.Route.Devices, &model.RouteDevice{ID: "phone", Name: "手机", Addresses: []string{"192.168.1.20/32"}, Final: exit})
		next := model.FillDefaults(doc)
		group := model.RouteGroupsOf(next, "phone")[0].ID
		next.Route.Rules = append(next.Route.Rules, &model.RouteRule{ID: "r-ph", DeviceID: "phone", GroupID: group, Name: "规则", Enabled: true,
			Action: model.ActionRoute, Outbound: model.Some(exit), Match: map[string]any{}})
		*doc = *next
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.addProfile("工作", "")
	h.addProfile("副本", "phone")
	return h.scope("工作"), h.scope("副本")
}

func (h *harness) addProfile(name, from string) {
	h.t.Helper()
	if err := h.s.Update(func(doc *model.Doc) {
		next, err := model.AddProfile(doc, "phone", name, from)
		if err != nil {
			h.t.Fatal(err)
		}
		*doc = *next
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) profile(name string) model.DeviceProfile {
	h.t.Helper()
	for _, d := range h.s.Doc().Route.Devices {
		for _, p := range model.DeviceProfiles(d) {
			if d.ID == "phone" && p.Name == name {
				return p
			}
		}
	}
	h.t.Fatalf("no profile %q", name)
	return model.DeviceProfile{}
}

func (h *harness) scope(name string) string { return h.profile(name).Scope }

// switchTo makes the profile in effect, as the sidebar does.
func (h *harness) switchTo(id string) {
	h.t.Helper()
	if err := h.s.Update(func(doc *model.Doc) {
		next, err := model.SetActiveProfile(doc, "phone", id)
		if err != nil {
			h.t.Fatal(err)
		}
		*doc = *next
	}); err != nil {
		h.t.Fatal(err)
	}
}

// phoneServer answers the active connections: a and b matched rules of the
// scope phone (b its default exit), c a rule of another scope.
func phoneServer(h *harness, l *callLog) {
	h.srv.Route("GET", "/connections", func(w http.ResponseWriter, r *http.Request) {
		l.add("list")
		testkit.JSON(api.ConnectionPage{Total: 3, Rows: []api.ConnectionRow{
			{Connection: api.Connection{ID: "a", RuleOwner: "phone", RuleID: "r-ph"}},
			{Connection: api.Connection{ID: "b", RuleOwner: "phone"}},
			{Connection: api.Connection{ID: "c", RuleOwner: "tv", RuleID: "r-tv"}},
		}})(w, r)
	})
	interruptServerApply(h, l)
}

// interruptServerApply logs the apply and the close.
func interruptServerApply(h *harness, l *callLog) {
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		testkit.JSON(map[string]any{"applied": true, "live": true})(w, r)
	})
	h.srv.Route("POST", "/connections/close", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = testkit.Body(r, &body)
		l.mu.Lock()
		l.ids = body.IDs
		l.mu.Unlock()
		l.add("close")
		testkit.JSON(api.CloseResult{Matched: len(body.IDs), Closed: len(body.IDs)})(w, r)
	})
}

func TestExitSequencesDiffer(t *testing.T) {
	h := newHarness(t, nil)
	work, copied := h.withDevice()
	doc := h.s.Doc()
	if got := store.ExitSequence(doc, "phone"); len(got) != 2 || !strings.HasPrefix(got[0], "route:") || !strings.HasPrefix(got[1], "final:") {
		t.Fatalf("sequence %v", got)
	}
	if store.ExitSequencesDiffer(doc, "phone", copied) {
		t.Error("an identical copy differs")
	}
	if !store.ExitSequencesDiffer(doc, "phone", work) {
		t.Error("a blank profile does not differ")
	}
	// A disabled rule and a renamed rule are no exit.
	if err := h.s.Update(func(doc *model.Doc) {
		for _, r := range doc.Route.Rules {
			if r.DeviceID == copied {
				r.Enabled = false
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if store.ExitSequencesDiffer(h.s.Doc(), work, copied) {
		t.Error("the copy without its enabled rule keeps the blank profile's sequence")
	}
}

// A device switching to a profile with other exits closes the connections
// of its old scope's rules, default exit included, after the apply and not
// those of other scopes; the mark is spent.
func TestProfileSwitchClosesTheOldScopeConnections(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	phoneServer(h, l)
	work, _ := h.withDevice()
	h.s.NoteApplied(h.s.Doc())
	h.switchTo(h.profile("工作").ID)
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"list", "apply", "close"}) {
		t.Fatalf("calls %v", got)
	}
	if !slices.Equal(l.ids, []string{"a", "b"}) {
		t.Fatalf("closed %v", l.ids)
	}
	if out := h.s.InterruptOutcome(); out.Closed != 2 || out.Error != "" {
		t.Fatalf("outcome %+v", out)
	}
	if devices := h.s.InterruptDevices(); len(devices) != 0 {
		t.Fatalf("marks left: %v", devices)
	}
	if scope, ok := h.s.AppliedScope("phone"); !ok || scope != work {
		t.Fatalf("applied scope %q %v", scope, ok)
	}
}

// Rule marks and device marks name the same connection once.
func TestProfileSwitchAndRuleMarksCloseEachConnectionOnce(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	h.srv.Route("GET", "/connections", func(w http.ResponseWriter, r *http.Request) {
		l.add("list " + r.URL.Query().Get("rule"))
		testkit.JSON(api.ConnectionPage{Total: 1, Rows: []api.ConnectionRow{
			{Connection: api.Connection{ID: "a", RuleOwner: "phone", RuleID: "r-ph"}}}})(w, r)
	})
	interruptServerApply(h, l)
	work, _ := h.withDevice()
	h.switchTo(h.profile("工作").ID)
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	h.s.SetInterrupt("r-ph", "elsewhere", h.s.Doc().Outbounds[0].ID, true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.ids, []string{"a"}) {
		t.Fatalf("closed %v", l.ids)
	}
}

// A → B → A leaves nothing to close: the mark goes with the switch back,
// and a mark that survives anyway is dropped before the apply.
func TestSwitchingBackClearsTheMark(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	phoneServer(h, l)
	work, _ := h.withDevice()
	main := h.profile("默认").ID
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	if from, ok := h.s.ProfileInterrupt("phone"); !ok || from != "phone" {
		t.Fatalf("mark %q %v", from, ok)
	}
	// Back to the scope the connections are in; the later answer's origin
	// is the first one's.
	h.s.SetProfileInterrupt("phone", work, "phone", true)
	if devices := h.s.InterruptDevices(); len(devices) != 0 {
		t.Fatalf("the switch back left the mark: %v", devices)
	}
	// A mark left behind with the device back on its origin does nothing.
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	h.switchTo(h.profile("工作").ID)
	h.switchTo(main)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"apply"}) {
		t.Fatalf("calls %v", got)
	}
	if devices := h.s.InterruptDevices(); len(devices) != 0 {
		t.Fatalf("marks left: %v", devices)
	}
}

// Marks that no longer describe a switch are dropped: the profile or the
// device was deleted, or the two profiles have the same exits.
func TestStaleProfileMarksAreDropped(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	phoneServer(h, l)
	work, copied := h.withDevice()
	workID, copyID := h.profile("工作").ID, h.profile("副本").ID
	main := h.profile("默认").ID

	// The copy has the exits of the original.
	h.switchTo(copyID)
	h.s.SetProfileInterrupt("phone", "phone", copied, true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	// The profile switched to was deleted.
	h.switchTo(workID)
	h.s.SetProfileInterrupt("phone", copied, work, true)
	if err := h.s.Update(func(doc *model.Doc) {
		next, err := model.DeleteProfile(doc, "phone", workID)
		if err != nil {
			t.Fatal(err)
		}
		*doc = *next
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	// The device was deleted.
	h.switchTo(main)
	if err := h.s.Update(func(doc *model.Doc) {
		next, err := model.AddProfile(doc, "phone", "第三", "")
		if err != nil {
			t.Fatal(err)
		}
		*doc = *next
	}); err != nil {
		t.Fatal(err)
	}
	h.s.SetProfileInterrupt("phone", "phone", h.scope("第三"), true)
	h.switchTo(h.profile("第三").ID)
	if err := h.s.Update(func(doc *model.Doc) {
		next, err := model.RemovePolicyDevice(doc, "phone")
		if err != nil {
			t.Fatal(err)
		}
		*doc = *next
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	for _, call := range l.list() {
		if call != "apply" {
			t.Fatalf("a stale mark listed or closed connections: %v", l.list())
		}
	}
	if devices := h.s.InterruptDevices(); len(devices) != 0 {
		t.Fatalf("marks left: %v", devices)
	}
}

// When the connections cannot be listed the mark stays for a retry.
func TestProfileMarkSurvivesAListFailure(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServerApply(h, l)
	h.srv.Route("GET", "/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"核心未响应"}`))
	})
	work, _ := h.withDevice()
	h.switchTo(h.profile("工作").ID)
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if out := h.s.InterruptOutcome(); out.Error == "" {
		t.Fatalf("outcome %+v", out)
	}
	if devices := h.s.InterruptDevices(); !slices.Equal(devices, []string{"phone"}) {
		t.Fatalf("marks %v", devices)
	}
}

// A refused apply names the device and the profile, says the core keeps the
// previous profile, and leaves the mark for a retry.
func TestFailedProfileSwitchNamesTheDevice(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	phoneServer(h, l)
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"不停机应用失败"}`))
	})
	work, _ := h.withDevice()
	h.s.NoteApplied(h.s.Doc())
	h.switchTo(h.profile("工作").ID)
	h.s.SetProfileInterrupt("phone", "phone", work, true)
	err := h.applyLive()
	if err == nil {
		t.Fatal("the apply should fail")
	}
	for _, want := range []string{"设备「手机」", "「工作」", "核心仍使用原配置", "不停机应用失败"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if h.s.ApplyError != err.Error() {
		t.Errorf("ApplyError %q", h.s.ApplyError)
	}
	if devices := h.s.InterruptDevices(); !slices.Equal(devices, []string{"phone"}) {
		t.Fatalf("marks %v", devices)
	}
	if got := l.list(); slices.Contains(got, "close") {
		t.Fatalf("connections closed after a failed apply: %v", got)
	}
}

// A failure with no switch pending keeps its own text.
func TestFailedApplyWithoutASwitchKeepsItsText(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"不停机应用失败"}`))
	})
	h.withDevice()
	h.s.NoteApplied(h.s.Doc())
	err := h.applyLive()
	if err == nil || strings.Contains(err.Error(), "profile") {
		t.Fatalf("error %v", err)
	}
}

// The core is told of the switch like any other change; the marks follow
// the console's profile and restarts.
func TestProfileMarksAreClearedWithTheOthers(t *testing.T) {
	h := newHarness(t, nil)
	h.s.SetProfileInterrupt("phone", "a", "b", true)
	h.s.SetInterrupt("rr-1", "x", "y", true)
	h.s.ClearInterrupts()
	if len(h.s.InterruptDevices()) != 0 || len(h.s.InterruptRules()) != 0 {
		t.Fatal("marks left")
	}
	h.s.SetProfileInterrupt("phone", "a", "b", true)
	h.env.Post(func() { h.s.SetProfiles(h.s.Profiles(), "another", nil) })
	h.until(func() bool { return len(h.s.InterruptDevices()) == 0 })
}
