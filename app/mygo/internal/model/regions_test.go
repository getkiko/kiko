package model_test

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestRegionOf(t *testing.T) {
	cases := map[string]string{
		"🇭🇰 香港 01":     "hk",
		"HK-IEPL":      "hk",
		"Hong Kong 02": "hk",
		"台北 01":        "tw",
		"JP Tokyo":     "jp",
		"新加坡 BGP":      "sg",
		"US-LA":        "us",
		"美国 洛杉矶":       "us",
		"Seoul 01":     "kr",
		"UK London":    "uk",
		"Thinkpad":     "",
		"CHKDSK":       "",
		"Australia 澳洲": "",
	}
	for name, want := range cases {
		r, ok := model.RegionOf(nil, name)
		if (want == "") == ok || r.ID != want {
			t.Errorf("%s: got %q %v, want %q", name, r.ID, ok, want)
		}
	}
}

func TestCompileNodePatternRewritesLookaround(t *testing.T) {
	re, err := model.CompileNodePattern(`(?i)(?<![a-z])hk(?![a-z])`)
	if err != nil || !re.MatchString("HK 01") || re.MatchString("chkdsk") {
		t.Fatalf("%v %v", re, err)
	}
	if _, err := model.CompileNodePattern("("); err == nil {
		t.Fatal("invalid pattern compiled")
	}
}

func TestGroupMembersFilter(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	node := func(id, tag string) *model.Outbound {
		return &model.Outbound{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "outbound", Type: "socks"}
	}
	doc.Outbounds = append(doc.Outbounds, node("a", "香港 01"), node("b", "日本 01"), node("c", "香港 剩余流量：10G"),
		node("d", "香港 IEPL"))
	group := model.MakeOutbound("urltest", "香港")
	group.Members = []string{"b"}
	group.Filter = &model.GroupFilter{Regions: []string{"hk"}}
	doc.Outbounds = append(doc.Outbounds, group)
	if got := model.GroupMembers(doc, group); !slices.Equal(got, []string{"b", "a", "d"}) {
		t.Fatalf("members %v", got)
	}
	group.Filter.Exclude = "IEPL"
	group.Filter.KeepInfo = true
	if got := model.GroupMembers(doc, group); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatalf("members %v", got)
	}
	group.Filter = &model.GroupFilter{Pattern: "("}
	if got := model.GroupMembers(doc, group); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("invalid pattern members %v", got)
	}
	plain := model.MakeOutbound("selector", "plain")
	plain.Members = []string{"a"}
	if got := model.GroupMembers(doc, plain); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("plain members %v", got)
	}
}

func TestAddRegionGroups(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.Outbounds = append(doc.Outbounds,
		&model.Outbound{Entity: model.Entity{ID: "a", Tag: "香港 01", Enabled: true}, Kind: "outbound", Type: "socks"},
		&model.Outbound{Entity: model.Entity{ID: "b", Tag: "JP 01", Enabled: true}, Kind: "outbound", Type: "socks"},
		&model.Outbound{Entity: model.Entity{ID: "c", Tag: "其他", Enabled: true}, Kind: "outbound", Type: "socks"})
	ids := model.AddRegionGroups(doc)
	if len(ids) != 3 {
		t.Fatalf("ids %v", ids)
	}
	last := doc.Outbounds[len(doc.Outbounds)-1]
	if last.Type != "selector" || last.Tag != "节点选择" || !slices.Equal(last.Members, ids[:2]) {
		t.Fatalf("selector %+v", last)
	}
	hk := doc.Outbounds[len(doc.Outbounds)-3]
	if hk.Tag != "🇭🇰 香港" || hk.Type != "urltest" || !slices.Equal(model.GroupMembers(doc, hk), []string{"a"}) {
		t.Fatalf("hk %+v", hk)
	}
}

func TestRegionOfPrefersTheFlag(t *testing.T) {
	m := model.NewRegionMatcher(model.DefaultRegions())
	for name, want := range map[string]string{
		"🇭🇰 Hong Kong 01": "hk",
		"🇯🇵 HK 中转 日本":     "jp", // the flag wins over the words
		"🇬🇧 London":       "uk", // the table's region with that flag
		"🇩🇪 Frankfurt":    "de", // a flag outside the table
		"美国 洛杉矶":          "us",
		"🇨🇳→🇭🇰 IPLC 01":   "hk", // a relay's exit
		"🇨🇳 中转 香港":        "hk", // 🇨🇳 alone: the patterns first
		"🇨🇳 北京":           "cn",
	} {
		r, ok := m.RegionOf(name)
		if !ok || r.ID != want {
			t.Errorf("RegionOf(%q) = %q, %v; want %q", name, r.ID, ok, want)
		}
	}
	if r, _ := m.RegionOf("🇩🇪 Frankfurt"); r.Label != "德国" || r.Flag != "🇩🇪" {
		t.Errorf("flag region %+v", r)
	}
	if _, ok := m.RegionOf("Auto"); ok {
		t.Error("Auto has a region")
	}
}

// A region whose pattern names a flag takes it without a Flag of its own.
func TestRegionOfFlagByPattern(t *testing.T) {
	m := model.NewRegionMatcher(&model.RegionSettings{Regions: []model.Region{{ID: "region-x", Label: "德国", Pattern: "🇩🇪|德国"}}})
	if r, ok := m.RegionOf("🇩🇪 Frankfurt"); !ok || r.ID != "region-x" {
		t.Errorf("got %q %v", r.ID, ok)
	}
}
