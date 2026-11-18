package logs

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		line string
		want []Segment
	}{
		{"before \x1b[36mINFO\x1b[0m after", []Segment{{Text: "before "}, {Text: "INFO", Style: TextStyle{Color: "#2dd4bf"}}, {Text: " after"}}},
		{"\x1b[38;5;196mred\x1b[0m \x1b[38;2;12;34;56mcustom", []Segment{
			{Text: "red", Style: TextStyle{Color: "rgb(255, 0, 0)"}}, {Text: " "}, {Text: "custom", Style: TextStyle{Color: "rgb(12, 34, 56)"}}}},
		{"2026-08-29T08:22:43+08:00 +0800 2026-08-29 08:22:43 INFO router started", []Segment{
			{Text: "2026-08-29T08:22:43+08:00 +0800 2026-08-29 08:22:43 "}, {Text: "INFO", Style: TextStyle{Color: "#2dd4bf", Weight: 600}}, {Text: " router started"}}},
		{"\x1b[2Kplain\x1b[999m text", []Segment{{Text: "plain text"}}},
	}
	for _, c := range cases {
		if got := ParseLine(c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseLine(%q) = %+v, want %+v", c.line, got, c.want)
		}
	}
}

func update(t *testing.T, raw string) api.CoreLogUpdate {
	t.Helper()
	var u api.CoreLogUpdate
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCoreLogResets(t *testing.T) {
	id := 0
	next := func() int { id++; return id - 1 }
	first := AppendCoreLogs(nil, update(t, `{"reset":true,"messages":[{"level":"INFO","message":"mentions DEBUG"},{"level":6,"message":"trace"}]}`), next)
	if first[0].Level != 4 || first[1].Level != 6 {
		t.Errorf("levels %+v", first)
	}
	second := AppendCoreLogs(first, update(t, `{"messages":[{"level":"WARN","message":"warning"}]}`), next)
	if len(second) != 3 || second[0] != first[0] {
		t.Errorf("second %+v", second)
	}
	reset := AppendCoreLogs(second, update(t, `{"reset":true,"messages":[{"level":"ERROR","message":"replacement"}]}`), next)
	if !reflect.DeepEqual(reset, []Entry{{ID: 3, Level: 2, Text: "replacement"}}) {
		t.Errorf("reset %+v", reset)
	}
	if got := AppendCoreLogs(reset, update(t, `{"reset":true}`), next); len(got) != 0 {
		t.Errorf("empty reset %+v", got)
	}
}

func TestCoreLogBoundsAndPlain(t *testing.T) {
	id := 0
	var u api.CoreLogUpdate
	for i := range 3100 {
		u.Messages = append(u.Messages, api.CoreLogMessage{Level: api.CoreLogLevel{Name: "DEBUG"}, Message: strconv.Itoa(i)})
	}
	entries := AppendCoreLogs(nil, u, func() int { id++; return id })
	if len(entries) != 3000 || entries[0].Text != "100" {
		t.Errorf("len %d first %q", len(entries), entries[0].Text)
	}
	if got := Plain("\x1b[31mERROR\x1b[0m line\nnext"); got != "ERROR line\nnext" {
		t.Errorf("plain %q", got)
	}
}

func TestApplicationLogs(t *testing.T) {
	for text, want := range map[string]bool{
		"[kiko] 配置应用完成": true, "2026-10-21T12:34:56+08:00 [kiko] 配置应用完成": true, "[usbip] USB 服务已启动": true,
		"2026-10-21T12:34:56+08:00 INFO[0000] inbound started": false, "FATAL[0000] sing-box startup failed": false,
	} {
		if got := IsApplicationLog(text, false); got != want {
			t.Errorf("IsApplicationLog(%q) = %v", text, got)
		}
	}
	if !IsApplicationLog("正在生成配置", true) {
		t.Error("local diagnostics excluded")
	}
	if appLogLevel("[kiko] warn: x") != 3 || appLogLevel("[kiko] hello") != 4 {
		t.Error("app levels")
	}
}
