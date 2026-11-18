package emit_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func mitmWarnings(r emit.Result) []string {
	var out []string
	for _, w := range r.Warnings {
		if strings.HasPrefix(w, "HTTPS 解密") {
			out = append(out, w)
		}
	}
	return out
}

func TestEmitMITMAbsentUnlessEnabled(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	if _, ok := emit.Emit(d, emit.Options{}).Config["mitm"]; ok {
		t.Fatal("mitm emitted without httpTools")
	}
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Hostnames: &model.MITMHostnames{Include: []string{"a.com"}}}}
	if _, ok := emit.Emit(d, emit.Options{}).Config["mitm"]; ok {
		t.Fatal("mitm emitted while disabled")
	}
}

func TestEmitMITMManagedHostnames(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{
		Enabled: true, QUIC: "allow", SaveBody: true,
		Hostnames: &model.MITMHostnames{
			Include: []string{"Example.com", "*.api.example.com", "example.com", "https://x.com"},
			Exclude: []string{"bank.example.com", "*"},
		},
	}}
	r := emit.Emit(d, emit.Options{})
	want := map[string]any{
		"enabled":     true,
		"certificate": map[string]any{"source": "managed"},
		"hostnames": map[string]any{
			"include": []any{"example.com", "*.api.example.com"},
			"exclude": []any{"bank.example.com"},
		},
		"quic":    "allow",
		"capture": captureWant(true),
	}
	if !reflect.DeepEqual(r.Config["mitm"], want) {
		t.Fatalf("mitm = %v", r.Config["mitm"])
	}
	wantWarn := []string{
		"HTTPS 解密包含列表中的「https://x.com」不是有效主机名，已跳过",
		"HTTPS 解密排除列表中的「*」不是有效主机名，已跳过",
	}
	if got := mitmWarnings(r); !reflect.DeepEqual(got, wantWarn) {
		t.Fatalf("warnings = %v", got)
	}
	d.HTTPTools.MITM = &model.MITMSettings{Enabled: true}
	got := emit.Emit(d, emit.Options{}).Config["mitm"]
	if !reflect.DeepEqual(got, map[string]any{"enabled": true, "certificate": map[string]any{"source": "managed"}, "capture": captureWant(false)}) {
		t.Fatalf("bare mitm = %v", got)
	}
}

func TestEmitMITMDefaultsWriteNothingExtra(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	on := true
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{
		Enabled: true, BuiltinExclude: &on, QUIC: "block", PinningSkipMinutes: 30,
		Upstream: &model.MITMUpstream{Fingerprint: "chrome"},
	}}
	got := emit.Emit(d, emit.Options{}).Config["mitm"]
	if !reflect.DeepEqual(got, map[string]any{"enabled": true, "certificate": map[string]any{"source": "managed"}, "capture": captureWant(false)}) {
		t.Fatalf("mitm = %v", got)
	}
}

func TestEmitMITMFullPolicyOptions(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	off := false
	d.Route.Devices = []*model.RouteDevice{{ID: "d1", Name: "TV", Addresses: []string{"192.168.1.20", "10.0.0.0/24", "bad"}}}
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{
		Enabled: true, BuiltinExclude: &off, DisabledBuiltin: []string{"x"},
		Devices:            []string{"d1", "gone"},
		PinningSkipMinutes: 90,
		Upstream: &model.MITMUpstream{
			Fingerprint: "chrome", SkipVerify: []string{"Internal.example.com", "http://x"},
			CustomRoots: []string{" /etc/ca.pem ", "/etc/ca.pem", ""},
		},
	}}
	r := emit.Emit(d, emit.Options{})
	want := map[string]any{
		"enabled":     true,
		"certificate": map[string]any{"source": "managed"},
		"capture":     captureWant(false),
		"hostnames":   map[string]any{"builtin_exclude": false},
		"sources":     []any{"local", "192.168.1.20", "10.0.0.0/24"},
		"upstream": map[string]any{
			"skip_verify":  []any{"internal.example.com"},
			"custom_roots": []any{"/etc/ca.pem"},
		},
		"pinning_skip_duration": "90m",
	}
	if !reflect.DeepEqual(r.Config["mitm"], want) {
		t.Fatalf("mitm = %v", r.Config["mitm"])
	}
	if got := mitmWarnings(r); len(got) != 3 {
		t.Fatalf("warnings = %v", got)
	}
}

func TestEmitMITMDisabledBuiltinAndNoLocal(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.Route.Devices = []*model.RouteDevice{{ID: "d1", Name: "TV", Addresses: []string{"192.168.1.20"}}}
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{
		Enabled: true, DisabledBuiltin: []string{"apple", " apple ", ""}, Devices: []string{"d1"}, NoLocal: true,
	}}
	want := map[string]any{
		"enabled":     true,
		"certificate": map[string]any{"source": "managed"},
		"capture":     captureWant(false),
		"hostnames":   map[string]any{"disabled_builtin": []any{"apple"}},
		"sources":     []any{"192.168.1.20"},
	}
	if got := emit.Emit(d, emit.Options{}).Config["mitm"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("mitm = %v", got)
	}
}

func TestEmitMITMNoLocalWithoutValidDeviceAddress(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.Route.Devices = []*model.RouteDevice{{ID: "d1", Name: "TV", Addresses: []string{"bad"}}}
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Devices: []string{"d1", "gone"}, NoLocal: true}}
	r := emit.Emit(d, emit.Options{})
	if _, ok := r.Config["mitm"]; ok {
		t.Fatalf("mitm must stay off: %v", r.Config["mitm"])
	}
	if got := mitmWarnings(r); len(got) != 3 {
		t.Fatalf("warnings = %v", got)
	}
	d.HTTPTools.MITM.NoLocal = false
	if got := emit.Emit(d, emit.Options{}).Config["mitm"]; got == nil {
		t.Fatal("local scope still decrypts without NoLocal")
	}
}

func TestEmitMITMFingerprint(t *testing.T) {
	for fp, want := range map[string]any{"": nil, "chrome": nil, "none": "none", "safari": "safari"} {
		d := model.FillDefaults(model.EmptyDoc("linux"))
		d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Upstream: &model.MITMUpstream{Fingerprint: fp}}}
		mitm, _ := emit.Emit(d, emit.Options{}).Config["mitm"].(map[string]any)
		upstream, _ := mitm["upstream"].(map[string]any)
		if got := upstream["fingerprint"]; got != want {
			t.Errorf("%q emitted %v, want %v", fp, got, want)
		}
	}
}

func captureWant(body bool) map[string]any {
	return map[string]any{"enabled": true, "body": body, "max_body": "512KiB", "max_memory": "64MiB", "max_records": float64(5000)}
}

func rewriteDoc(rw *model.HTTPRewrites, enabled bool) *model.Doc {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: enabled}, Rewrites: rw}
	return d
}

func rewriteSection(r emit.Result) map[string]any {
	m, _ := r.Config["mitm"].(map[string]any)
	s, _ := m["http_rewrite"].(map[string]any)
	return s
}

func TestEmitRewriteFull(t *testing.T) {
	rw := &model.HTTPRewrites{
		URL: []*model.URLRewrite{
			{ID: "u1", Pattern: `^https?://ads\.example\.com/`, Action: "reject"},
			{ID: "u2", Pattern: `^https://a\.com/(.*)`, Action: "302", Target: " https://b.com/$1 "},
			{ID: "u3", Pattern: `x`, Action: "reject-img", Disabled: true},
		},
		Headers: []*model.HeaderRewrite{{ID: "h1", Pattern: `a\.com`, Scope: "response", Ops: []*model.HeaderEdit{
			{Action: "add", Name: "X-A", Value: "v"},
			{Action: "delete", Name: "X-B"},
			{Action: "replace", Name: "X-C", Value: "w"},
			{Action: "replace-regex", Name: "X-D", Regex: `a(b)`, Value: "$1"},
		}}},
		MapLocal: []*model.MapLocal{
			{ID: "m1", Pattern: `/api`, Status: 200, Headers: []model.HeaderField{{Name: "Content-Type", Value: "application/json"}}, Body: "{}"},
			{ID: "m2", Pattern: `/bin`, Status: 404, Body: "ignored", BodyBase64: "AQI="},
		},
	}
	r := emit.Emit(rewriteDoc(rw, true), emit.Options{})
	want := map[string]any{
		"url": []any{
			map[string]any{"id": "u1", "pattern": `^https?://ads\.example\.com/`, "action": "reject"},
			map[string]any{"id": "u2", "pattern": `^https://a\.com/(.*)`, "action": "302", "target": "https://b.com/$1"},
		},
		"headers": []any{map[string]any{"id": "h1", "pattern": `a\.com`, "scope": "response", "operations": []any{
			map[string]any{"action": "add", "name": "X-A", "value": "v"},
			map[string]any{"action": "delete", "name": "X-B"},
			map[string]any{"action": "replace", "name": "X-C", "value": "w"},
			map[string]any{"action": "replace-regex", "name": "X-D", "regex": "a(b)", "value": "$1"},
		}}},
		"map_local": []any{
			map[string]any{"id": "m1", "pattern": "/api", "status": float64(200), "headers": []any{map[string]any{"name": "Content-Type", "value": "application/json"}}, "body": "{}"},
			map[string]any{"id": "m2", "pattern": "/bin", "status": float64(404), "body_base64": "AQI="},
		},
	}
	if got := rewriteSection(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("http_rewrite = %v", got)
	}
	if got := mitmWarnings(r); len(got) != 0 {
		t.Fatalf("warnings = %v", got)
	}
}

func TestEmitRewriteSkipsInvalidAndDisabled(t *testing.T) {
	rw := &model.HTTPRewrites{
		URL: []*model.URLRewrite{
			{ID: "u1", Name: "坏正则", Pattern: `(`, Action: "reject"},
			{ID: "u2", Pattern: `a`, Action: "bogus"},
			{ID: "u3", Pattern: `a`, Action: "reject-dict"},
		},
		Headers: []*model.HeaderRewrite{
			{ID: "h1", Pattern: `a`, Scope: "request", Ops: []*model.HeaderEdit{{Action: "delete", Name: "Host"}}},
			{ID: "h2", Pattern: `a`, Scope: "request", Disabled: true, Ops: []*model.HeaderEdit{{Action: "delete", Name: "X"}}},
		},
		MapLocal: []*model.MapLocal{{ID: "m1", Pattern: `a`, Status: 99}},
	}
	r := emit.Emit(rewriteDoc(rw, true), emit.Options{})
	got := rewriteSection(r)
	want := map[string]any{"url": []any{map[string]any{"id": "u3", "pattern": "a", "action": "reject-dict"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("http_rewrite = %v", got)
	}
	warn := mitmWarnings(r)
	if len(warn) != 4 || !strings.Contains(warn[0], "坏正则") || !strings.Contains(warn[2], "h1") {
		t.Fatalf("warnings = %v", warn)
	}
}

func TestEmitRewriteBodyTotalCap(t *testing.T) {
	big := strings.Repeat("a", model.MaxMapLocalBody)
	rw := &model.HTTPRewrites{MapLocal: []*model.MapLocal{
		{ID: "m1", Pattern: "a", Status: 200, Body: big},
		{ID: "m2", Pattern: "b", Status: 200, Body: big},
		{ID: "m3", Pattern: "c", Status: 200, Body: "x"},
	}}
	r := emit.Emit(rewriteDoc(rw, true), emit.Options{})
	list, _ := rewriteSection(r)["map_local"].([]any)
	if len(list) != 2 {
		t.Fatalf("map_local = %d rules", len(list))
	}
	if list[0].(map[string]any)["id"] != "m1" || list[1].(map[string]any)["id"] != "m2" {
		t.Fatalf("map_local = %v", list)
	}
	if warn := mitmWarnings(r); len(warn) != 1 || !strings.Contains(warn[0], "m3") {
		t.Fatalf("warnings = %v", warn)
	}
}

func TestEmitRewriteAbsentWhenOffOrEmpty(t *testing.T) {
	rw := &model.HTTPRewrites{URL: []*model.URLRewrite{{ID: "u1", Pattern: "a", Action: "reject"}}}
	if r := emit.Emit(rewriteDoc(rw, false), emit.Options{}); r.Config["mitm"] != nil {
		t.Fatalf("mitm = %v", r.Config["mitm"])
	}
	for name, rw := range map[string]*model.HTTPRewrites{
		"nil":      nil,
		"empty":    {},
		"disabled": {URL: []*model.URLRewrite{{ID: "u1", Pattern: "a", Action: "reject", Disabled: true}}},
		"invalid":  {URL: []*model.URLRewrite{{ID: "u1", Pattern: "(", Action: "reject"}}},
	} {
		r := emit.Emit(rewriteDoc(rw, true), emit.Options{})
		m, _ := r.Config["mitm"].(map[string]any)
		if _, ok := m["http_rewrite"]; ok || m == nil {
			t.Fatalf("%s: mitm = %v", name, r.Config["mitm"])
		}
	}
}

func TestEmitRewriteBodyAndInternalHeader(t *testing.T) {
	rw := &model.HTTPRewrites{
		URL: []*model.URLRewrite{{ID: "u1", Pattern: `^https://a\.com/(.*)`, Action: "header", Target: " https://b.com/$1 "}},
		Body: []*model.BodyRewrite{
			{ID: "b1", Pattern: `a\.com/api`, Scope: "response", ContentTypes: []string{" json "}, Edits: []*model.BodyEdit{
				{Action: "replace-regex", Regex: `"a":(\d+)`, Value: `"a":$1`},
				{Action: "json-set", Path: "$.x", Value: `{"k":1}`},
				{Action: "json-delete", Path: "$..y", Value: "ignored"},
				{Action: "json-append", Path: "$.l", Value: "2"},
			}},
			{ID: "b2", Pattern: `(`, Scope: "request", Edits: []*model.BodyEdit{{Action: "json-delete", Path: "$.a"}}},
			{ID: "b3", Pattern: `a`, Scope: "request", Disabled: true, Edits: []*model.BodyEdit{{Action: "json-delete", Path: "$.a"}}},
			{ID: "b4", Pattern: `a`, Scope: "request", Edits: []*model.BodyEdit{{Action: "json-set", Path: "$.a", Value: "{"}}},
		},
		MaxBodySize: 4 << 20,
	}
	r := emit.Emit(rewriteDoc(rw, true), emit.Options{})
	want := map[string]any{
		"url": []any{map[string]any{"id": "u1", "pattern": `^https://a\.com/(.*)`, "action": "header", "target": "https://b.com/$1"}},
		"body": []any{map[string]any{"id": "b1", "pattern": `a\.com/api`, "scope": "response", "content_types": []any{"json"}, "edits": []any{
			map[string]any{"action": "replace-regex", "regex": `"a":(\d+)`, "value": `"a":$1`},
			map[string]any{"action": "json-set", "path": "$.x", "value": `{"k":1}`},
			map[string]any{"action": "json-delete", "path": "$..y"},
			map[string]any{"action": "json-append", "path": "$.l", "value": "2"},
		}}},
		"max_body_size": "4096KB",
	}
	if got := rewriteSection(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("http_rewrite = %v", got)
	}
	if warn := mitmWarnings(r); len(warn) != 2 || !strings.Contains(warn[0], "b2") || !strings.Contains(warn[1], "b4") {
		t.Fatalf("warnings = %v", warn)
	}
	// Default size and invalid size.
	rw = &model.HTTPRewrites{Body: rw.Body[:1], MaxBodySize: model.DefaultBodySize}
	if _, ok := rewriteSection(emit.Emit(rewriteDoc(rw, true), emit.Options{}))["max_body_size"]; ok {
		t.Fatal("default size written")
	}
	rw.MaxBodySize = 100
	r = emit.Emit(rewriteDoc(rw, true), emit.Options{})
	if _, ok := rewriteSection(r)["max_body_size"]; ok || len(mitmWarnings(r)) != 1 {
		t.Fatalf("invalid size: %v %v", rewriteSection(r), mitmWarnings(r))
	}
}

func TestEmitMITMCapturePersist(t *testing.T) {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, SaveBody: true}}
	section := func() map[string]any {
		m, _ := emit.Emit(d, emit.Options{}).Config["mitm"].(map[string]any)
		c, _ := m["capture"].(map[string]any)
		return c
	}
	// Absent until the user touches it: no persist key, so older documents
	// keep their output and no directory is ever mentioned.
	if _, ok := section()["persist"]; ok {
		t.Fatal("persist written without settings")
	}
	d.HTTPTools.MITM.Persist = &model.MITMPersist{Enabled: true}
	want := map[string]any{"enabled": true, "max_bytes": "256MiB", "max_records": float64(20000), "retention": "168h"}
	if got := section()["persist"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %v", got)
	}
	d.HTTPTools.MITM.Persist = &model.MITMPersist{
		Enabled: true, Body: true, MaxMB: 999999, MaxRecords: 5, RetentionDays: 2,
		RedactHeaders: []string{" X-Mine ", "x-mine", "bad name", "", "X-Other"},
	}
	r := emit.Emit(d, emit.Options{})
	m, _ := r.Config["mitm"].(map[string]any)
	persist, _ := m["capture"].(map[string]any)["persist"].(map[string]any)
	want = map[string]any{
		"enabled": true, "body": true, "max_bytes": "65536MiB", "max_records": float64(5), "retention": "48h",
		"redact_headers": []any{"X-Mine", "X-Other"},
	}
	if !reflect.DeepEqual(persist, want) {
		t.Fatalf("persist = %v", persist)
	}
	if got := mitmWarningsAll(r); len(got) != 0 {
		// The invalid header name is reported once.
		if len(got) != 1 || !strings.Contains(got[0], "bad name") {
			t.Fatalf("warnings = %v", got)
		}
	} else {
		t.Fatal("invalid header name not reported")
	}
	if _, ok := persist["path"]; ok {
		t.Fatal("the stored directory must not be part of the document output")
	}
	// Switched off keeps the section so that the core stops writing.
	d.HTTPTools.MITM.Persist = &model.MITMPersist{MaxMB: 64}
	if got := section()["persist"].(map[string]any); got["enabled"] != false || got["max_bytes"] != "64MiB" {
		t.Fatalf("off = %v", got)
	}
}

func mitmWarningsAll(r emit.Result) []string {
	var out []string
	for _, w := range r.Warnings {
		if strings.HasPrefix(w, "抓取存储") {
			out = append(out, w)
		}
	}
	return out
}
