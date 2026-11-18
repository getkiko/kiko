package kit

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNamedCell(t *testing.T) {
	const long = "书房里那台名字特别特别长的 Mac mini 设备"
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(40).Gap(20).Children(func() {
			ui.Row(c).Width(170).Height(24).Children(func() { NamedCell(c, "192.168.1.23:50123", long, "") })
			ui.Row(c).Width(320).Height(24).Children(func() { NamedCell(c, "172.18.0.2:61804", "客厅电视", "") })
			ui.Row(c).Width(170).Height(40).Children(func() { NamedCell(c, "10.0.0.9:443", "", "tun-in") })
		})
	}, 600, 260)
	tt.Frame()
	tt.Frame()
	ip, ok := tt.Find("192.168.1.23:50123")
	name, ok2 := tt.Find(long)
	if !ok || !ok2 {
		t.Fatalf("texts %q", tt.Texts())
	}
	// The address keeps its width; the name follows on the same line and
	// is clipped to what is left of the cell.
	if name.X < ip.X+ip.W || name.Y > ip.Y+ip.H/2 || name.X+name.W > 40+170+0.5 {
		t.Errorf("address %+v, name %+v", ip, name)
	}
	if w, _ := ui.ShapeText("192.168.1.23:50123", ui.Font{Size: FontTable}, 0).Size(); ip.W+1 < w {
		t.Errorf("address clipped: %v < %v", ip.W, w)
	}
	// A clipped name shows "IP · name" whole in a hint; one that fits none.
	full := "192.168.1.23:50123 · " + long
	tt.Move(ip.X+4, ip.Y+ip.H/2)
	tt.Advance(1700 * time.Millisecond)
	if !tt.HasText(full) {
		t.Errorf("no hint for a clipped name: %q", tt.Texts())
	}
	short, _ := tt.Find("172.18.0.2:61804")
	tt.Move(short.X+4, short.Y+short.H/2)
	tt.Advance(1700 * time.Millisecond)
	if tt.HasText(full) || tt.HasText("172.18.0.2:61804 · 客厅电视") {
		t.Errorf("a hint for a name that fits: %q", tt.Texts())
	}
	// Without a name it is Cell, with its second line.
	if !tt.HasText("10.0.0.9:443") || !tt.HasText("tun-in") {
		t.Errorf("unnamed cell: %q", tt.Texts())
	}
}

func TestNamedProperty(t *testing.T) {
	width := float32(420)
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(20).Children(func() {
			ui.Column(c).Width(width).Children(func() {
				NamedProperty(c, "源地址", "172.18.0.2:61804", "客厅电视")
				NamedProperty(c, "目标", "10.0.0.9:443", "")
			})
		})
	}, 600, 300)
	tt.Frame()
	tt.Frame()
	ip, _ := tt.Find("172.18.0.2:61804")
	name, ok := tt.Find("客厅电视")
	if !ok || !tt.HasText("10.0.0.9:443") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if name.X < ip.X+ip.W || name.Y > ip.Y+ip.H/2 {
		t.Errorf("wide: address %+v, name %+v not on one line", ip, name)
	}
	// Too narrow for both: the name goes below the address.
	width = 220
	tt.Frame()
	tt.Frame()
	ip, _ = tt.Find("172.18.0.2:61804")
	name, _ = tt.Find("客厅电视")
	if name.Y < ip.Y+ip.H-0.5 {
		t.Errorf("narrow: address %+v, name %+v on one line", ip, name)
	}
}
