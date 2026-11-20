package subscriptions

import (
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
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
		for _, item := range f.w.browserItems(o) {
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
	if items := f.w.browserItems(block); len(items) != 0 {
		t.Errorf("block offers %v", items)
	}
}

// A node of the nodes sheet opens a browser through it from its context
// menu, as an outbound card does; a disabled node has the items disabled.
func TestNodeBrowserMenu(t *testing.T) {
	doc := sampleDoc()
	off := outboundOf("out_node_off", "trojan", "A-东京", model.Fields{"server": "tk.example", "server_port": 443.0, "password": "p"})
	off.SubID, off.Enabled = "sub_a", false
	doc.Outbounds = append(doc.Outbounds, off)
	f := newFixture(t, doc)
	var mu sync.Mutex
	var asked []map[string]any
	f.srv.Route("POST", "/browser/open", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		mu.Lock()
		asked = append(asked, m)
		mu.Unlock()
		testkit.JSON(map[string]string{"browser": "Chrome"})(w, r)
	})
	f.click("查看 机场 A 的节点")
	// The sheet slides in.
	f.tt.Advance(time.Second)
	f.tt.Frame()
	if err := f.tt.RightClick("A-新加坡"); err != nil {
		t.Fatalf("right click: %v; texts %q", err, f.tt.Texts())
	}
	menu := f.tt.Menu()
	if !slices.Contains(menu, "用 Chrome 打开") || slices.Contains(menu, "用此出站打开浏览器") != (runtime.GOOS == "darwin") {
		t.Fatalf("menu %q", menu)
	}
	if err := f.tt.ChooseMenuItem("用 Chrome 打开"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(asked) == 1 && asked[0]["outbound"] == "A-新加坡" && asked[0]["engine"] == "chrome"
	})

	if err := f.tt.RightClick("A-东京"); err != nil {
		t.Fatalf("right click: %v; texts %q", err, f.tt.Texts())
	}
	if err := f.tt.ChooseMenuItem("用 Chrome 打开"); err == nil {
		t.Error("a disabled node opens a browser")
	}
	f.tt.CloseMenu()
	f.tt.Frame()
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Errorf("a disabled item opened a browser: %v", asked)
	}
}
