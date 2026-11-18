package overview

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func nodeOutbound(id, tag, host string) *model.Outbound {
	return &model.Outbound{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "outbound", Type: "trojan",
		Fields: model.Fields{"server": host, "server_port": 443.0}}
}

func groupDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.Outbounds = append(doc.Outbounds, nodeOutbound("o_hk", "香港", "hk.example"), nodeOutbound("o_jp", "日本", "jp.example"),
		nodeOutbound("o_us", "美国", "us.example"))
	return doc
}

type selectorFeed struct {
	mu     sync.Mutex
	groups []api.Selector
}

func (s *selectorFeed) set(groups ...api.Selector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups = groups
}

func (s *selectorFeed) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	groups := append([]api.Selector(nil), s.groups...)
	s.mu.Unlock()
	testkit.JSON(map[string]any{"selectors": groups})(w, r)
}

// The policy group card follows an applied configuration: groups added,
// renamed and removed show without a restart of the page or the core.
func TestSelectorGroupsFollowApply(t *testing.T) {
	f := newFixtureWith(t, groupDoc())
	feed := &selectorFeed{}
	feed.set(api.Selector{Name: "自动", Type: "URLTest", Now: "香港", All: []string{"香港", "日本"}})
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	f.srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "自动")

	feed.set(api.Selector{Name: "自动", Type: "URLTest", Now: "香港", All: []string{"香港", "日本"}},
		api.Selector{Name: "流媒体", Type: "Selector", Now: "日本", All: []string{"日本", "美国"}})
	f.store.RefreshApplied()
	testkit.UntilText(t, f.tt, "流媒体")

	feed.set(api.Selector{Name: "流媒体2", Type: "Selector", Now: "日本", All: []string{"日本", "美国"}})
	f.store.RefreshApplied()
	testkit.UntilText(t, f.tt, "流媒体2")
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("自动") })
}

// A group card shows the latency of the member it has chosen, tagged by
// its state.
func TestSelectorCardShowsChoiceLatency(t *testing.T) {
	f := newFixtureWith(t, groupDoc())
	feed := &selectorFeed{}
	feed.set(api.Selector{Name: "节点选择", Type: "Selector", Now: "香港", All: []string{"香港", "日本"}})
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "节点选择")
	ms := 86.0
	f.store.Latency["o_hk"] = api.LatencyResult{ID: "o_hk", MedianConnectMs: &ms, SuccessSamples: 1}
	f.tt.Frame()
	if !f.tt.HasText("86 ms") {
		t.Errorf("choice latency missing: %q", f.tt.Texts())
	}
}

// The chooser's test button tests the members listed, those a search
// leaves, and each row shows its own state.
func TestChooserTestsVisibleMembers(t *testing.T) {
	f := newFixtureWith(t, groupDoc())
	feed := &selectorFeed{}
	feed.set(api.Selector{Name: "节点选择", Type: "Selector", Now: "香港", All: []string{"香港", "日本", "美国"}})
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	var mu sync.Mutex
	var asked []string
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			mu.Lock()
			asked = append(asked, target.ID)
			mu.Unlock()
			ms := 42.0
			result := api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 1}
			if target.ID == "o_us" {
				result = api.LatencyResult{ID: target.ID, Error: "Timeout"}
			}
			results = append(results, result)
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "节点选择")
	f.click(t, "节点选择")
	testkit.UntilText(t, f.tt, "美国")
	f.click(t, "测速")
	testkit.Until(t, f.tt, func() bool {
		return !f.store.TestingAny([]string{"o_hk", "o_jp", "o_us"}) && f.store.Latency["o_us"].ID != ""
	})
	f.tt.Frame()
	mu.Lock()
	if len(asked) != 3 {
		t.Errorf("tested %q, want all three members", asked)
	}
	asked = nil
	mu.Unlock()
	if !f.tt.HasText("42 ms") || !f.tt.HasText("超时") {
		t.Errorf("row tags missing: %q", f.tt.Texts())
	}
	f.w.selector.query = "日"
	f.tt.Frame()
	f.click(t, "测速")
	testkit.Until(t, f.tt, func() bool { mu.Lock(); defer mu.Unlock(); return len(asked) > 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "o_jp" {
		t.Errorf("filtered test asked %q, want only o_jp", asked)
	}
}

// TestLatencyScreenshots saves the chooser with its four tag states, and
// the cards while several tests run, where OVERVIEW_PREVIEW_DIR says.
func TestLatencyScreenshots(t *testing.T) {
	doc := groupDoc()
	doc.Outbounds = append(doc.Outbounds, nodeOutbound("o_sg", "新加坡", "sg.example"))
	f := newFixtureWith(t, doc)
	feed := &selectorFeed{}
	feed.set(api.Selector{Name: "节点选择", Type: "Selector", Now: "香港", All: []string{"香港", "日本", "美国", "新加坡"}},
		api.Selector{Name: "自动", Type: "URLTest", Now: "日本", All: []string{"日本", "新加坡"}})
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			<-release
			ms := 90.0
			results = append(results, api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 1})
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "节点选择")
	hk, us, sg := 86.0, 240.0, 380.0
	f.store.Latency["o_hk"] = api.LatencyResult{ID: "o_hk", MedianConnectMs: &hk, SuccessSamples: 1}
	f.store.Latency["o_us"] = api.LatencyResult{ID: "o_us", MedianConnectMs: &us, SuccessSamples: 1}
	f.store.Latency["o_sg"] = api.LatencyResult{ID: "o_sg", Error: "Timeout"}
	_ = sg
	f.store.MeasureLatencyOf([]string{"o_jp"})
	f.click(t, "节点选择")
	testkit.UntilText(t, f.tt, "美国")
	shoot := func(name string) {
		for _, dark := range []bool{false, true} {
			f.tt.SetDark(dark)
			f.tt.Frame()
			f.tt.Advance(time.Second)
			f.tt.Frame()
			suffix := "light"
			if dark {
				suffix = "dark"
			}
			savePreview(t, f.tt, "a-"+name+"-"+suffix+".png")
		}
		f.tt.SetDark(false)
	}
	shoot("group-popup-latency")
	f.w.selector.open = false
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.store.MeasureLatencyOf([]string{"o_hk", "o_us"})
	f.tt.Frame()
	shoot("simultaneous-tests")
}
