package store

import (
	"context"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// prefInterruptRules is the preference holding the traffic rules whose
// connections the next live apply closes.
const prefInterruptMarks = "kiko-interrupt-marks"

// InterruptResult is what the last live apply did to the connections of the
// rules marked with SetInterrupt: how many it closed, or why it could not.
type InterruptResult struct {
	Closed int
	Error  string
}

// interruptMark is the answer "Interrupt" for one traffic rule: the outbound the
// rule was saved with, and a generation that tells the answers apart.
type interruptMark struct {
	// From is the outbound the rule had when the first answer was given:
	// the one its connections use now. Exit is where the rule goes.
	From string `json:"from"`
	Exit string `json:"exit"`
	Gen  int    `json:"gen"`
}

// prefInterruptGen holds the generation counter, which only grows, so a
// generation never names two answers.
const prefInterruptGen = "kiko-interrupt-gen"

// SetInterrupt records the answer to the question of rule ruleID, saved
// with the outbound exit while its connections use from: interrupt marks it
// for the next live apply, which closes the rule's connections; false
// clears the mark. Saving a rule's new outbound only records the choice;
// no connection closes until the apply. The marks survive restarts of the
// console, like the unapplied change they belong to, and the latest answer
// for a rule replaces the earlier one (keeping the outbound the
// connections use, so a rule sent back there has nothing to close).
func (s *Store) SetInterrupt(ruleID, from, exit string, interrupt bool) {
	if ruleID == "" {
		return
	}
	var gen int
	s.env.Prefs.Load(prefInterruptGen, &gen)
	s.env.Prefs.Save(prefInterruptGen, gen+1)
	marks := s.loadInterrupts()
	if !interrupt {
		delete(marks, ruleID)
		s.saveInterrupts(marks)
		return
	}
	if earlier, ok := marks[ruleID]; ok {
		from = earlier.From
	}
	marks[ruleID] = interruptMark{From: from, Exit: exit, Gen: gen + 1}
	s.saveInterrupts(marks)
}

// prefAppliedExits holds the outbound of each traffic rule as of the
// configuration last applied from this console, by rule id.
const prefAppliedExits = "kiko-applied-exits"

// NoteApplied records the outbounds the traffic rules of doc have in the
// configuration just applied: where their connections go now. Only rules
// of the scopes in effect count; those of inactive device profiles have no
// connections.
func (s *Store) NoteApplied(doc *model.Doc) {
	exits := map[string]string{}
	active := model.PolicyOwners(doc)
	for _, r := range doc.Route.Rules {
		if (r.Action == "" || r.Action == model.ActionRoute) && slices.Contains(active, r.PolicyOwner()) {
			exits[r.ID] = r.Outbound.ID()
		}
	}
	s.env.Prefs.Save(prefAppliedExits, exits)
	s.noteAppliedScopes(doc)
}

func (s *Store) appliedExits() map[string]string {
	var exits map[string]string
	s.env.Prefs.Load(prefAppliedExits, &exits)
	return exits
}

// AppliedExit is the outbound traffic rule ruleID had in the configuration
// last applied from this console, if known.
func (s *Store) AppliedExit(ruleID string) (string, bool) {
	var exits map[string]string
	s.env.Prefs.Load(prefAppliedExits, &exits)
	exit, ok := exits[ruleID]
	return exit, ok && exit != ""
}

// InterruptFrom is the outbound the connections of rule ruleID use while it
// is marked, and whether it is marked.
func (s *Store) InterruptFrom(ruleID string) (string, bool) {
	m, ok := s.loadInterrupts()[ruleID]
	return m.From, ok
}

func (s *Store) loadInterrupts() map[string]interruptMark {
	marks := map[string]interruptMark{}
	s.env.Prefs.Load(prefInterruptMarks, &marks)
	if marks == nil {
		marks = map[string]interruptMark{}
	}
	return marks
}

func (s *Store) saveInterrupts(marks map[string]interruptMark) {
	if marks == nil {
		marks = map[string]interruptMark{}
	}
	s.env.Prefs.Save(prefInterruptMarks, marks)
}

// InterruptRules lists the traffic rules marked for interruption, sorted.
func (s *Store) InterruptRules() []string {
	var rules []string
	for id := range s.loadInterrupts() {
		rules = append(rules, id)
	}
	slices.Sort(rules)
	return rules
}

// ClearInterrupts forgets every mark: the core restarted its services, which
// closed the connections anyway, or another profile is in use.
func (s *Store) ClearInterrupts() {
	s.saveInterrupts(nil)
	s.saveProfileMarks(nil)
}

// pendingInterrupt is a mark an apply acts on.
type pendingInterrupt struct {
	rule string
	gen  int
}

// validInterrupts splits marks by the document about to be applied: those
// whose rule still exists, is enabled, routes to an outbound and still goes
// to the outbound the answer was given for and not back to the one its
// connections use or the one last applied, and the rest, which no longer
// describe a pending change (the rule was deleted, disabled or restored).
func validInterrupts(doc *model.Doc, marks map[string]interruptMark, applied map[string]string) (valid, stale []pendingInterrupt) {
	for id, mark := range marks {
		p := pendingInterrupt{id, mark.Gen}
		ok := false
		for _, r := range doc.Route.Rules {
			if r.ID == id {
				ok = r.Enabled && (r.Action == "" || r.Action == model.ActionRoute) && r.Outbound.ID() == mark.Exit && mark.Exit != mark.From && mark.Exit != applied[id]
			}
		}
		if ok {
			valid = append(valid, p)
		} else {
			stale = append(stale, p)
		}
	}
	slices.SortFunc(valid, func(a, b pendingInterrupt) int { return strings.Compare(a.rule, b.rule) })
	return valid, stale
}

// spendInterrupts forgets the marks of pending that no newer answer has
// replaced since the apply began.
func (s *Store) spendInterrupts(pending []pendingInterrupt) {
	if len(pending) == 0 {
		return
	}
	marks := s.loadInterrupts()
	for _, p := range pending {
		if m, ok := marks[p.rule]; ok && m.Gen == p.gen {
			delete(marks, p.rule)
		}
	}
	s.saveInterrupts(marks)
}

// InterruptOutcome is the result of the last live apply's interruption.
func (s *Store) InterruptOutcome() InterruptResult { return s.interrupted }

// ruleConnections lists the ids of the active connections each rule
// carries now, before an apply changes where they go.
func (s *Store) ruleConnections(ctx context.Context, pending []pendingInterrupt) ([]string, error) {
	var ids []string
	for _, p := range pending {
		q := api.ConnectionQuery{State: "active", Rule: p.rule, Group: "none", Limit: 200}
		for q.Page = 0; ; q.Page++ {
			page, err := s.env.API.QueryConnections(ctx, q)
			if err != nil {
				return nil, err
			}
			for _, row := range page.Rows {
				ids = append(ids, row.ID)
			}
			if len(page.Rows) == 0 || (q.Page+1)*q.Limit >= page.Total {
				break
			}
		}
	}
	return ids, nil
}
