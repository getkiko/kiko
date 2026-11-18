package nodeview

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestSections(t *testing.T) {
	table := model.DefaultRegions()
	names := []string{"🇯🇵 东京 01", "UK London", "🇭🇰 香港 01", "node-x", "🇺🇸 洛杉矶", "🇭🇰 香港 02"}
	got := Sections(table, model.NewRegionMatcher(table), names)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if want := []string{"", "hk", "us", "jp", "uk"}; !slices.Equal(ids, want) {
		t.Fatalf("section ids = %q, want %q", ids, want)
	}
	if got[0].Label != "未识别" || !slices.Equal(got[0].Names, []string{"node-x"}) {
		t.Errorf("unrecognized = %+v", got[0])
	}
	if !slices.Equal(got[1].Names, []string{"🇭🇰 香港 01", "🇭🇰 香港 02"}) || got[1].Flag != "🇭🇰" {
		t.Errorf("hk = %+v", got[1])
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"🇭🇰 🇸🇬 香港 01": "香港 01", " 香港 🇭🇰": "香港 🇭🇰", "🇭🇰": ""} {
		if got := WithoutFlags(in); got != want {
			t.Errorf("WithoutFlags(%q) = %q, want %q", in, got, want)
		}
	}
	if got := BesideFlag("🇭🇰", "🇭🇰 香港 01"); got != "香港 01" {
		t.Errorf("BesideFlag = %q", got)
	}
	if got := BesideFlag("", "🇭🇰 香港 01"); got != "🇭🇰 香港 01" {
		t.Errorf("BesideFlag without a flag = %q", got)
	}
	for in, want := range map[string]string{"hysteria2": "HY2", "direct": "直连", "foo": "FOO"} {
		if got := ProtocolTag(in); got != want {
			t.Errorf("ProtocolTag(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"TAGSS": "T", "🌍国外媒体": "国", "★": "#"} {
		if got := GroupInitial(in); got != want {
			t.Errorf("GroupInitial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMembers(t *testing.T) {
	doc := &model.Doc{
		Subscriptions: []*model.Subscription{{ID: "s1", Tag: "机场", NamePrefix: "[A] "}},
		Outbounds: []*model.Outbound{
			{Entity: model.Entity{ID: "o1", Tag: "[A] 香港 01"}, Type: "vless", SubID: "s1"},
			{Entity: model.Entity{ID: "o2", Tag: "节点选择"}, Type: "selector"},
			{Entity: model.Entity{ID: "o3", Tag: "节点选择"}, Type: "direct"},
		},
	}
	got := Members(doc)
	if m := got["[A] 香港 01"]; m != (Member{ID: "o1", Name: "香港 01", Protocol: "VLESS", Source: "机场"}) {
		t.Errorf("node = %+v", m)
	}
	if m := got["节点选择"]; m.ID != "o2" || !m.Group || m.Protocol != "手动" {
		t.Errorf("group = %+v", m)
	}
}

func TestLatencyStateOf(t *testing.T) {
	ms := 80.0
	cases := []struct {
		name    string
		r       api.LatencyResult
		testing bool
		want    LatencyState
	}{
		{"untested", api.LatencyResult{}, false, LatencyUntested},
		{"testing wins over a result", api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms}, true, LatencyTesting},
		{"measured", api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms}, false, LatencyMeasured},
		{"timeout", api.LatencyResult{Error: "Timeout"}, false, LatencyTimeout},
		{"deadline", api.LatencyResult{Error: "context deadline exceeded"}, false, LatencyTimeout},
		{"failed", api.LatencyResult{Error: "connection refused"}, false, LatencyFailed},
	}
	for _, c := range cases {
		if got := LatencyStateOf(c.r, c.testing); got != c.want {
			t.Errorf("%s: state %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLatencyHintNamesTheMetric(t *testing.T) {
	ms := 80.0
	proxy := api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms}
	direct := api.LatencyResult{SuccessSamples: 3, MedianConnectMs: &ms, Mode: api.LatencyModeDirect}
	if LatencyHint(api.LatencyResult{}) != "" || LatencyHint(proxy) == "" || LatencyHint(proxy) == LatencyHint(direct) {
		t.Errorf("hints: untested %q, proxy %q, direct %q", LatencyHint(api.LatencyResult{}), LatencyHint(proxy), LatencyHint(direct))
	}
	unified := api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms, Mode: api.LatencyModeUnified}
	hints := map[string]string{"统一延迟": LatencyHint(unified), "HTTPS 请求耗时": LatencyHint(proxy), "直连探测": LatencyHint(direct)}
	for want, got := range hints {
		if !strings.HasPrefix(got, want) {
			t.Errorf("hint %q does not start with %q", got, want)
		}
	}
	if got := LatencyHint(api.LatencyResult{Error: "boom"}); got != "boom" {
		t.Errorf("error hint %q", got)
	}
}
