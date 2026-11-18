package routes

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A group target follows the core's choices, else a selector's default
// or first member; a urltest without a choice ends the chain.
func TestOutboundChain(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	us01, us02 := model.MakeOutbound("direct", "US01"), model.MakeOutbound("direct", "US02")
	auto := model.MakeOutbound("urltest", "美国节点")
	auto.Members = []string{us01.ID, us02.ID}
	pick := model.MakeOutbound("selector", "自选")
	pick.Members = []string{auto.ID, us02.ID}
	doc.Outbounds = append(doc.Outbounds, us01, us02, auto, pick)
	byTag := tagIndex(doc)
	if got := outboundChain(doc, pick.ID, nil, byTag); got != "自选 → 美国节点" {
		t.Fatalf("stopped core: %q", got)
	}
	now := map[string]string{"自选": "美国节点", "美国节点": "US01"}
	if got := outboundChain(doc, pick.ID, now, byTag); got != "自选 → 美国节点 → US01" {
		t.Fatalf("running core: %q", got)
	}
	if pick.Fields == nil {
		pick.Fields = model.Fields{}
	}
	pick.Fields["default_ref"] = us02.ID
	if got := outboundChain(doc, pick.ID, nil, byTag); got != "自选 → US02" {
		t.Fatalf("default: %q", got)
	}
	if got := outboundChain(doc, us01.ID, now, byTag); got != "US01" {
		t.Fatalf("plain: %q", got)
	}
}
