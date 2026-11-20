package subscriptions

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

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

// shoot saves the screen as name-light.png and name-dark.png when
// CONSOLE_PREVIEW_DIR is set.
func (f *fixture) shoot(name string) {
	dir := os.Getenv("CONSOLE_PREVIEW_DIR")
	if dir == "" {
		return
	}
	for _, dark := range []bool{false, true} {
		f.tt.SetDark(dark)
		f.tt.Frame()
		// Colors ease to the look.
		f.tt.Advance(time.Second)
		f.tt.Frame()
		look := "light"
		if dark {
			look = "dark"
		}
		out, err := os.Create(filepath.Join(dir, name+"-"+look+".png"))
		if err != nil {
			f.t.Fatal(err)
		}
		_ = png.Encode(out, f.tt.Image())
		out.Close()
	}
	f.tt.SetDark(false)
	f.tt.Frame()
}

// hopsShown waits for the chain editor to show exactly hops, in order,
// left to right.
func (f *fixture) hopsShown(hops ...string) {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool {
		var last float32 = -1
		for i, hop := range hops {
			r, ok := f.tt.Find(fmt.Sprintf("第 %d 跳 %s", i+1, hop))
			if !ok || r.X <= last {
				return false
			}
			last = r.X
		}
		next := fmt.Sprintf("第 %d 跳 ", len(hops)+1)
		return !slices.ContainsFunc(f.tt.Texts(), func(text string) bool { return strings.HasPrefix(text, next) })
	})
}

// The outbound editor builds a proxy chain hop by hop, in the order
// traffic takes it, left to right, and saves it; a group may only be the
// first hop, and a hop's × removes it.
func TestOutboundEditorBuildsChain(t *testing.T) {
	f := newFixture(t, chainFixtureDoc())
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_my")
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "本机")
	f.hopsShown()

	f.addHop("添加一跳：＋ 添加", "自选")
	f.hopsShown("自选")
	for _, o := range forms.ChainCandidates(f.store.Doc(), []string{"out_my"}, []string{"out_g"}) {
		if o.Value == "out_g" || o.Detail == "策略组" {
			t.Fatalf("offered after the first hop: %+v", o)
		}
	}
	f.addHop("添加一跳：＋ 添加", "日本")
	f.hopsShown("自选", "日本 01")
	f.addHop("添加一跳：＋ 添加", "香港")
	f.hopsShown("自选", "日本 01", "香港 01")
	f.click("移除 香港 01")
	f.hopsShown("自选", "日本 01")
	f.shoot("chain-editor")

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

// Dropping a hop on another moves it to that place.
func TestOutboundEditorReordersChain(t *testing.T) {
	doc := chainFixtureDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_my" {
			o.Chain = []string{"out_jp", "out_hk"}
		}
	}
	f := newFixture(t, doc)
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_my")
	f.tt.Frame()
	f.hopsShown("日本 01", "香港 01")
	from, _ := f.tt.Find("第 2 跳 香港 01")
	to, _ := f.tt.Find("第 1 跳 日本 01")
	x0, y0 := from.X+12, from.Y+from.H/2
	x1, y1 := to.X+12, to.Y+to.H/2
	f.tt.Press(x0, y0)
	for i := 1; i <= 8; i++ {
		f.tt.Move(x0+(x1-x0)*float32(i)/8, y0+(y1-y0)*float32(i)/8)
		f.tt.Frame()
	}
	f.tt.Release(x1, y1)
	f.tt.Frame()
	f.hopsShown("香港 01", "日本 01")
}

// A hop that may not stand where it is is marked, its problem below the
// chain, and the chain does not save.
func TestOutboundEditorMarksHopAtFault(t *testing.T) {
	doc := chainFixtureDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_my" {
			o.Chain = []string{"out_jp", "out_g"}
		}
	}
	f := newFixture(t, doc)
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_my")
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "第 2 跳起只能选择具体的服务器，「自选」是策略组")
	f.hopsShown("日本 01", "自选")
	f.shoot("chain-editor-error")
	f.click("保存")
	if !f.w.outbounds.editor.open {
		t.Fatal("a chain at fault saved")
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
	f.addHop("添加一跳：＋ 添加", "香港")
	testkit.UntilText(t, f.tt, "循环依赖：马来西亚服务器 → 香港 01 → 马来西亚服务器，请调整代理链")
	f.click("保存")
	if !f.w.outbounds.editor.open {
		t.Fatal("a chain with a cycle saved")
	}
}

// A card of an outbound with a proxy chain shows "链" left of its
// protocol tag; one without shows none.
func TestOutboundCardChainTag(t *testing.T) {
	doc := chainFixtureDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_my" {
			o.Chain = []string{"out_jp"}
		}
	}
	f := newFixture(t, doc)
	testkit.UntilText(t, f.tt, "马来西亚服务器")
	f.tt.SetSize(1400, 1000)
	f.tt.Frame()
	if n := slices.Index(f.tt.Texts(), "链"); n < 0 || f.tt.Texts()[n-1] != "马来西亚服务器" || f.tt.Texts()[n+1] != "SS" {
		t.Fatalf("no chain tag before the protocol: %q", f.tt.Texts())
	}
	if strings.Count(strings.Join(f.tt.Texts(), "\n"), "\n链\n") != 1 {
		t.Fatalf("chain tags on cards without a chain: %q", f.tt.Texts())
	}
	tag, _ := f.tt.Find("链")
	proto, _ := f.tt.Find("my.example:8388")
	if tag.Y > proto.Y {
		t.Fatalf("chain tag %+v not in the header", tag)
	}
	f.shoot("outbound-chain-tag")
}
