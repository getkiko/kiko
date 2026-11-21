// Package favicon holds the websites' icons Connections and Usage show
// beside domains: fetched by the server through the outbound Settings
// names, kept in memory here, on disk there.
package favicon

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const (
	// memoryEntries bounds the icons held; past it the least recently
	// shown quarter goes.
	memoryEntries = 512
	// fetchers bounds the requests to the server at once; queued, the
	// requests started and waiting for one.
	fetchers = 4
	queued   = 8
	// fetchTimeout bounds a request: the server may wait for a fetcher and
	// the outbound's proxy, then fetch the site.
	fetchTimeout = 50 * time.Second
	// missRetry is how long a site without an icon, or out of reach (the
	// server keeps its mark as long), is not asked again; downRetry, while
	// the outbound cannot fetch; failRetry, after another failed request.
	missRetry = 3 * time.Hour
	downRetry = 5 * time.Minute
	failRetry = time.Minute
)

type entry struct {
	bitmap  *ui.Bitmap
	pending bool
	retry   time.Time
	used    uint64
}

// Store holds the website icons of one console's server, by root domain.
// Bitmap and View run on the UI thread; requests run on goroutines and
// post their results.
type Store struct {
	env     *env.Env
	entries map[string]*entry
	tick    uint64
	slots   chan struct{}
	pending int
	// roots memoizes Root of the domains shown.
	roots map[string]string
	now   func() time.Time

	// The outbound's tag in the running core, for the document version.
	version int
	tag     string
}

var stores sync.Map // *env.Env → *Store

// For returns the store of e's console, creating it on first use.
func For(e *env.Env) *Store {
	if s, ok := stores.Load(e); ok {
		return s.(*Store)
	}
	s, _ := stores.LoadOrStore(e, &Store{env: e, entries: map[string]*entry{}, slots: make(chan struct{}, fetchers), now: time.Now, version: -1})
	return s.(*Store)
}

// Root is the registrable root of domain, whose icon it shows, as the
// server works it out: "" for an IP address, a local name or one without
// a public suffix, which have none.
func Root(domain string) string {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if ascii, err := idna.Lookup.ToASCII(domain); err == nil {
		domain = ascii
	}
	if domain == "" || len(domain) > 253 || !strings.Contains(domain, ".") || strings.Trim(domain, "0123456789.") == "" {
		return ""
	}
	for _, r := range domain {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return ""
		}
	}
	root, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return ""
	}
	if _, icann := publicsuffix.PublicSuffix(root); !icann || strings.HasSuffix(root, ".local") || strings.HasSuffix(root, ".localhost") {
		return ""
	}
	return root
}

// root is Root of domain, worked out once. UI thread.
func (s *Store) root(domain string) string {
	if r, ok := s.roots[domain]; ok {
		return r
	}
	if s.roots == nil || len(s.roots) >= 4096 {
		s.roots = map[string]string{}
	}
	r := Root(domain)
	s.roots[domain] = r
	return r
}

// Outbound is the tag of Settings' icon outbound in the running core, ""
// while icons are off or it is not running: disabled or gone.
func Outbound(doc *model.Doc) string {
	if doc == nil || !doc.FaviconOutbound.Valid() {
		return ""
	}
	return emit.EmittedTags(doc).Outbounds[doc.FaviconOutbound.ID()]
}

// outbound is Outbound for the store's document, worked out again as it
// changes.
func (s *Store) outbound(doc *model.Doc, version int) string {
	if version != s.version {
		tag := Outbound(doc)
		if tag != s.tag {
			// Another outbound may reach the sites the last one did not.
			for k, e := range s.entries {
				if e.bitmap == nil && !e.pending {
					delete(s.entries, k)
				}
			}
		}
		s.version, s.tag = version, tag
	}
	return s.tag
}

// Bitmap returns domain's icon, starting its request when it is not
// known; nil while icons are off, while it loads, or when the site has
// none. UI thread.
func (s *Store) Bitmap(domain string, doc *model.Doc, version int) *ui.Bitmap {
	root := s.root(domain)
	outbound := s.outbound(doc, version)
	if root == "" || outbound == "" || !s.env.Server.Runtime.Running() {
		// The core fetches nothing while it is stopped.
		return nil
	}
	s.tick++
	e := s.entries[root]
	if e != nil {
		e.used = s.tick
		if e.bitmap != nil || e.pending || s.now().Before(e.retry) {
			return e.bitmap
		}
	}
	if s.pending >= queued {
		// Asked again as a request ends and the page redraws.
		return nil
	}
	if e == nil {
		s.trim()
		e = &entry{used: s.tick}
		s.entries[root] = e
	}
	e.pending = true
	s.pending++
	go s.load(root, outbound, e)
	return nil
}

// On reports whether website icons show: an outbound is set and runs.
// UI thread.
func (s *Store) On(doc *model.Doc, version int) bool {
	return s.outbound(doc, version) != "" && s.env.Server.Runtime.Running()
}

func (s *Store) load(root, outbound string, e *entry) {
	s.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	data, err := s.env.API.Favicon(ctx, root, outbound)
	cancel()
	<-s.slots
	var bitmap *ui.Bitmap
	retry := failRetry
	if err == nil {
		bitmap, err = ui.DecodeBitmap(data)
	}
	// A site without an icon, or a name the server takes for none, waits
	// longer than a failure to reach it.
	var apiErr *api.Error
	switch {
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusBadGateway):
		retry = missRetry
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusServiceUnavailable:
		retry = downRetry
	}
	s.env.Post(func() {
		s.pending--
		e.pending = false
		if bitmap != nil {
			e.bitmap = bitmap
			return
		}
		if outbound != s.tag {
			// Asked through an outbound since replaced: the new one asks
			// afresh.
			if s.entries[root] == e {
				delete(s.entries, root)
			}
			return
		}
		e.retry = s.now().Add(retry)
	})
}

// trim drops the least recently shown quarter past memoryEntries. UI
// thread.
func (s *Store) trim() {
	if len(s.entries) < memoryEntries {
		return
	}
	type aged struct {
		key  string
		used uint64
	}
	var all []aged
	for k, e := range s.entries {
		if !e.pending {
			all = append(all, aged{k, e.used})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used < all[j].used })
	for _, a := range all[:len(all)/4] {
		delete(s.entries, a.key)
	}
}

// Clear forgets every icon, as the server's cache is cleared. UI thread.
func (s *Store) Clear() {
	for k, e := range s.entries {
		if !e.pending {
			delete(s.entries, k)
		}
	}
}
