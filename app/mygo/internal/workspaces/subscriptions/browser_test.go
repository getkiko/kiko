package subscriptions

import (
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// An outbound's menu opens a browser through it, by the tag the core runs
// it with; outbounds that forward nothing have no such items.
func TestOutboundBrowserItems(t *testing.T) {
	doc := chainFixtureDoc()
	doc.BrowserHomeURL = "https://example.com/ip"
	doc.BrowserWebRTC = true
	f := newFixture(t, doc)
	var mu sync.Mutex
	var asked map[string]any
	f.srv.Route("POST", "/browser/open", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &asked)
		mu.Unlock()
		testkit.JSON(map[string]string{"browser": "Chrome"})(w, r)
	})
	doc = f.store.Doc()
	var labels []string
	for _, o := range doc.Outbounds {
		if o.ID != "out_jp" {
			continue
		}
		for _, item := range f.w.outbounds.browserItems(o) {
			labels = append(labels, item.Label)
			if item.Label == "用 Chrome 打开" {
				item.Run(ui.Element{})
			}
		}
	}
	want := 1
	if runtime.GOOS == "darwin" {
		want = 2
	}
	if len(labels) != want {
		t.Fatalf("items %v", labels)
	}
	testkit.Until(t, f.tt, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return asked["outbound"] == "日本 01" && asked["engine"] == "chrome" && asked["home"] == "https://example.com/ip" && asked["webRTC"] == true
	})
	block := outboundOf("out_block", "block", "拒绝", nil)
	if items := f.w.outbounds.browserItems(block); len(items) != 0 {
		t.Errorf("block offers %v", items)
	}
}
