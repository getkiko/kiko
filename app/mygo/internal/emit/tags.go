package emit

import (
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Tags are the tags Emit gives a document's enabled entities, by entity
// ID: empty tags fall back to the type ("<type>-in" for inbounds), tags
// are trimmed and duplicates are renamed "tag 2", "tag 3", ….
type Tags struct {
	// Outbounds holds outbounds and endpoints, which share one namespace.
	Outbounds map[string]string
	// Inbounds holds inbounds.
	Inbounds map[string]string
}

// EmittedTags returns the tags Emit gives doc's enabled outbounds,
// endpoints and inbounds, with exactly the naming Emit uses.
func EmittedTags(doc *model.Doc) Tags {
	out, _ := outboundTags(enabledOutbounds(doc), enabledEndpoints(doc))
	in, _ := inboundTags(enabledInbounds(doc))
	return Tags{Outbounds: out, Inbounds: in}
}

// enabledOutbounds are the enabled outbounds of enabled subscriptions (or
// none), in emitted order.
func enabledOutbounds(doc *model.Doc) []*model.Outbound {
	enabledSubscriptions := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			enabledSubscriptions[s.ID] = true
		}
	}
	var out []*model.Outbound
	for _, o := range model.OrderedOutbounds(doc) {
		if o.Enabled && (o.SubID == "" || enabledSubscriptions[o.SubID]) {
			out = append(out, o)
		}
	}
	return out
}

func enabledEndpoints(doc *model.Doc) []*model.Endpoint {
	var out []*model.Endpoint
	for _, ep := range doc.Endpoints {
		if ep.Enabled {
			out = append(out, ep)
		}
	}
	return out
}

func enabledInbounds(doc *model.Doc) []*model.Inbound {
	var out []*model.Inbound
	for _, in := range doc.Inbounds {
		if in.Enabled {
			out = append(out, in)
		}
	}
	return out
}

// tagNamer hands out unique tags in claim order.
type tagNamer struct {
	used orderedSet
}

// claim returns the unique tag of want (trimmed, or fallback when empty)
// and whether it had to be renamed.
func (n *tagNamer) claim(want, fallback string) (string, bool) {
	t := strings.TrimSpace(want)
	if t == "" {
		t = fallback
	}
	renamed := false
	if n.used.has(t) {
		i := 2
		for n.used.has(t + " " + strconv.Itoa(i)) {
			i++
		}
		t = t + " " + strconv.Itoa(i)
		renamed = true
	}
	n.used.add(t)
	return t, renamed
}

// outboundTags names outbounds, then endpoints, in one namespace. renamed
// lists the tags that were renamed, in claim order.
func outboundTags(outbounds []*model.Outbound, endpoints []*model.Endpoint) (tags map[string]string, renamed []string) {
	tags = map[string]string{}
	var n tagNamer
	for _, o := range outbounds {
		t, r := n.claim(o.Tag, o.Type)
		if r {
			renamed = append(renamed, t)
		}
		tags[o.ID] = t
	}
	for _, ep := range endpoints {
		t, r := n.claim(ep.Tag, ep.Type)
		if r {
			renamed = append(renamed, t)
		}
		tags[ep.ID] = t
	}
	return tags, renamed
}

// inboundTags names inbounds; renamed lists the renamed tags in order.
func inboundTags(inbounds []*model.Inbound) (tags map[string]string, renamed []string) {
	tags = map[string]string{}
	var n tagNamer
	for _, ib := range inbounds {
		t, r := n.claim(ib.Tag, ib.Type+"-in")
		if r {
			renamed = append(renamed, t)
		}
		tags[ib.ID] = t
	}
	return tags, renamed
}
