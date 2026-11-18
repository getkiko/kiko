package kit

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	// FacetPreview is how many options a folded facet group shows.
	FacetPreview = 8
	// FacetSearchFrom is how many options give a facet group a search
	// field.
	FacetSearchFrom = 15
)

// FacetState is what a facet group keeps between frames: whether it shows
// all its options, and its search.
type FacetState struct {
	All    bool
	Search string
}

// Facets keeps the state of a pane's facet groups by key.
type Facets map[string]*FacetState

// Of is the state of the group key, made on first use.
func (f *Facets) Of(key string) *FacetState {
	if *f == nil {
		*f = Facets{}
	}
	s := (*f)[key]
	if s == nil {
		s = &FacetState{}
		(*f)[key] = s
	}
	return s
}

// FacetGroup is a connection filter group of n options, the busiest
// first, without a scroll of its own: folded, it shows the first
// FacetPreview and the selected ones, with "Show all N" to unfold it;
// from FacetSearchFrom options a search field narrows it by label.
// option builds option i.
func FacetGroup(c *ui.Context, title string, s *FacetState, n int, label func(i int) string, selected func(i int) bool, option func(i int)) ui.Element {
	k := T(c)
	return ui.Column(c).Gap(2).Shrink(0).Label(title).Children(func() {
		ui.Text(c, title).FontSize(10).FontWeight(700).TextColor(k.Label3).Padding(0, 3, 4)
		query := strings.ToLower(strings.TrimSpace(s.Search))
		if n >= FacetSearchFrom {
			Search(c, "facet-search:"+title, &s.Search, SearchOptions{Placeholder: "搜索" + title}).Label("搜索" + title)
			ui.Box(c).Height(4).Shrink(0)
		} else {
			query = ""
		}
		matched := 0
		for i := 0; i < n; i++ {
			if query != "" && !strings.Contains(strings.ToLower(label(i)), query) {
				continue
			}
			matched++
			// Searching shows every match; folded, the first few and the
			// selected ones.
			if query == "" && !s.All && matched > FacetPreview && !selected(i) {
				continue
			}
			option(i)
		}
		switch {
		case query != "" && matched == 0:
			ui.Text(c, "无匹配项").FontSize(FontCaption).TextColor(k.Label3).Padding(4, 7)
		case query == "" && n > FacetPreview:
			text := fmt.Sprintf("显示全部 %d 项", n)
			if s.All {
				text = "收起"
			}
			more := ui.ButtonBase(c).MinHeight(24).PaddingX(7).Radius(7).Justify(ui.Start).
				FontSize(FontCaption).TextColor(k.AccentText).Label(text).Transition(HoverMotion)
			if more.Hovered() {
				more.Background(k.Fill)
			}
			more.Children(func() { ui.Text(c, text) })
			if more.Clicked() {
				s.All = !s.All
			}
		}
	})
}
