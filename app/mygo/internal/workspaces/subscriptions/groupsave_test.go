package subscriptions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// This file uses only what the policy group editor had before its
// redesign (F8), so that it runs on that code too and shows the saved
// documents and configurations unchanged. Since the editor's redesign
// (Fix item 40) opening a group turns a pattern of literal alternatives
// into keywords (model.MigrateFilterKeywords), which select the same
// nodes: the expected group is migrated too, and saving unchanged emits
// the configuration of the group saved as it was.

// parityDoc is sampleDoc with nodes in two regions, an info node, and a
// group of each mode with members, a filter and fields of its own.
func parityDoc() *model.Doc {
	doc := sampleDoc()
	for i, name := range []string{"🇭🇰 香港 01", "🇭🇰 香港 02", "🇯🇵 日本 01", "🇯🇵 日本 02", "剩余流量：1 GB"} {
		n := outboundOf(fmt.Sprintf("out_p%02d", i), "trojan", name, model.Fields{
			"server": fmt.Sprintf("p%02d.example", i), "server_port": 443.0, "password": "p",
			"__key": fmt.Sprintf("trojan|p%02d.example|443|p", i),
		})
		n.SubID = "sub_a"
		doc.Outbounds = append(doc.Outbounds, n)
	}
	group := func(id, typ, tag string, members []string, filter *model.GroupFilter, fields model.Fields) {
		o := model.MakeOutbound(typ, tag)
		o.ID, o.Members, o.Filter = id, members, filter
		for key, value := range fields {
			o.Fields[key] = value
		}
		doc.Outbounds = append(doc.Outbounds, o)
	}
	group("out_pg_manual", model.ModeManual, "P 手选", []string{"out_ss", "out_vless"}, nil,
		model.Fields{"default_ref": "out_vless", "interrupt_exist_connections": true})
	group("out_pg_fastest", model.ModeFastest, "P 最快", []string{"out_ss"}, &model.GroupFilter{Regions: []string{"hk"}},
		model.Fields{"tolerance": 80.0, "interval": "5m"})
	group("out_pg_fallback", model.ModeFallback, "P 故障", []string{"out_vless", "out_ss"}, &model.GroupFilter{Pattern: "日本"},
		model.Fields{"max_failures": 4.0, "no_return": true})
	group("out_pg_lb", model.ModeLoadBalance, "P 均衡", nil, &model.GroupFilter{Regions: []string{"hk", "jp"}, Exclude: "02"},
		model.Fields{"strategy": "sticky_sessions", "timeout": "8s"})
	group("out_pg_smart", model.ModeSmart, "P 智能", []string{"out_ss"}, &model.GroupFilter{KeepInfo: true, Pattern: "(?i)香港"},
		model.Fields{"idle_timeout": "30m"})
	return doc
}

// paritySave opens group id in the editor, lets change edit the draft,
// saves, and returns the server's document before and after.
func paritySave(t *testing.T, id string, change func(*model.Outbound)) (before, after *model.Doc) {
	t.Helper()
	f := newFixture(t, parityDoc())
	revision := f.srv.Store().Revision
	before = model.DecodeDoc(f.srv.Store().Doc)
	f.w.outbounds.editor.openEdit(f.store.Doc(), id)
	f.tt.Frame()
	f.tt.Frame()
	if change != nil {
		change(f.w.outbounds.editor.draft)
		f.tt.Frame()
	}
	f.click("保存")
	testkit.Until(t, f.tt, func() bool {
		return !f.w.outbounds.editor.open && !f.store.Saving() && f.srv.Store().Revision > revision
	})
	return before, model.DecodeDoc(f.srv.Store().Doc)
}

// expectSaved is doc with group id put through want, as saving does by
// the model's functions alone; migrate migrates its filter to keywords
// first, as opening the editor does.
func expectSaved(doc *model.Doc, id string, migrate bool, want func(*model.Outbound)) *model.Doc {
	out := model.Clone(doc)
	for i, o := range out.Outbounds {
		if o.ID == id {
			g := model.Clone(o)
			if migrate {
				model.MigrateFilterKeywords(g.Filter)
			}
			if want != nil {
				want(g)
			}
			normalizeFilter(g)
			model.PruneGroupFields(g)
			out.Outbounds[i] = g
		}
	}
	return out
}

func outboundJSON(t *testing.T, doc *model.Doc, id string) []byte {
	t.Helper()
	for _, o := range doc.Outbounds {
		if o.ID == id {
			raw, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
	}
	t.Fatalf("no outbound %s", id)
	return nil
}

// checkParity compares the saved group, document and configuration with
// the expected ones byte for byte.
func checkParity(t *testing.T, id string, got, want *model.Doc) {
	t.Helper()
	if g, w := outboundJSON(t, got, id), outboundJSON(t, want, id); !bytes.Equal(g, w) {
		t.Errorf("saved group\n got %s\nwant %s", g, w)
	}
	gotDoc, _ := json.Marshal(got)
	wantDoc, _ := json.Marshal(want)
	if !bytes.Equal(gotDoc, wantDoc) {
		t.Errorf("saved document differs\n got %s\nwant %s", gotDoc, wantDoc)
	}
	g, w := emit.Emit(got, emit.Options{}), emit.Emit(want, emit.Options{})
	if !bytes.Equal(g.JSON, w.JSON) {
		t.Errorf("emitted configuration differs\n got %s\nwant %s", g.JSON, w.JSON)
	}
	if len(g.JSON) == 0 {
		t.Error("nothing emitted")
	}
}

var parityGroups = []string{"out_pg_manual", "out_pg_fastest", "out_pg_fallback", "out_pg_lb", "out_pg_smart"}

// TestGroupEditorSavesUnchanged: opening a group of each mode and saving
// it unchanged stores the group as the model's own functions would, and
// the document and its configuration are those computed without the
// editor.
func TestGroupEditorSavesUnchanged(t *testing.T) {
	for _, id := range parityGroups {
		t.Run(id, func(t *testing.T) {
			before, after := paritySave(t, id, nil)
			checkParity(t, id, after, expectSaved(before, id, true, nil))
			g, w := emit.Emit(after, emit.Options{}), emit.Emit(expectSaved(before, id, false, nil), emit.Options{})
			if !bytes.Equal(g.JSON, w.JSON) {
				t.Errorf("keywords select other nodes than the pattern\n got %s\nwant %s", g.JSON, w.JSON)
			}
		})
	}
}

// TestGroupEditorSavesSwitchedMode: switching a group's mode in the
// editor and saving stores what model.SwitchGroupMode and the save's
// pruning make of the group.
func TestGroupEditorSavesSwitchedMode(t *testing.T) {
	to := map[string]string{
		"out_pg_manual": model.ModeFallback, "out_pg_fastest": model.ModeLoadBalance, "out_pg_fallback": model.ModeSmart,
		"out_pg_lb": model.ModeManual, "out_pg_smart": model.ModeFastest,
	}
	for _, id := range parityGroups {
		mode := to[id]
		t.Run(id+"-"+mode, func(t *testing.T) {
			switchTo := func(o *model.Outbound) { model.SwitchGroupMode(o, mode) }
			before, after := paritySave(t, id, switchTo)
			checkParity(t, id, after, expectSaved(before, id, true, switchTo))
		})
	}
}
