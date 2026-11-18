package kit

import "testing"

// TestFacetChoiceOrAcrossValues: a group passes any of its chosen values,
// unchoosing the last drops the group, and a group's own choice does not
// narrow its options (FacetFilter.Counts).
func TestFacetChoiceOrAcrossValues(t *testing.T) {
	c := FacetChoice[string]{}
	c.Toggle("source", "a")
	c.Add("source", "b")
	c.Toggle("app", "x")
	type row struct{ source, app string }
	var f FacetFilter[row]
	for _, g := range []string{"source", "app"} {
		match := c.Match(g)
		f.Add(g, func(r row) bool {
			if g == "source" {
				return match(r.source)
			}
			return match(r.app)
		})
	}
	rows := []row{{"a", "x"}, {"b", "x"}, {"c", "x"}, {"a", "y"}, {"c", "y"}}
	shown, sources, apps := 0, map[string]int{}, map[string]int{}
	for _, r := range rows {
		miss := f.Miss(r)
		if miss == FacetAll {
			shown++
		}
		if f.Counts(miss, "source") {
			sources[r.source]++
		}
		if f.Counts(miss, "app") {
			apps[r.app]++
		}
	}
	if shown != 2 || sources["c"] != 1 || sources["a"] != 1 || apps["y"] != 1 || apps["x"] != 2 {
		t.Fatalf("shown %d sources %v apps %v", shown, sources, apps)
	}
	c.Toggle("source", "a")
	c.Toggle("source", "b")
	if len(c["source"]) != 0 || c.Match("source") != nil || !c.Has("app", "x") || len(c) != 1 {
		t.Fatalf("choice %v", c)
	}
}
