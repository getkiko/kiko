package mitmpolicy

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func docWith(settings *model.MITMSettings) *model.Doc {
	return &model.Doc{HTTPTools: &model.HTTPTools{MITM: settings}}
}

func TestMatchHost(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"Example.com", "EXAMPLE.com.", true},
		{"example.com", "www.example.com", false},
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "badexample.com", false},
		{"*", "example.com", false},
		{"https://example.com", "example.com", false},
		{"example.com", "", false},
	}
	for _, c := range cases {
		if got := MatchHost(c.pattern, c.host); got != c.want {
			t.Errorf("MatchHost(%q, %q) = %v", c.pattern, c.host, got)
		}
	}
}

func TestEvaluate(t *testing.T) {
	settings := &model.MITMSettings{Enabled: true, Hostnames: &model.MITMHostnames{
		Include: []string{"*.example.com", "api.other.com"},
		Exclude: []string{"bank.example.com"},
	}}
	cases := []struct {
		name    string
		doc     *model.Doc
		host    string
		trusted bool
		want    Outcome
	}{
		{"no http tools", &model.Doc{}, "www.example.com", true, Outcome{Decision: Skipped, Reason: ReasonFeatureOff}},
		{"off", docWith(&model.MITMSettings{}), "www.example.com", true, Outcome{Decision: Skipped, Reason: ReasonFeatureOff}},
		{"untrusted", docWith(settings), "www.example.com", false, Outcome{Decision: Skipped, Reason: ReasonNotTrusted}},
		{"excluded beats include", docWith(settings), "bank.example.com", true, Outcome{Decision: Skipped, Reason: ReasonExcluded}},
		{"not included", docWith(settings), "www.other.com", true, Outcome{Decision: Skipped, Reason: ReasonNotIncluded}},
		{"decrypted", docWith(settings), "WWW.example.com", true, Outcome{Decision: Decrypted, Pending: true}},
		{"exact include", docWith(settings), "api.other.com", true, Outcome{Decision: Decrypted, Pending: true}},
		{"empty include", docWith(&model.MITMSettings{Enabled: true}), "a.com", true, Outcome{Decision: Skipped, Reason: ReasonNotIncluded}},
		{"no host", docWith(settings), "", true, Outcome{Decision: None}},
	}
	scoped := &model.MITMSettings{Enabled: true, Devices: []string{"d1"}, NoLocal: true, Hostnames: settings.Hostnames}
	for _, c := range cases {
		if got := Evaluate(c.doc, c.host, c.trusted, Source{}); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	sourced := []struct {
		name    string
		doc     *model.Doc
		host    string
		trusted bool
		src     Source
		want    Outcome
	}{
		{"no local", docWith(scoped), "www.example.com", true, Source{Device: model.LocalDeviceID}, Outcome{Decision: Skipped, Reason: ReasonScope}},
		{"listed device", docWith(scoped), "www.example.com", true, Source{Device: "d1"}, Outcome{Decision: Decrypted, Pending: true}},
		{"unlisted device", docWith(scoped), "www.example.com", true, Source{Device: "d2"}, Outcome{Decision: Skipped, Reason: ReasonScope}},
		{"other devices", docWith(settings), "www.example.com", true, Source{Device: model.OtherDeviceID}, Outcome{Decision: Skipped, Reason: ReasonScope}},
		{"port 443", docWith(settings), "www.example.com", true, Source{Port: 443}, Outcome{Decision: Decrypted, Pending: true}},
		{"port 8443", docWith(settings), "www.example.com", true, Source{Port: 8443}, Outcome{Decision: Skipped, Reason: ReasonNotIncluded}},
	}
	for _, c := range sourced {
		if got := Evaluate(c.doc, c.host, c.trusted, c.src); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMergeDefersToCore(t *testing.T) {
	pending := Outcome{Decision: Decrypted, Pending: true}
	if got := Merge(pending, "", false); got != pending {
		t.Fatalf("no core value: %+v", got)
	}
	if got := Merge(pending, "decrypted", true); got.Decision != Decrypted || got.Pending {
		t.Fatalf("decrypt: %+v", got)
	}
	got := Merge(pending, "skipped:builtin", true)
	if got.Decision != Skipped || got.Reason != ReasonBuiltin || got.Label() != "内置排除" {
		t.Fatalf("builtin: %+v", got)
	}
	if got := Merge(pending, "skipped:something new", true); got.Reason != ReasonCoreOther || got.Detail != "something new" {
		t.Fatalf("unknown reason: %+v", got)
	}
	if got := Merge(pending, "blocked-quic", true); got.Decision != BlockedQUIC {
		t.Fatalf("quic: %+v", got)
	}
	for _, reason := range []string{ReasonFeatureOff, ReasonNotTrusted} {
		skipped := Outcome{Decision: Skipped, Reason: reason}
		if got := Merge(skipped, "decrypted", true); got != skipped {
			t.Fatalf("local %s must stand: %+v", reason, got)
		}
	}
	excluded := Outcome{Decision: Skipped, Reason: ReasonExcluded}
	if got := Merge(excluded, "decrypted", true); got.Decision != Decrypted {
		t.Fatalf("core wins over the document: %+v", got)
	}
	if got := Merge(excluded, "decrypted", false); got != excluded {
		t.Fatalf("local before the answer: %+v", got)
	}
}

func TestCoreValuesThroughMerge(t *testing.T) {
	pending := Outcome{Decision: Decrypted, Pending: true}
	cases := []struct {
		core     string
		decision string
		reason   string
		label    string
	}{
		{"decrypted", Decrypted, "", "解密"},
		{"skipped:pinned", Skipped, ReasonPinning, "证书固定"},
		{"skipped:builtin", Skipped, ReasonBuiltin, "内置排除"},
		{"skipped:authority_pending", Skipped, ReasonAuthorityPending, "证书未就绪"},
		{"blocked-quic", BlockedQUIC, "", "阻止 QUIC"},
		{"decrypt", Decrypted, "", "解密"},
		{"skip:pinned", Skipped, ReasonPinning, "证书固定"},
		{"block_quic", BlockedQUIC, "", "阻止 QUIC"},
	}
	for _, c := range cases {
		got := Merge(pending, c.core, true)
		if got.Decision != c.decision || got.Reason != c.reason || got.Label() != c.label || got.Pending {
			t.Errorf("%s: %+v", c.core, got)
		}
	}
	for core, label := range map[string]string{"skipped:scope": "设备未启用", "skipped:not_included": "不在包含列表", "skipped:excluded": "已排除",
		"skipped:override_skip": "规则跳过", "skipped:not_tls": "非 TLS"} {
		if got := Merge(pending, core, true); got.Decision != Skipped || got.Label() != label {
			t.Errorf("%s: %+v", core, got)
		}
		if got := ReasonText(strings.TrimPrefix(core, "skipped:")); got != label {
			t.Errorf("reason text %s: %q", core, got)
		}
	}
	if got := Merge(pending, "", true); got.Decision != None || got.Pending {
		t.Errorf("empty answer: %+v", got)
	}
	if got := Merge(pending, "", false); got != pending {
		t.Errorf("no answer: %+v", got)
	}
	if got := ReasonText("Pinned"); got != "证书固定" {
		t.Errorf("reason text %q", got)
	}
	if got := ReasonText("odd"); got != "odd" {
		t.Errorf("unknown reason text %q", got)
	}
}

func TestEveryCoreReasonHasLabel(t *testing.T) {
	reasons := []string{"scope", "not_included", "excluded", "builtin", "pinned", "authority_pending", "override_skip", "outbound_handler", "not_tls"}
	for _, reason := range reasons {
		if text := ReasonText(reason); text == reason || text == "" {
			t.Errorf("ReasonText(%q) = %q, want a label", reason, text)
		}
		outcome := FromCore("skipped:" + reason)
		if outcome.Decision != Skipped {
			t.Errorf("FromCore(%q).Decision = %q", reason, outcome.Decision)
		}
		if outcome.Reason == ReasonCoreOther {
			t.Errorf("FromCore(%q) maps to the generic reason", reason)
		}
		if label := outcome.Label(); label == "跳过" || label == "" {
			t.Errorf("FromCore(%q).Label() = %q, want a specific label", reason, label)
		}
	}
	if got := FromCore("skipped:outbound_handler").Label(); got != "出站直接处理" {
		t.Errorf("outbound_handler label = %q", got)
	}
}

// A profile scope is decided by its device.
func TestEvaluateProfileScopeUsesDevice(t *testing.T) {
	doc := docWith(&model.MITMSettings{Enabled: true, Devices: []string{"phone"}, NoLocal: true,
		Hostnames: &model.MITMHostnames{Include: []string{"*.example.com"}}})
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Final: "direct"}}
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	next.HTTPTools = doc.HTTPTools
	for _, p := range model.DeviceProfiles(next.Route.Devices[0]) {
		got := Evaluate(next, "www.example.com", true, Source{Device: p.Scope})
		if got.Decision != Decrypted {
			t.Errorf("scope %q: %+v", p.Scope, got)
		}
	}
}
