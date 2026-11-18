package srcname

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestName(t *testing.T) {
	devices := []*model.RouteDevice{
		{Name: "iPhone", Addresses: []string{"192.168.1.20", "fd00::20", " 192.168.1.21/32 "}},
		{Name: "Mac", Addresses: []string{"192.168.1.30"}},
		{Name: "客厅", Addresses: []string{"192.168.2.0/24", "invalid"}},
		{Name: "走廊", Addresses: []string{"192.168.2.128/25"}},
		{Name: "  ", Addresses: []string{"192.168.1.40"}},
		{Name: "重复", Addresses: []string{"192.168.1.30"}},
		nil,
	}
	local := []string{"192.168.1.5/32", "::1/128", "bad", "192.168.1.30"}
	n := New(devices, local)
	cases := []struct{ ip, want string }{
		{"192.168.1.20", "iPhone"},
		{"192.168.1.21", "iPhone"},
		{"fd00::20", "iPhone"},
		{"FD00:0:0:0:0:0:0:20", "iPhone"},
		{"::ffff:192.168.1.20", "iPhone"},
		{" 192.168.1.20 ", "iPhone"},
		{"192.168.1.30", Local},
		{"192.168.1.5", Local},
		{"::1", Local},
		{"192.168.2.7", "客厅"},
		{"192.168.2.200", "走廊"},
		{"192.168.1.40", ""},
		{"192.168.9.9", ""},
		{"not-an-ip", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := n.Name(c.ip); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.ip, got, c.want)
		}
	}
}

func TestNoDevices(t *testing.T) {
	var nilNames *Names
	for _, n := range []*Names{nilNames, New(nil, nil)} {
		if got := n.Name("10.0.0.1"); got != "" {
			t.Errorf("Name = %q", got)
		}
		if got := n.Text("10.0.0.1"); got != "10.0.0.1" {
			t.Errorf("Text = %q", got)
		}
	}
}

func TestMultipleAddressesShareTheName(t *testing.T) {
	n := New([]*model.RouteDevice{{Name: "iPad", Addresses: []string{"10.0.0.2", "10.0.0.3"}}}, nil)
	if n.Name("10.0.0.2") != "iPad" || n.Name("10.0.0.3") != "iPad" {
		t.Error("each address carries the device name")
	}
	if got := n.Text("10.0.0.3"); got != "10.0.0.3 iPad" {
		t.Errorf("Text = %q", got)
	}
}

func TestRenameFollowsTheDevices(t *testing.T) {
	device := &model.RouteDevice{Name: "旧名", Addresses: []string{"10.0.0.2"}}
	if got := New([]*model.RouteDevice{device}, nil).Name("10.0.0.2"); got != "旧名" {
		t.Fatal(got)
	}
	device.Name = "新名"
	if got := New([]*model.RouteDevice{device}, nil).Name("10.0.0.2"); got != "新名" {
		t.Errorf("rename: %q", got)
	}
}

func TestLongNameIsKeptWhole(t *testing.T) {
	name := strings.Repeat("长", 60)
	n := New([]*model.RouteDevice{{Name: name, Addresses: []string{"10.0.0.2"}}}, nil)
	if n.Name("10.0.0.2") != name {
		t.Error("the name is truncated by the view, not the label")
	}
}

func TestFromDocSupplementaryLocalAddresses(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Route.LocalAddresses = []string{"10.9.0.1", "10.8.0.0/16"}
	if got := FromDoc(doc, []string{"192.168.1.5/32"}); got.Name("10.9.0.1") != Local || got.Name("192.168.1.5") != Local || got.Name("10.8.0.1") != "" {
		t.Error("supplementary host addresses are the host's")
	}
	// The local scope switch decides routing; the addresses stay the host's.
	off := false
	doc.Route.LocalEnabled = &off
	if got := FromDoc(doc, nil).Name("10.9.0.1"); got != Local {
		t.Errorf("disabled local scope: %q", got)
	}
}

// The host's connections through TUN come from the interface's own
// address: it is the local server from the document alone, with no core running and no
// detected addresses.
func TestFromDocTunAndLoopbackAreLocal(t *testing.T) {
	doc := model.EmptyDoc("darwin")
	tun := model.MakeInbound("tun", "tun-in", "darwin", true)
	tun.Enabled = true
	tun.Fields["address"] = []any{"172.18.0.1/30", "fdfe:dcba:9876::1/126"}
	doc.Inbounds = append(doc.Inbounds, tun)
	doc.Route.Devices = []*model.RouteDevice{{Name: "网关", Addresses: []string{"172.18.0.0/24"}}}
	n := FromDoc(doc, nil)
	for _, ip := range []string{"172.18.0.1", "fdfe:dcba:9876::1", "FDFE:DCBA:9876:0:0:0:0:1", "127.0.0.1", "127.8.9.10", "::1"} {
		if got := n.Name(ip); got != Local {
			t.Errorf("Name(%q) = %q, want %s", ip, got, Local)
		}
	}
	// The interface's peers are not the host.
	if got := n.Name("172.18.0.2"); got != "网关" {
		t.Errorf("Name(172.18.0.2) = %q", got)
	}
	if got := n.Name("fdfe:dcba:9876::2"); got != "" {
		t.Errorf("Name(fdfe:dcba:9876::2) = %q", got)
	}

	// A TUN without addresses gets the platform's defaults from the core.
	bare := model.MakeInbound("tun", "tun-2", "linux", true)
	bare.Enabled = true
	delete(bare.Fields, "address")
	plain := model.EmptyDoc("linux")
	plain.Inbounds = append(plain.Inbounds, bare)
	if got := FromDoc(plain, nil); got.Name("172.18.0.1") != Local || got.Name("fdfe:dcba:9876::1") != Local {
		t.Errorf("default TUN addresses: %q %q", got.Name("172.18.0.1"), got.Name("fdfe:dcba:9876::1"))
	}

	// A disabled TUN still names the history it left.
	tun.Enabled = false
	if got := FromDoc(doc, nil).Name("172.18.0.1"); got != Local {
		t.Errorf("disabled TUN: %q", got)
	}
}
