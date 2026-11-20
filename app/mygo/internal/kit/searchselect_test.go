package kit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSearchSelectKeysWithPointerResting moves the active option with the
// keys while the pointer rests on another: the keys' choice holds until
// the pointer moves.
func TestSearchSelectKeysWithPointerResting(t *testing.T) {
	value := "a"
	changed := false
	options := []Option{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta"}, {Value: "c", Label: "Gamma"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		if SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "选项", Width: 200}).Changed() {
			changed = true
		}
	}, 640, 600)
	tt.Frame()
	if err := tt.Click("选项：Alpha"); err != nil {
		t.Fatalf("open: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	gamma, ok := tt.Find("Gamma")
	if !ok {
		t.Fatalf("no Gamma: %q", tt.Texts())
	}
	tt.Move(gamma.X+gamma.W/2, gamma.Y+gamma.H/2)
	tt.Frame()
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !changed || value != "b" {
		t.Fatalf("chose %q (changed %v), want b", value, changed)
	}
}

// TestSearchMultiSelectToggles adds and removes options while the panel
// stays open, keeping the order of choice.
func TestSearchMultiSelectToggles(t *testing.T) {
	values := []string{"b"}
	changes := 0
	options := []Option{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta"}, {Value: "c", Label: "Gamma"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		if SearchMultiSelect(c, "pick", &values, options, SearchSelectOptions{Label: "选项", Width: 200}).Changed() {
			changes++
		}
	}, 640, 600)
	tt.Frame()
	if err := tt.Click("选项：Beta"); err != nil {
		t.Fatalf("open: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Gamma"); err != nil {
		t.Fatalf("gamma: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Alpha"); err != nil {
		t.Fatalf("alpha: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	if len(values) != 3 || values[0] != "b" || values[1] != "c" || values[2] != "a" {
		t.Fatalf("values %v", values)
	}
	if err := tt.Click("Beta"); err != nil {
		t.Fatalf("beta: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	if len(values) != 2 || values[0] != "c" || values[1] != "a" || changes != 3 {
		t.Fatalf("values %v changes %d", values, changes)
	}
	if _, ok := tt.Find("选项：Gamma、Alpha"); !ok {
		t.Fatalf("trigger %q", tt.Texts())
	}
}

// OnQuery hears the empty text as the panel opens and each change of the
// search text after it.
func TestSearchSelectOnQuery(t *testing.T) {
	value := ""
	var queries []string
	options := []Option{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "选项", Width: 200,
			OnQuery: func(q string) { queries = append(queries, q) }})
	}, 640, 600)
	tt.Frame()
	if len(queries) != 0 {
		t.Fatalf("asked before opening: %q", queries)
	}
	if err := tt.Click("选项：请选择"); err != nil {
		t.Fatalf("open: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	tt.Type("be")
	tt.Frame()
	if len(queries) != 2 || queries[0] != "" || queries[1] != "be" {
		t.Fatalf("queries %q", queries)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click("选项：请选择"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	tt.Frame()
	if n := len(queries); n != 3 || queries[2] != "" {
		t.Fatalf("queries after reopening %q", queries)
	}
}

// OnOpen is called once per open of the panel, before the first query.
func TestSearchSelectOnOpen(t *testing.T) {
	value := ""
	var events []string
	options := []Option{{Value: "a", Label: "Alpha"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "选项", Width: 200,
			OnOpen:  func() { events = append(events, "open") },
			OnQuery: func(q string) { events = append(events, "q:"+q) }})
	}, 640, 600)
	tt.Frame()
	for i := 0; i < 2; i++ {
		if err := tt.Click("选项：请选择"); err != nil {
			t.Fatalf("open: %v", err)
		}
		tt.Frame()
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
	}
	if got := strings.Join(events, ","); got != "open,q:,open,q:" {
		t.Fatalf("events %q", got)
	}
}

// Without OnQuery, OnOpen still runs once per opening, not once per frame.
func TestSearchSelectOnOpenWithoutQueryOnce(t *testing.T) {
	value := ""
	opens := 0
	options := []Option{{Value: "a", Label: "Alpha"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "选项", Width: 200,
			OnOpen: func() { opens++ }})
	}, 640, 600)
	tt.Frame()
	for i := 1; i <= 2; i++ {
		if err := tt.Click("选项：请选择"); err != nil {
			t.Fatalf("open: %v", err)
		}
		for j := 0; j < 5; j++ {
			tt.Frame()
		}
		if opens != i {
			t.Fatalf("after opening %d: OnOpen ran %d times", i, opens)
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
	}
}

// Every option is listed, however many: the panel opens at the current
// one far down the list, and Down reaches the last.
func TestSearchSelectListsEveryOption(t *testing.T) {
	var options []Option
	for i := range 300 {
		options = append(options, Option{Value: fmt.Sprint(i), Label: fmt.Sprintf("节点 %03d", i)})
	}
	value := "260"
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "出站", Width: 200})
	}, 640, 600)
	tt.Frame()
	if err := tt.Click("出站：节点 260"); err != nil {
		t.Fatalf("open: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("300 项匹配") || !tt.HasText("节点 260") {
		t.Fatalf("panel: %q", tt.Texts())
	}
	if tt.HasText("节点 000") {
		t.Fatal("rows out of view are built")
	}
	for range 50 {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if value != "299" {
		t.Fatalf("chose %q, want the last option", value)
	}
}
