package profiles

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeConfigURL(t *testing.T) {
	if got, err := NormalizeConfigURL(" https://example.com/config.json "); err != nil || got != "https://example.com/config.json" {
		t.Errorf("got %q, %v", got, err)
	}
	for value, want := range map[string]string{
		"file:///tmp/config.json":                     "HTTP 或 HTTPS",
		"https://user:secret@example.com/config.json": "自定义 Header",
		"not a url": "HTTP 或 HTTPS",
	} {
		if _, err := NormalizeConfigURL(value); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NormalizeConfigURL(%q) = %v, want %q", value, err, want)
		}
	}
}

func TestParseCustomHeaders(t *testing.T) {
	got, err := ParseCustomHeaders("Authorization: Bearer abc:def\nX-Profile: prod")
	want := map[string]string{"Authorization": "Bearer abc:def", "X-Profile": "prod"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := ParseCustomHeaders("  \n"); err != nil || len(got) != 0 {
		t.Errorf("blank: %v, %v", got, err)
	}
	for value, want := range map[string]string{
		"Missing colon":                          "格式",
		"Authorization: one\nauthorization: two": "重复",
		"Host: example.com":                      "网络层管理",
	} {
		if _, err := ParseCustomHeaders(value); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseCustomHeaders(%q) = %v, want %q", value, err, want)
		}
	}
}

func TestFetchConfigText(t *testing.T) {
	respond := func(status int, body string) URLRequester {
		return func(context.Context, string, map[string]string) (URLResponse, error) {
			return URLResponse{Status: status, Body: body}, nil
		}
	}
	ctx := context.Background()
	if got, err := FetchConfigText(ctx, "https://example.com/config.json", nil, respond(200, "{}")); err != nil || got != "{}" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{401, "expired token", "expired token"},
		{500, "", "HTTP 500"},
		{200, "", "为空"},
		{200, strings.Repeat("x", 5*1024*1024+1), "5 MiB"},
	} {
		if _, err := FetchConfigText(ctx, "https://example.com/config.json", nil, respond(c.status, c.body)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: %v, want %q", c.status, err, c.want)
		}
	}
}
