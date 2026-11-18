package model

import "testing"

func TestTailscaleLimit(t *testing.T) {
	doc := FillDefaults(EmptyDoc(PlatformLinux))
	doc.Endpoints = nil
	if CanAddEndpoint(doc, "tailscale") != "" || TailscaleLimitError(doc) != "" {
		t.Fatal("an empty document takes a Tailscale endpoint")
	}
	first := MakeEndpoint("tailscale", "ts", false)
	first.Enabled = false
	doc.Endpoints = []*Endpoint{first}
	if CanAddEndpoint(doc, "tailscale") != TailscaleLimitMessage {
		t.Fatal("a disabled Tailscale endpoint still takes the place")
	}
	if CanAddEndpoint(doc, "wireguard") != "" || IsExtraTailscale(doc, first) {
		t.Fatal("other types and the first Tailscale endpoint are fine")
	}
	if HasBlockingReferenceWarnings(doc, nil) {
		t.Fatal("one Tailscale endpoint does not block apply")
	}
	second := MakeEndpoint("tailscale", "ts2", false)
	second.Enabled = false
	doc.Endpoints = append(doc.Endpoints, second)
	if !IsExtraTailscale(doc, second) || !HasInvalidEndpointReferences(doc, second, nil) {
		t.Fatal("the second Tailscale endpoint is flagged")
	}
	if TailscaleLimitError(doc) == "" || !HasBlockingReferenceWarnings(doc, nil) {
		t.Fatal("two Tailscale endpoints block apply")
	}
	second.Enabled = true
	if IsExtraTailscale(doc, second) || !IsExtraTailscale(doc, first) {
		t.Fatal("the first enabled Tailscale endpoint is kept, the disabled one is extra")
	}
}
