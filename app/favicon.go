package main

// Website icons for Connections and Usage: GET /api/v1/favicon gives a
// domain's icon as a PNG at most faviconSide pixels on a side, fetched
// from the site itself through the outbound the request names and kept on
// disk for faviconFresh. Domains share their registrable root's icon, so
// api.github.com and gist.github.com take github.com's.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	// faviconFresh is how long a fetched icon is served before it is
	// fetched again; an older one is still served while it is.
	faviconFresh = 7 * 24 * time.Hour
	// faviconMissFor is how long a site that gave no icon is not asked
	// again.
	faviconMissFor = 24 * time.Hour
	// The cache's bounds: past either the oldest icons go.
	faviconMaxEntries = 2000
	faviconMaxBytes   = 64 << 20
	// faviconWaitFor is how long a site an outbound could not reach, or
	// that answered with an error, is not asked again through it.
	faviconWaitFor = 15 * time.Minute
	// faviconFetchers bounds the sites fetched at once; faviconRefreshers,
	// the stale icons fetched again at once, among them.
	faviconFetchers   = 4
	faviconRefreshers = 2
	// faviconQueueTimeout bounds the wait for a fetcher and for the
	// outbound's proxy to start.
	faviconQueueTimeout = 30 * time.Second
	// faviconTimeout bounds a domain's fetch, all its candidates.
	faviconTimeout = 12 * time.Second
	// faviconBodyLimit bounds a page or an icon read.
	faviconBodyLimit = 1 << 20
	// faviconProxyIdle is how long an outbound's proxy stays without a
	// fetch before it closes.
	faviconProxyIdle = 5 * time.Minute
	// faviconInbound is the inbound tag the fetches show as in Connections.
	faviconInbound = "favicon"
	// faviconUserAgent is a browser's, as some sites refuse other clients.
	faviconUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// errNoFavicon is a site without an icon Kiko can show: it answered, but
// gave none. Only this is remembered as a miss.
var errNoFavicon = errors.New("no icon")

// errUnreachable is a site the outbound did not reach: no answer about
// its icon, so nothing is remembered.
var errUnreachable = errors.New("site unreachable")

// faviconProxyError is the outbound's proxy failing to start: no answer
// about the site, so nothing is cached.
type faviconProxyError struct{ err error }

func (e faviconProxyError) Error() string { return e.err.Error() }

// favicons are the server's website icons: the cache on disk, the fetches
// under way and the outbounds' proxies.
type favicons struct {
	s   *server
	dir string

	mu       sync.Mutex
	inflight map[string]*faviconCall
	proxies  map[string]*faviconProxy
	reaping  bool
	closed   bool
	slots    chan struct{}
	// refreshing holds the stale icons fetched again; trimmed is when the
	// cache was last trimmed.
	refreshing chan struct{}
	trimmed    time.Time

	// client, when set, stands in for the outbound's HTTP client (tests).
	client func(ctx context.Context, outbound string) (*http.Client, error)
	// now stands in for the clock (tests).
	now func() time.Time
}

// faviconCall is one fetch of a root, which other requests for it wait on.
type faviconCall struct {
	done chan struct{}
	png  []byte
	err  error
}

// faviconProxy is the core's proxy through an outbound for the fetches.
type faviconProxy struct {
	ready chan struct{}
	// client sends its requests through the proxy, one transport for all
	// the outbound's fetches; err is why the proxy did not start.
	client *http.Client
	err    error
	cancel context.CancelFunc
	last   time.Time
	// down tells the proxy failed since it first listened.
	down atomic.Bool
}

// end stops the proxy and drops the client's idle connections.
func (p *faviconProxy) end() {
	p.cancel()
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
}

func newFavicons(s *server, dir string) *favicons {
	return &favicons{s: s, dir: dir, inflight: map[string]*faviconCall{}, proxies: map[string]*faviconProxy{},
		slots: make(chan struct{}, faviconFetchers), refreshing: make(chan struct{}, faviconRefreshers), now: time.Now}
}

// favicons returns the server's website icons, made on first use.
func (s *server) faviconService() *favicons {
	s.faviconOnce.Do(func() { s.favicons = newFavicons(s, filepath.Join(s.dataDir, "favicons")) })
	return s.favicons
}

// faviconRoot is the registrable root of domain, whose icon it shows:
// lower case, without a trailing dot. IP addresses, single labels and
// local names have none.
func faviconRoot(domain string) (string, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = host
	}
	invalid := errors.New("invalid domain")
	if domain == "" || len(domain) > 253 || net.ParseIP(strings.Trim(domain, "[]")) != nil || !strings.Contains(domain, ".") {
		return "", invalid
	}
	for _, r := range domain {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", invalid
		}
	}
	root, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return "", invalid
	}
	if !listedSuffix(root) || strings.HasSuffix(root, ".local") || strings.HasSuffix(root, ".localhost") {
		return "", invalid
	}
	return root, nil
}

// listedSuffix reports whether domain's public suffix is one the list
// names, ICANN's (com, co.uk) or a site host's (github.io, pages.dev),
// rather than an unknown top level name taken for one (lan, internal).
func listedSuffix(domain string) bool {
	suffix, icann := publicsuffix.PublicSuffix(domain)
	return icann || strings.Contains(suffix, ".")
}

func (f *favicons) file(root, ext string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(f.dir, hex.EncodeToString(sum[:16])+ext)
}

// handleFavicon is GET /api/v1/favicon?domain=&outbound=: the icon of
// domain's root, fetched through outbound, the tag in the running core;
// 404 for a site without one, 503 while the outbound cannot fetch.
func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	root, err := faviconRoot(r.URL.Query().Get("domain"))
	outbound := strings.TrimSpace(r.URL.Query().Get("outbound"))
	if err != nil || outbound == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要域名和出站"})
		return
	}
	icon, err := s.faviconService().icon(r.Context(), root, outbound)
	var proxyErr faviconProxyError
	switch {
	case errors.As(err, &proxyErr):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": proxyErr.Error()})
	case errors.Is(err, errNoFavicon):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "该网站没有图标"})
	case err != nil:
		// Unreachable, timed out or cancelled: ask again later.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "暂时无法获取网站图标"})
	default:
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(icon)
	}
}

// icon is root's icon: from the cache while fresh, else fetched through
// outbound. A stale icon is served at once and fetched again behind it,
// a few at a time. A site that gave none through outbound is not asked
// again through it for faviconMissFor, one it could not reach for
// faviconWaitFor.
func (f *favicons) icon(ctx context.Context, root, outbound string) ([]byte, error) {
	now := f.now()
	if info, err := os.Stat(f.file(root, ".png")); err == nil {
		if data, err := os.ReadFile(f.file(root, ".png")); err == nil {
			if now.Sub(info.ModTime()) >= faviconFresh {
				f.refresh(root, outbound)
			}
			return data, nil
		}
	}
	mark := f.mark(root, outbound)
	if info, err := os.Stat(mark + ".miss"); err == nil && now.Sub(info.ModTime()) < faviconMissFor {
		return nil, errNoFavicon
	}
	if info, err := os.Stat(mark + ".wait"); err == nil && now.Sub(info.ModTime()) < faviconWaitFor {
		return nil, errUnreachable
	}
	return f.fetch(ctx, root, outbound)
}

// refresh fetches a stale icon again in the background, when one of the
// few refreshes allowed at once is free; until it replaces the icon, the
// icon is not refreshed again sooner than a missing one is asked.
func (f *favicons) refresh(root, outbound string) {
	select {
	case f.refreshing <- struct{}{}:
	default:
		return
	}
	retry := f.now().Add(faviconMissFor - faviconFresh)
	_ = os.Chtimes(f.file(root, ".png"), retry, retry)
	go func() {
		defer func() { <-f.refreshing }()
		_, _ = f.fetch(context.Background(), root, outbound)
	}()
}

// mark is the path, less its extension, of what is known of root through
// outbound: another outbound may reach the site.
func (f *favicons) mark(root, outbound string) string {
	path := f.file(root+"\x00"+outbound, "")
	return path
}

// fetch fetches root's icon through outbound once at a time, keeps it or
// marks the site, and returns it. The site's own time, faviconTimeout,
// starts once a fetcher is free and the outbound's proxy runs.
func (f *favicons) fetch(ctx context.Context, root, outbound string) ([]byte, error) {
	key := root + "\x00" + outbound
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, faviconProxyError{errors.New("服务正在退出")}
	}
	if call := f.inflight[key]; call != nil {
		f.mu.Unlock()
		select {
		case <-call.done:
			return call.png, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &faviconCall{done: make(chan struct{})}
	f.inflight[key] = call
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.inflight, key)
		f.mu.Unlock()
		close(call.done)
	}()

	// The fetch runs to its own end: requests waiting on it may outlive
	// the one that started it.
	queue, stop := context.WithTimeout(context.Background(), faviconQueueTimeout)
	defer stop()
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	case <-queue.Done():
		call.err = queue.Err()
		return nil, call.err
	}
	client, err := f.httpClient(queue, outbound)
	if err != nil {
		call.err = faviconProxyError{err}
		return nil, call.err
	}
	fetchCtx, cancel := context.WithTimeout(context.Background(), faviconTimeout)
	defer cancel()
	call.png, call.err = fetchFavicon(fetchCtx, client, root)
	mark := f.mark(root, outbound)
	var proxyErr faviconProxyError
	switch {
	case errors.As(call.err, &proxyErr):
		// The core's proxy failed, not the site: rebuild the proxy.
		f.proxyDown(outbound)
	case call.err == nil:
		f.keep(root, call.png)
		_ = os.Remove(mark + ".miss")
		_ = os.Remove(mark + ".wait")
	case errors.Is(call.err, errNoFavicon):
		f.touch(mark + ".miss")
	default:
		f.touch(mark + ".wait")
	}
	return call.png, call.err
}

// keep writes root's icon and, now and then, trims the cache to its
// bounds.
func (f *favicons) keep(root string, png []byte) {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return
	}
	if err := atomicWrite(f.file(root, ".png"), png, 0o600); err != nil {
		log.Printf("[favicon] keep %s: %v", root, err)
		return
	}
	f.mu.Lock()
	due := f.now().Sub(f.trimmed) >= time.Minute
	if due {
		f.trimmed = f.now()
	}
	f.mu.Unlock()
	if due {
		f.trim()
	}
}

// touch writes an empty mark.
func (f *favicons) touch(path string) {
	if err := os.MkdirAll(f.dir, 0o700); err == nil {
		_ = os.WriteFile(path, nil, 0o600)
	}
}

// faviconFile is a file of the cache.
type faviconFile struct {
	path string
	size int64
	mod  time.Time
}

func (f *favicons) files() []faviconFile {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil
	}
	var out []faviconFile
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || ext != ".png" && ext != ".miss" && ext != ".wait" {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, faviconFile{filepath.Join(f.dir, e.Name()), info.Size(), info.ModTime()})
		}
	}
	return out
}

// trim removes the oldest icons past the cache's bounds.
func (f *favicons) trim() {
	files := f.files()
	var total int64
	for _, file := range files {
		total += file.size
	}
	if len(files) <= faviconMaxEntries && total <= faviconMaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, file := range files {
		if len(files) <= faviconMaxEntries && total <= faviconMaxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
			files = files[1:]
		}
	}
}

// faviconCacheStats is GET /api/v1/favicons/cache, and what DELETE cleared.
type faviconCacheStats struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// stats counts the icons kept, not the sites marked without one.
func (f *favicons) stats() faviconCacheStats {
	var out faviconCacheStats
	for _, file := range f.files() {
		if filepath.Ext(file.path) == ".png" {
			out.Count++
		}
		out.Bytes += file.size
	}
	return out
}

// clear removes every icon and mark.
func (f *favicons) clear() faviconCacheStats {
	var out faviconCacheStats
	for _, file := range f.files() {
		if os.Remove(file.path) == nil {
			if filepath.Ext(file.path) == ".png" {
				out.Count++
			}
			out.Bytes += file.size
		}
	}
	return out
}

func (s *server) handleFaviconCache(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.faviconService().stats())
}

func (s *server) handleFaviconCacheClear(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.faviconService().clear())
}

// httpClient is a client whose requests go through the core's proxy for
// outbound, started on first use and closed after faviconProxyIdle.
func (f *favicons) httpClient(ctx context.Context, outbound string) (*http.Client, error) {
	if f.client != nil {
		return f.client(ctx, outbound)
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, errors.New("服务正在退出")
	}
	p := f.proxies[outbound]
	if p != nil && p.down.Load() {
		// Its port may not come back, as when the core restarted on
		// another configuration: start anew.
		p.end()
		delete(f.proxies, outbound)
		p = nil
	}
	if p == nil {
		proxyCtx, cancel := context.WithCancel(context.Background())
		p = &faviconProxy{ready: make(chan struct{}), cancel: cancel}
		f.proxies[outbound] = p
		go func() {
			wait, stop := context.WithTimeout(context.Background(), browserReadyTimeout)
			defer stop()
			address, err := f.s.startOutboundProxy(proxyCtx, wait, outbound, faviconInbound, func(up bool) { p.down.Store(!up) })
			f.mu.Lock()
			if err != nil {
				p.err = err
				cancel()
				if f.proxies[outbound] == p {
					delete(f.proxies, outbound)
				}
			} else {
				p.client = faviconClient(address)
			}
			f.mu.Unlock()
			close(p.ready)
		}()
		if !f.reaping {
			f.reaping = true
			go f.reap()
		}
	}
	p.last = f.now()
	f.mu.Unlock()
	select {
	case <-p.ready:
		return p.client, p.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// proxyDown marks outbound's proxy as failed, to start anew.
func (f *favicons) proxyDown(outbound string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.proxies[outbound]; p != nil {
		p.down.Store(true)
	}
}

// faviconClient sends its requests through the HTTP proxy at address.
func faviconClient(address string) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(&url.URL{Scheme: "http", Host: address}),
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   faviconTimeout,
		ResponseHeaderTimeout: faviconTimeout,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: transport, CheckRedirect: faviconRedirect}
}

// reap closes the proxies idle for faviconProxyIdle, until none is left.
func (f *favicons) reap() {
	for {
		time.Sleep(time.Minute)
		f.mu.Lock()
		for tag, p := range f.proxies {
			if f.now().Sub(p.last) >= faviconProxyIdle {
				p.end()
				delete(f.proxies, tag)
			}
		}
		if len(f.proxies) == 0 || f.closed {
			f.reaping = false
			f.mu.Unlock()
			return
		}
		f.mu.Unlock()
	}
}

// close ends the proxies, as the server exits.
func (f *favicons) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for tag, p := range f.proxies {
		p.end()
		delete(f.proxies, tag)
	}
}

// faviconPublic reports whether u is a page of a public website the
// fetches may ask: http or https on its default port, at a domain with a
// public suffix, never an IP address or a local name, so neither a page's
// links nor its redirects reach this host or its network.
func faviconPublic(u *url.URL) bool {
	if u == nil || u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" && port != "443" && port != "80" {
		return false
	}
	_, err := faviconRoot(u.Hostname())
	return err == nil
}

// faviconRedirect follows at most five redirects, to public pages only.
func faviconRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if !faviconPublic(req.URL) {
		return fmt.Errorf("redirect to %s", req.URL.Redacted())
	}
	return nil
}

// faviconAnswer is a site's answer that gave no icon, as a 404: unlike a
// failure to reach it, it tells the site has none there.
type faviconAnswer struct{ err error }

func (e faviconAnswer) Error() string { return e.err.Error() }

// faviconGet reads target, at most faviconBodyLimit, and the URL it ended
// at after redirects. An answer without the resource is a faviconAnswer.
func faviconGet(ctx context.Context, client *http.Client, target, accept string) ([]byte, *url.URL, error) {
	return faviconRead(ctx, client, target, accept, false)
}

// faviconPage is faviconGet for a page: its first faviconBodyLimit bytes,
// which hold its head, however long it is.
func faviconPage(ctx context.Context, client *http.Client, target string) ([]byte, *url.URL, error) {
	return faviconRead(ctx, client, target, "text/html,application/xhtml+xml", true)
}

func faviconRead(ctx context.Context, client *http.Client, target, accept string, truncate bool) ([]byte, *url.URL, error) {
	u, err := url.Parse(target)
	if err != nil || !faviconPublic(u) {
		return nil, nil, faviconAnswer{fmt.Errorf("not a public page: %s", target)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", faviconUserAgent)
	req.Header.Set("Accept", accept)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, nil, faviconAnswer{fmt.Errorf("%s: %s", target, resp.Status)}
	case resp.StatusCode != http.StatusOK:
		// Refused, limited or failing: no answer about the icon.
		return nil, nil, fmt.Errorf("%s: %s", target, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, faviconBodyLimit+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > faviconBodyLimit {
		if !truncate {
			return nil, nil, faviconAnswer{fmt.Errorf("%s: too large", target)}
		}
		data = data[:faviconBodyLimit]
	}
	return data, resp.Request.URL, nil
}

// fetchFavicon finds root's icon on the site: the icons its home page
// links, best first, then /favicon.ico, at the root and then at www. A
// site that answered without a usable icon gives errNoFavicon; one never
// reached, errUnreachable.
func fetchFavicon(ctx context.Context, client *http.Client, root string) ([]byte, error) {
	// answered tells an icon candidate's own answer, not the page's: a
	// page that loads while its icons fail is no answer about them.
	var last error = errNoFavicon
	answered := false
	note := func(err error) {
		var answer faviconAnswer
		if errors.As(err, &answer) {
			answered = true
		}
		last = err
	}
	proxyDown := func(err error) bool {
		// The core's proxy itself refused: nothing about the site.
		return err != nil && strings.Contains(err.Error(), "proxyconnect")
	}
	visited := map[string]bool{}
	for _, host := range []string{root, "www." + root} {
		if visited[host] {
			// The root's page already led there.
			continue
		}
		var candidates []string
		page, final, err := faviconPage(ctx, client, "https://"+host+"/")
		switch {
		case err == nil:
			candidates = faviconLinks(page, final)
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case proxyDown(err):
			return nil, faviconProxyError{err}
		default:
			last = err
		}
		base := "https://" + host
		if final != nil {
			base = final.Scheme + "://" + final.Host
			visited[final.Hostname()] = true
		}
		candidates = append(candidates, base+"/favicon.ico")
		for _, candidate := range candidates {
			data, err := faviconData(ctx, client, candidate)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if proxyDown(err) {
					return nil, faviconProxyError{err}
				}
				note(err)
				continue
			}
			answered = true
			icon, err := faviconPNG(data)
			if err == nil {
				return icon, nil
			}
			last = err
		}
	}
	if !answered {
		return nil, fmt.Errorf("%w: %v", errUnreachable, last)
	}
	return nil, fmt.Errorf("%w: %v", errNoFavicon, last)
}

// faviconData is the bytes of an icon candidate: a data URL's or the
// resource's.
func faviconData(ctx context.Context, client *http.Client, candidate string) ([]byte, error) {
	if strings.HasPrefix(candidate, "data:") {
		return decodeDataURL(candidate)
	}
	data, _, err := faviconGet(ctx, client, candidate, "image/*")
	return data, err
}
