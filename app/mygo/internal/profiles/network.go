package profiles

import (
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// networkRefs returns the network-config reference maps of every entity
// that may use the library: the inbounds, outbounds, endpoints, services
// and the DNS servers that dial.
func networkRefs(doc *model.Doc) []model.NetworkConfigRefs {
	var refs []model.NetworkConfigRefs
	for _, e := range doc.Inbounds {
		refs = append(refs, e.NetworkConfigRefs)
	}
	for _, e := range doc.Outbounds {
		refs = append(refs, e.NetworkConfigRefs)
	}
	for _, e := range doc.Endpoints {
		refs = append(refs, e.NetworkConfigRefs)
	}
	for _, e := range doc.Services {
		refs = append(refs, e.NetworkConfigRefs)
	}
	for _, s := range doc.DNS.Servers {
		if !slices.Contains([]string{"hosts", "fakeip", "tailscale"}, s.Type) {
			refs = append(refs, s.NetworkConfigRefs)
		}
	}
	return refs
}
