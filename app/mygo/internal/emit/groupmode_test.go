package emit_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func emittedOutbound(t *testing.T, r emit.Result, tag string) map[string]any {
	t.Helper()
	for _, o := range r.Config["outbounds"].([]any) {
		if m := o.(map[string]any); m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("outbound %q not emitted:\n%s", tag, r.JSON)
	return nil
}

// Every mode emits its members and only its own fields; a mode switched
// from another keeps no field of it.
func TestEmitGroupModes(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds, &model.Outbound{Entity: model.Entity{ID: "second", Tag: "备用", Enabled: true}, Kind: "outbound", Type: "trojan",
		Fields: model.Fields{"server": "backup.example.com", "server_port": float64(443), "password": "p", "tls.enabled": true}})
	cases := []struct {
		mode string
		want map[string]any
	}{
		{model.ModeFallback, map[string]any{"type": "fallback", "url": "https://www.gstatic.com/generate_204", "interval": "3m", "timeout": "5s"}},
		{model.ModeLoadBalance, map[string]any{"type": "loadbalance", "strategy": "round_robin", "url": "https://www.gstatic.com/generate_204", "interval": "3m", "timeout": "5s"}},
		{model.ModeSmart, map[string]any{"type": "smart", "url": "https://www.gstatic.com/generate_204", "interval": "3m", "timeout": "5s"}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			group := model.MakeOutbound(model.ModeFastest, "组")
			group.Members = []string{"proxy", "second"}
			group.Fields["tolerance"] = float64(80)
			model.SwitchGroupMode(group, c.mode)
			model.PruneGroupFields(group)
			doc := model.Clone(d)
			doc.Outbounds = append(doc.Outbounds, group)
			r := emit.Emit(doc, emit.Options{})
			got := emittedOutbound(t, r, "组")
			if !reflect.DeepEqual(got["outbounds"], []any{"代理", "备用"}) {
				t.Fatalf("members %v", got["outbounds"])
			}
			if _, kept := got["tolerance"]; kept {
				t.Fatalf("urltest's tolerance kept: %v", got)
			}
			for key, value := range c.want {
				if fmt.Sprint(got[key]) != fmt.Sprint(value) {
					t.Fatalf("%s = %v, want %v in %v", key, got[key], value, got)
				}
			}
		})
	}
}

func TestEmitFallbackOptions(t *testing.T) {
	d := doc()
	group := model.MakeOutbound(model.ModeFallback, "故障转移")
	group.Members = []string{"proxy"}
	group.Fields["no_return"] = true
	group.Fields["max_failures"] = float64(5)
	d.Outbounds = append(d.Outbounds, group)
	got := emittedOutbound(t, emit.Emit(d, emit.Options{}), "故障转移")
	if got["no_return"] != true || fmt.Sprint(got["max_failures"]) != "5" {
		t.Fatalf("outbound %v", got)
	}
}
