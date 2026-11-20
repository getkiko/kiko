package main

import (
	"net/url"
	"slices"
	"testing"
)

// On the All tab the open connections come first and the ended ones
// (closed or refused) after, each part in the chosen order; the other
// tabs keep the order alone. Groups whose connections all ended go last.
func TestAllTabListsEndedConnectionsLast(t *testing.T) {
	at := func(ms int64) func(*connRow) { return func(r *connRow) { r.Conn.CreatedAt = ms } }
	closed := func(r *connRow) { r.ClosedAt = 9000 }
	refused := func(r *connRow) { r.Conn.RejectCount = 1 }
	rows := []*connRow{
		row("a", editorApp, at(1000)),
		row("b", editorApp, at(3000), closed),
		row("c", editorApp, at(2000)),
		row("d", editorApp, at(4000), refused),
	}
	ids := func(state string, extra url.Values) []string {
		values := url.Values{"state": {state}}
		for k, v := range extra {
			values[k] = v
		}
		q, err := parseConnectionQuery(values)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range queryConnectionPage(rows, nil, nil, q).Rows {
			out = append(out, r.ID)
		}
		return out
	}
	// Newest first by default: the open c and a, then the ended d and b.
	if got := ids("all", nil); !slices.Equal(got, []string{"c", "a", "d", "b"}) {
		t.Fatalf("all, newest first: %v", got)
	}
	if got := ids("all", url.Values{"order": {"asc"}}); !slices.Equal(got, []string{"a", "c", "b", "d"}) {
		t.Fatalf("all, oldest first: %v", got)
	}
	if got := ids("closed", nil); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("closed: %v", got)
	}

	// A group of ended connections only goes after the groups still open.
	other := proc(30, 1, "worker", func(p *connProcess) { p.Path = "/usr/bin/worker" })
	grouped := []*connRow{row("x", other, at(5000), closed), row("y", editorApp, at(1000))}
	tree := buildOrderedConnectionTree(grouped, captureConnectionProcesses(grouped, procs(editorApp, other), nil), groupApp, colCreated, true, true)
	if len(tree) != 2 || tree[0].ended() || !tree[1].ended() {
		t.Fatalf("groups %v %v", tree[0].Label, tree[1].Label)
	}
}
