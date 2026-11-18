package dnsservers

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestHelpers(t *testing.T) {
	if got := valuesOf("a, b\nc"); len(got) != 3 || got[2] != "c" {
		t.Fatal(got)
	}
	if objectText(map[string]any{"a": 1.0}) != "{\n  \"a\": 1\n}" || objectText([]any{}) != "" {
		t.Fatal("objectText")
	}
	s := &model.DNSServer{Fields: model.Fields{"headers": "[1]"}}
	if normalizeJSONFields(s) != "HTTP 请求头必须是 JSON 对象" {
		t.Fatal("headers")
	}
	if serverEndpoint(&model.DNSServer{Type: "dhcp"}) != "自动选择网络接口" || protocolLabel("x") != "X" {
		t.Fatal("endpoint labels")
	}
	if countText([]*model.DNSServer{{Enabled: true}, {}}, 1, true) != "1 / 2 个" ||
		countText([]*model.DNSServer{{Enabled: true}, {}}, 2, false) != "2 个" {
		t.Fatal("count")
	}
}
