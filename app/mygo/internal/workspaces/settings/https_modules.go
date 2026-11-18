package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// moduleHostRow is a hostname an enabled module adds to the include or
// exclude list, shown below the user's own and read-only.
type moduleHostRow struct {
	Host, ModuleID, ModuleName string
	// Unconfirmed marks an include host the user has not confirmed: it is
	// not in force.
	Unconfirmed bool
	// Overridden marks an include host an exclude pattern covers: exclusion
	// wins over inclusion, so it is not decrypted.
	Overridden bool
}

// moduleHostRows lists the hosts the enabled modules add to the include
// (exclude false) or exclude list, in module order. The user's exclude
// list and the modules' exclude hosts decide Overridden.
func moduleHostRows(doc *model.Doc, exclude bool) []moduleHostRow {
	all := model.ModuleHosts(doc)
	if len(all) == 0 {
		return nil
	}
	excluded := append([]string(nil), hostList(mitmSettingsOf(doc), true)...)
	for _, h := range all {
		if h.Enabled && h.Exclude {
			excluded = append(excluded, h.Host)
		}
	}
	var rows []moduleHostRow
	for _, h := range all {
		if !h.Enabled || h.Exclude != exclude {
			continue
		}
		row := moduleHostRow{Host: h.Host, ModuleID: h.ModuleID, ModuleName: h.ModuleName, Unconfirmed: !exclude && !h.Approved}
		if !exclude {
			for _, p := range excluded {
				if mitmpolicy.MatchHost(p, h.Host) {
					row.Overridden = true
					break
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// moduleHostLine is a module's host row: the host in secondary text, the
// module and its state beside it, no remove button.
func (p *httpsPage) moduleHostLine(c *ui.Context, row moduleHostRow) {
	k := kit.T(c)
	ui.Column(c.Key("module:" + row.ModuleID + ":" + row.Host)).Children(func() {
		kit.SettingsRow(c, row.Host, "", func() {
			switch {
			case row.Unconfirmed:
				kit.Badge(c, "待确认", kit.WarningTone)
			case row.Overridden:
				kit.Badge(c, "被排除", kit.Neutral)
			}
			ui.Row(c).Gap(5).Shrink(0).Children(func() {
				kit.Icon(c, kit.SymLayers, 13).TextColor(k.Label3)
				kit.Clipped(c, row.ModuleName, kit.FontCaption, 400).TextColor(k.Label3).MaxWidth(160)
			})
			kit.IconButton(c, kit.SymArrowRight, "前往模块 "+row.ModuleName, kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 14}).
				OnClick(func() { p.w.env.Navigate(env.PageRewrites, "module:"+row.ModuleID) })
		})
	})
}
