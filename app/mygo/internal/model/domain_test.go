package model_test

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Ported from the former TypeScript console's domain-input tests (their
// calls could not be recorded: the TypeScript needed the re2js package).

func TestParseDomainEntry(t *testing.T) {
	cases := []struct {
		input, field string
		want         model.DomainEntry
	}{
		{" Example.COM. ", "", model.DomainEntry{Field: "domain", Value: "example.com"}},
		{"API.Example.com", "domain_suffix", model.DomainEntry{Field: "domain_suffix", Value: "api.example.com"}},
		{"*.example.com", "", model.DomainEntry{Field: "domain", Value: "example.com"}},
		{".example.com", "", model.DomainEntry{Field: "domain", Value: "example.com"}},
		{"*.example.com", "domain_suffix", model.DomainEntry{Field: "domain_suffix", Value: "example.com"}},
		{"^example\\.com$", "", model.DomainEntry{Field: "domain_regex", Value: "^example\\.com$"}},
		{"/^(api|www)\\.example\\.com$/i", "", model.DomainEntry{Field: "domain_regex", Value: "(?i)^(api|www)\\.example\\.com$"}},
		{"regex: ^example\\.com$", "", model.DomainEntry{Field: "domain_regex", Value: "^example\\.com$"}},
		{" legacy ", "domain_keyword", model.DomainEntry{Field: "domain_keyword", Value: "legacy"}},
	}
	for _, c := range cases {
		got, problem := model.ParseDomainEntry(c.input, c.field)
		if problem != "" || got != c.want {
			t.Errorf("ParseDomainEntry(%q, %q) = %+v %q, want %+v", c.input, c.field, got, problem, c.want)
		}
	}
	for _, input := range []string{"*.example.com", ".example.com"} {
		if model.IsDomainRegex(input) {
			t.Errorf("IsDomainRegex(%q) = true", input)
		}
	}
}

func TestNormalizeDomainName(t *testing.T) {
	long := strings.Repeat("a", 63)
	for input, want := range map[string]string{
		"例子.测试": "xn--fsqu00a.xn--0zwm56d", "123": "123", "example.123": "example.123",
		"localhost": "localhost", long + ".com": long + ".com",
	} {
		if got := model.NormalizeDomainName(input); got != want {
			t.Errorf("NormalizeDomainName(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "example com", "https://example.com", "example.com/path", "example.com:443",
		"user@example.com", "example.com?x", "a..com", "-example.com", "example-.com", strings.Repeat("a", 64) + ".com",
		strings.Join([]string{long, long, long, long, long}, ".")} {
		if got := model.NormalizeDomainName(input); got != "" {
			t.Errorf("NormalizeDomainName(%q) = %q, want invalid", input, got)
		}
		if _, problem := model.ParseDomainEntry(input, "domain"); problem == "" {
			t.Errorf("ParseDomainEntry(%q, domain) accepted", input)
		}
	}
}

func TestDomainRegex(t *testing.T) {
	for _, input := range []string{"^example\\.com$", "(api|www)\\.example\\.com", ".*example.*", "[a-z]+\\.com", "regex:example", "/example\\.com/i"} {
		if !model.IsDomainRegex(input) {
			t.Errorf("IsDomainRegex(%q) = false", input)
		}
	}
	for _, input := range []string{"example.com", "my-domain.example", "localhost", "例子.测试"} {
		if model.IsDomainRegex(input) {
			t.Errorf("IsDomainRegex(%q) = true", input)
		}
	}
	pattern, _, problem := model.CompileDomainRegex("(?i)^(api|www)\\.example\\.com$")
	if problem != "" || !pattern.MatchString("API.example.com") || pattern.MatchString("other.example.com") {
		t.Errorf("flagged regex: %q", problem)
	}
	pattern, _, _ = model.CompileDomainRegex("(?i)example\\.com")
	if !pattern.MatchString("API.EXAMPLE.COM") || pattern.MatchString("api.other.com") {
		t.Error("regex search semantics")
	}
	if pattern, _, _ := model.CompileDomainRegex("^example\\.com$"); pattern.MatchString("api.example.com") {
		t.Error("anchored regex matched a subdomain")
	}
	for _, input := range []string{"(", "[abc", "(?=example)example", "(example)\\1", "/example/g", "regex:", "//"} {
		if _, _, problem := model.CompileDomainRegex(input); problem == "" {
			t.Errorf("CompileDomainRegex(%q) accepted", input)
		}
		if _, problem := model.ParseDomainEntry(input, "domain_regex"); problem == "" {
			t.Errorf("ParseDomainEntry(%q, domain_regex) accepted", input)
		}
	}
}

func TestDomainTokens(t *testing.T) {
	values := []model.MatchValue{{Field: "domain_suffix", Value: "example.com"}, {Field: "domain", Value: "api.other.com"}, {Field: "domain_regex", Value: "^cdn[0-9]+\\.example\\.net$"}}
	group := model.AddMatchValues(model.EmptyConditionGroup(), model.TypedDomain, values)
	tokens := model.MatchTokens(group, model.TypedDomain)
	if len(tokens) != 3 {
		t.Fatalf("tokens %+v", tokens)
	}
	for i, token := range tokens {
		if token.Field != values[i].Field || token.Value != values[i].Value {
			t.Errorf("token %d = %+v", i, token)
		}
	}
	labels := []string{model.DomainKindLabel(tokens[0].Field), model.DomainKindLabel(tokens[1].Field), model.DomainKindLabel(tokens[2].Field)}
	if strings.Join(labels, ",") != "后缀,全匹配,正则" {
		t.Errorf("labels %v", labels)
	}
	if n := len(model.MatchTokens(model.AddMatchValues(group, model.TypedDomain, values), model.TypedDomain)); n != 3 {
		t.Errorf("duplicate values added: %d tokens", n)
	}
	excluded := model.SetMatchExclusion(group, model.TypedDomain, true)
	excluded = model.AddMatchValues(excluded, model.TypedDomain, []model.MatchValue{{Field: "domain_regex", Value: "^cdn\\."}})
	if all, mixed := model.MatchExclusion(excluded, model.TypedDomain); !all || mixed {
		t.Errorf("exclusion %v %v", all, mixed)
	}
}

func TestSSHOptions(t *testing.T) {
	store := memoryStore{}
	model.SaveSSHOptions(store, "tailnet", "peer", model.SSHOptions{Username: "alice", TerminalType: "vt100", Remember: true})
	if got := model.LoadSSHOptions(store, "tailnet", "peer"); got != (model.SSHOptions{Username: "alice", TerminalType: "vt100", Remember: true}) {
		t.Errorf("remembered %+v", got)
	}
	model.SaveSSHOptions(store, "tailnet", "peer", model.SSHOptions{Username: "bob", TerminalType: "xterm"})
	if got := model.LoadSSHOptions(store, "tailnet", "peer"); got != model.DefaultSSHOptions || len(store) != 0 {
		t.Errorf("forgotten %+v, store %v", got, store)
	}
}

type memoryStore map[string]string

func (m memoryStore) Get(key string) (string, bool) { v, ok := m[key]; return v, ok }
func (m memoryStore) Set(key, value string)         { m[key] = value }
func (m memoryStore) Remove(key string)             { delete(m, key) }
