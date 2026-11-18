package subscriptions

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func inboundsDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	for i, tag := range []string{"入站甲", "入站乙", "入站丙"} {
		in := model.MakeInbound("mixed", tag, "linux", false)
		in.ID = []string{"in_a", "in_b", "in_c"}[i]
		in.Fields["listen_port"] = float64(7000 + i)
		doc.Inbounds = append(doc.Inbounds, in)
	}
	return doc
}

func inboundIDs(doc *model.Doc) []string {
	var ids []string
	for _, in := range doc.Inbounds {
		ids = append(ids, in.ID)
	}
	return ids
}

// TestDragAfterListShrinks drags the last inbound card while another is
// deleted: the drop moves the dragged card instead of reading past the
// shortened list.
func TestDragAfterListShrinks(t *testing.T) {
	f := newFixture(t, inboundsDoc())
	tt := f.tt
	testkit.UntilText(t, tt, "入站丙")
	from, _ := tt.Find("入站丙")
	to, _ := tt.Find("入站甲")
	tt.Press(from.X+from.W/2, from.Y+from.H/2)
	for i := 1; i <= 4; i++ {
		tt.Move(from.X+from.W/2+(to.X-from.X)*float32(i)/8, from.Y+from.H/2)
	}
	if err := f.store.Update(func(d *model.Doc) { removeInbound(d, "in_b") }); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for i := 5; i <= 8; i++ {
		tt.Move(from.X+from.W/2+(to.X-from.X)*float32(i)/8, from.Y+from.H/2)
	}
	tt.Move(to.X+to.W/2, to.Y+to.H/2)
	tt.Release(to.X+to.W/2, to.Y+to.H/2)
	f.saved(func(doc *model.Doc) bool {
		ids := inboundIDs(doc)
		return len(ids) == 3 && slices.Equal(ids[1:], []string{"in_c", "in_a"})
	})
}

// TestReorderWithoutChangeDoesNotSave: a drop that moves nothing, or a
// card that is gone, saves nothing.
func TestReorderWithoutChangeDoesNotSave(t *testing.T) {
	f := newFixture(t, sampleDoc())
	saves := f.srv.Saves()
	f.w.reorderInbound("in_gone", f.store.Doc().Inbounds[0].ID)
	f.w.reorderTopLevel("sub:gone", "out_ss")
	f.w.reorderTopLevel("out_ss", "out_ss")
	f.tt.Frame()
	if f.srv.Saves() != saves || f.store.Saving() {
		t.Fatalf("saves %d → %d", saves, f.srv.Saves())
	}
	f.w.reorderTopLevel("sub:sub_a", "out_ss")
	f.saved(func(doc *model.Doc) bool {
		sub, ss := slices.Index(doc.OutboundOrder, "sub:sub_a"), slices.Index(doc.OutboundOrder, "out_ss")
		return sub >= 0 && ss >= 0 && sub < ss
	})
}

// TestSubEditorReopenEndsEdit: opening the subscription editor while it
// is open does not leave the store editing after it closes.
func TestSubEditorReopenEndsEdit(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.w.subs.editor.openEdit(f.store.Doc(), "sub_a")
	f.w.subs.editor.openAdd(f.store.Doc())
	f.w.subs.editor.openEdit(f.store.Doc(), "sub_a")
	f.w.subs.editor.close()
	if f.store.Editing() {
		t.Fatal("store still editing")
	}
}

// TestOutboundFormStartsFresh: a folding group opened in one outbound's
// editor is closed again when the editor switches to another outbound of
// the same type.
func TestOutboundFormStartsFresh(t *testing.T) {
	doc := sampleDoc()
	doc.Outbounds = append(doc.Outbounds, outboundOf("out_ss2", "shadowsocks", "台湾 SS",
		model.Fields{"server": "tw.example", "server_port": 8389.0, "method": "aes-128-gcm", "password": "secret"}))
	f := newFixture(t, doc)
	f.click("编辑 香港 SS")
	closed := len(f.tt.Texts())
	f.click("多路复用")
	if len(f.tt.Texts()) <= closed {
		t.Fatalf("group did not open: %q", f.tt.Texts())
	}
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_ss2")
	f.tt.Frame()
	if !f.tt.HasText("编辑出站 · 台湾 SS") || len(f.tt.Texts()) != closed {
		t.Fatalf("form state carried over: %d → %d %q", closed, len(f.tt.Texts()), f.tt.Texts())
	}
	f.w.outbounds.editor.close()
	if f.store.Editing() {
		t.Error("store still editing")
	}
}

// TestInboundFormStartsFresh: the same for inbounds.
func TestInboundFormStartsFresh(t *testing.T) {
	f := newFixture(t, inboundsDoc())
	f.click("编辑 入站甲")
	closed := len(f.tt.Texts())
	f.click("基本")
	if len(f.tt.Texts()) == closed {
		t.Fatalf("group did not change: %q", f.tt.Texts())
	}
	f.w.inbounds.editor.openEdit(f.store.Doc(), "in_b")
	f.tt.Frame()
	if !f.tt.HasText("编辑 入站乙") || len(f.tt.Texts()) != closed {
		t.Fatalf("form state carried over: %d → %d %q", closed, len(f.tt.Texts()), f.tt.Texts())
	}
	f.w.inbounds.editor.close()
}
