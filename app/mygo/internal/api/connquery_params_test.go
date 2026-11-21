package api

import (
	"strings"
	"testing"
)

func TestConnectionQueryHideInboundParams(t *testing.T) {
	p := ConnectionQuery{State: "active", HideInbounds: []string{"favicon"}}.Params()
	if !strings.Contains(p, "hideInbound=favicon") {
		t.Fatalf("params %q", p)
	}
}
