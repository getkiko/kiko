package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func validScript() *HTTPScript {
	return &HTTPScript{ID: "script_1a2b3c4d", Type: ScriptHTTPResponse, Pattern: `^https://a\.com/`, Source: "$done({});"}
}

func TestHTTPScriptIssue(t *testing.T) {
	if issue := validScript().Issue(); issue != "" {
		t.Fatal(issue)
	}
	good := map[string]func(s *HTTPScript){
		"request":   func(s *HTTPScript) { s.Type = ScriptHTTPRequest },
		"generic":   func(s *HTTPScript) { s.Type = ScriptGeneric; s.Pattern = ""; s.Timeout = 60 },
		"tag":       func(s *HTTPScript) { s.Tag = "Fix.feed_1-b" },
		"tag64":     func(s *HTTPScript) { s.Tag = strings.Repeat("a", 64) },
		"maxsize":   func(s *HTTPScript) { s.MaxSize = 32 << 20 },
		"minsize":   func(s *HTTPScript) { s.MaxSize = 1024 },
		"timeout30": func(s *HTTPScript) { s.Timeout = 30 },
		"argument":  func(s *HTTPScript) { s.Argument = "a=1&b=中文" },
		"body":      func(s *HTTPScript) { s.RequiresBody, s.BinaryBody = true, true },
		"chainsemi": func(s *HTTPScript) { s.Source = strings.Repeat("x.y = z.w;\n", 2000) },
		"regex":     func(s *HTTPScript) { s.Source = "var r = /[(]{/g; $done({});" },
		"template":  func(s *HTTPScript) { s.Source = "var t = `a${b + `c${d}`}e`;" },
		"depth": func(s *HTTPScript) {
			s.Source = strings.Repeat("(", MaxScriptDepth) + strings.Repeat(")", MaxScriptDepth)
		},
		"genericsize": func(s *HTTPScript) { s.Type = ScriptGeneric; s.MaxSize = 7 },
	}
	for name, mutate := range good {
		s := validScript()
		mutate(s)
		if issue := s.Issue(); issue != "" {
			t.Errorf("%s: %s", name, issue)
		}
	}
	bad := map[string]func(s *HTTPScript){
		"type":         func(s *HTTPScript) { s.Type = "cron" },
		"notype":       func(s *HTTPScript) { s.Type = "" },
		"pattern":      func(s *HTTPScript) { s.Pattern = "(" },
		"nopattern":    func(s *HTTPScript) { s.Pattern = "" },
		"longpat":      func(s *HTTPScript) { s.Pattern = strings.Repeat("a", MaxPattern+1) },
		"source":       func(s *HTTPScript) { s.Source = " \n" },
		"bigsource":    func(s *HTTPScript) { s.Source = strings.Repeat("a", MaxScriptSource+1) },
		"nul":          func(s *HTTPScript) { s.Source = "a\x00b" },
		"utf8":         func(s *HTTPScript) { s.Source = "a\xffb" },
		"tag":          func(s *HTTPScript) { s.Tag = "a b" },
		"tagslash":     func(s *HTTPScript) { s.Tag = "../x" },
		"tag65":        func(s *HTTPScript) { s.Tag = strings.Repeat("a", 65) },
		"tagunicode":   func(s *HTTPScript) { s.Tag = "脚本" },
		"notag":        func(s *HTTPScript) { s.ID = "" },
		"size":         func(s *HTTPScript) { s.MaxSize = 1500 },
		"smallsize":    func(s *HTTPScript) { s.MaxSize = 512 },
		"bigsize":      func(s *HTTPScript) { s.MaxSize = 33 << 20 },
		"negsize":      func(s *HTTPScript) { s.MaxSize = -1024 },
		"timeout":      func(s *HTTPScript) { s.Timeout = 31 },
		"negtimeout":   func(s *HTTPScript) { s.Timeout = -1 },
		"gentimeout":   func(s *HTTPScript) { s.Type = ScriptGeneric; s.Timeout = 61 },
		"argument":     func(s *HTTPScript) { s.Argument = strings.Repeat("a", MaxScriptArgument+1) },
		"binary":       func(s *HTTPScript) { s.BinaryBody = true },
		"chain":        func(s *HTTPScript) { s.Source = strings.Repeat("x.y = z.w\n", 2000) },
		"unterminated": func(s *HTTPScript) { s.Source = "var s = 'abc" },
		"deep": func(s *HTTPScript) {
			s.Source = strings.Repeat("(", MaxScriptDepth+1) + strings.Repeat(")", MaxScriptDepth+1)
		},
		"pat4097": func(s *HTTPScript) { s.Pattern = strings.Repeat("a", 4097) },
		"argnul":  func(s *HTTPScript) { s.Argument = "a\x00" },
	}
	for name, mutate := range bad {
		s := validScript()
		mutate(s)
		if s.Issue() == "" {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestHTTPScriptJSONAndEnabled(t *testing.T) {
	var tools HTTPTools
	text := `{"scripts":[{"id":"a","type":"generic","source":"x","disabled":true},{"id":"b","type":"http-request","pattern":"p","source":"y","requiresBody":true,"maxSize":2048,"timeout":3,"argument":"k=v"}]}`
	if err := json.Unmarshal([]byte(text), &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools.Scripts) != 2 || !tools.Scripts[0].Disabled || tools.Scripts[1].MaxSize != 2048 || !tools.Scripts[1].RequiresBody {
		t.Fatalf("scripts = %+v", tools.Scripts)
	}
	if !tools.EnabledScripts() {
		t.Fatal("enabled script not seen")
	}
	tools.Scripts[1].Disabled = true
	if tools.EnabledScripts() || (*HTTPTools)(nil).EnabledScripts() {
		t.Fatal("disabled scripts count")
	}
	out, _ := json.Marshal(HTTPTools{})
	if string(out) != "{}" {
		t.Fatalf("empty tools = %s", out)
	}
}

func TestCronIssue(t *testing.T) {
	good := []string{"* * * * *", "0 0 * * *", "*/5 * * * *", "0-30/10 1,2 * jan-mar mon-fri", "59 23 31 12 7", "0 0 * * 0", "@daily", "@HOURLY", "@midnight", "@annually", "0 0 * JAN SUN", "*/60 * * * *", "* */24 * * *", "* * * * */8", "005 * * * *", " 1  2\t3 4 5 "}
	for _, e := range good {
		if issue := CronIssue(e); issue != "" {
			t.Errorf("%q: %s", e, issue)
		}
	}
	bad := []string{"", " ", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 0 *", "* * * 13 *", "* * * * 8", "1/5 * * * *", "*/0 * * * *", "5-1 * * * *",
		"? * * * *", "* * L * *", "* * * * 1#2", "@reboot", "@every 5m", "@daily *", "* * * * mon/2", "0 0 * * monday", "-1 * * * *", "1,,2 * * * *", "* * * * *,", "jan * * * *", "*/61 * * * *", "0005 * * * *", "*/0060 * * * *", "* * * * mon-fri/9999", "* * * * */9", "\n* * * * *", "* * * * " + strings.Repeat("*", 130)}
	for _, e := range bad {
		if CronIssue(e) == "" {
			t.Errorf("%q accepted", e)
		}
	}
}

func TestCronScriptIssue(t *testing.T) {
	s := &HTTPScript{ID: "c", Type: ScriptCron, Cron: "*/5 * * * *", Source: "$done();", Timeout: 60}
	if issue := s.Issue(); issue != "" || s.IsHTTP() || !s.IsRun() {
		t.Fatal(issue)
	}
	s.Timeout = 61
	if s.Issue() == "" {
		t.Fatal("61s accepted")
	}
	s.Timeout, s.Cron = 0, ""
	if s.Issue() == "" {
		t.Fatal("empty cron accepted")
	}
	if !ValidScriptType(ScriptCron) || !(&HTTPScript{Type: ScriptGeneric}).IsRun() || (&HTTPScript{Type: ScriptHTTPRequest}).IsRun() {
		t.Fatal("type predicates")
	}
}
