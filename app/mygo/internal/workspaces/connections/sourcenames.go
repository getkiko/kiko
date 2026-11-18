package connections

import (
	"fmt"
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/srcname"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// optionCount is the count an option shows, "" for none.
func optionCount(n int) string {
	if n < 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// sourceNamer names source addresses from the route devices of the
// current document and the host's addresses, for the filters, lists and
// inspectors of the three monitors. The names are rebuilt when the
// document or the host's addresses change, so a renamed device shows at
// once, and are otherwise kept.
type sourceNamer struct {
	store   *store.Store
	names   *srcname.Names
	version int
	local   []string
}

// get is the current names; nil, which names nothing, without a store.
func (n *sourceNamer) get() *srcname.Names {
	if n == nil || n.store == nil {
		return nil
	}
	s := n.store
	if n.names == nil || n.version != s.Version() || !slices.Equal(n.local, s.LocalAddresses) {
		n.names = srcname.FromDoc(s.Doc(), s.LocalAddresses)
		n.version = s.Version()
		n.local = slices.Clone(s.LocalAddresses)
	}
	return n.names
}

// sourceNames names the source addresses of the Network Connections monitor.
func (m *connMonitor) sourceNames() *srcname.Names { return m.names.get() }

// sourceName is the name of the address of source, an address with or
// without its port, "" for none.
func sourceName(names *srcname.Names, source string) string {
	if source == "" {
		return ""
	}
	return names.Name(sourceHost(source))
}
