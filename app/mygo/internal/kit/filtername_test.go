package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFilterOptionNamed(t *testing.T) {
	const long = "一台名字特别特别特别特别特别特别特别特别长的设备"
	var clicked []string
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(40).Children(func() {
			ui.Column(c).Width(180).Children(func() {
				if FilterOptionNamed(c.Key("a"), FilterConnection, false, "10.0.0.2", "客厅电视", "21").Clicked() {
					clicked = append(clicked, "a")
				}
				FilterOptionNamed(c.Key("b"), FilterConnection, true, "10.0.0.3", long, "7")
				if FilterOptionNamed(c.Key("c"), FilterConnection, false, "10.0.0.4", "", "3").Clicked() {
					clicked = append(clicked, "c")
				}
			})
		})
	}, 400, 200)
	tt.Frame()
	for _, text := range []string{"10.0.0.2", "客厅电视", "21", "10.0.0.4", "3"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q: %q", text, tt.Texts())
		}
	}
	if err := tt.Click("10.0.0.2 客厅电视"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("10.0.0.4"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(clicked) != 2 || clicked[0] != "a" || clicked[1] != "c" {
		t.Errorf("clicked %v", clicked)
	}
}
