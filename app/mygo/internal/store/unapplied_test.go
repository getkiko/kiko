package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/env"
)

func decodeConfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestClassifyUnapplied(t *testing.T) {
	applied := decodeConfig(t, `{"log":{"level":"info"},"outbounds":[{"tag":"a"}],
		"route":{"rules":[{"outbound":"a"}],"final":"a","auto_detect_interface":true},
		"dns":{"servers":[{"tag":"d"}],"rules":[]}}`)
	for name, tc := range map[string]struct {
		current string
		parts   []string
		pages   []string
	}{
		"same": {`{"log":{"level":"info"},"outbounds":[{"tag":"a"}],"route":{"rules":[{"outbound":"a"}],"final":"a","auto_detect_interface":true},"dns":{"servers":[{"tag":"d"}],"rules":[]}}`, nil, nil},
		"rules and dns servers": {`{"log":{"level":"info"},"outbounds":[{"tag":"a"}],"route":{"rules":[],"final":"a","auto_detect_interface":true},"dns":{"servers":[{"tag":"e"}],"rules":[]}}`,
			[]string{"路由规则", "DNS 服务器"}, []string{env.PageRoute, env.PageSettings}},
		"settings and a new inbound": {`{"log":{"level":"debug"},"inbounds":[{"tag":"in"}],"outbounds":[{"tag":"a"}],"route":{"rules":[{"outbound":"a"}],"final":"a"},"dns":{"servers":[{"tag":"d"}],"rules":[]}}`,
			[]string{"入站", "设置"}, []string{env.PageSubscriptions, env.PageSettings}},
		"scripts": {`{"log":{"level":"info"},"outbounds":[{"tag":"a"}],"route":{"rules":[{"outbound":"a"}],"final":"a","auto_detect_interface":true},"dns":{"servers":[{"tag":"d"}],"rules":[]},"scripts":[{"tag":"mod_a1-s"}]}`,
			[]string{"改写"}, []string{env.PageRewrites}},
		"dns rules and final": {`{"log":{"level":"info"},"outbounds":[{"tag":"a"}],"route":{"rules":[{"outbound":"a"}],"final":"a","auto_detect_interface":true},"dns":{"servers":[{"tag":"d"}],"rules":[{"server":"d"}],"final":"d"}}`,
			[]string{"DNS 分流"}, []string{env.PageRoute}},
	} {
		t.Run(name, func(t *testing.T) {
			u := classifyUnapplied(decodeConfig(t, tc.current), applied)
			if !reflect.DeepEqual(u.Parts, tc.parts) {
				t.Errorf("parts %q, want %q", u.Parts, tc.parts)
			}
			want := map[string]bool{}
			for _, page := range tc.pages {
				want[page] = true
			}
			if !reflect.DeepEqual(u.Pages, want) {
				t.Errorf("pages %v, want %v", u.Pages, want)
			}
		})
	}
}

// The host's addresses change as the core brings up its TUN interface;
// the local source set they fill is no change of the document.
func TestClassifyUnappliedIgnoresTheLocalSourceSet(t *testing.T) {
	set := func(addresses, extra string) map[string]any {
		return decodeConfig(t, `{"route":{"rules":[{"rule_set":["kiko-local-source"]}],"rule_set":[
			{"type":"inline","tag":"kiko-local-source","rules":[{"source_ip_cidr":[`+addresses+`]}]}`+extra+`]}}`)
	}
	applied := set(`"192.168.1.2/32"`, "")
	if u := classifyUnapplied(set(`"192.168.1.2/32","172.19.0.1/32"`, ""), applied); u.Count() != 0 {
		t.Errorf("local addresses counted as a change: %q", u.Parts)
	}
	added := set(`"192.168.1.2/32"`, `,{"type":"inline","tag":"mine","rules":[]}`)
	if u := classifyUnapplied(added, applied); !reflect.DeepEqual(u.Parts, []string{"规则集"}) {
		t.Errorf("parts %q, want 规则集", u.Parts)
	}
}

// A configuration applied before the rename to Kiko names its generated
// local source set and default DNS server sbcfg-…; after upgrading, the same
// document emits kiko-… and is no change.
func TestClassifyUnappliedNormalizesLegacyTags(t *testing.T) {
	config := func(prefix string) map[string]any {
		return decodeConfig(t, `{"route":{"rules":[{"rule_set":["`+prefix+`-local-source-2"],"outbound":"a"}],"rule_set":[
			{"type":"inline","tag":"`+prefix+`-local-source-2","rules":[{"source_ip_cidr":["192.168.1.2/32"]}]}]},
			"dns":{"servers":[{"tag":"`+prefix+`-default-dns","type":"local"}],"rules":[{"server":"`+prefix+`-default-dns"}],"final":"`+prefix+`-default-dns"}}`)
	}
	if u := classifyUnapplied(config("kiko"), config("sbcfg")); u.Count() != 0 {
		t.Errorf("legacy tags counted as a change: %q", u.Parts)
	}
	// Only generated tags are renamed: a user's own sbcfg- tag still differs.
	own := decodeConfig(t, `{"outbounds":[{"tag":"sbcfg-proxy"}]}`)
	if u := classifyUnapplied(decodeConfig(t, `{"outbounds":[{"tag":"kiko-proxy"}]}`), own); u.Count() == 0 {
		t.Error("a user's tag was renamed")
	}
}
