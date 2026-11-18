package subscription

import (
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestParseInfo(t *testing.T) {
	got := ParseInfo("upload=1024; download=2048; total=104857600; expire=1780000000")
	want := model.Traffic{Upload: 1024, Download: 2048, Total: 104857600, Expire: 1780000000}
	if got == nil || *got != want {
		t.Errorf("got %+v", got)
	}
	if ParseInfo("  ") != nil {
		t.Error("blank header parsed")
	}
	if got := ParseInfo("upload=-5; total=abc"); got == nil || got.Upload != 0 || got.Total != 0 {
		t.Errorf("invalid numbers %+v", got)
	}
}

func TestFormatTraffic(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{3 * (1 << 30), "3.00 GB"},
		{100 * (1 << 30), "100 GB"},
		{0, "0.00 B"},
		{1536, "1.50 KB"},
		{1000 * (1 << 20), "1000 MB"},
	} {
		if got := FormatTraffic(c.in); got != c.want {
			t.Errorf("FormatTraffic(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatExpireDate(t *testing.T) {
	ts := time.Date(2027, 7, 28, 0, 0, 0, 0, time.Local).Unix()
	if got := FormatExpireDate(float64(ts)); got != "2027-07-28" {
		t.Errorf("got %q", got)
	}
	if FormatExpireDate(0) != "" {
		t.Error("zero expiry formatted")
	}
}
