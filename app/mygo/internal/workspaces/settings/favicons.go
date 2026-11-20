package settings

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/favicon"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// faviconRows are the websites' icons: the outbound the server fetches
// them through, none by default, marked when it does not run; and the
// icons kept, cleared after a confirmation.
func (w *Workspace) faviconRows(c *ui.Context, doc *model.Doc) {
	kit.SettingsRow(c, "网站图标", "", func() {
		if doc.FaviconOutbound.Valid() && favicon.Outbound(doc) == "" {
			kit.Badge(c, "需修正", kit.DangerTone)
		}
		value := doc.FaviconOutbound.ID()
		options := append([]kit.Option{{Value: "", Label: "不加载"}}, forms.OutboundOptions(doc)...)
		if kit.SearchSelect(c, "favicon-outbound", &value, options, kit.SearchSelectOptions{Label: "网站图标", Width: 220}).Changed() {
			w.update(func(d *model.Doc) {
				d.FaviconOutbound = model.Ref{}
				if value != "" {
					d.FaviconOutbound = model.Some(value)
				}
			})
		}
	})
	if w.favicons == nil && !w.faviconLoading && !w.faviconFailed {
		w.loadFavicons()
	}
	kit.SettingsRow(c, "网站图标缓存", faviconCacheText(w.favicons), func() {
		// Even at 0 the cache may hold the marks of sites without icons,
		// which clearing asks again.
		clear := kit.Button(c, "清理", kit.ButtonOptions{Small: true, Disabled: w.favicons == nil || w.faviconConfirm.Busy})
		if clear.Clicked() {
			w.faviconConfirm.Open = true
		}
		message := ""
		if w.favicons != nil {
			message = fmt.Sprintf("将删除 %d 个网站图标，释放约 %s。", w.favicons.Count, formatLogSize(w.favicons.Bytes))
		}
		kit.ConfirmPopover(c, clear, &w.faviconConfirm, kit.ConfirmOptions{Title: "清理网站图标", Message: message,
			Confirm: "清理", Danger: true, Run: w.clearFavicons})
	})
}

// faviconCacheText is the cache's count and size, "" until read.
func faviconCacheText(stats *api.FaviconCache) string {
	if stats == nil {
		return ""
	}
	return fmt.Sprintf("%d 个 · %s", stats.Count, formatLogSize(stats.Bytes))
}

func (w *Workspace) loadFavicons() {
	w.faviconLoading = true
	env.Go(w.env, func(ctx context.Context) (api.FaviconCache, error) {
		return w.env.API.FaviconCacheStats(ctx)
	}, func(stats api.FaviconCache, err error) {
		w.faviconLoading = false
		if err == nil {
			w.favicons = &stats
		} else {
			// Read again as the page shows next.
			w.faviconFailed = true
		}
	})
}

func (w *Workspace) clearFavicons(done func(error)) {
	env.Go(w.env, func(ctx context.Context) (api.FaviconCache, error) {
		return w.env.API.ClearFaviconCache(ctx)
	}, func(cleared api.FaviconCache, err error) {
		if err != nil {
			done(fmt.Errorf("%s", errorText(err, "操作失败")))
			return
		}
		favicon.For(w.env).Clear()
		w.env.Toast(fmt.Sprintf("已清理 %d 个网站图标，释放 %s", cleared.Count, formatLogSize(cleared.Bytes)))
		w.favicons = nil
		done(nil)
	})
}
