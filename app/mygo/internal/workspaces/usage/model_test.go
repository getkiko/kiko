package usage

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

func TestTotalsKeepIntegerPrecision(t *testing.T) {
	var c api.UsageCounters
	if err := json.Unmarshal([]byte(`{"upload":"9007199254740993","download":"9007199254740994","connections":"2"}`), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Total(); got != 18014398509481987 {
		t.Errorf("total %d", got)
	}
}

func TestQueryParamsSurviveEncoding(t *testing.T) {
	q := Query{From: 1, To: 2, Group: "process", Secondary: "rule",
		Filters: map[string][]string{"process": {"/Applications/Test & QA.app/Contents/MacOS/Test"}, "rule": {"rule_set=cn&private=true"}},
		Search:  "example.com + tls", Page: 0, HasPage: true, Limit: 50, Sort: "download", Order: "asc"}
	params, err := url.ParseQuery(q.Params())
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"filter.process": "/Applications/Test & QA.app/Contents/MacOS/Test",
		"filter.rule":    "rule_set=cn&private=true",
		"secondary":      "rule", "search": "example.com + tls", "page": "0",
	} {
		if got := params.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestTimeRanges(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 30, 0, 0, time.Local)
	if from, _ := TimeRange("today", "", "", now); from != time.Date(2026, 10, 16, 0, 0, 0, 0, time.Local).UnixMilli() {
		t.Errorf("today from %d", from)
	}
	for rng, d := range map[string]int64{"hour": 3600000, "day": 86400000, "week": 604800000, "month": 2592000000} {
		if from, to := TimeRange(rng, "", "", now); to-from != d {
			t.Errorf("%s spans %d", rng, to-from)
		}
	}
	if from, to := TimeRange("all", "", "", now); from != 0 || to != now.UnixMilli() {
		t.Errorf("all %d %d", from, to)
	}
}

func TestCustomRange(t *testing.T) {
	at := time.Date(2026, 10, 16, 14, 30, 0, 0, time.Local)
	start, end := LocalDateTime(at.UnixMilli()), LocalDateTime(at.Add(time.Hour).UnixMilli())
	from, to := TimeRange("custom", start, end, time.Now())
	if from != at.UnixMilli() || to != at.Add(time.Hour).UnixMilli() {
		t.Errorf("custom %d %d", from, to)
	}
	if from, to := TimeRange("custom", "", end, time.Now()); ValidRange(from, to) {
		t.Error("empty start is valid")
	}
	if from, to := TimeRange("custom", end, start, time.Now()); ValidRange(from, to) {
		t.Error("reversed range is valid")
	}
}

func TestPreferredDetail(t *testing.T) {
	for _, c := range []struct{ group, secondary, want string }{
		{"source", "", "rootDomain"}, {"process", "rootDomain", "host"}, {"rootDomain", "", "host"},
		{"rootDomain", "host", "source"}, {"host", "source", "rootDomain"},
	} {
		if got := PreferredDetail(c.group, c.secondary); got != c.want {
			t.Errorf("PreferredDetail(%q, %q) = %q, want %q", c.group, c.secondary, got, c.want)
		}
	}
}

func TestExportParams(t *testing.T) {
	q := Query{From: -5, To: 0, Group: "host", Filters: map[string][]string{"source": {"device"}}, Search: "missing", Page: 9, Limit: 1}
	if got := ExportParams(q, true); got != "scope=all" {
		t.Errorf("all export %q", got)
	}
	if got := ExportParams(q, false); got != q.Params() {
		t.Errorf("summary export %q", got)
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[uint64]string{0: "0 B", 512: "512 B", 1000: "1,000 B", 1536: "1.5 KB", 1024: "1 KB", 5 << 30: "5 GB", 1 << 50: "1 PB"} {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
	if got := Label(`C:\Program Files\app.exe`, "process"); got != "app.exe" {
		t.Errorf("process label %q", got)
	}
	if got := Label("a/b", "host"); got != "a/b" {
		t.Errorf("host label %q", got)
	}
	if got := Number(1234567); got != "1,234,567" {
		t.Errorf("Number %q", got)
	}
	if got := Share(api.UsageCounters{Upload: 1}, api.UsageCounters{Upload: 3}); got != 33.33 {
		t.Errorf("share %v", got)
	}
}
