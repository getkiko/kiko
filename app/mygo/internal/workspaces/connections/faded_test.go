package connections

import (
	"image/png"
	"os"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// Among all connections the ended ones fade; the Closed tab shows them as
// they are, and group lines and active connections never fade.
func TestClosedConnectionsFade(t *testing.T) {
	m := &connMonitor{tab: tabAll}
	closed := visibleNode{Node: leafNode(&Row{ClosedAt: 1})}
	active := visibleNode{Node: leafNode(&Row{})}
	group := visibleNode{Node: &node{Key: "app:x"}}
	if !m.faded(closed) || m.faded(active) || m.faded(group) || m.faded(visibleNode{MoreKey: "more"}) {
		t.Fatal("All tab: only ended connections fade")
	}
	for _, tab := range []int{tabActive, tabClosed, tabRejected} {
		m.tab = tab
		if m.faded(closed) {
			t.Fatalf("tab %d fades its connections", tab)
		}
	}

	h := newHarness(t, func(*testkit.Server) {})
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, h.tt, "mtalk.google.com")
	testkit.UntilText(t, h.tt, "8.8.8.8:53")
	if dir := os.Getenv("CONSOLE_PREVIEW_DIR"); dir != "" {
		f, err := os.Create(dir + "/connections-faded.png")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_ = png.Encode(f, h.tt.Image())
	}
}
