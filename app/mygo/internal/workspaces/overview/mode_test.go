package overview

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

func TestConfiguredMode(t *testing.T) {
	for _, tc := range []struct {
		doc  *model.Doc
		want string
	}{
		{nil, "Rule"},
		{&model.Doc{Experimental: model.Experimental{ClashAPI: true, DefaultMode: "global"}}, "Global"},
		{&model.Doc{Experimental: model.Experimental{ClashAPI: true, DefaultMode: "Direct"}}, "Direct"},
		{&model.Doc{Experimental: model.Experimental{ClashAPI: false, DefaultMode: "Direct"}}, "Rule"},
		{&model.Doc{Experimental: model.Experimental{ClashAPI: true}}, "Rule"},
	} {
		if got := configuredMode(tc.doc); got != tc.want {
			t.Errorf("configuredMode(%+v) = %q, want %q", tc.doc, got, tc.want)
		}
	}
}

func TestDashboardHelpers(t *testing.T) {
	if n, u := splitUnit("1.5 KB/s"); n != "1.5" || u != "KB/s" {
		t.Errorf("splitUnit = %q %q", n, u)
	}
	if n, u := splitUnit("—"); n != "—" || u != "" {
		t.Errorf("splitUnit(—) = %q %q", n, u)
	}
	for name, want := range map[string]string{"TAGSS": "T", "🌍国外媒体": "国", "netflix": "N", "★": "#"} {
		if got := nodeview.GroupInitial(name); got != want {
			t.Errorf("GroupInitial(%q) = %q, want %q", name, got, want)
		}
	}
	for width, want := range map[float32]int{0: 4, 1000: 4, 600: 2, 300: 1} {
		if got := metricColumns(width); got != want {
			t.Errorf("metricColumns(%v) = %d, want %d", width, got, want)
		}
	}
}
