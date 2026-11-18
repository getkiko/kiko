package model_test

import (
	"reflect"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func viewItems(entries ...model.RuleSetEntry) []model.RuleSetViewItem {
	out := make([]model.RuleSetViewItem, len(entries))
	for i, e := range entries {
		out[i] = model.RuleSetViewItem{RuleSetItem: model.RuleSetItem{Field: e.Field, Value: e.Value}}
	}
	return out
}

func searchValues(items []model.RuleSetViewItem, query string) []string {
	var out []string
	for _, i := range model.FilterRuleSetView(items, query) {
		out = append(out, items[i].Value)
	}
	return out
}

// The search shows an equal value first, then values starting with the
// query, then values it matches as whole labels (a domain suffix), then
// the other values containing it; each tier in the set's order.
func TestFilterRuleSetViewRanks(t *testing.T) {
	items := viewItems(
		entry("domain_keyword", "notgoogle.com.evil"), // contains
		entry("domain_suffix", "mail.google.com"),     // labels
		entry("domain", "google.com.hk"),              // prefix
		entry("domain_suffix", "agoogle.com"),         // contains
		entry("domain", "GOOGLE.COM"),                 // exact, another case
		entry("domain_suffix", "www.google.com"),      // labels
		entry("domain", "google.com"),                 // exact
		entry("domain", "a.google.com.cn"),            // labels inside
		entry("domain", "example.org"),                // none
		entry("domain_regex", `^google\.com$`),        // contains com only
		entry("domain", "google.community"),           // prefix
	)
	got := searchValues(items, "  Google.com ")
	want := []string{
		"GOOGLE.COM", "google.com",
		"google.com.hk", "google.community",
		"mail.google.com", "www.google.com", "a.google.com.cn",
		"notgoogle.com.evil", "agoogle.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked %q, want %q", got, want)
	}
	// A leading dot bounds the labels: the values holding it.
	if got := searchValues(items, ".google.com"); !reflect.DeepEqual(got, []string{"mail.google.com", "www.google.com", "a.google.com.cn"}) {
		t.Errorf(".google.com ranked %q", got)
	}
	// One label: whole labels before parts of one.
	if got := searchValues(items, "com"); !reflect.DeepEqual(got, []string{
		"notgoogle.com.evil", "mail.google.com", "google.com.hk", "agoogle.com", "GOOGLE.COM", "www.google.com", "google.com", "a.google.com.cn",
		`^google\.com$`, "google.community",
	}) {
		t.Errorf("com ranked %q", got)
	}
}

// An empty search keeps every item in order; a field name matches as a
// contained value does, after the values.
func TestFilterRuleSetViewFields(t *testing.T) {
	items := viewItems(entry("ip_cidr", "10.0.0.0/8"), entry("domain", "ip.example"), entry("domain", "a.example"))
	if got := model.FilterRuleSetView(items, " "); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("empty search %v", got)
	}
	// ip is a prefix of ip.example, and the field of the address range.
	if got := searchValues(items, "IP"); !reflect.DeepEqual(got, []string{"ip.example", "10.0.0.0/8"}) {
		t.Errorf("ip ranked %q", got)
	}
	if got := searchValues(items, "IP 段"); !reflect.DeepEqual(got, []string{"10.0.0.0/8"}) {
		t.Errorf("label search %q", got)
	}
	if got := searchValues(items, "zzz"); len(got) != 0 {
		t.Errorf("no match %q", got)
	}
}

// A suffix written with its leading dot equals the domain searched.
func TestFilterRuleSetViewLeadingDot(t *testing.T) {
	items := viewItems(entry("domain", "google.com.hk"), entry("domain_suffix", "www.google.com"), entry("domain_suffix", ".Google.com"))
	if got := searchValues(items, "google.com"); !reflect.DeepEqual(got, []string{".Google.com", "google.com.hk", "www.google.com"}) {
		t.Errorf("ranked %q", got)
	}
	if got := searchValues(items, ".google.com"); !reflect.DeepEqual(got, []string{".Google.com", "www.google.com"}) {
		t.Errorf(".google.com ranked %q", got)
	}
}
