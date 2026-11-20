package subscriptions

import (
	"context"
	"runtime"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// browserItems are the menu items that open a browser going through
// outbound o alone, to use or check the web as it reaches it: the built-in
// browser on macOS, and Chrome. Outbounds that forward nothing (block,
// dns) have none.
func (s *outboundsSection) browserItems(o *model.Outbound) []kit.EntityMenuItem {
	switch o.Type {
	case "block", "dns":
		return nil
	}
	id := o.ID
	open := func(engine string) func(ui.Element) {
		return func(ui.Element) { s.openBrowser(id, engine) }
	}
	var items []kit.EntityMenuItem
	if runtime.GOOS == "darwin" {
		items = append(items, kit.EntityMenuItem{Label: "用此出站打开浏览器", Icon: kit.SymGlobe, Disabled: !o.Enabled, Run: open("webview")})
	}
	return append(items, kit.EntityMenuItem{Label: "用 Chrome 打开", Icon: kit.SymExternalLink, Disabled: !o.Enabled, Run: open("chrome")})
}

// openBrowser opens the browser of engine through outbound id, by the tag
// the core runs it with; a failure shows as a toast.
func (s *outboundsSection) openBrowser(id, engine string) {
	e := s.w.env
	tag := emit.EmittedTags(s.w.store.Doc()).Outbounds[id]
	if tag == "" {
		e.Toast("该出站未启用，无法打开浏览器")
		return
	}
	e.Run(func(ctx context.Context) error { return e.API.BrowserOpen(ctx, tag, engine, "") }, func(err error) {
		if err != nil {
			e.Toast("打开浏览器失败：" + err.Error())
		}
	})
}
