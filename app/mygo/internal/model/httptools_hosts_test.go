package model_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestNormalizeMITMHostname(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":        "example.com",
		"  *.Example.COM \n": "*.example.com",
		"localhost":          "localhost",
		"_dmarc.a.com":       "_dmarc.a.com",
		"1a.com":             "1a.com",
	} {
		if got, ok := model.NormalizeMITMHostname(in); !ok || got != want {
			t.Errorf("%q = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "   ", "*", "*.", "**.a.com", "a.*.com", "*a.com", "a.com:443", "https://a.com",
		"a.com/path", "a.com?x", "a b.com", "a b.com", ".a.com", "a.com.", "a..com", "user@a.com", "*.*.a.com", "[::1]", "*.com", "例え.jp", "a!b.com", "a$.com", "*.localhost", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com"} {
		if got, ok := model.NormalizeMITMHostname(bad); ok {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestNormalizeMITMHostnames(t *testing.T) {
	got := model.NormalizeMITMHostnames([]string{"B.com", "a.com", "b.com", "*", " ", "a.com:1", "*.A.com"})
	want := model.MITMHostList{Hosts: []string{"b.com", "a.com", "*.a.com"}, Invalid: []string{"*", "a.com:1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got := model.NormalizeMITMHostnames(nil); len(got.Hosts) != 0 || len(got.Invalid) != 0 {
		t.Fatalf("nil list: %+v", got)
	}
}
