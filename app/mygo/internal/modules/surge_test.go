package modules

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func parse(t *testing.T, text string, opts ...Options) *Result {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	res, err := ParseSurge(text, o)
	if err != nil {
		t.Fatalf("ParseSurge: %v", err)
	}
	if _, err := res.Resolve(standIn(res)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return res
}

func reasons(res *Result) string {
	var b strings.Builder
	for _, u := range res.Unsupported {
		b.WriteString(u.Section + ":" + itoa(u.Line) + " " + u.Reason + "\n")
	}
	return b.String()
}

func TestMetadataAndArguments(t *testing.T) {
	res := parse(t, `#!name = Demo
#!desc=Hello
#!version=1.2
#!arguments=A:one,B:"x,y",C:http://h/p
#!arguments-desc=A: first\nB：second
[MITM]
hostname = {{{A}}}.example.com
`)
	if res.Name != "Demo" || res.Description != "Hello" || res.Version != "1.2" {
		t.Fatalf("metadata: %+v", res)
	}
	want := []model.ModuleArgument{{Name: "A", Default: "one", Description: "first"}, {Name: "B", Default: "x,y", Description: "second"}, {Name: "C", Default: "http://h/p"}}
	if len(res.Arguments) != 3 {
		t.Fatalf("arguments: %+v", res.Arguments)
	}
	for i, a := range want {
		if res.Arguments[i] != a {
			t.Errorf("argument %d = %+v, want %+v", i, res.Arguments[i], a)
		}
	}
	if got := res.Contents.Hostnames.Include; len(got) != 1 || got[0] != "one.example.com" {
		t.Errorf("include = %v", got)
	}
}

func TestArgumentInjection(t *testing.T) {
	text := "#!name=x\n#!arguments=H:a.com\n[MITM]\nhostname = {{{H}}}\n"
	for _, value := range []string{"a.com\n[Script]\nx = type=generic,script-path=https://e/x.js", "a.com\r\n[Rule]", "a\x00b"} {
		if _, err := ParseSurge(text, Options{Arguments: map[string]string{"H": value}}); err == nil {
			t.Errorf("value %q accepted", value)
		}
	}
	// A value that looks like a section or a placeholder stays text: it is
	// substituted once, after the line is classified.
	res := parse(t, text, Options{Arguments: map[string]string{"H": "[Script]"}})
	if len(res.Contents.Scripts) != 0 || res.Contents.Hostnames != nil {
		t.Errorf("injected: %+v", res.Contents)
	}
	res = parse(t, "#!name=x\n#!arguments=A:{{{B}}},B:b.com\n[MITM]\nhostname = {{{A}}}\n")
	if res.Contents.Hostnames != nil {
		t.Errorf("nested placeholder expanded: %+v", res.Contents.Hostnames)
	}
	if _, err := ParseSurge(text, Options{Arguments: map[string]string{"H": strings.Repeat("a", model.MaxModuleArgumentValue+1)}}); err == nil {
		t.Error("oversized value accepted")
	}
}

func TestMITMSection(t *testing.T) {
	res := parse(t, `[MITM]
hostname = %APPEND% a.com, *.b.com, -c.com, d.com:443, a.com, bad host, *, e.com:8443
skip-server-cert-verify = true
h2 = true
tcp-connection = true
hostname-disabled = x.com
`)
	h := res.Contents.Hostnames
	if strings.Join(h.Include, ",") != "a.com,*.b.com,d.com" || strings.Join(h.Exclude, ",") != "c.com" {
		t.Errorf("hostnames: %+v", h)
	}
	r := reasons(res)
	for _, want := range []string{"MITM:2 主机名「bad host」", "MITM:2 主机名「*」", "MITM:2 主机名「e.com:8443」", "MITM:3 模块不能修改解密的全局设置", "MITM:4 ", "MITM:5 ", "MITM:6 已停用"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
}

func TestRuleSection(t *testing.T) {
	res := parse(t, `[Rule]
DOMAIN,Ads.Example.com,REJECT
DOMAIN-SUFFIX,.example.org,REJECT-TINYGIF
DOMAIN-KEYWORD,track,REJECT-DROP
IP-CIDR,1.2.3.4/32,REJECT,no-resolve
IP-CIDR,10.0.0.7,DIRECT
IP-CIDR6,2001:db8::1/64,REJECT
IP-CIDR,2001:db8::/32,REJECT
DOMAIN,a.com,Proxy
RULE-SET,https://x/list,REJECT
GEOIP,CN,DIRECT
FINAL,DIRECT
DOMAIN,bad domain,REJECT
IP-CIDR,999.1.1.1/8,REJECT
DOMAIN,x.com,REJECT,extended-matching
`)
	var got []string
	for _, r := range res.Contents.Rules {
		got = append(got, r.Type+" "+r.Value+" "+r.Action)
	}
	want := []string{
		"domain ads.example.com reject", "domain-suffix example.org reject", "domain-keyword track reject",
		"ip-cidr 1.2.3.4/32 reject", "ip-cidr 10.0.0.7/32 direct", "ip-cidr6 2001:db8::/64 reject", "ip-cidr6 2001:db8::/32 reject", "domain x.com reject",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rules:\n%v\nwant\n%v", got, want)
	}
	if !res.Contents.Rules[3].NoResolve {
		t.Error("no-resolve lost")
	}
	r := reasons(res)
	for _, want := range []string{"策略「Proxy」不支持", "规则类型「RULE-SET」", "规则类型「GEOIP」", "仅支持域名与 IP 段规则", "域名无效", "IP 段无效", "选项「extended-matching」已忽略"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
}

func TestURLRewriteSection(t *testing.T) {
	res := parse(t, `[URL Rewrite]
^https?://a\.com/(.*) https://b.com/$1 302
^https?://a\.com/x - 307
^https?://a\.com/y https://c.com/y header
^https?://a\.com/r1 - reject
^https?://a\.com/r2 - reject-200
^https?://a\.com/r3 - reject-img
^https?://a\.com/r4 - reject-dict
^https?://a\.com/r5 - reject-array
^https?://a\.com/r6 - reject-tinygif
^https?://a\.com/r7 - reject-no-drop
^https?://a\.com/r8 - reject-drop
"^https?://a\.com/q u" - reject
^https?://a\.com/(?!z) - reject
^https?://a\.com/n https://b.com/ 301
reject-only
`)
	rw := res.Contents.Rewrites
	var actions []string
	for _, r := range rw.URL {
		actions = append(actions, r.Action)
	}
	// The 307 line has no target and is refused.
	got := strings.Join(actions, ",")
	if got != "302,header,reject,reject-200,reject-img,reject-dict,reject-array,reject-tinygif,reject,reject,reject" {
		t.Errorf("actions = %s", got)
	}
	r := reasons(res)
	for _, want := range []string{"URL Rewrite:3 跳转地址须为 http 或 https 地址", "近似映射为 reject", "匹配模式无效", "类型「301」不支持", "格式应为"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
	if rw.URL[0].Target != "https://b.com/$1" {
		t.Errorf("target = %q", rw.URL[0].Target)
	}
}

func TestHeaderRewriteSection(t *testing.T) {
	res := parse(t, `[Header Rewrite]
http-request ^https://a\.com header-add X-A 1
http-response ^https://a\.com header-del Set-Cookie
http-request ^https://a\.com header-replace User-Agent "My Agent 1.0"
http-response ^https://a\.com header-replace-regex Location ^http:// https://
http-request ^https://a\.com header-add Host a.com
http-request ^https://a\.com header-add X-B
http-request ^https://a\.com header-frob X-B 1
http-sideways ^https://a\.com header-del X-B
`)
	h := res.Contents.Rewrites.Headers
	if len(h) != 4 {
		t.Fatalf("headers = %d\n%s", len(h), reasons(res))
	}
	if op := h[2].Ops[0]; op.Action != "replace" || op.Value != "My Agent 1.0" {
		t.Errorf("quoted value: %+v", op)
	}
	if op := h[3].Ops[0]; op.Action != "replace-regex" || op.Regex != "^http://" || op.Value != "https://" || h[3].Scope != "response" {
		t.Errorf("regex op: %+v", op)
	}
	r := reasons(res)
	for _, want := range []string{"该头由代理管理", "缺少头的值", "操作「header-frob」不支持", "作用范围「http-sideways」不支持"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
}

func TestBodyRewriteSection(t *testing.T) {
	res := parse(t, `[Body Rewrite]
http-response-replace-regex ^https://a\.com/ "ad":\s*true "ad":false (\d+)px $1em
http-request-replace-regex ^https://a\.com/ foo bar
http-response-replace-regex ^https://a\.com/ onlyone
http-response-jq ^https://a\.com/ 'del(.data.ads, .data.list[].promo)'
http-response-jq ^https://a\.com/ '.data.enabled = false | .items[0].name = "x" | .n = 3'
http-response-jq ^https://a\.com/ '.a |= 1'
http-response-jq ^https://a\.com/ 'map(select(.x))'
http-response-jq ^https://a\.com/ '.a = foo'
http-response-jq ^https://a\.com/ 'del(.a[] | select(.b))'
http-response-jq ^https://a\.com/ '.["k y"] = null'
http-response-jq ^https://a\.com/ '.a == 1'
http-request-binary ^https://a\.com/ x
`)
	b := res.Contents.Rewrites.Body
	if len(b) != 5 {
		t.Fatalf("body rules = %d\n%s", len(b), reasons(res))
	}
	if len(b[0].Edits) != 2 || b[0].Edits[1].Regex != `(\d+)px` || b[0].Edits[1].Value != "$1em" || b[0].Scope != "response" {
		t.Errorf("replace edits: %+v", b[0].Edits[1])
	}
	if b[1].Scope != "request" {
		t.Errorf("scope: %+v", b[1])
	}
	type e struct{ action, path, value string }
	var got []e
	for _, r := range b[2:4] {
		for _, ed := range r.Edits {
			got = append(got, e{ed.Action, ed.Path, ed.Value})
		}
	}
	want := []e{{"json-delete", "$.data.ads", ""}, {"json-delete", "$.data.list[*].promo", ""},
		{"json-set", "$.data.enabled", "false"}, {"json-set", "$.items[0].name", `"x"`}, {"json-set", "$.n", "3"}}
	if len(got) != len(want) {
		t.Fatalf("jq edits = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("edit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if e := b[4].Edits[0]; e.Path != "$['k y']" || e.Value != "null" {
		t.Errorf("bracket key: %+v", e)
	}
	r := reasons(res)
	for _, want := range []string{"正则与替换内容应成对出现", "|=", "jq 表达式「map(select(.x))」不支持", "jq 表达式「.a = foo」不支持", "jq 路径", "jq 表达式「.a == 1」不支持", "类型「http-request-binary」不支持"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
}

func TestMapLocalSection(t *testing.T) {
	res := parse(t, `[Map Local]
^https://a\.com/1 data="{"code":0,"msg":"a b"}" status-code=200 header="Content-Type:application/json|X-Y: z"
^https://a\.com/2 data-type=base64 data="aGVsbG8=" status-code=201
^https://a\.com/3 data-type=tiny-gif status-code=200
"^https://a\.com/4" data="https://example.com/x.json" header="Content-Type:application/json"
^https://a\.com/5 data-type=file data="https://example.com/y.bin" status-code=404
^https://a\.com/6 data-type=file data="/local/file.json"
^https://a\.com/7 data-type=text data="plain text" extra=1
^https://a\.com/8 data="x" status-code=99
^https://a\.com/9 data-type=base64 data="***"
^https://a\.com/10 data-type=zip data="x"
^https://a\.com/11 data="x" header="NoColon"
^https://a\.com/12 data="{\"esc\":1}"
`)
	m := res.Contents.Rewrites.MapLocal
	if len(m) != 7 {
		t.Fatalf("map local = %d\n%s", len(m), reasons(res))
	}
	if m[0].Body != `{"code":0,"msg":"a b"}` || len(m[0].Headers) != 2 || m[0].Headers[1] != (model.HeaderField{Name: "X-Y", Value: "z"}) {
		t.Errorf("rule 1: %+v", m[0])
	}
	if m[1].BodyBase64 != "aGVsbG8=" || m[1].Status != 201 {
		t.Errorf("rule 2: %+v", m[1])
	}
	if m[2].BodyBase64 == "" || m[2].Headers[0].Value != "image/gif" {
		t.Errorf("tiny gif: %+v", m[2])
	}
	if m[3].Body != "{}" || m[4].Body != "{}" || m[4].Status != 404 {
		t.Errorf("fetched: %+v %+v", m[3], m[4])
	}
	if m[6].Body != `{"esc":1}` {
		t.Errorf("escape: %q", m[6].Body)
	}
	if len(res.Fetches) != 2 || res.Fetches[0].Kind != FetchData {
		t.Errorf("fetches: %+v", res.Fetches)
	}
	r := reasons(res)
	for _, want := range []string{"Map Local:7 ", "参数「extra」已忽略", "不支持本地文件", "状态码须在", "base64 内容无效", "数据类型「zip」不支持", "缺少冒号"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
}

func TestScriptSection(t *testing.T) {
	res := parse(t, `[Script]
a = type=http-response, pattern=^https://a\.com/(x,y), requires-body=true, max-size=300, binary-body-mode=true, argument="k=v,w=1", timeout=60, script-path=https://e.com/a.js, script-update-interval=3600, debug=true
b = type=http-request, pattern=^https://b\.com/, script-path=https://e.com/b.js, binary-body-mode=true
c = type=cron, cronexp="0 */2 * * *", script-path=https://e.com/c.js, timeout=45, wake-system=1
d = type=generic, script-path=https://e.com/d.js, enable=false
e = type=event, event-name=network-changed, script-path=https://e.com/e.js
f = type=rule, script-path=https://e.com/f.js, timeout=5, pattern=^https://x
g = type=http-response, script-path=https://e.com/g.js
h = type=http-response, pattern=^https://h, script-path=/local/h.js
i = type=cron, cronexp="61 * * * *", script-path=https://e.com/i.js
j = type=http-response, pattern=^https://j, script-path=https://e.com/missing.js
k = type=http-response, pattern=^https://k, script-path=rel/k.js
`, Options{BaseURL: "https://base.example/dir/mod.sgmodule"})
	var tags []string
	for _, s := range res.Contents.Scripts {
		tags = append(tags, s.Tag)
	}
	if strings.Join(tags, ",") != "a,b,c,d,f,j,k" {
		t.Fatalf("tags = %v\n%s", tags, reasons(res))
	}
	a := res.Contents.Scripts[0]
	if a.Pattern != `^https://a\.com/(x,y)` || !a.RequiresBody || !a.BinaryBody || a.MaxSize != 1024 || a.Argument != "k=v,w=1" || a.Timeout != model.MaxScriptTimeoutHTTP {
		t.Errorf("script a: %+v", a)
	}
	if b := res.Contents.Scripts[1]; b.BinaryBody {
		t.Errorf("binary without body kept: %+v", b)
	}
	if c := res.Contents.Scripts[2]; c.Cron != "0 */2 * * *" || c.Timeout != 45 || c.Pattern != "" {
		t.Errorf("cron: %+v", c)
	}
	if d := res.Contents.Scripts[3]; !d.Disabled || d.Type != "generic" {
		t.Errorf("generic: %+v", d)
	}
	if f := res.Contents.Scripts[4]; f.Type != model.ScriptRule || f.Timeout != model.MaxScriptTimeoutDecision || f.Pattern != "" {
		t.Errorf("rule: %+v", f)
	}
	found := false
	for _, f := range res.Fetches {
		found = found || f.URL == "https://base.example/dir/rel/k.js"
	}
	if !found {
		t.Errorf("relative path not resolved: %+v", res.Fetches)
	}
	r := reasons(res)
	for _, want := range []string{"超时 60 秒降为上限 30 秒", "参数「wake-system」已忽略", "未开启 requires-body", "脚本类型「event」不支持", "超时 5 秒降为上限 1000 毫秒", "缺少 pattern", "不支持本地文件"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
	if strings.Contains(r, "script-update-interval") || strings.Contains(r, "debug") {
		t.Errorf("cosmetic keys reported:\n%s", r)
	}
	// An unsupplied download moves the script to the unsupported list.
	res2, _ := ParseSurge(`[Script]
j = type=http-response, pattern=^https://j, script-path=https://e.com/missing.js
ok = type=generic, script-path=https://e.com/ok.js
`, Options{})
	missing, err := res2.Resolve(map[string][]byte{"https://e.com/ok.js": []byte("1;")})
	if err != nil || len(missing) != 1 || len(res2.Contents.Scripts) != 1 || res2.Contents.Scripts[0].Tag != "ok" {
		t.Errorf("resolve: %v %v %+v", err, missing, res2.Contents.Scripts)
	}
	if !strings.Contains(reasons(res2), "脚本未能下载") {
		t.Errorf("reasons: %s", reasons(res2))
	}
}

func TestScriptTagUniqueAndSanitized(t *testing.T) {
	res := parse(t, `[Script]
我的 脚本 = type=generic, script-path=https://e.com/1.js
我的 脚本 = type=generic, script-path=https://e.com/2.js
a/b c = type=generic, script-path=https://e.com/3.js
`+"x"+strings.Repeat("y", 100)+` = type=generic, script-path=https://e.com/4.js
`)
	var tags []string
	for _, s := range res.Contents.Scripts {
		tags = append(tags, s.Tag)
		if len(s.Tag) > model.MaxModuleScriptTag {
			t.Errorf("tag %q too long", s.Tag)
		}
	}
	if strings.Join(tags[:3], ",") != "script,script-2,a_b_c" {
		t.Errorf("tags = %v", tags)
	}
}

func TestOtherSectionsAreListed(t *testing.T) {
	res := parse(t, `#!name=x
[General]
skip-proxy = 1.1.1.1
[Host]
a.com = 1.2.3.4
[Rule]
DOMAIN,a.com,REJECT
`)
	if len(res.Contents.Rules) != 1 {
		t.Errorf("rules: %+v", res.Contents.Rules)
	}
	r := reasons(res)
	if !strings.Contains(r, "General:2 整节未支持") || !strings.Contains(r, "Host:4 整节未支持") {
		t.Errorf("reasons:\n%s", r)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := ParseSurge("hello world", Options{}); err != ErrNotModule {
		t.Errorf("plain text: %v", err)
	}
	if _, err := ParseSurge(strings.Repeat("x", model.MaxModuleRaw+1), Options{}); err != ErrTooLarge {
		t.Errorf("large: %v", err)
	}
	if _, err := ParseSurge("[Rule]\n\xff\xfe", Options{}); err != ErrNotModule {
		t.Errorf("invalid utf8: %v", err)
	}
	var b strings.Builder
	for i := 0; i < maxSections+2; i++ {
		b.WriteString("[S" + itoa(i) + "]\n")
	}
	if _, err := ParseSurge(b.String(), Options{}); err != ErrTooMany {
		t.Errorf("sections: %v", err)
	}
	b.Reset()
	b.WriteString("[Rule]\n")
	for i := 0; i < maxEntries+1; i++ {
		b.WriteString("a\n")
	}
	if _, err := ParseSurge(b.String(), Options{}); err != ErrTooMany {
		t.Errorf("entries: %v", err)
	}
	// An over-long line is listed, not parsed.
	res, err := ParseSurge("[Rule]\nDOMAIN,"+strings.Repeat("a", maxLine)+".com,REJECT\n", Options{})
	if err == nil && len(res.Unsupported) != 1 {
		t.Errorf("long line: %+v %v", res, err)
	}
	// Many hosts are capped.
	b.Reset()
	b.WriteString("[MITM]\nhostname = ")
	for i := 0; i < model.MaxModuleHosts+10; i++ {
		b.WriteString("h" + itoa(i) + ".example.com,")
	}
	res = parse(t, b.String())
	if n := len(res.Contents.Hostnames.Include); n != model.MaxModuleHosts {
		t.Errorf("hosts = %d", n)
	}
}

func TestCommentsAndBOM(t *testing.T) {
	res := parse(t, "\xef\xbb\xbf#!name=B\r\n# c\r\n; c\r\n// c\r\n[Rule]\r\n# DOMAIN,off.com,REJECT\r\nDOMAIN,on.com,REJECT\r\n")
	if res.Name != "B" || len(res.Contents.Rules) != 1 || len(res.Unsupported) != 0 {
		t.Errorf("%+v", res)
	}
}

func TestOversizedBodies(t *testing.T) {
	res, _ := ParseSurge(`[Map Local]
^https://a\.com/ data="https://e.com/big.bin"
[Script]
s = type=generic, script-path=https://e.com/big.js
`, Options{})
	missing, err := res.Resolve(map[string][]byte{
		"https://e.com/big.bin": make([]byte, model.MaxMapLocalBody+1),
		"https://e.com/big.js":  []byte(strings.Repeat("a", model.MaxScriptSource+1)),
	})
	if err != nil || len(missing) != 0 {
		t.Fatalf("%v %v", err, missing)
	}
	if res.Contents.Rewrites != nil || len(res.Contents.Scripts) != 0 {
		t.Errorf("oversized content kept: %+v", res.Contents)
	}
	if r := reasons(res); !strings.Contains(r, "正文超过 2 MiB") || !strings.Contains(r, "脚本超过 1 MiB") {
		t.Errorf("reasons:\n%s", r)
	}
}

func TestKikoRoundTrip(t *testing.T) {
	data := `{"x":1}`
	res := parse(t, `#!name=P
#!desc=d
[URL Rewrite]
^https://a\.com/ - reject
[Map Local]
^https://a\.com/m data="https://e.com/d.json"
[Script]
s = type=generic, script-path=https://e.com/s.js
[Rule]
DOMAIN,a.com,DIRECT
[MITM]
hostname = a.com, -b.com
`)
	_ = data
	m := model.NewHTTPModule()
	m.Name, m.Description, m.Version = res.Name, res.Description, "3"
	m.Enabled = true
	m.Approved.Hostnames = []string{"a.com"}
	m.Contents = res.Contents
	m.Pending = &model.ModulePending{}
	out := ExportKiko(m)
	if strings.Contains(string(out), "approved") || strings.Contains(string(out), "enabled") || strings.Contains(string(out), "pending") {
		t.Errorf("run state exported:\n%s", out)
	}
	back, err := ParseKiko(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != "P" || back.Version != "3" || summarizeContents(back) != summarizeContents(res) {
		t.Errorf("round trip differs:\n%s\n%s", summarizeContents(back), summarizeContents(res))
	}
	again := ExportKiko(&model.HTTPModule{Name: back.Name, Description: back.Description, Version: back.Version, Contents: back.Contents, Unsupported: back.Unsupported})
	if string(again) != string(out) {
		t.Error("export is not deterministic")
	}
}

func summarizeContents(r *Result) string { return summarize(&Result{Contents: r.Contents}) }

func TestKikoStrict(t *testing.T) {
	good := `{"format":"kikomodule","formatVersion":1,"name":"N","contents":{"rules":[{"type":"domain","value":"a.com","action":"reject"}]}}`
	if _, err := ParseKiko([]byte(good)); err != nil {
		t.Fatalf("good: %v", err)
	}
	legacy := strings.Replace(good, `"kikomodule"`, `"poppymodule"`, 1)
	if _, err := ParseKiko([]byte(legacy)); err != nil {
		t.Fatalf("legacy poppymodule: %v", err)
	}
	bad := map[string]string{
		"format":     strings.Replace(good, `"kikomodule"`, `"other"`, 1),
		"version":    strings.Replace(good, `"formatVersion":1`, `"formatVersion":2`, 1),
		"unknown":    strings.Replace(good, `"name":"N"`, `"name":"N","extra":1`, 1),
		"noname":     strings.Replace(good, `"name":"N"`, `"name":""`, 1),
		"badrule":    strings.Replace(good, `"a.com"`, `"A B"`, 1),
		"trailing":   good + `{}`,
		"notjson":    `kiko`,
		"emptyDoc":   ``,
		"badpattern": strings.Replace(good, `"rules":[`, `"rewrites":{"url":[{"id":"1","pattern":"(","action":"reject"}]},"rules":[`, 1),
		"setting":    strings.Replace(good, `"rules":[`, `"rewrites":{"maxBodySize":4096,"url":[{"id":"1","pattern":"x","action":"reject"}]},"rules":[`, 1),
	}
	for name, text := range bad {
		if _, err := ParseKiko([]byte(text)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ParseKiko(make([]byte, MaxKikoSize+1)); err == nil {
		t.Error("oversized accepted")
	}
}

func FuzzParseSurge(f *testing.F) {
	seeds := []string{
		"#!name=x\n#!arguments=A:1\n[MITM]\nhostname = %APPEND% {{{A}}}.com\n[Script]\ns = type=cron, cronexp=\"* * * * *\", script-path=https://e/x.js\n",
		"[Map Local]\n^https://a data=\"{\"a\":1}\" status-code=200 header=\"A:b\"\n",
		"[Body Rewrite]\nhttp-response-jq ^https://a 'del(.a[0].b) | .c = 1'\n",
		"[URL Rewrite]\n\"^a b\" - reject\n^x https://y/$1 302\n[Header Rewrite]\nhttp-request ^x header-replace-regex A b c\n[Rule]\nIP-CIDR,1.2.3.4/32,REJECT,no-resolve\n",
		"[Script]\na = type=http-response,pattern=\"(\",argument=\"{\"a\":\"b\"}\",script-path=https://e/x.js\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	for _, name := range []string{"youtube_enhance", "keepads", "bilibili_helper"} {
		if data, err := readFixture(name); err == nil {
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, text string) {
		res, err := ParseSurge(text, Options{})
		if err != nil {
			return
		}
		fetched := map[string][]byte{}
		for i, fe := range res.Fetches {
			if i%2 == 0 {
				fetched[fe.URL] = []byte("$done({});")
			}
		}
		if _, err := res.Resolve(fetched); err != nil {
			return
		}
		if issue := res.Contents.Issue(); issue != "" {
			t.Fatalf("invalid content after Resolve: %s\ninput: %q", issue, text)
		}
	})
}

// Surge's own spelling of a regex replacement has no "-replace-regex" suffix:
// "http-response <url> <regex> <replacement> [<regex> <replacement> ...]".
func TestBodyRewriteSurgeSpelling(t *testing.T) {
	res := parse(t, `[Body Rewrite]
http-request ^http(s)?://example\.com abc 123
http-response ^http(s)?://example\.com documents Surge
http-response ^https://a\.com/ "list":\[.+\] "list":[] (\d+)px $1em
http-response ^https://a\.com/ onlyone
http-response ^https://a\.com/
`)
	b := res.Contents.Rewrites.Body
	if len(b) != 3 {
		t.Fatalf("body rules = %d\n%s", len(b), reasons(res))
	}
	if b[0].Scope != "request" || b[0].Pattern != `^http(s)?://example\.com` || len(b[0].Edits) != 1 || b[0].Edits[0].Regex != "abc" || b[0].Edits[0].Value != "123" {
		t.Errorf("request rule: %+v %+v", b[0], b[0].Edits[0])
	}
	if b[1].Scope != "response" || b[1].Edits[0].Regex != "documents" || b[1].Edits[0].Value != "Surge" {
		t.Errorf("response rule: %+v", b[1])
	}
	if len(b[2].Edits) != 2 || b[2].Edits[0].Regex != `"list":\[.+\]` || b[2].Edits[0].Value != `"list":[]` || b[2].Edits[1].Value != "$1em" {
		t.Errorf("pairs: %+v", b[2].Edits)
	}
	if n := len(res.Unsupported); n != 2 {
		t.Errorf("unsupported = %d\n%s", n, reasons(res))
	}
}

func TestRuleScopeIsLimited(t *testing.T) {
	res := parse(t, `[Rule]
IP-CIDR,0.0.0.0/0,DIRECT
IP-CIDR,10.0.0.0/7,REJECT
IP-CIDR,10.0.0.0/8,REJECT
IP-CIDR6,::/0,DIRECT
IP-CIDR6,2001:db8::/32,DIRECT
DOMAIN-SUFFIX,com,REJECT
DOMAIN-SUFFIX,example.com,REJECT
DOMAIN-KEYWORD,a,REJECT
DOMAIN-KEYWORD,ad,DIRECT
DOMAIN-KEYWORD,ads,REJECT
`)
	var got []string
	for _, r := range res.Contents.Rules {
		got = append(got, r.Type+","+r.Value)
	}
	want := "ip-cidr,10.0.0.0/8 ip-cidr6,2001:db8::/32 domain-suffix,example.com domain-keyword,ads"
	if strings.Join(got, " ") != want {
		t.Errorf("rules = %v, want %s", got, want)
	}
	broad := 0
	for _, u := range res.Unsupported {
		if strings.Contains(u.Reason, "范围过大") {
			broad++
		}
	}
	if broad != 6 {
		t.Errorf("范围过大 reasons = %d\n%s", broad, reasons(res))
	}
}

func TestKikoRefusesBroadRules(t *testing.T) {
	file := func(rule string) []byte {
		return []byte(`{"format":"kikomodule","formatVersion":1,"name":"N","contents":{"rules":[` + rule + `]}}`)
	}
	for _, rule := range []string{
		`{"type":"domain-suffix","value":"com","action":"direct"}`,
		`{"type":"ip-cidr","value":"0.0.0.0/0","action":"direct"}`,
		`{"type":"ip-cidr","value":"10.0.0.0/7","action":"reject"}`,
		`{"type":"ip-cidr6","value":"::/0","action":"direct"}`,
		`{"type":"domain-keyword","value":"ab","action":"reject"}`,
	} {
		_, err := ParseKiko(file(rule))
		if err == nil || !strings.Contains(err.Error(), "范围过大") {
			t.Errorf("%s: err = %v", rule, err)
		}
	}
	if _, err := ParseKiko(file(`{"type":"domain-suffix","value":"example.com","action":"direct"}`)); err != nil {
		t.Errorf("narrow rule: %v", err)
	}
}

func TestScriptRuleAndDNSScripts(t *testing.T) {
	res, perr := ParseSurge(`[Rule]
SCRIPT,pick,DIRECT
SCRIPT,block,REJECT
SCRIPT,missing,DIRECT
SCRIPT,web,DIRECT
SCRIPT,gone,REJECT
DOMAIN,a.example.com,DIRECT
[Script]
pick = type=rule, script-path=https://e.com/pick.js
block = type=rule, script-path=https://e.com/block.js, timeout=1
web = type=http-response, pattern=^https://w, script-path=https://e.com/web.js
gone = type=rule, script-path=https://e.com/gone.js
dnsjs = type=dns, script-path=https://e.com/dns.js
`, Options{})
	if perr != nil {
		t.Fatal(perr)
	}
	missing, err := res.Resolve(map[string][]byte{
		"https://e.com/pick.js": []byte("1;"), "https://e.com/block.js": []byte("1;"),
		"https://e.com/web.js": []byte("1;"), "https://e.com/dns.js": []byte("1;"),
	})
	if err != nil || len(missing) != 1 {
		t.Fatalf("resolve: %v %v", err, missing)
	}
	var rules []string
	for _, r := range res.Contents.Rules {
		rules = append(rules, r.Type+","+r.Value+","+r.Action)
	}
	if got := strings.Join(rules, ";"); got != "script,pick,direct;script,block,reject;domain,a.example.com,direct" {
		t.Errorf("rules = %s\n%s", got, reasons(res))
	}
	r := reasons(res)
	for _, want := range []string{"脚本「missing」不存在", "脚本「web」不是 rule 类型", "脚本「gone」不可用"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in\n%s", want, r)
		}
	}
	var dns *model.HTTPScript
	for _, s := range res.Contents.Scripts {
		if s.Type == model.ScriptDNS {
			dns = s
		}
	}
	if dns == nil || dns.Tag != "dnsjs" {
		t.Errorf("dns script: %+v", res.Contents.Scripts)
	}
}
