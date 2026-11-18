package emit_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func BenchmarkEmit(b *testing.B) {
	d := model.EmptyDoc("linux")
	for i := 0; i < 300; i++ {
		o := model.MakeOutbound("vless", "")
		o.Fields["server"] = "example.com"
		o.Fields["server_port"] = float64(443)
		d.Outbounds = append(d.Outbounds, o)
	}
	for _, typ := range model.InboundTypes {
		d.Inbounds = append(d.Inbounds, model.MakeInbound(typ, "", "linux", false))
	}
	d = model.FillDefaults(d)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emit.Emit(d, emit.Options{})
	}
}
