package overview

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// Typing into the search highlights the new query's matches: the field
// changes the query while the frame is built, after the old query's
// matches were found, and adjacent matches of a short query overlapped
// the longer one's highlights (a slice out of range).
func TestConfigViewerTypesAdjacentMatches(t *testing.T) {
	v := &configViewer{}
	v.show("config.json", `{"tag": "aaaa", "type": "a"}`)
	e := &env.Env{}
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		v.view(c, e, 300, false)
	}, 900, 400)
	tt.Frame()
	if err := tt.Click("搜索配置文件"); err != nil {
		t.Fatal(err)
	}
	for _, r := range "aa" {
		tt.Type(string(r))
		tt.Frame()
	}
	if v.query != "aa" || v.found != "aa" {
		t.Fatalf("query %q found %q", v.query, v.found)
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "1 / 2") {
		t.Errorf("counter: %q", tt.Texts())
	}
}
