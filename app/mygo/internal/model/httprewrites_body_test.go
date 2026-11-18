package model

import (
	"strings"
	"testing"
)

func TestJSONPathSubset(t *testing.T) {
	good := []string{"$.a", "$.a.b", "$..id", "$['a b']", "$.a[0]", "$.a[-1]", "$.a[*]", "$.a[0,2]", "$['a','b']", "$.a['it\\'s']", "$..a[*].b", "$['x']['y']", "$.*", "$.a.*", "$..['a b']"}
	for _, p := range good {
		if issue := JSONPathIssue(p); issue != "" {
			t.Fatalf("%q: %s", p, issue)
		}
	}
	bad := []string{"", "a.b", "$.", "$..", "$.a.", "$.a[", "$.a[]", "$.a[?(@.x>1)]", "$.a[1:2]", "$.a[0,'b']", "$['a]", "$.a b", "$..*", "$..[0]", "$..[*]", "$.a[x]"}
	for _, p := range bad {
		if JSONPathIssue(p) == "" {
			t.Fatalf("%q accepted", p)
		}
	}
	// Differential cases against the core's parser.
	for _, p := range []string{"$", `$..["a"]`, `$["a"]`, "$[ 0 ]", "$[99999999999]", "$[ 'a' , 'b' ]", "$[0 ,1]", "$[" + strings.TrimSuffix(strings.Repeat("0,", 64), ",") + "]"} {
		if issue := JSONPathIssue(p); issue != "" {
			t.Fatalf("%q: %s", p, issue)
		}
	}
	for _, p := range []string{"$..['a','b']", "$.a(b", "$.a:b", "$.a?b", "$.a=b", "$.a!", "$.a@", "$.a<b", "$.a\nb", "$.a\rb", "$.a\x01b", "$.a\x7fb",
		"$[" + strings.Repeat("0,", 64) + "0]", "$.a>b", "$.a\"b"} {
		if JSONPathIssue(p) == "" {
			t.Fatalf("%q accepted", p)
		}
	}
}

func TestBodyRewriteIssue(t *testing.T) {
	ok := &BodyRewrite{Pattern: "a", Scope: "response", Edits: []*BodyEdit{
		{Action: BodyActionReplaceRegex, Regex: "(a)b", Value: "$1"},
		{Action: BodyActionJSONSet, Path: "$.a", Value: `{"x":1}`},
		{Action: BodyActionJSONDelete, Path: "$..b"},
		{Action: BodyActionJSONAppend, Path: "$.l", Value: "1"},
	}}
	if issue := ok.Issue(); issue != "" {
		t.Fatal(issue)
	}
	mutate := map[string]func(r *BodyRewrite){
		"scope":      func(r *BodyRewrite) { r.Scope = "x" },
		"empty":      func(r *BodyRewrite) { r.Edits = nil },
		"regex":      func(r *BodyRewrite) { r.Edits[0].Regex = "(" },
		"regexempty": func(r *BodyRewrite) { r.Edits[0].Regex = "" },
		"regexlong":  func(r *BodyRewrite) { r.Edits[0].Regex = strings.Repeat("a", MaxPattern+1) },
		"json":       func(r *BodyRewrite) { r.Edits[1].Value = "{x" },
		"path":       func(r *BodyRewrite) { r.Edits[2].Path = "$.a[" },
		"value":      func(r *BodyRewrite) { r.Edits[0].Value = strings.Repeat("a", MaxHeaderValue+1) },
		"action":     func(r *BodyRewrite) { r.Edits[0].Action = "x" },
		"many": func(r *BodyRewrite) {
			for len(r.Edits) <= MaxBodyEdits {
				r.Edits = append(r.Edits, &BodyEdit{Action: BodyActionJSONDelete, Path: "$.a"})
			}
		},
		"ctype": func(r *BodyRewrite) { r.ContentTypes = []string{" "} },
	}
	for name, f := range mutate {
		r := &BodyRewrite{Pattern: ok.Pattern, Scope: ok.Scope}
		for _, e := range ok.Edits {
			c := *e
			r.Edits = append(r.Edits, &c)
		}
		f(r)
		if r.Issue() == "" {
			t.Fatalf("%s accepted", name)
		}
	}
	r := &BodyRewrite{Pattern: "a", Scope: "request", Edits: make([]*BodyEdit, MaxBodyEdits)}
	for i := range r.Edits {
		r.Edits[i] = &BodyEdit{Action: BodyActionJSONDelete, Path: "$.a"}
	}
	if issue := r.Issue(); issue != "" {
		t.Fatal(issue)
	}
}

func TestMaxBodySizeIssue(t *testing.T) {
	for n, ok := range map[int]bool{0: true, 1024: true, 2 << 20: true, 32 << 20: true, 1023: false, 512: false, 32<<20 + 1024: false, -1: false, 1500: false} {
		if got := MaxBodySizeIssue(n) == ""; got != ok {
			t.Fatalf("%d valid=%v want %v", n, got, ok)
		}
	}
}

func TestHeaderTargetIssue(t *testing.T) {
	for target, ok := range map[string]bool{
		"https://b.com": true, "http://b.com:8080/x?y=1": true, "https://b.com/$1": true, "HTTPS://B.com": true, "http://$1.b.com/x": true,
		"ftp://b.com": false, "https://": false, "https:///x": false, "https://u:p@b.com": false, "https://u@$1": true, "$1/new/$2": true, "${1}": true, "https://@$1": true, "https://a.com/$1\nx": false, "$1 x": false,
		"https://b.com/\nx": false, "https://b .com": false, "": false, "b.com": false, "https://b.com/" + strings.Repeat("a", MaxRewriteTargetLength): false,
	} {
		if got := HeaderTargetIssue(target) == ""; got != ok {
			t.Fatalf("target %q valid=%v want %v (%s)", target, got, ok, HeaderTargetIssue(target))
		}
		r := &URLRewrite{Pattern: "a", Action: URLActionHeader, Target: target}
		if (r.Issue() == "") != ok {
			t.Fatalf("rule target %q", target)
		}
	}
}

func TestHeaderTargetCoreParity(t *testing.T) {
	for _, target := range []string{"https://a.com:99999", "https://a.com:0", "https://a.com:", "https://a.com:/x", "https://[::1]:", "https://:443/",
		"https://a.com%25x/", "https://a%com/", "https://a.com\\b/", "https://a.com:65536/"} {
		if HeaderTargetIssue(target) == "" {
			t.Fatalf("%q accepted", target)
		}
	}
	for _, target := range []string{"https://a.com:1/", "https://a.com:65535", "https://[::1]:8080/x", "http://a.com?q=1", "https://a.com#f"} {
		if issue := HeaderTargetIssue(target); issue != "" {
			t.Fatalf("%q: %s", target, issue)
		}
	}
}

func TestJSONValueDepth(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	if JSONValueIssue(deep(128)) != "" || JSONValueIssue(deep(129)) == "" {
		t.Fatal("depth limit")
	}
	if JSONValueIssue(`"[[[[`+strings.Repeat("[", 200)+`"`) != "" {
		t.Fatal("brackets in strings count")
	}
}

func TestJSONPathLimits(t *testing.T) {
	segs := func(n int) string { return "$" + strings.Repeat(".a", n) }
	if JSONPathIssue(segs(64)) != "" || JSONPathIssue(segs(65)) == "" {
		t.Fatal("segment limit")
	}
	union := func(n int) string { return "$[" + strings.TrimSuffix(strings.Repeat("1,", n), ",") + "]" }
	if JSONPathIssue(union(64)) != "" || JSONPathIssue(union(65)) == "" {
		t.Fatal("union limit")
	}
	long := func(n int) string { return "$." + strings.Repeat("a", n-2) }
	if JSONPathIssue(long(MaxPattern)) != "" || JSONPathIssue(long(MaxPattern+1)) == "" {
		t.Fatal("length limit")
	}
}

func TestBodyContentTypeTrim(t *testing.T) {
	r := &BodyRewrite{Pattern: "a", Scope: "request", Edits: []*BodyEdit{{Action: BodyActionJSONDelete, Path: "$.a"}}}
	r.ContentTypes = []string{" " + strings.Repeat("a", MaxBodyContentTypeLen) + " "}
	if issue := r.Issue(); issue != "" {
		t.Fatal(issue)
	}
	r.ContentTypes = []string{strings.Repeat("a", MaxBodyContentTypeLen+1)}
	if r.Issue() == "" {
		t.Fatal("too long accepted")
	}
}
