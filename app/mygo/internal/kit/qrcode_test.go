package kit

import (
	"strings"
	"testing"
)

func qrRows(n int, dark func(x, y int) bool) []string {
	rows := make([]string, n)
	for y := range n {
		var b strings.Builder
		for x := range n {
			if dark(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return rows
}

func TestParseQR(t *testing.T) {
	rows := qrRows(25, func(x, y int) bool { return x == y })
	m, ok := ParseQR(rows)
	if !ok || m.Size != 25 || !m.Cells[3][3] || m.Cells[3][4] {
		t.Fatalf("parse: %v %+v", ok, m)
	}
	for name, bad := range map[string][]string{
		"empty":      nil,
		"too small":  qrRows(20, func(int, int) bool { return true }),
		"too large":  qrRows(178, func(int, int) bool { return true }),
		"not square": append(append([]string{}, rows[:24]...), rows[24]+"0"),
		"bad digit":  append(append([]string{}, rows[:24]...), strings.Repeat("2", 25)),
	} {
		if _, ok := ParseQR(bad); ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestQRModuleSize(t *testing.T) {
	cases := []struct {
		modules int
		want    float32
	}{{21, 6}, {29, 4}, {37, 3}, {57, 2}}
	for _, tc := range cases {
		got := QRModuleSize(QRSize, tc.modules)
		if got != tc.want || got != float32(int(got)) {
			t.Errorf("%d modules: %v, want %v", tc.modules, got, tc.want)
		}
		if side := got * float32(tc.modules+2*QRQuietZone); side > QRSize {
			t.Errorf("%d modules: tile %v exceeds %d", tc.modules, side, QRSize)
		}
	}
}
