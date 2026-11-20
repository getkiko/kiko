package favicon

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// View shows domain's website icon, size DIPs square with rounded
// corners, or the globe domains show while there is none: icons off, the
// icon loading, a site without one. UI thread.
func View(c *ui.Context, s *Store, domain string, doc *model.Doc, version int, size float32) ui.Element {
	if s != nil {
		if bitmap := s.Bitmap(domain, doc, version); bitmap != nil {
			return ui.Image(c, bitmap).Size(size, size).Radius(size * 0.22).Shrink(0)
		}
	}
	return Placeholder(c, size)
}

// Placeholder is the globe a domain shows without an icon.
func Placeholder(c *ui.Context, size float32) ui.Element {
	k := kit.T(c)
	box := ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(k.Label3)
	box.Children(func() { kit.Icon(c, kit.SymGlobe, size*0.85) })
	return box
}

// Site is View for a console's store of the document: e's icons, the
// outbound of st's document; the globe without a document.
func Site(c *ui.Context, e *env.Env, st *store.Store, domain string, size float32) ui.Element {
	if st == nil || e == nil {
		return Placeholder(c, size)
	}
	return View(c, For(e), domain, st.Doc(), st.Version(), size)
}

// On reports whether website icons show for st's document: an outbound
// is set and runs. UI thread.
func On(e *env.Env, st *store.Store) bool {
	return st != nil && e != nil && For(e).On(st.Doc(), st.Version())
}
