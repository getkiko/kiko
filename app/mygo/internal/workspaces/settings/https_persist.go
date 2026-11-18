package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Presets of the stored copy; the segmented controls choose among them.
var (
	persistMBChoices      = []int{64, 256, 1024}
	persistRecordsChoices = []int{5000, 20000, 100000}
	persistDaysChoices    = []int{1, 7, 30}
)

// persistState is the state of Capture Storage on the HTTPS page.
type persistState struct {
	// caps and the counters come from the core; nil until read.
	caps *api.HTTPCapabilities
	// offConfirm asks what happens to the stored data when the copy is
	// switched off; offDelete is its choice (0 keep, 1 delete).
	offConfirm kit.ConfirmState
	offDelete  int
	purgeAll   kit.ConfirmState
	redact     hostField
}

// loadPersist reads the counters of the stored copy.
func (p *httpsPage) loadPersist() {
	env.Go(p.w.env, func(ctx context.Context) (api.HTTPCapabilities, error) {
		return p.w.env.API.HTTPCapabilities(ctx)
	}, func(caps api.HTTPCapabilities, err error) {
		if err == nil {
			p.persist.caps = &caps
		}
	})
}

// persistOf reads the stored settings, defaults when absent.
func persistOf(m model.MITMSettings) model.MITMPersist {
	if m.Persist == nil {
		return model.MITMPersist{}
	}
	return *m.Persist
}

func editPersist(m *model.MITMSettings, fn func(p *model.MITMPersist)) {
	if m.Persist == nil {
		m.Persist = &model.MITMPersist{}
	}
	fn(m.Persist)
}

// nearest is the index of the preset closest to value.
func nearest(choices []int, value int) int {
	best := 0
	for i, choice := range choices {
		if abs(choice-value) < abs(choices[best]-value) {
			best = i
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// persistSummary is the data line of the stored copy.
func persistSummary(st *api.HTTPStatus) string {
	if st == nil || st.PersistRecords == 0 {
		return "0 条"
	}
	return fmt.Sprintf("%s 条 · %s", kit.Count(st.PersistRecords), kit.Bytes(float64(st.PersistBytes)))
}

// persistGroup is Capture Storage: whether finished exchanges are written to disk,
// its limits and the stored data.
func (p *httpsPage) persistGroup(c *ui.Context) {
	m := mitmSettingsOf(p.w.store.Doc())
	persist := persistOf(m)
	st := p.persist.caps
	var status *api.HTTPStatus
	if st != nil {
		status = st.Status
	}
	unsupported := st != nil && !st.Persist
	kit.SettingsGroup(c, "抓取存储", "", func() {
		kit.SettingsRow(c, "保存到磁盘", "", func() {
			on := persist.Enabled
			sw := kit.Switch(c, &on, "保存到磁盘")
			if unsupported && !on {
				sw.Disabled(true)
			}
			if sw.Changed() {
				if on {
					p.w.update(func(d *model.Doc) {
						editMITM(d, func(m *model.MITMSettings) { editPersist(m, func(p *model.MITMPersist) { p.Enabled = true }) })
					})
				} else {
					p.persist.offDelete = 0
					p.persist.offConfirm.Open = true
				}
			}
			kit.ConfirmPopover(c, sw, &p.persist.offConfirm, kit.ConfirmOptions{
				Title: "关闭磁盘保存？",
				Body: func() {
					kit.Segmented(c, "persist-off", &p.persist.offDelete, kit.Segment{Label: "保留数据"}, kit.Segment{Label: "删除数据"}).Label("已保存的数据")
				},
				Confirm: "关闭", Danger: p.persist.offDelete == 1,
				Run: p.switchOff,
			})
		})
		if unsupported {
			ui.Text(c, "当前核心不支持").FontSize(kit.FontCaption).TextColor(kit.T(c).Label3).PaddingY(6)
		}
		if persist.Enabled {
			kit.SettingsRow(c, "正文", "", func() {
				on := persist.Body
				sw := kit.Switch(c, &on, "保存正文到磁盘")
				if !m.SaveBody && !on {
					sw.Disabled(true)
				}
				if sw.Changed() {
					p.w.update(func(d *model.Doc) {
						editMITM(d, func(m *model.MITMSettings) { editPersist(m, func(p *model.MITMPersist) { p.Body = on }) })
					})
				}
			})
			limits := persist.Limits()
			p.choiceRow(c, "容量", "persist-mb", persistMBChoices, limits.MaxMB, []string{"64 MB", "256 MB", "1 GB"},
				func(p *model.MITMPersist, v int) { p.MaxMB = v })
			p.choiceRow(c, "条数", "persist-records", persistRecordsChoices, limits.MaxRecords, []string{"5 千", "2 万", "10 万"},
				func(p *model.MITMPersist, v int) { p.MaxRecords = v })
			p.choiceRow(c, "保留", "persist-days", persistDaysChoices, limits.RetentionDays, []string{"1 天", "7 天", "30 天"},
				func(p *model.MITMPersist, v int) { p.RetentionDays = v })
			p.redactRows(c, persist)
		}
		if persist.Enabled || (status != nil && status.PersistRecords > 0) {
			kit.SettingsRow(c, "已保存", persistSummary(status), func() {
				purge := kit.Button(c, "全部删除", kit.ButtonOptions{Small: true, Icon: kit.SymTrash2, Disabled: status != nil && status.PersistRecords == 0 && !persist.Enabled})
				purge.OnClick(func() { p.persist.purgeAll.Open = true })
				kit.ConfirmPopover(c, purge, &p.persist.purgeAll, kit.ConfirmOptions{
					Title: "删除全部已保存的请求？", Confirm: "全部删除", Danger: true, Run: p.purge,
				})
			})
		}
		if status != nil && persist.Enabled && (status.PersistFailed || status.PersistErrors > 0 || status.PersistDropped > 0) {
			text := ""
			switch {
			case status.PersistFailed:
				text = "存储目录无法打开"
			case status.PersistErrors > 0:
				text = fmt.Sprintf("%d 条写入失败", status.PersistErrors)
			default:
				text = fmt.Sprintf("%d 条因写入过慢被跳过", status.PersistDropped)
			}
			ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, text) })
		}
	})
}

// choiceRow is a row choosing one of the presets of a limit.
func (p *httpsPage) choiceRow(c *ui.Context, label, key string, choices []int, current int, labels []string, set func(p *model.MITMPersist, v int)) {
	kit.SettingsRow(c, label, "", func() {
		selected := nearest(choices, current)
		segments := make([]kit.Segment, len(labels))
		for i, l := range labels {
			segments[i] = kit.Segment{Label: l}
		}
		if kit.Segmented(c, key, &selected, segments...).Label(label).Changed() {
			value := choices[selected]
			p.w.update(func(d *model.Doc) {
				editMITM(d, func(m *model.MITMSettings) { editPersist(m, func(p *model.MITMPersist) { set(p, value) }) })
			})
		}
	})
}

// redactRows lists the extra header names masked before writing.
func (p *httpsPage) redactRows(c *ui.Context, persist model.MITMPersist) {
	limits := persist.Limits()
	field := &p.persist.redact
	var remove string
	for _, name := range limits.RedactHeaders {
		ui.Column(c.Key("redact:" + name)).Children(func() {
			kit.SettingsRow(c, name, "", func() {
				if kit.IconButton(c, kit.SymX, "移除 "+name, kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 14}).Clicked() {
					remove = name
				}
			})
		})
	}
	add := func() {
		name := strings.TrimSpace(field.text)
		switch {
		case name == "":
			return
		case !model.ValidMITMHeaderName(name):
			field.err, field.errAt = "请求头名称无效", time.Now()
		case slices.ContainsFunc(limits.RedactHeaders, func(h string) bool { return strings.EqualFold(h, name) }):
			field.err, field.errAt = "已在列表中", time.Now()
		case len(limits.RedactHeaders) >= model.MITMPersistMaxRedactHeaders:
			field.err, field.errAt = "已达上限", time.Now()
		default:
			field.text, field.err = "", ""
			next := append(slices.Clone(persist.RedactHeaders), name)
			p.w.update(func(d *model.Doc) {
				editMITM(d, func(m *model.MITMSettings) { editPersist(m, func(p *model.MITMPersist) { p.RedactHeaders = next }) })
			})
		}
	}
	kit.SettingsRow(c, "脱敏请求头", "", func() {
		ui.Column(c).Width(200).Children(func() {
			if kit.Input(c, "persist-redact", &field.text, "X-Token").Label("额外脱敏的请求头").Submitted() {
				add()
			}
		})
		kit.Button(c, "添加", kit.ButtonOptions{Small: true, Disabled: field.text == ""}).Label("添加脱敏请求头").OnClick(add)
	})
	if field.err != "" {
		if left := httpsErrorShown - time.Since(field.errAt); left > 0 {
			c.After(left)
			ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, field.err) })
		} else {
			field.err = ""
		}
	}
	if remove != "" {
		p.w.update(func(d *model.Doc) {
			editMITM(d, func(m *model.MITMSettings) {
				editPersist(m, func(p *model.MITMPersist) {
					p.RedactHeaders = slices.DeleteFunc(slices.Clone(p.RedactHeaders), func(h string) bool { return strings.EqualFold(strings.TrimSpace(h), remove) })
				})
			})
		})
	}
}

// switchOff turns the stored copy off, deleting the data first when chosen.
func (p *httpsPage) switchOff(done func(error)) {
	finish := func() {
		done(p.w.store.Update(func(d *model.Doc) {
			editMITM(d, func(m *model.MITMSettings) { editPersist(m, func(p *model.MITMPersist) { p.Enabled = false }) })
		}))
		p.loadPersist()
	}
	if p.persist.offDelete != 1 {
		finish()
		return
	}
	env.Go(p.w.env, func(ctx context.Context) (api.HTTPStatus, error) {
		return p.w.env.API.PurgeHTTPPersisted(ctx)
	}, func(_ api.HTTPStatus, err error) {
		if err != nil {
			done(errors.New(errorText(err, "删除失败")))
			return
		}
		finish()
	})
}

// purge deletes everything stored.
func (p *httpsPage) purge(done func(error)) {
	env.Go(p.w.env, func(ctx context.Context) (api.HTTPStatus, error) {
		return p.w.env.API.PurgeHTTPPersisted(ctx)
	}, func(_ api.HTTPStatus, err error) {
		if err != nil {
			done(errors.New(errorText(err, "删除失败")))
			return
		}
		p.loadPersist()
		done(nil)
	})
}
