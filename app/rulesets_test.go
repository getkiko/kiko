package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func ruleSetTestServer(controller string) *server {
	return &server{
		state:       runtimeState{State: "running"},
		cmd:         &exec.Cmd{},
		clashAddr:   strings.TrimPrefix(controller, "http://"),
		clashSecret: "runtime-secret",
	}
}

func binaryRuleSetForTest(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	err := srs.Write(&buffer, option.PlainRuleSet{Rules: []option.HeadlessRule{{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultHeadlessRule{
			Domain:       badoption.Listable[string]{"a.test"},
			DomainSuffix: badoption.Listable[string]{"b.test"},
			IPCIDR:       badoption.Listable[string]{"10.0.0.0/8"},
		},
	}}}, C.RuleSetVersionCurrent)
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type ruleSetSourceReply struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
	From    string           `json:"from"`
	Error   string           `json:"error"`
}

func postRuleSetSource(t *testing.T, s *server, body string) (int, ruleSetSourceReply) {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.ruleSetSource(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/rulesets/source", strings.NewReader(body)))
	var reply ruleSetSourceReply
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode %d %q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return recorder.Code, reply
}

func TestDecodeRuleSetSourceBinaryAndJSON(t *testing.T) {
	binary, err := decodeRuleSetSource(binaryRuleSetForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	rule := binary.Options.Rules[0].DefaultOptions
	if binary.Version != C.RuleSetVersionCurrent || len(binary.Options.Rules) != 1 ||
		strings.Join(rule.Domain, ",") != "a.test" || strings.Join(rule.DomainSuffix, ",") != "b.test" || strings.Join(rule.IPCIDR, ",") != "10.0.0.0/8" {
		t.Fatalf("binary rule-set: %+v", binary)
	}
	source, err := decodeRuleSetSource([]byte(`{"version": 2, // comment
		"rules": [{"domain_keyword": ["kw"]}, {"type": "logical", "mode": "and", "rules": [{"port": 443}, {"domain": "x.test"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if source.Version != 2 || len(source.Options.Rules) != 2 || source.Options.Rules[1].Type != C.RuleTypeLogical {
		t.Fatalf("source rule-set: %+v", source)
	}
	for _, bad := range []string{"", "not json", `{"rules":[]}`, string(srs.MagicBytes[:]) + "garbage"} {
		if _, err := decodeRuleSetSource([]byte(bad)); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
}

func TestRuleSetSourceFromCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/rulesets/geo%20site/source" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("core request: %s %s auth=%q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"version":3,"rules":[{"domain":["a.test"]}]}`)
	}))
	defer core.Close()
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downloaded although the core holds the rule-set")
	}))
	defer download.Close()
	status, reply := postRuleSetSource(t, ruleSetTestServer(core.URL), `{"tag":"geo site","url":"`+download.URL+`"}`)
	if status != http.StatusOK || reply.From != "core" || reply.Version != 3 || len(reply.Rules) != 1 || reply.Rules[0]["domain"] == nil {
		t.Fatalf("source: %d %+v", status, reply)
	}
}

func TestRuleSetSourceDownloadsWithoutCore(t *testing.T) {
	binary := binaryRuleSetForTest(t)
	var agents []string
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/set.srs":
			_, _ = w.Write(binary)
		case "/set.json":
			_, _ = io.WriteString(w, `{"version":1,"rules":[{"domain_suffix":["c.test"]}]}`)
		case "/bad":
			_, _ = io.WriteString(w, `<html></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer download.Close()

	// The core is stopped.
	stopped := ruleSetTestServer("")
	stopped.state.State = "stopped"
	status, reply := postRuleSetSource(t, stopped, `{"tag":"geo","url":"`+download.URL+`/set.srs"}`)
	if status != http.StatusOK || reply.From != "download" || reply.Version != C.RuleSetVersionCurrent || len(reply.Rules) != 1 {
		t.Fatalf("binary download: %d %+v", status, reply)
	}
	if reply.Rules[0]["ip_cidr"] == nil || reply.Rules[0]["domain"] == nil {
		t.Fatalf("binary download keeps the lists: %+v", reply.Rules[0])
	}
	if agents[0] != fetchUserAgent {
		t.Fatalf("User-Agent = %q", agents[0])
	}

	// The core does not hold the rule-set.
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Resource not found"}`)
	}))
	defer core.Close()
	running := ruleSetTestServer(core.URL)
	status, reply = postRuleSetSource(t, running, `{"tag":"new","url":"`+download.URL+`/set.json"}`)
	if status != http.StatusOK || reply.From != "download" || reply.Version != 1 || reply.Rules[0]["domain_suffix"] == nil {
		t.Fatalf("source download: %d %+v", status, reply)
	}

	// A URL alone downloads.
	status, reply = postRuleSetSource(t, running, `{"url":"`+download.URL+`/set.json"}`)
	if status != http.StatusOK || reply.From != "download" {
		t.Fatalf("url only: %d %+v", status, reply)
	}
}

func TestRuleSetSourceErrors(t *testing.T) {
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			_, _ = io.WriteString(w, `<html></html>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer download.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"rule-set has no saved content: file does not exist"}`)
	}))
	defer core.Close()
	stopped := ruleSetTestServer("")
	stopped.state.State = "stopped"
	for _, c := range []struct {
		name   string
		s      *server
		body   string
		status int
		error  string
	}{
		{"nothing to read", stopped, `{}`, http.StatusBadRequest, "tag or url required"},
		{"core stopped without url", stopped, `{"tag":"geo"}`, http.StatusServiceUnavailable, "unavailable"},
		{"core without content", ruleSetTestServer(core.URL), `{"tag":"geo"}`, http.StatusNotFound, "no saved content"},
		{"not a rule-set", stopped, `{"tag":"geo","url":"` + download.URL + `/bad"}`, http.StatusUnprocessableEntity, "decode source rule-set"},
		{"download fails", stopped, `{"tag":"geo","url":"` + download.URL + `/missing"}`, http.StatusBadGateway, "404"},
		{"not http", stopped, `{"url":"file:///etc/passwd"}`, http.StatusBadRequest, "http/https"},
	} {
		status, reply := postRuleSetSource(t, c.s, c.body)
		if status != c.status || !strings.Contains(reply.Error, c.error) {
			t.Errorf("%s: %d %+v", c.name, status, reply)
		}
	}
}

func TestUpdateRuleSetForwardsToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("core request: %s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		switch r.URL.EscapedPath() {
		case "/rulesets/geo%20site":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tag":"geo site","type":"remote","lastUpdatedAt":1792720440000}`)
		case "/rulesets/broken":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"message":"unexpected status: 500 Internal Server Error"}`)
		case "/rulesets/mine":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"inline rule-set cannot be updated"}`)
		default:
			t.Errorf("core path %s", r.URL.EscapedPath())
		}
	}))
	defer core.Close()
	update := func(s *server, body string) (int, map[string]any) {
		recorder := httptest.NewRecorder()
		s.updateRuleSet(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/rulesets/update", strings.NewReader(body)))
		var reply map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
			t.Fatalf("decode %d %q: %v", recorder.Code, recorder.Body.String(), err)
		}
		return recorder.Code, reply
	}
	running := ruleSetTestServer(core.URL)
	status, reply := update(running, `{"tag":"geo site"}`)
	if status != http.StatusOK || reply["tag"] != "geo site" || reply["lastUpdatedAt"] != float64(1792720440000) || reply["type"] != "remote" {
		t.Fatalf("update: %d %+v", status, reply)
	}
	status, reply = update(running, `{"tag":"broken"}`)
	if status != http.StatusBadGateway || reply["error"] != "unexpected status: 500 Internal Server Error" {
		t.Fatalf("failed update: %d %+v", status, reply)
	}
	status, reply = update(running, `{"tag":"mine"}`)
	if status != http.StatusBadRequest || reply["error"] != "inline rule-set cannot be updated" {
		t.Fatalf("inline update: %d %+v", status, reply)
	}
	status, _ = update(running, `{"tag":""}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty tag: %d", status)
	}
	stopped := ruleSetTestServer(core.URL)
	stopped.state.State = "stopped"
	status, reply = update(stopped, `{"tag":"geo site"}`)
	if status != http.StatusServiceUnavailable || reply["error"] == nil {
		t.Fatalf("core stopped: %d %+v", status, reply)
	}
}

func TestRuleSetRoutesAreRegistered(t *testing.T) {
	m := (&server{}).routeTable()
	want := map[string]routeClass{"POST /api/v1/rulesets/update": routeOwn, "POST /api/v1/rulesets/source": routeRead}
	for _, spec := range m.specs {
		if class, ok := want[spec.Pattern]; ok {
			if spec.Class != class {
				t.Errorf("%s: class %s, want %s", spec.Pattern, spec.Class, class)
			}
			delete(want, spec.Pattern)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing routes: %v", want)
	}
}

// After a remote rule set's link changed, the core's content of the old
// link does not stand for the new one: the new link is downloaded.
func TestRuleSetSourceFollowsTheLink(t *testing.T) {
	coreCalls := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coreCalls++
		_, _ = io.WriteString(w, `{"version":3,"rules":[{"domain":["old.test"]}]}`)
	}))
	defer core.Close()
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":2,"rules":[{"domain":["new.test"]}]}`)
	}))
	defer download.Close()
	s := ruleSetTestServer(core.URL)
	s.dataDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(s.dataDir, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"route":{"rule_set":[{"type":"remote","tag":"geo","url":"` + download.URL + `/old.json"}]}}`
	if err := os.WriteFile(filepath.Join(s.dataDir, "runtime", "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	status, reply := postRuleSetSource(t, s, `{"tag":"geo","url":"`+download.URL+`/new.json"}`)
	if status != http.StatusOK || reply.From != "download" || reply.Rules[0]["domain"] == nil || coreCalls != 0 {
		t.Fatalf("changed link: %d %+v, core calls %d", status, reply, coreCalls)
	}
	status, reply = postRuleSetSource(t, s, `{"tag":"geo","url":"`+download.URL+`/old.json"}`)
	if status != http.StatusOK || reply.From != "core" || coreCalls != 1 {
		t.Fatalf("same link: %d %+v, core calls %d", status, reply, coreCalls)
	}
	// Applied inline, the core's content is not the link's.
	config = `{"route":{"rule_set":[{"type":"inline","tag":"geo","rules":[{"domain":["inline.test"]}]}]}}`
	if err := os.WriteFile(filepath.Join(s.dataDir, "runtime", "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	status, reply = postRuleSetSource(t, s, `{"tag":"geo","url":"`+download.URL+`/new.json"}`)
	if status != http.StatusOK || reply.From != "download" || coreCalls != 1 {
		t.Fatalf("applied inline: %d %+v, core calls %d", status, reply, coreCalls)
	}
}
