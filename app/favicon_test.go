package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPNG(t *testing.T, side int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// testICO is an ICO of one 16 pixel image: a PNG, or a 32 bit BMP.
func testICO(t *testing.T, asPNG bool) []byte {
	t.Helper()
	var image []byte
	if asPNG {
		image = testPNG(t, 16)
	} else {
		header := make([]byte, 40)
		binary.LittleEndian.PutUint32(header[0:], 40)
		binary.LittleEndian.PutUint32(header[4:], 16)
		binary.LittleEndian.PutUint32(header[8:], 32)
		binary.LittleEndian.PutUint16(header[12:], 1)
		binary.LittleEndian.PutUint16(header[14:], 32)
		pixels := bytes.Repeat([]byte{10, 20, 30, 255}, 16*16)
		mask := make([]byte, 4*16)
		image = append(append(header, pixels...), mask...)
	}
	out := []byte{0, 0, 1, 0, 1, 0}
	entry := make([]byte, 16)
	entry[0], entry[1] = 16, 16
	binary.LittleEndian.PutUint32(entry[8:], uint32(len(image)))
	binary.LittleEndian.PutUint32(entry[12:], 22)
	return append(append(out, entry...), image...)
}

func TestFaviconRoot(t *testing.T) {
	for in, want := range map[string]string{
		"api.github.com": "github.com", "WWW.Example.co.uk.": "example.co.uk", "github.com:443": "github.com",
	} {
		if got, err := faviconRoot(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "1.2.3.4", "[::1]", "localhost", "printer.local", "co.uk", "a b.com", "intranet"} {
		if got, err := faviconRoot(in); err == nil {
			t.Errorf("%q gave %q", in, got)
		}
	}
}

func TestFaviconLinks(t *testing.T) {
	page := []byte(`<html><head>
<link rel="icon" href="/small.png" sizes="16x16">
<link rel="icon" type="image/svg+xml" href="/logo.svg">
<link rel="apple-touch-icon" href="/touch.png">
<link rel="shortcut icon" href="https://cdn.net/icon-96.png" sizes="96x96">
</head><body><link rel="icon" href="/late.png"></body></html>`)
	base, _ := http.NewRequest("GET", "https://site.com/home/", nil)
	got := faviconLinks(page, base.URL)
	want := []string{"https://cdn.net/icon-96.png", "https://site.com/touch.png", "https://site.com/small.png"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("links %q", got)
	}
}

func TestFaviconPNG(t *testing.T) {
	big := testPNG(t, 180)
	data := map[string][]byte{"png": big, "ico png": testICO(t, true), "ico bmp": testICO(t, false)}
	for name, raw := range data {
		out, err := faviconPNG(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(out))
		if err != nil || img.Bounds().Dx() > faviconSide || img.Bounds().Dy() > faviconSide {
			t.Fatalf("%s: %v %v", name, img.Bounds(), err)
		}
	}
	out, _ := faviconPNG(testICO(t, false))
	img, _ := png.Decode(bytes.NewReader(out))
	if c := color.NRGBAModel.Convert(img.At(3, 3)).(color.NRGBA); c.R != 30 || c.B != 10 || c.A != 255 {
		t.Errorf("BMP pixel %v", c)
	}
	if _, err := faviconPNG([]byte("<svg/>")); err == nil {
		t.Error("SVG decoded")
	}
	raw := testPNG(t, 8)
	if got, err := decodeDataURL("data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)); err != nil || !bytes.Equal(got, raw) {
		t.Errorf("data URL: %v", err)
	}
}

// siteClient is a client whose every connection goes to srv, whatever the
// host, as through a proxy.
func siteClient(srv *httptest.Server) *http.Client {
	return &http.Client{CheckRedirect: faviconRedirect, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
}

// The service fetches a site's linked icon once, keeps it, serves it
// from the cache, marks a site without one, and clears.
func TestFaviconService(t *testing.T) {
	var fetches atomic.Int32
	ico := testICO(t, false)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		switch {
		case r.Host == "good.com" && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<link rel="icon" href="/i.ico">`))
		case r.Host == "good.com" && r.URL.Path == "/i.ico":
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write(ico)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := newFavicons(nil, t.TempDir())
	var asked atomic.Value
	f.client = func(_ context.Context, outbound string) (*http.Client, error) {
		asked.Store(outbound)
		return siteClient(srv), nil
	}
	// Two requests at once fetch once.
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := f.icon(context.Background(), "good.com", "香港 01")
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if asked.Load() != "香港 01" {
		t.Errorf("outbound %v", asked.Load())
	}
	first := fetches.Load()
	if _, err := f.icon(context.Background(), "good.com", "香港 01"); err != nil || fetches.Load() != first {
		t.Errorf("cached icon fetched again: %v, %d → %d", err, first, fetches.Load())
	}
	if first != 2 {
		t.Errorf("fetches %d, want the page and the icon once", first)
	}

	// A site without an icon is marked, and not asked again for a day.
	if _, err := f.icon(context.Background(), "none.com", "x"); err == nil {
		t.Fatal("icon for a site without one")
	}
	before := fetches.Load()
	if _, err := f.icon(context.Background(), "none.com", "x"); err == nil || fetches.Load() != before {
		t.Error("a missing icon was fetched again")
	}
	f.now = func() time.Time { return time.Now().Add(faviconMissFor + time.Hour) }
	if _, err := f.icon(context.Background(), "none.com", "x"); err == nil || fetches.Load() == before {
		t.Error("a day later the site was not asked again")
	}
	f.now = time.Now

	// A stale icon is served and fetched again behind it.
	old := time.Now().Add(-faviconFresh - time.Hour)
	_ = os.Chtimes(f.file("good.com", ".png"), old, old)
	before = fetches.Load()
	if _, err := f.icon(context.Background(), "good.com", "x"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fetches.Load() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fetches.Load() == before {
		t.Error("stale icon not refreshed")
	}
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.inflight)
		f.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stats := f.stats()
	if stats.Count != 1 || stats.Bytes == 0 {
		t.Fatalf("stats %+v", stats)
	}
	if cleared := f.clear(); cleared.Count != 1 || f.stats().Count != 0 || f.stats().Bytes != 0 {
		t.Errorf("clear %+v, left %+v", cleared, f.stats())
	}
}

// A failing outbound proxy is no answer about the site: nothing is kept.
func TestFaviconProxyFailureNotCached(t *testing.T) {
	f := newFavicons(nil, t.TempDir())
	f.client = func(context.Context, string) (*http.Client, error) {
		return nil, os.ErrDeadlineExceeded
	}
	_, err := f.icon(context.Background(), "good.com", "x")
	if _, ok := err.(faviconProxyError); !ok {
		t.Fatalf("error %T %v", err, err)
	}
	if _, err := os.Stat(f.mark("good.com", "x") + ".miss"); err == nil {
		t.Error("proxy failure marked the site")
	}
}

// The cache keeps to its bound of entries, the oldest going first.
func TestFaviconTrim(t *testing.T) {
	f := newFavicons(nil, t.TempDir())
	raw := testPNG(t, 4)
	for i := 0; i < faviconMaxEntries+5; i++ {
		_ = os.MkdirAll(f.dir, 0o700)
		path := f.file("site"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".com", ".png")
		_ = os.WriteFile(path, raw, 0o600)
		at := time.Now().Add(-time.Hour + time.Duration(i)*time.Millisecond)
		_ = os.Chtimes(path, at, at)
	}
	f.keep("newest.com", raw)
	if n := len(f.files()); n > faviconMaxEntries {
		t.Errorf("%d entries kept", n)
	}
	if _, err := os.Stat(f.file("newest.com", ".png")); err != nil {
		t.Error("the newest icon was trimmed")
	}
}

// A malformed ICO does not panic, and an image declaring a huge size is
// refused before its pixels are made.
func TestFaviconHostileImages(t *testing.T) {
	ico := testICO(t, false)
	binary.LittleEndian.PutUint32(ico[22:], 0xFFFFFFF0) // the bitmap's header size
	if _, err := faviconPNG(ico); err == nil {
		t.Error("malformed ICO decoded")
	}
	var huge bytes.Buffer
	_ = png.Encode(&huge, image.NewGray(image.Rect(0, 0, 4000, 1)))
	if _, err := faviconPNG(huge.Bytes()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("huge image: %v", err)
	}
}

// Sites never reached are not marked as without an icon, and links and
// redirects to this host or its network are not followed.
func TestFaviconUnreachableAndPrivate(t *testing.T) {
	f := newFavicons(nil, t.TempDir())
	f.client = func(context.Context, string) (*http.Client, error) {
		return &http.Client{CheckRedirect: faviconRedirect, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, os.ErrDeadlineExceeded
		})}, nil
	}
	_, err := f.icon(context.Background(), "down.com", "x")
	if !errors.Is(err, errUnreachable) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(f.mark("down.com", "x") + ".miss"); err == nil {
		t.Error("an unreachable site was marked as without an icon")
	}
	if _, err := os.Stat(f.mark("down.com", "x") + ".wait"); err != nil {
		t.Error("an unreachable site is asked again at once")
	}
	// Through another outbound it is asked again.
	if _, err := f.icon(context.Background(), "down.com", "y"); !errors.Is(err, errUnreachable) {
		t.Errorf("other outbound: %v", err)
	}

	page := []byte(`<link rel="icon" href="http://192.168.1.1/x.png"><link rel="icon" href="http://localhost/y.png"><link rel="icon" href="https://cdn.example.com:8443/z.png"><link rel="icon" href="https://cdn.example.com/ok.png">`)
	base, _ := http.NewRequest("GET", "https://example.com/", nil)
	if got := faviconLinks(page, base.URL); len(got) != 1 || got[0] != "https://cdn.example.com/ok.png" {
		t.Errorf("links %q", got)
	}
	private, _ := http.NewRequest("GET", "http://10.0.0.1/", nil)
	if faviconRedirect(private, nil) == nil {
		t.Error("redirect to a private address followed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A site refusing or limiting the fetch (403, 429, 5xx) gave no answer
// about its icon: it is asked again after faviconWaitFor, not a day.
func TestFaviconRefusedIsNotMiss(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	f := newFavicons(nil, t.TempDir())
	f.client = func(context.Context, string) (*http.Client, error) { return siteClient(srv), nil }
	if _, err := f.icon(context.Background(), "busy.com", "x"); !errors.Is(err, errUnreachable) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(f.mark("busy.com", "x") + ".miss"); err == nil {
		t.Error("a limited site was marked as without an icon")
	}
}

// A site out of reach is not fetched again for three hours: the clients
// wait as long, so that it is not asked over and over.
func TestFaviconUnreachableWaitsHours(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	f := newFavicons(nil, t.TempDir())
	f.client = func(context.Context, string) (*http.Client, error) { return siteClient(srv), nil }
	now := time.Now()
	f.now = func() time.Time { return now }
	if _, err := f.icon(context.Background(), "down.com", "x"); !errors.Is(err, errUnreachable) {
		t.Fatalf("error %v", err)
	}
	first := hits
	now = now.Add(2*time.Hour + 59*time.Minute)
	if _, err := f.icon(context.Background(), "down.com", "x"); !errors.Is(err, errUnreachable) || hits != first {
		t.Fatalf("asked again within 3 hours: %v, %d hits", err, hits)
	}
	now = now.Add(2 * time.Minute)
	_, _ = f.icon(context.Background(), "down.com", "x")
	if hits == first {
		t.Fatal("not asked again after 3 hours")
	}
}

// Only a site out of reach answers 502, waited for hours; a fetch that
// never started (no fetcher freed in time) answers 503, asked again soon.
func TestFaviconQueueTimeoutIsNotUnreachable(t *testing.T) {
	if errors.Is(context.DeadlineExceeded, errUnreachable) {
		t.Fatal("a queue timeout reads as a site out of reach")
	}
}
