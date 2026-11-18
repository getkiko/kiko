package model

// The app keeps at most one Tailscale endpoint: its UDP path hook and
// state are process-wide in the core, and the console manages one tailnet
// identity.

// TailscaleTag is the Tailscale endpoint's fixed tag: with one endpoint
// its type names it, and the console shows no name to edit.
const TailscaleTag = "Tailscale"

// TailscaleTagFor is the tag of the document's Tailscale endpoint selfID
// ("" for a new one): TailscaleTag, or a numbered one while another
// entity has that tag.
func TailscaleTagFor(doc *Doc, selfID string) string {
	if TagValidationError(doc, TailscaleTag, selfID) == "" {
		return TailscaleTag
	}
	var taken []string
	for _, in := range doc.Inbounds {
		taken = append(taken, in.Tag)
	}
	for _, o := range doc.Outbounds {
		taken = append(taken, o.Tag)
	}
	for _, e := range doc.Endpoints {
		taken = append(taken, e.Tag)
	}
	for _, s := range doc.DNS.Servers {
		taken = append(taken, s.Tag)
	}
	return UniqueTag(TailscaleTag, taken)
}

// nameTailscale gives the endpoint the document keeps the fixed tag,
// unless another entity has it; references go by ID, so they follow.
func nameTailscale(doc *Doc) {
	kept := keptTailscale(doc)
	if kept == nil || kept.Tag == TailscaleTag || TagValidationError(doc, TailscaleTag, kept.ID) != "" {
		return
	}
	for i, e := range doc.Endpoints {
		if e == kept {
			c := *e
			c.Tag = TailscaleTag
			doc.Endpoints[i] = &c
		}
	}
}

// TailscaleLimitMessage says why a second Tailscale endpoint is refused.
const TailscaleLimitMessage = "已有 Tailscale 端点，只能保留一个"

// TailscaleEndpoint is the document's first Tailscale endpoint, enabled
// or not, or nil.
func TailscaleEndpoint(doc *Doc) *Endpoint {
	for _, e := range doc.Endpoints {
		if e.Type == "tailscale" {
			return e
		}
	}
	return nil
}

// CanAddEndpoint reports why an endpoint of typ cannot be added to doc
// ("" when it can): a document holds one Tailscale endpoint at most.
func CanAddEndpoint(doc *Doc, typ string) string {
	if typ == "tailscale" && TailscaleEndpoint(doc) != nil {
		return TailscaleLimitMessage
	}
	return ""
}

// keptTailscale is the Tailscale endpoint a document over the limit
// keeps: its first enabled one, else its first.
func keptTailscale(doc *Doc) *Endpoint {
	for _, e := range doc.Endpoints {
		if e.Type == "tailscale" && e.Enabled {
			return e
		}
	}
	return TailscaleEndpoint(doc)
}

// IsExtraTailscale reports a Tailscale endpoint other than the one the
// document keeps (see keptTailscale). Disabled ones still count.
func IsExtraTailscale(doc *Doc, endpoint *Endpoint) bool {
	if endpoint.Type != "tailscale" {
		return false
	}
	kept := keptTailscale(doc)
	return kept != nil && kept.ID != endpoint.ID
}

// TailscaleLimitError is the apply-blocking problem of a document with
// more than one Tailscale endpoint, "" otherwise.
func TailscaleLimitError(doc *Doc) string {
	count := 0
	for _, e := range doc.Endpoints {
		if e.Type == "tailscale" {
			count++
		}
	}
	if count > 1 {
		return "只能保留一个 Tailscale 端点，当前有 " + itoa(count) + " 个，请删除多余的端点"
	}
	return ""
}
