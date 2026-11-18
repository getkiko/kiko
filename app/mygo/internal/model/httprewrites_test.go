package model

import (
	"strings"
	"testing"
)

func TestHeaderNameIssueReserved(t *testing.T) {
	for _, name := range []string{"Host", "content-length", "Transfer_Encoding", "Expect", "keep_alive", "Proxy_Authorization"} {
		if HeaderNameIssue(name) == "" {
			t.Fatalf("%q must be reserved", name)
		}
	}
	for _, name := range []string{"X-A", "X_A", "Accept"} {
		if issue := HeaderNameIssue(name); issue != "" {
			t.Fatalf("%q: %s", name, issue)
		}
	}
}

func TestURLRewriteRedirectTarget(t *testing.T) {
	for target, ok := range map[string]bool{
		"https://b.com/x": true, "http://b.com": true, "https://b.com/$1": true, "$1": true, "${x}/a": true,
		"http://": false, "http:foo": false, "https:///x": false, "ftp://b.com": false, "/rel": false, "": false,
		"https://b.com/\nx": false,
	} {
		r := &URLRewrite{Pattern: "a", Action: URLActionFound, Target: target}
		if got := r.Issue() == ""; got != ok {
			t.Fatalf("target %q valid=%v want %v (%s)", target, got, ok, r.Issue())
		}
	}
}

func TestMapLocalStatusWithoutBody(t *testing.T) {
	for _, status := range []int{204, 205, 304} {
		if (&MapLocal{Pattern: "a", Status: status, Body: "x"}).Issue() == "" {
			t.Fatalf("%d with body must be invalid", status)
		}
		if (&MapLocal{Pattern: "a", Status: status, BodyBase64: "AQ=="}).Issue() == "" {
			t.Fatalf("%d with base64 body must be invalid", status)
		}
		if issue := (&MapLocal{Pattern: "a", Status: status}).Issue(); issue != "" {
			t.Fatalf("%d without body: %s", status, issue)
		}
	}
}

func TestRewriteLimits(t *testing.T) {
	if PatternIssue(strings.Repeat("a", MaxPattern)) != "" || PatternIssue(strings.Repeat("a", MaxPattern+1)) == "" {
		t.Fatal("pattern limit")
	}
	if HeaderValueIssue(strings.Repeat("a", MaxHeaderValue)) != "" || HeaderValueIssue(strings.Repeat("a", MaxHeaderValue+1)) == "" {
		t.Fatal("header value limit")
	}
}

func TestHeaderValueControlCharacters(t *testing.T) {
	for _, v := range []string{"a\rb", "a\nb", "a\x00b", "a\x01b", "a\x1fb", "a\x7fb"} {
		if HeaderValueIssue(v) == "" {
			t.Fatalf("%q accepted", v)
		}
	}
	for _, v := range []string{"", "a\tb", "a b", "é", "x=\"y\"; z"} {
		if issue := HeaderValueIssue(v); issue != "" {
			t.Fatalf("%q: %s", v, issue)
		}
	}
}
