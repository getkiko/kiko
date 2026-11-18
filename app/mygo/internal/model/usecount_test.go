package model

import "testing"

func TestOutboundUseCount(t *testing.T) {
	off := false
	doc := &Doc{Route: Route{
		Rules: []*RouteRule{
			{ID: "r1", Enabled: true, Action: ActionRoute, Outbound: Some("direct")},
			{ID: "r2", Enabled: true, Action: ActionRoute, Outbound: Some("direct"), DeviceID: "phone"},
			{ID: "r3", Enabled: false, Action: ActionRoute, Outbound: Some("direct")},
			{ID: "r4", Enabled: true, Action: ActionReject, Outbound: Some("direct")},
			{ID: "r5", Enabled: true, Action: ActionRoute, Outbound: Some("block")},
		},
		Devices: []*RouteDevice{{ID: "phone", Final: "direct"}, {ID: "tv", Final: "proxy"}},
		Final:   Some("proxy"),
	}}
	cases := []struct {
		id   string
		want int
	}{
		// r1, r2 and the phone's default.
		{"direct", 3},
		{"block", 1},
		// The tv's default, route.final and the local scope inheriting it.
		{"proxy", 3},
		{"gone", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := OutboundUseCount(doc, tc.id); got != tc.want {
			t.Errorf("OutboundUseCount(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}

	doc.Route.LocalFinal = Some("direct")
	if got := OutboundUseCount(doc, "direct"); got != 4 {
		t.Errorf("with the local default: direct %d, want 4", got)
	}
	if got := OutboundUseCount(doc, "proxy"); got != 2 {
		t.Errorf("with the local default: proxy %d, want 2", got)
	}
	doc.Route.LocalEnabled = &off
	// r1 and the local default go with the local scope: r2 and the
	// phone's default remain.
	if got := OutboundUseCount(doc, "direct"); got != 2 {
		t.Errorf("without the local scope: direct %d, want 2", got)
	}
	if got := OutboundUseCount(doc, "block"); got != 0 {
		t.Errorf("without the local scope: block %d, want 0", got)
	}
	if OutboundUseCount(nil, "direct") != 0 {
		t.Error("nil doc")
	}
}
