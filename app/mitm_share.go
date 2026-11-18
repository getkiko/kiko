package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skip2/go-qrcode"
)

// LAN certificate download page: once explicitly started, it listens on a random port
// at a chosen private address of this machine, under a path with a 128-bit random
// token. It closes immediately on expiry, stop, root certificate change or app exit.
// Only the root certificate (public part) is served, never the private key.

const (
	mitmShareBadPerIP   = 10
	mitmShareBadGlobal  = 30
	mitmShareBadWindow  = time.Minute
	mitmShareBadMaxKeys = 1024
)

var (
	// mitmShareLifetime is how long the download page stays up; tests may shorten it.
	mitmShareLifetime = 10 * time.Minute
	// mitmShareLocalAddresses enumerates candidate private addresses; tests may replace it.
	mitmShareLocalAddresses = mitmLocalShareAddresses
)

// mitmShare holds the current download page session; the zero value is ready to use.
type mitmShare struct {
	mu  sync.Mutex
	cur *mitmShareSession
}

type mitmShareSession struct {
	token       string
	address     string
	port        int
	url         string
	fingerprint string
	expiresAt   time.Time
	downloads   atomic.Int64
	srv         *http.Server
	done        chan struct{}
	timer       *time.Timer
	limiter     mitmShareLimiter
}

type mitmShareResponse struct {
	Active      bool     `json:"active"`
	Addresses   []string `json:"addresses"`
	Address     string   `json:"address,omitempty"`
	URL         string   `json:"url,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	QR          []string `json:"qr,omitempty"`
	Downloads   *int64   `json:"downloads,omitempty"`
}

// mitmShareIfaceSkipped skips loopback, container and virtual bridge interfaces.
func mitmShareIfaceSkipped(name string) bool {
	if name == "lo" {
		return true
	}
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// mitmShareRank returns the sort rank of a candidate address, or -1 if it is not a candidate.
// Order: 192.168/16, 10/8, 172.16/12, IPv6 ULA, and link-local last.
func mitmShareRank(ip net.IP) int {
	if ip.IsLoopback() {
		return -1
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 192 && ip4[1] == 168:
			return 0
		case ip4[0] == 10:
			return 1
		case ip4.IsPrivate():
			return 2
		case ip4.IsLinkLocalUnicast():
			return 4
		}
		return -1
	}
	if ip.IsPrivate() {
		return 3
	}
	return -1
}

type mitmShareIfaceAddr struct {
	iface string
	ip    net.IP
}

// mitmRankShareAddresses filters, deduplicates and sorts addresses by rank (ties sorted by text).
func mitmRankShareAddresses(entries []mitmShareIfaceAddr) []string {
	type ranked struct {
		text string
		rank int
	}
	var list []ranked
	seen := map[string]bool{}
	for _, e := range entries {
		if mitmShareIfaceSkipped(e.iface) {
			continue
		}
		rank := mitmShareRank(e.ip)
		text := e.ip.String()
		if rank < 0 || seen[text] {
			continue
		}
		seen[text] = true
		list = append(list, ranked{text, rank})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].rank != list[j].rank {
			return list[i].rank < list[j].rank
		}
		return list[i].text < list[j].text
	})
	var out []string
	for _, r := range list {
		out = append(out, r.text)
	}
	return out
}

// mitmLocalShareAddresses returns private IPv4 (private and link-local) and IPv6 ULA
// addresses on enabled, non-loopback interfaces.
func mitmLocalShareAddresses() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var entries []mitmShareIfaceAddr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				entries = append(entries, mitmShareIfaceAddr{iface.Name, ipNet.IP})
			}
		}
	}
	return mitmRankShareAddresses(entries)
}

func (s *server) mitmShareCandidates() []string {
	list := mitmShareLocalAddresses()
	if list == nil {
		list = []string{}
	}
	return list
}

// mitmShareStop closes the download page immediately; it is safe to call repeatedly.
func (s *server) mitmShareStop() {
	sh := &s.mitm.share
	sh.mu.Lock()
	cur := sh.cur
	sh.cur = nil
	sh.mu.Unlock()
	cur.close()
}

// close shuts down the listener, connections and timer, and waits for the server goroutine to exit.
func (c *mitmShareSession) close() {
	if c == nil {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	_ = c.srv.Close()
	<-c.done
}

// mitmShareExpire closes the session if it is still the current one.
func (s *server) mitmShareExpire(c *mitmShareSession) {
	sh := &s.mitm.share
	sh.mu.Lock()
	if sh.cur != c {
		sh.mu.Unlock()
		return
	}
	sh.cur = nil
	sh.mu.Unlock()
	c.close()
}

// mitmShareStart starts the download page on addr (the first candidate if empty),
// replacing any existing session.
func (s *server) mitmShareStart(address string) (int, string) {
	root, _ := s.mitmRootSnapshot()
	if root == nil {
		return http.StatusConflict, "没有根证书"
	}
	candidates := s.mitmShareCandidates()
	if address == "" && len(candidates) > 0 {
		address = candidates[0]
	}
	found := false
	for _, c := range candidates {
		if c == address {
			found = true
		}
	}
	if !found {
		return http.StatusBadRequest, "地址不可用"
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return http.StatusInternalServerError, "无法启动下载服务"
	}
	sh := &s.mitm.share
	sh.mu.Lock()
	defer sh.mu.Unlock()
	old := sh.cur
	sh.cur = nil
	old.close()
	ln, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		return http.StatusInternalServerError, "无法启动下载服务"
	}
	port := ln.Addr().(*net.TCPAddr).Port
	sess := &mitmShareSession{
		token:       base64.RawURLEncoding.EncodeToString(buf),
		address:     address,
		port:        port,
		fingerprint: certFingerprint(root.Raw),
		expiresAt:   time.Now().Add(mitmShareLifetime).UTC().Truncate(time.Second),
		done:        make(chan struct{}),
	}
	sess.url = "http://" + net.JoinHostPort(address, strconv.Itoa(port)) + "/" + sess.token + "/"
	sess.srv = &http.Server{
		Handler:           s.mitmShareHandler(sess),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	sess.srv.SetKeepAlivesEnabled(false)
	go func() {
		defer close(sess.done)
		_ = sess.srv.Serve(ln)
	}()
	sess.timer = time.AfterFunc(mitmShareLifetime, func() { s.mitmShareExpire(sess) })
	sh.cur = sess
	return 0, ""
}

// mitmShareSnapshot builds the API response; a session whose root certificate has
// changed is treated as stale and closed.
func (s *server) mitmShareSnapshot() mitmShareResponse {
	resp := mitmShareResponse{Addresses: s.mitmShareCandidates()}
	sh := &s.mitm.share
	sh.mu.Lock()
	cur := sh.cur
	sh.mu.Unlock()
	if cur == nil {
		return resp
	}
	if root, _ := s.mitmRootSnapshot(); root == nil || certFingerprint(root.Raw) != cur.fingerprint {
		s.mitmShareExpire(cur)
		return resp
	}
	resp.Active = true
	resp.Address = cur.address
	resp.URL = cur.url
	resp.Fingerprint = cur.fingerprint
	resp.ExpiresAt = cur.expiresAt.Format(time.RFC3339)
	resp.QR = mitmShareQR(cur.url)
	n := cur.downloads.Load()
	resp.Downloads = &n
	return resp
}

// mitmShareQR returns the QR code matrix for url (error correction level M, no quiet
// zone), where '1' is a dark module.
func mitmShareQR(url string) []string {
	q, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return nil
	}
	q.DisableBorder = true
	bitmap := q.Bitmap()
	rows := make([]string, len(bitmap))
	for i, row := range bitmap {
		var b strings.Builder
		for _, dark := range row {
			if dark {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[i] = b.String()
	}
	return rows
}

func (s *server) mitmShareGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mitmShareSnapshot())
}

func (s *server) mitmSharePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		mitmError(w, http.StatusBadRequest, "请求无效")
		return
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &body); err != nil {
			mitmError(w, http.StatusBadRequest, "请求无效")
			return
		}
	}
	if code, message := s.mitmShareStart(strings.TrimSpace(body.Address)); code != 0 {
		mitmError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, s.mitmShareSnapshot())
}

func (s *server) mitmShareDelete(w http.ResponseWriter, r *http.Request) {
	s.mitmShareStop()
	writeJSON(w, http.StatusOK, s.mitmShareSnapshot())
}

// mitmShareLimiter rate-limits bad-token requests with one sliding window per source
// and one global window.
type mitmShareLimiter struct {
	mu     sync.Mutex
	global mitmShareWindow
	perIP  map[string]*mitmShareWindow
}

type mitmShareWindow struct {
	start time.Time
	n     int
}

func (w *mitmShareWindow) hit(now time.Time) int {
	if now.Sub(w.start) >= mitmShareBadWindow {
		w.start, w.n = now, 0
	}
	w.n++
	return w.n
}

// bad records a bad request and reports whether the limit is exceeded.
func (l *mitmShareLimiter) bad(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP == nil || len(l.perIP) >= mitmShareBadMaxKeys {
		l.perIP = map[string]*mitmShareWindow{}
	}
	w := l.perIP[ip]
	if w == nil {
		w = &mitmShareWindow{}
		l.perIP[ip] = w
	}
	perIP := w.hit(now)
	global := l.global.hit(now)
	return perIP > mitmShareBadPerIP || global > mitmShareBadGlobal
}

func mitmShareHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
}

// mitmShareHandler only answers token paths; responses for every other path are identical.
func (s *server) mitmShareHandler(sess *mitmShareSession) http.Handler {
	token := []byte(sess.token)
	notFound := func(w http.ResponseWriter) {
		mitmShareHeaders(w.Header())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found\n"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		seg, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if subtle.ConstantTimeCompare([]byte(seg), token) != 1 {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if sess.limiter.bad(ip) {
				mitmShareHeaders(w.Header())
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			notFound(w)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mitmShareHeaders(w.Header())
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		root, _ := s.mitmRootSnapshot()
		if root == nil || certFingerprint(root.Raw) != sess.fingerprint {
			notFound(w)
			return
		}
		if !strings.Contains(path[1:], "/") {
			mitmShareHeaders(w.Header())
			http.Redirect(w, r, "/"+sess.token+"/", http.StatusFound)
			return
		}
		formats := map[string]string{
			"ca.cer": "cer", "ca.crt": "crt", "ca.pem": "pem",
			"ca.p12": "p12", "ca.mobileconfig": "mobileconfig",
		}
		if rest == "" {
			mitmShareHeaders(w.Header())
			s.mitmSharePage(w, r, root)
			return
		}
		format, ok := formats[rest]
		if !ok {
			notFound(w)
			return
		}
		body, contentType, name, err := mitmExportBody(root, format)
		if err != nil {
			notFound(w)
			return
		}
		mitmShareHeaders(w.Header())
		w.Header().Set("Content-Type", contentType)
		if format != "mobileconfig" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			sess.downloads.Add(1)
			_, _ = w.Write(body)
		}
	})
}
