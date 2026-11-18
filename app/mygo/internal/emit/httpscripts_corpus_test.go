package emit_test

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// TestDumpScriptCorpus writes the emitted script items of many generated
// scripts, at and around every limit, to $SCRIPT_CORPUS. The core-side test
// in app (TestScriptModelNeverExceedsCore) compiles them with the real core.
func TestDumpScriptCorpus(t *testing.T) {
	out := os.Getenv("SCRIPT_CORPUS")
	if out == "" {
		t.Skip("SCRIPT_CORPUS not set")
	}
	rng := rand.New(rand.NewSource(7))
	types := []string{"http-request", "http-response", "generic", "http-request", "http-response", "cron", "rule", "dns", "", "x"}
	tags := []string{"a", "a", "a", "A.b_c-1", strings.Repeat("a", 64), strings.Repeat("a", 65), "", "a b", "a/b", "../x", "脚本", "a\n", "-", "."}
	patterns := []string{"^https://a\\.com/", "^https://a\\.com/", "", " ", "(", "[", "a{1,1001}", strings.Repeat("a", 4096), strings.Repeat("a", 4097), "(?i)x", "\\p{Foo}", "(?P<n>a)\\1", "a**", "\\z", "\\Z", "(?<n>a)", "x*+"}
	sources := []string{"$done({});", "$done({});", "$done({});", "", " \n\t", "var = ;", strings.Repeat("(", 2000) + strings.Repeat(")", 2000), strings.Repeat("(", 2001), strings.Repeat("[", 1500) + strings.Repeat("{", 501), "//" + strings.Repeat("x", 1<<20-2), strings.Repeat("a", 1<<20+1), "a\x00b", "a\xffb", "\u00e9\u4e2d", "\ufeff$done()"}
	sources = append(sources,
		strings.Repeat("x.y = z.w\n", 2000), strings.Repeat("x.y = z.w;\n", 2000), strings.Repeat("x.y = z.w,\n", 2000), strings.Repeat("a+", 5000)+"1", strings.Repeat("a+", 4990)+"1",
		"var r = /[(]{/g; $done({});", "var r = /\\/(((/; $done({});", "x = a / b / c; $done({});", "return /[/]/.test(s)", "// ((((((\n$done({});", "/* [[[[ */ $done({});",
		"var t = `a${b + `c${d}`}e`; $done({});", "var t = `((((`; $done({});", "var s = '((('; $done({});", "var s = \"/*\"; $done({});", "var s = 'abc", "var t = `abc", "/* open", "var r = /abc",
		strings.Repeat("`${", 700)+strings.Repeat("}`", 700), strings.Repeat("[", 2000)+strings.Repeat("]", 2000), strings.Repeat("(", 1000)+"/(/"+strings.Repeat(")", 1000),
		strings.Repeat("x=1;", 20000), strings.Repeat("if(a)b=1;else ", 3000)+"c=1;", "typeof typeof typeof a", strings.Repeat("typeof ", 5001)+"a")
	dense := strings.Repeat("a[b(c)]{d:e}", 3000)
	sources = append(sources,
		"if (a) /re/.test(b); $done({});", "if (a) {} /re/g.test(b); $done({});", "x = {}\n/re/g.test(b)", "a = b\n/re/g.test(c)", "a++ /b/ c; $done({});", "a-- / b / c", "x = (a) / b / c", "f(a) /b/ c",
		"function f() {} /re/.test(s); $done({});", "var f = function () {} / 2 / 3;", "class A {} /re/.test(s)", "var A = class {} / 2 / 3", "var o = {a: 1} / 2 / 3;", "x = y => {} / 2",
		"for (var i of /a/g.exec(s)) {}", "await /x/.test(s)", "yield /x/.test(s)", "typeof /x/", "return /x/", "case /x/:", "throw /x/", "a = b ? /x/ : /y/",
		"var s = '((((((((((((((((((((((((((((((((('; $done({});", "var s = \"[[[[{{{{\"; var r = /[[[[/;", "var r = /[/]((((/; $done({});", "var t = `${`${`${a}`}`}`;", "var t = `${{a: `${b}`}.a}`;",
		"var x = () => ({a: 1}); var y = async () => { await z; }; async function g() { await /a/.test(b); }",
		"label: { a = 1 } /re/.test(s)", "switch (a) { case 1: /re/.test(b); }", "do /re/.test(a); while (b)", "try {} finally {} /re/.test(a)", "else /x/",
		dense, strings.Repeat("(", 130)+"x"+strings.Repeat(")", 130), strings.Repeat("a=(", 130)+"1"+strings.Repeat(")", 130), strings.Repeat("{x:", 130)+"1"+strings.Repeat("}", 130),
		strings.Repeat("1+", 100000)+"1", strings.Repeat("x/", 20000)+"1", strings.Repeat("a /b/ c;", 3000), strings.Repeat("}/", 5000), strings.Repeat("{}/a/g;", 4000),
		strings.Repeat("(", 60000)+strings.Repeat(")", 60000), strings.Repeat("[]", 60000), strings.Repeat("a=1;", 130000),
		"function f(a, b) { return a / b / 2; } $done({});", "var re = /\\//; var d = 4 / 2 / 1;", "}", ")", "(", "[", "{", "a = (", "x ${ }",
	)
	sizes := []int{0, 0, 1, 512, 1024, 1500, 2048, 1 << 20, 32 << 20, 32<<20 + 1024, 33 << 20, -1024}
	timeouts := []int{0, 0, 1, 5, 29, 30, 31, 59, 60, 61, -1}
	arguments := []string{"", "", "k=v", strings.Repeat("a", 16<<10), strings.Repeat("a", 16<<10+1), "a\x00b", " pad ", "\xff"}
	// Verdicts of the source scan alone: "scan" is true when the model's
	// scanner refuses the source.
	type verdict struct {
		Source string `json:"source"`
		Scan   bool   `json:"scan"`
		OK     bool   `json:"ok"`
	}
	var verdicts []verdict
	for _, source := range sources {
		issue := (&model.HTTPScript{ID: "v", Type: model.ScriptGeneric, Source: source}).Issue()
		verdicts = append(verdicts, verdict{Source: source, Scan: strings.HasPrefix(issue, "脚本："), OK: issue == ""})
	}
	if data, err := json.Marshal(verdicts); err != nil || os.WriteFile(out+".sources", data, 0o600) != nil {
		t.Fatal("write verdicts", err)
	}
	var items []any
	for i := 0; i < 60000; i++ {
		s := &model.HTTPScript{
			ID: "script_1", Tag: tags[rng.Intn(len(tags))], Type: types[rng.Intn(len(types))],
			Pattern: patterns[rng.Intn(len(patterns))], Source: sources[rng.Intn(len(sources))],
			RequiresBody: rng.Intn(2) == 0, BinaryBody: rng.Intn(3) == 0,
			MaxSize: sizes[rng.Intn(len(sizes))], Timeout: timeouts[rng.Intn(len(timeouts))], Argument: arguments[rng.Intn(len(arguments))],
		}
		if s.Issue() != "" {
			continue
		}
		doc := model.FillDefaults(model.EmptyDoc("linux"))
		doc.HTTPTools = &model.HTTPTools{Scripts: []*model.HTTPScript{s}}
		got, _ := emit.Emit(doc, emit.Options{}).Config["scripts"].([]any)
		if len(got) != 1 {
			t.Fatalf("model accepted but emit dropped: %+v", s)
		}
		items = append(items, got[0])
	}
	data, _ := json.Marshal(items)
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d accepted scripts", len(items))
}

// TestDumpCronCorpus writes cron expressions with the model's verdict to
// $SCRIPT_CORPUS.cron; TestCronModelMatchesCore in app compiles a cron
// script for each with the real core and compares the verdicts.
func TestDumpCronCorpus(t *testing.T) {
	out := os.Getenv("SCRIPT_CORPUS")
	if out == "" {
		t.Skip("SCRIPT_CORPUS not set")
	}
	exprs := []string{
		"* * * * *", "0 0 * * *", "*/5 * * * *", "0-59/15 * * * *", "0,15,30,45 * * * *", "0 0 1 1 *", "59 23 31 12 7", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *",
		"* * * 0 *", "* * * 13 *", "* * * * 8", "* * * * 0", "* * * * 7", "0 0 * * 0-7", "0 0 * * 7-0", "5-1 * * * *", "*/0 * * * *", "*/ * * * *", "1/5 * * * *", "1-5/2 * * * *",
		"*/2/3 * * * *", "1-2-3 * * * *", "-1 * * * *", "+1 * * * *", "1- * * * *", "-5 * * * *", ",1 * * * *", "1, * * * *", "1,,2 * * * *", "0 0 * jan *", "0 0 * JAN *", "0 0 * Jan-Mar *",
		"0 0 * jan,mar,dec *", "0 0 * * mon-fri", "0 0 * * MON-FRI", "0 0 * * sun", "0 0 * * sat-sun", "0 0 * * sun-sat", "0 0 * * mon/2", "0 0 * * mon-fri/2", "0 0 * january *", "0 0 * * monday",
		"0 jan * * *", "mon 0 * * *", "0 0 jan * *", "0 0 * * jan", "@hourly", "@daily", "@midnight", "@weekly", "@monthly", "@yearly", "@annually", "@HOURLY", "@Daily", "@reboot", "@every 5m", "@hourly *", "@",
		"", " ", "* * * *", "* * * * * *", "0 * * * * *", "? * * * *", "* * ? * *", "* * L * *", "* * * * 5L", "* * 15W * *", "* * * * 1#2", "  * * * * *  ", "*\t*\t*\t*\t*", "*  *  *  *  *",
		"\n* * * * *", "* * * * *\n", "*\n*\n*\n*\n*", "* * * * *\x00", "00 00 01 01 00", "007 * * * *", "0x1 * * * *", "1e1 * * * *", "١ * * * *", "* * * * *,", "**  * * * *", "*-5 * * * *", "*/5-10 * * * *",
		"0-5,10-15/2,30 * * * *", "0 0-23/2 * * *", "0 */7 */3 */5 */2", "0 0 */31 * *", "0 0 */32 * *", "*/60 * * * *", "*/59 * * * *", "*/99999999999 * * * *", "99999999999 * * * *", strings.Repeat("0,", 60) + "0 * * * *",
		"*/60 * * * *", "*/61 * * * *", "* */24 * * *", "* */25 * * *", "* * */31 * *", "* * */32 * *", "* * * */12 *", "* * * */13 *", "* * * * */7", "* * * * */8", "* * * * */9",
		"0-59/60 * * * *", "0-59/61 * * * *", "1-5/5 * * * *", "1-5/6 * * * *", "* * * * mon-fri/8", "* * * * mon-fri/9", "* * * * mon-fri/9999", "*/0060 * * * *", "0005 * * * *", "005 * * * *", "0005-0006 * * * *",
		"*/001 * * * *", "*/1000 * * * *", "999 * * * *", "100 * * * *", "* * * jan-dec/12 *", "* * * jan-dec/13 *", "* * * * sun-sat/7", "* * * * sun-sat/8", "* * * * sun/2", "* * * mar/2 *", "* * * * mon-fri/MON",
		"* * * * *\x7f", "\v* * * * *", "* * * * *\r", "* * * * é", "@daily\t", "\t@daily", "@Daily ", strings.Repeat("*", 128), strings.Repeat(" ", 200) + "* * * * *", "* * * * *" + strings.Repeat(" ", 119), "* * * * *" + strings.Repeat(" ", 120), "0 0 29 2 *", "0 0 31 4 *", "0 0 30 2 *",
	}
	fields := [5][]string{
		{"*", "0", "59", "60", "*/7", "5-10", "5-10/2", "1,2,3", "*/0", "10-5", "x", "-1", ""},
		{"*", "0", "23", "24", "*/6", "9-17", "9-17/4", "1,12", "*/0", "17-9", "x"},
		{"*", "1", "31", "0", "32", "*/10", "1-15", "1-15/7", "1,15", "15-1", "x"},
		{"*", "1", "12", "0", "13", "jan", "DEC", "*/3", "jan-jun", "jun-jan", "1-6", "1,6", "x", "jan/2", "sun"},
		{"*", "0", "7", "8", "sun", "SAT", "mon-fri", "1-5", "*/2", "5-1", "7-7", "0-7", "x", "mon/2", "jan"},
	}
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 4000; i++ {
		parts := make([]string, 5)
		for j := range parts {
			parts[j] = fields[j][rng.Intn(len(fields[j]))]
		}
		exprs = append(exprs, strings.Join(parts, " "))
	}
	type verdict struct {
		Expr string `json:"expr"`
		OK   bool   `json:"ok"`
	}
	var verdicts []verdict
	seen := map[string]bool{}
	for _, e := range exprs {
		if seen[e] {
			continue
		}
		seen[e] = true
		verdicts = append(verdicts, verdict{Expr: e, OK: model.CronIssue(e) == ""})
	}
	data, _ := json.Marshal(verdicts)
	if err := os.WriteFile(out+".cron", data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d cron expressions", len(verdicts))
}
