package subscriptions

import (
	"image/png"

	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// addHop opens "+ Add hop" and chooses the option matching query.
func (f *fixture) addHop(trigger, query string) {
	f.t.Helper()
	f.click(trigger)
	f.tt.Type(query)
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()
}

func chainFixtureDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	ss := func(id, tag, server string) *model.Outbound {
		return outboundOf(id, "shadowsocks", tag, model.Fields{"server": server, "server_port": 8388.0, "method": "aes-128-gcm", "password": "secret"})
	}
	group := outboundOf("out_g", "selector", "自选", model.Fields{})
	group.Members = []string{"out_hk"}
	doc.Outbounds = append(doc.Outbounds,
		ss("out_hk", "香港 01", "hk.example"), ss("out_jp", "日本 01", "jp.example"), ss("out_my", "马来西亚服务器", "my.example"), group)
	return doc
}

// The outbound editor builds a proxy chain hop by hop, in the order
// traffic takes it, and saves it; a group may only be the first hop.
func TestOutboundEditorBuildsChain(t *testing.T) {
	f := newFixture(t, chainFixtureDoc())
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_my")
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "本机 → 本出站")

	f.addHop("添加一跳：＋ 添加第 1 跳", "自选")
	testkit.UntilText(t, f.tt, "本机 → 自选 → 本出站")
	for _, o := range forms.ChainCandidates(f.store.Doc(), []string{"out_my"}, []string{"out_g"}) {
		if o.Value == "out_g" || o.Detail == "策略组" {
			t.Fatalf("offered after the first hop: %+v", o)
		}
	}
	f.addHop("添加一跳：＋ 添加第 2 跳", "日本")
	testkit.UntilText(t, f.tt, "本机 → 自选 → 日本 01 → 本出站")
	if dir := os.Getenv("CONSOLE_PREVIEW_DIR"); dir != "" {
		out, _ := os.Create(filepath.Join(dir, "chain-editor.png"))
		_ = png.Encode(out, f.tt.Image())
		out.Close()
	}

	f.click("保存")
	doc := f.saved(func(d *model.Doc) bool {
		for _, o := range d.Outbounds {
			if o.ID == "out_my" {
				return len(o.Chain) == 2
			}
		}
		return false
	})
	for _, o := range doc.Outbounds {
		if o.ID == "out_my" && (!slices.Equal(o.Chain, []string{"out_g", "out_jp"}) || o.Fields["detour"] != nil) {
			t.Fatalf("saved chain %v fields %v", o.Chain, o.Fields)
		}
	}
}

// A chain leading back to the outbound shows the cycle and does not save.
func TestOutboundEditorRefusesCycle(t *testing.T) {
	doc := chainFixtureDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_hk" {
			o.Chain = []string{"out_my"} // 香港 01 goes through 马来西亚服务器
		}
	}
	f := newFixture(t, doc)
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_my")
	f.tt.Frame()
	f.addHop("添加一跳：＋ 添加第 1 跳", "香港")
	testkit.UntilText(t, f.tt, "循环依赖：马来西亚服务器 → 香港 01 → 马来西亚服务器，请调整代理链")
	f.click("保存")
	if !f.w.outbounds.editor.open {
		t.Fatal("a chain with a cycle saved")
	}
}
