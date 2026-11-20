package connections

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// A PID filter option shows its process name after the number and still
// filters by the PID.
func TestConnectionPIDFacetNames(t *testing.T) {
	h := newHarness(t, func(*testkit.Server) {})
	tt := h.tt
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "mtalk.google.com")
	for _, text := range []string{"PID", "100", "101", "102", "codex", "Doubaoime"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	savePreview(t, tt, "connections-pid-facet.png")
	h.click(t, "101 codex")
	testkit.Until(t, tt, func() bool { return !tt.HasText("mtalk.google.com") })
	if !tt.HasText("api.github.com") || tt.HasText("ime.doubao.com") {
		t.Fatalf("pid filter: %q", tt.Texts())
	}
	h.conns.mu.Lock()
	last := h.conns.queries[len(h.conns.queries)-1]
	h.conns.mu.Unlock()
	if !strings.Contains(last, "filter.pid=101") {
		t.Fatalf("query %q", last)
	}
	savePreview(t, tt, "connections-pid-facet-selected.png")
}
