package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// prefProfileMarks holds the answers "Interrupt" given when a device switched its
// profile, by device id, next to the marks of single rules.
const prefProfileMarks = "kiko-interrupt-profile-marks"

// prefAppliedScopes holds, by device id, the scope in effect in the
// configuration last applied from this console.
const prefAppliedScopes = "kiko-applied-scopes"

// profileMark is the answer "Interrupt" for one device: its connections matched the
// rules of scope From; the device now uses scope To. Gen tells answers apart.
type profileMark struct {
	From string `json:"from"`
	To   string `json:"to"`
	Gen  int    `json:"gen"`
}

// SetProfileInterrupt records the answer to the question of device
// deviceID switching from scope fromScope (where its connections are) to
// scope toScope: interrupt marks it for the next live apply, which closes
// the connections the device's rules of fromScope carry; false clears the
// mark. A later answer keeps the first answer's fromScope, and a switch
// back to it clears the mark: there is nothing to close.
func (s *Store) SetProfileInterrupt(deviceID, fromScope, toScope string, interrupt bool) {
	if deviceID == "" {
		return
	}
	var gen int
	s.env.Prefs.Load(prefInterruptGen, &gen)
	s.env.Prefs.Save(prefInterruptGen, gen+1)
	marks := s.loadProfileMarks()
	if earlier, ok := marks[deviceID]; ok {
		fromScope = earlier.From
	}
	if !interrupt || fromScope == toScope {
		delete(marks, deviceID)
	} else {
		marks[deviceID] = profileMark{From: fromScope, To: toScope, Gen: gen + 1}
	}
	s.saveProfileMarks(marks)
}

// ProfileInterrupt is the scope the connections of device deviceID are in
// while it is marked, and whether it is marked.
func (s *Store) ProfileInterrupt(deviceID string) (from string, ok bool) {
	m, ok := s.loadProfileMarks()[deviceID]
	return m.From, ok
}

// InterruptDevices lists the devices marked for interruption, sorted.
func (s *Store) InterruptDevices() []string {
	var devices []string
	for id := range s.loadProfileMarks() {
		devices = append(devices, id)
	}
	slices.Sort(devices)
	return devices
}

func (s *Store) loadProfileMarks() map[string]profileMark {
	marks := map[string]profileMark{}
	s.env.Prefs.Load(prefProfileMarks, &marks)
	if marks == nil {
		marks = map[string]profileMark{}
	}
	return marks
}

func (s *Store) saveProfileMarks(marks map[string]profileMark) {
	if marks == nil {
		marks = map[string]profileMark{}
	}
	s.env.Prefs.Save(prefProfileMarks, marks)
}

// noteAppliedScopes records the scope each device of doc uses in the
// configuration just applied, the local scope under model.LocalDeviceID.
func (s *Store) noteAppliedScopes(doc *model.Doc) {
	scopes := map[string]string{}
	for _, d := range model.ProfileDevices(doc) {
		scopes[d.ID] = model.ActiveScope(d)
	}
	s.env.Prefs.Save(prefAppliedScopes, scopes)
}

func (s *Store) appliedScopes() map[string]string {
	var scopes map[string]string
	s.env.Prefs.Load(prefAppliedScopes, &scopes)
	return scopes
}

// AppliedScope is the scope device deviceID used in the configuration last
// applied from this console, if known: the one its connections match.
func (s *Store) AppliedScope(deviceID string) (string, bool) {
	scope, ok := s.appliedScopes()[deviceID]
	return scope, ok && scope != ""
}

// ConnectionScope is the scope the connections of device d match now: the
// one last applied, else the one a pending answer was given against, else
// the one in effect.
func (s *Store) ConnectionScope(doc *model.Doc, d *model.RouteDevice) string {
	candidates := []string{}
	if scope, ok := s.AppliedScope(d.ID); ok {
		candidates = append(candidates, scope)
	}
	if from, ok := s.ProfileInterrupt(d.ID); ok {
		candidates = append(candidates, from)
	}
	for _, scope := range candidates {
		if slices.Contains(model.DeviceScopes(d), scope) {
			return scope
		}
	}
	return model.ActiveScope(d)
}

// SwitchedProfileChanges lists the devices of doc whose profile in effect
// is not the one last applied: pending profile switches, as
// "Device X switched to Y", in Chinese.
func (s *Store) SwitchedProfileChanges(doc *model.Doc) []string {
	var out []string
	applied := s.appliedScopes()
	for _, d := range model.ProfileDevices(doc) {
		scope := model.ActiveScope(d)
		from, ok := applied[d.ID]
		if !ok {
			// Nothing applied from this console is known: a pending answer
			// still tells where the device was.
			from, ok = s.ProfileInterrupt(d.ID)
		}
		if ok && from != scope && slices.Contains(model.DeviceScopes(d), from) {
			out = append(out, fmt.Sprintf("设备「%s」切换到「%s」", d.Name, model.ActiveProfileOf(d).Name))
		}
	}
	return out
}

// pendingProfile is a device mark an apply acts on: the scope whose
// connections close.
type pendingProfile struct {
	device, from string
	gen          int
}

// validProfileInterrupts splits the device marks by the document about to
// be applied: those whose device still exists, uses the scope the answer was
// given for and not the one its connections match or the one last applied,
// whose old scope still exists and whose exit sequence differs from the
// new scope's; and the rest, which no longer describe a pending switch.
func validProfileInterrupts(doc *model.Doc, marks map[string]profileMark, applied map[string]string) (valid, stale []pendingProfile) {
	for id, mark := range marks {
		p := pendingProfile{id, mark.From, mark.Gen}
		ok := false
		for _, d := range model.ProfileDevices(doc) {
			if d.ID != id {
				continue
			}
			scopes := model.DeviceScopes(d)
			ok = model.ActiveScope(d) == mark.To && mark.To != mark.From && mark.To != applied[id] &&
				slices.Contains(scopes, mark.From) && slices.Contains(scopes, mark.To) &&
				ExitSequencesDiffer(doc, mark.From, mark.To)
		}
		if ok {
			valid = append(valid, p)
		} else {
			stale = append(stale, p)
		}
	}
	slices.SortFunc(valid, func(a, b pendingProfile) int { return strings.Compare(a.device, b.device) })
	return valid, stale
}

// spendProfileInterrupts forgets the device marks of pending that no newer
// answer has replaced since the apply began.
func (s *Store) spendProfileInterrupts(pending []pendingProfile) {
	if len(pending) == 0 {
		return
	}
	marks := s.loadProfileMarks()
	for _, p := range pending {
		if m, ok := marks[p.device]; ok && m.Gen == p.gen {
			delete(marks, p.device)
		}
	}
	s.saveProfileMarks(marks)
}

// scopeConnections lists the ids of the active connections that matched a
// rule of one of scopes: a rule the route table numbers or the scope's
// default exit. The core reports each connection's matched rule; the rule
// map of the applied configuration names its owner scope (RuleOwner), so
// the connections are those the old profile's rules carry, listed before an
// apply replaces them. A rule the map does not know (generated rules, rules
// of an applied configuration from another console) has no owner and its
// connections are not closed.
func (s *Store) scopeConnections(ctx context.Context, scopes []string) ([]string, error) {
	var ids []string
	if len(scopes) == 0 {
		return nil, nil
	}
	q := api.ConnectionQuery{State: "active", Group: "none", Limit: 200}
	for q.Page = 0; ; q.Page++ {
		page, err := s.env.API.QueryConnections(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, row := range page.Rows {
			if slices.Contains(scopes, row.RuleOwner) {
				ids = append(ids, row.ID)
			}
		}
		if len(page.Rows) == 0 || (q.Page+1)*q.Limit >= page.Total {
			break
		}
	}
	return ids, nil
}
