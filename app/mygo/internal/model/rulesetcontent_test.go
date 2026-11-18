package model_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func entry(field, value string) model.RuleSetEntry {
	return model.RuleSetEntry{Field: field, Value: value}
}

// decoded reads a source rule set as the server returns it, an srs file
// decoded to the source format.
func decoded(t *testing.T, text string) []any {
	t.Helper()
	var content struct {
		Rules []any `json:"rules"`
	}
	if err := json.Unmarshal([]byte(text), &content); err != nil {
		t.Fatal(err)
	}
	return content.Rules
}

func items(list []model.RuleSetItem) []string {
	var out []string
	for _, item := range list {
		out = append(out, item.Field+"="+item.Value)
	}
	return out
}

func TestFlattenRuleSet(t *testing.T) {
	rules := decoded(t, `{"version":3,"rules":[
		{"domain":["a.example"],"domain_suffix":["b.example","c.example"],"domain_keyword":["k"],"domain_regex":["^r\\."]},
		{"ip_cidr":["1.1.1.0/24"]},
		{"ip_cidr":"2.2.2.0/24"},
		{"domain":["q.example"],"ip_cidr":["3.3.3.0/24"]},
		{"source_ip_cidr":["10.0.0.0/8"]},
		{"process_name":["curl"],"invert":false},
		{"domain":["x.example"],"port":[443]},
		{"domain":["y.example"],"source_ip_cidr":["10.0.0.0/8"]},
		{"domain":["z.example"],"invert":true},
		{"type":"logical","mode":"and","rules":[{"domain":["l.example"]},{"port":[80]}]}
	]}`)
	got := items(model.FlattenRuleSet(rules))
	want := []string{"domain=a.example", "domain_suffix=b.example", "domain_suffix=c.example", "domain_keyword=k", "domain_regex=^r\\.",
		"ip_cidr=1.1.1.0/24", "ip_cidr=2.2.2.0/24", `={"domain":["q.example"],"ip_cidr":["3.3.3.0/24"]}`,
		"source_ip_cidr=10.0.0.0/8", "process_name=curl",
		`={"domain":["x.example"],"port":[443]}`, `={"domain":["y.example"],"source_ip_cidr":["10.0.0.0/8"]}`,
		`={"domain":["z.example"],"invert":true}`,
		`={"mode":"and","rules":[{"domain":["l.example"]},{"port":[80]}],"type":"logical"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flatten\n got %q\nwant %q", got, want)
	}
	if model.RuleSetFieldLabel("") != "组合规则" || model.RuleSetFieldLabel("ip_cidr") != "IP 段" {
		t.Error("labels")
	}
}

// Large sets flatten and merge fast enough for a background load.
func TestFlattenRuleSetLarge(t *testing.T) {
	values := make([]any, 300000)
	for i := range values {
		values[i] = fmt.Sprintf("d%d.example", i)
	}
	rules := []any{map[string]any{"domain_suffix": values}}
	start := time.Now()
	flat := model.FlattenRuleSet(rules)
	index := model.IndexRuleSet(flat)
	view := model.MergeRuleSetView(flat, index, &model.RuleSetOverrides{Remove: []model.RuleSetEntry{entry("domain_suffix", "d7.example")}})
	hits := model.FilterRuleSetView(view, "d29999")
	if len(flat) != 300000 || len(view) != 300000 || view[7].State != model.EntryRemoved || len(hits) != 11 {
		t.Fatalf("flat %d view %d state %v hits %d", len(flat), len(view), view[7].State, len(hits))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

func viewOf(source []any, o *model.RuleSetOverrides) []string {
	flat := model.FlattenRuleSet(source)
	var out []string
	for _, item := range model.MergeRuleSetView(flat, nil, o) {
		mark := map[model.RuleSetEntryState]string{model.EntrySource: "", model.EntryRemoved: "-", model.EntryAdded: "+"}[item.State]
		out = append(out, mark+item.Field+"="+item.Value)
	}
	return out
}

func TestRuleSetOverridesMergeAndUndo(t *testing.T) {
	source := []any{map[string]any{"domain": []any{"a.example", "b.example"}}, map[string]any{"ip_cidr": []any{"1.1.1.0/24"}}}
	index := model.IndexRuleSet(model.FlattenRuleSet(source))
	var o *model.RuleSetOverrides
	var changed bool
	o, changed = model.AddOverride(o, index, entry("domain_suffix", " c.example "))
	if !changed || !reflect.DeepEqual(o.Add, []model.RuleSetEntry{entry("domain_suffix", "c.example")}) {
		t.Fatalf("add %+v", o)
	}
	// Adding twice, or adding what the source has, changes nothing.
	if _, changed = model.AddOverride(o, index, entry("domain_suffix", "c.example")); changed {
		t.Error("added twice")
	}
	if _, changed = model.AddOverride(o, index, entry("domain", "a.example")); changed {
		t.Error("added a source entry")
	}
	o, _ = model.RemoveOverride(o, index, entry("domain", "b.example"))
	if _, changed = model.RemoveOverride(o, index, entry("domain", "b.example")); changed {
		t.Error("removed twice")
	}
	got := viewOf(source, o)
	want := []string{"+domain_suffix=c.example", "domain=a.example", "-domain=b.example", "ip_cidr=1.1.1.0/24"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("view %q", got)
	}
	if model.OverrideSummary(o) != "+1 −1" {
		t.Errorf("summary %q", model.OverrideSummary(o))
	}
	// The source updates: the overrides apply to the new content, a removal
	// it no longer has stays (marked) for a later one.
	next := []any{map[string]any{"domain": []any{"a.example", "d.example"}}, map[string]any{"domain_suffix": []any{"c.example"}}}
	got = viewOf(next, o)
	want = []string{"domain=a.example", "domain=d.example", "domain_suffix=c.example", "-domain=b.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after update %q", got)
	}
	// Removing an added entry takes the addition back; adding a removed one
	// takes the removal back.
	if back, _ := model.RemoveOverride(o, index, entry("domain_suffix", "c.example")); len(back.Add) != 0 || len(back.Remove) != 1 {
		t.Errorf("remove added %+v", back)
	}
	if back, _ := model.AddOverride(o, index, entry("domain", "b.example")); len(back.Remove) != 0 || len(back.Add) != 1 {
		t.Errorf("add removed %+v", back)
	}
	// Undo each mark; overrides that end empty are nil.
	view := model.MergeRuleSetView(model.FlattenRuleSet(source), index, o)
	for _, item := range view {
		if item.State != model.EntrySource {
			o, changed = model.UndoOverride(o, item)
			if !changed {
				t.Errorf("undo %+v", item)
			}
		}
	}
	if o != nil {
		t.Errorf("after undo %+v", o)
	}
	if _, changed = model.UndoOverride(nil, view[1]); changed {
		t.Error("undo of a source entry")
	}
}

func TestValidateRuleSetEntry(t *testing.T) {
	for _, c := range []struct {
		e  model.RuleSetEntry
		ok bool
	}{
		{entry("domain", "a.example"), true},
		{entry("domain", "  "), false},
		{entry("ip_cidr", "1.1.1.1"), true},
		{entry("ip_cidr", "1.1.1.0/24"), true},
		{entry("source_ip_cidr", "nope"), false},
		{entry("domain_regex", "("), false},
		{entry("port", "80"), false},
	} {
		if got := model.ValidateRuleSetEntry(c.e) == ""; got != c.ok {
			t.Errorf("%+v: %v", c.e, got)
		}
	}
}

func TestCustomRuleSetEntries(t *testing.T) {
	var rules []any
	var ok bool
	rules, ok = model.AddRuleSetEntry(rules, entry("domain_suffix", "a.example"))
	rules, _ = model.AddRuleSetEntry(rules, entry("ip_cidr", "10.0.0.0/8"))
	rules, _ = model.AddRuleSetEntry(rules, entry("domain_suffix", "b.example"))
	if !ok {
		t.Fatal("add")
	}
	if _, ok = model.AddRuleSetEntry(rules, entry("domain_suffix", "a.example")); ok {
		t.Error("added twice")
	}
	raw, _ := json.Marshal(rules)
	if string(raw) != `[{"domain_suffix":["a.example","b.example"]},{"ip_cidr":["10.0.0.0/8"]}]` {
		t.Fatalf("rules %s", raw)
	}
	before := string(raw)
	// Editing a value keeps its place; changing its field moves it.
	edited, ok := model.ReplaceRuleSetEntry(rules, entry("domain_suffix", "a.example"), entry("domain_suffix", "z.example"))
	raw, _ = json.Marshal(edited)
	if !ok || string(raw) != `[{"domain_suffix":["z.example","b.example"]},{"ip_cidr":["10.0.0.0/8"]}]` {
		t.Errorf("edit %s", raw)
	}
	if after, _ := json.Marshal(rules); string(after) != before {
		t.Errorf("rules modified: %s", after)
	}
	if _, ok = model.ReplaceRuleSetEntry(rules, entry("domain_suffix", "a.example"), entry("domain_suffix", "b.example")); ok {
		t.Error("edit onto an existing value")
	}
	moved, _ := model.ReplaceRuleSetEntry(rules, entry("ip_cidr", "10.0.0.0/8"), entry("domain", "c.example"))
	raw, _ = json.Marshal(moved)
	if string(raw) != `[{"domain_suffix":["a.example","b.example"]},{"domain":["c.example"]}]` {
		t.Errorf("move %s", raw)
	}
	// Removing the last value drops its rule; a compound rule goes by its
	// JSON.
	withCompound := append(moved, map[string]any{"domain": []any{"p.example"}, "port": []any{float64(443)}})
	flat := model.FlattenRuleSet(withCompound)
	compound := flat[len(flat)-1]
	if !compound.Compound() {
		t.Fatalf("compound %+v", compound)
	}
	left := model.RemoveRuleSetEntry(withCompound, model.RuleSetItem{Field: "domain", Value: "c.example"})
	left = model.RemoveRuleSetEntry(left, compound)
	raw, _ = json.Marshal(left)
	if string(raw) != `[{"domain_suffix":["a.example","b.example"]}]` {
		t.Errorf("remove %s", raw)
	}
	// Round trip through the document.
	doc := &model.RuleSet{ID: "mine", Tag: "mine", Enabled: true, Mode: "inline", Rules: left}
	back := model.Clone(doc)
	if got := items(model.FlattenRuleSet(back.Rules)); !reflect.DeepEqual(got, []string{"domain_suffix=a.example", "domain_suffix=b.example"}) {
		t.Errorf("round trip %q", got)
	}
}

func TestRuleSetKind(t *testing.T) {
	for _, c := range []struct {
		r    model.RuleSet
		want string
	}{
		{model.RuleSet{Tag: "Gfw", Mode: "remote"}, "内置"},
		{model.RuleSet{Tag: "a", Mode: "remote", URL: "https://e.com/a.srs"}, "远程 srs"},
		{model.RuleSet{Tag: "a", Mode: "remote", URL: "https://e.com/a.JSON?x=1"}, "远程 json"},
		{model.RuleSet{Tag: "a", Mode: "inline", URL: "https://e.com/a.json"}, "内联"},
		{model.RuleSet{Tag: "a", Mode: "inline"}, "自建"},
	} {
		if got := model.RuleSetKind(&c.r); got != c.want {
			t.Errorf("%+v: %s", c.r, got)
		}
	}
}

// Overrides follow a renamed rule set and survive a clone.
func TestRuleSetOverridesInDocument(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Route.RuleSets = []*model.RuleSet{{ID: "a", Tag: "old", Enabled: true, Mode: "remote", URL: "https://e.com/a.srs",
		Overrides: &model.RuleSetOverrides{Add: []model.RuleSetEntry{entry("domain", "a.example")}}}}
	renamed := model.RenameRuleSetReferences(doc, "old", "new")
	cloned := model.Clone(renamed)
	if o := cloned.Route.RuleSets[0].Overrides; o == nil || !reflect.DeepEqual(o.Add, []model.RuleSetEntry{entry("domain", "a.example")}) {
		t.Errorf("overrides %+v", o)
	}
}

// An added entry the source came to have shows as the source's; removing
// it takes the addition back and removes it at once.
func TestRemoveOverrideOfAddedSourceEntry(t *testing.T) {
	o := &model.RuleSetOverrides{Add: []model.RuleSetEntry{entry("domain", "a.example")}}
	source := []any{map[string]any{"domain": []any{"a.example"}}}
	flat := model.FlattenRuleSet(source)
	index := model.IndexRuleSet(flat)
	if got := viewOf(source, o); !reflect.DeepEqual(got, []string{"domain=a.example"}) {
		t.Fatalf("view %q", got)
	}
	next, changed := model.RemoveOverride(o, index, entry("domain", "a.example"))
	if !changed || next == nil || len(next.Add) != 0 || !reflect.DeepEqual(next.Remove, []model.RuleSetEntry{entry("domain", "a.example")}) {
		t.Fatalf("remove %+v", next)
	}
	if got := viewOf(source, next); !reflect.DeepEqual(got, []string{"-domain=a.example"}) {
		t.Errorf("view after %q", got)
	}
	// Removed already: only the addition goes.
	both := &model.RuleSetOverrides{Add: o.Add, Remove: next.Remove}
	if after, changed := model.RemoveOverride(both, index, entry("domain", "a.example")); !changed || len(after.Add) != 0 || len(after.Remove) != 1 {
		t.Errorf("remove again %+v", after)
	}
}

// The core leaves an inverted rule as it is: it shows whole.
func TestFlattenInvertedRule(t *testing.T) {
	flat := model.FlattenRuleSet([]any{map[string]any{"domain": []any{"a.example"}, "invert": true}})
	if len(flat) != 1 || !flat[0].Compound() {
		t.Fatalf("flat %+v", flat)
	}
}

func TestFoldOverrides(t *testing.T) {
	rules := []any{map[string]any{"domain": []any{"a.example", "b.example"}}, map[string]any{"ip_cidr": []any{"10.0.0.0/8"}},
		map[string]any{"domain": []any{"b.example"}, "port": []any{float64(443)}}}
	o := &model.RuleSetOverrides{Add: []model.RuleSetEntry{entry("domain", "c.example"), entry("process_name", "curl")},
		Remove: []model.RuleSetEntry{entry("domain", "b.example"), entry("ip_cidr", "10.0.0.0/8")}}
	raw, _ := json.Marshal(model.FoldOverrides(rules, o))
	want := `[{"domain":["a.example","c.example"]},{"domain":["b.example"],"port":[443]},{"process_name":["curl"]}]`
	if string(raw) != want {
		t.Errorf("fold %s", raw)
	}
}

// Address ranges are marked as the core subtracts them.
func TestMergeMarksAddressRanges(t *testing.T) {
	source := []any{map[string]any{"ip_cidr": []any{"10.1.0.0/16", "10.2.0.0/16", "192.168.0.0/24"}}}
	o := &model.RuleSetOverrides{Remove: []model.RuleSetEntry{entry("ip_cidr", "10.0.0.0/8")}}
	flat := model.FlattenRuleSet(source)
	view := model.MergeRuleSetView(flat, nil, o)
	if len(view) != 3 || view[0].State != model.EntryRemoved || view[1].State != model.EntryRemoved || view[2].State != model.EntrySource ||
		view[0].By != entry("ip_cidr", "10.0.0.0/8") {
		t.Fatalf("covered %+v", view)
	}
	// Undo takes back the removal that covers it.
	if next, changed := model.UndoOverride(o, view[0]); !changed || next != nil {
		t.Errorf("undo %+v", next)
	}
	// A /24 removal and a source that now has the /23 around it.
	o = &model.RuleSetOverrides{Remove: []model.RuleSetEntry{entry("ip_cidr", "1.1.1.0/24")}}
	updated := []any{map[string]any{"ip_cidr": []any{"1.1.0.0/23", "8.8.8.8"}}}
	view = model.MergeRuleSetView(model.FlattenRuleSet(updated), nil, o)
	if len(view) != 2 || view[0].State != model.EntryPartlyRemoved || view[0].By != entry("ip_cidr", "1.1.1.0/24") || view[1].State != model.EntrySource {
		t.Fatalf("partly %+v", view)
	}
	if next, changed := model.UndoOverride(o, view[0]); !changed || next != nil {
		t.Errorf("undo partly %+v", next)
	}
	// Another field's ranges stay apart.
	o = &model.RuleSetOverrides{Remove: []model.RuleSetEntry{entry("source_ip_cidr", "1.1.0.0/16")}}
	view = model.MergeRuleSetView(model.FlattenRuleSet(updated), nil, o)
	if len(view) != 3 || view[0].State != model.EntrySource || view[2].State != model.EntryRemoved {
		t.Errorf("other field %+v", view)
	}
}

func TestCountRuleSetEntries(t *testing.T) {
	rules := []any{map[string]any{"domain": []any{"a", "b"}, "domain_suffix": "c"}, map[string]any{"domain": []any{"x"}, "port": []any{float64(1)}},
		map[string]any{"type": "logical", "mode": "or", "rules": []any{}}}
	if n := model.CountRuleSetEntries(rules); n != 5 || n != len(model.FlattenRuleSet(rules)) {
		t.Errorf("count %d", n)
	}
}
