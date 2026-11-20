package subscriptions

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// subscriptionHeaders are the request headers of a subscription fetch;
// a subscription's own User-Agent replaces the default one.
var subscriptionHeaders = map[string]string{
	"User-Agent": "clash-verge/v2.5.4",
	"Accept":     "text/plain, application/octet-stream, application/json;q=0.9, */*;q=0.8",
}

// subEditor is the dialog adding or editing a subscription and fetching
// its nodes. It edits a copy: "Update subscription" keeps the fetched
// body, traffic and nodes in the copy too, and only Save writes them to the document; closing drops them.
type subEditor struct {
	w     *Workspace
	open  bool
	add   bool // titled "Add subscription", without "Delete subscription"
	draft model.Subscription
	// fetched is the last successful fetch, applied on save; nodes is
	// how many nodes the subscription then has.
	fetched *fetchOutcome
	nodes   int
	// prefix and chain are the document's, to know what saving changes.
	prefix string
	chain  []string

	busy    bool
	msg     string
	request int
	cancel  context.CancelFunc
	remove  kit.ConfirmState
}

func (e *subEditor) openAdd(doc *model.Doc) {
	e.close()
	e.reset()
	e.open, e.add = true, true
	e.draft = model.Subscription{ID: model.NewID("sub"), Tag: fmt.Sprintf("订阅 %d", len(doc.Subscriptions)+1), Enabled: true}
	e.chain = nil
	e.w.store.BeginEdit()
}

func (e *subEditor) openEdit(doc *model.Doc, id string) {
	sub := findSubscription(doc, id)
	if sub == nil {
		return
	}
	e.close()
	e.reset()
	e.open, e.add = true, false
	e.draft = model.Clone(*sub)
	e.prefix, e.chain = sub.NamePrefix, slices.Clone(sub.Chain)
	e.w.store.BeginEdit()
}

func (e *subEditor) reset() {
	e.cancelFetch()
	e.msg, e.remove, e.fetched = "", kit.ConfirmState{}, nil
}

// cancelFetch forgets the fetch in flight.
func (e *subEditor) cancelFetch() {
	e.request++
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.busy = false
}

func (e *subEditor) close() {
	if !e.open {
		return
	}
	e.cancelFetch()
	e.open = false
	e.w.store.EndEdit()
}

// write puts the draft into doc: a new subscription is added, an existing
// one takes the draft's fields, renaming its nodes for a new prefix and
// moving them to a new proxy chain.
func (e *subEditor) write(doc *model.Doc) {
	draft := e.draft
	draft.Tag = strings.TrimSpace(draft.Tag)
	draft.Chain, draft.Detour = slices.Clone(draft.Chain), model.Ref{}
	sub := findSubscription(doc, draft.ID)
	if sub == nil {
		copied := draft
		doc.Subscriptions = append(doc.Subscriptions, &copied)
		sub = &copied
	} else {
		sub.Tag, sub.URL, sub.UserAgent = draft.Tag, draft.URL, draft.UserAgent
		sub.NamePrefix, sub.Chain, sub.Detour = draft.NamePrefix, draft.Chain, model.Ref{}
		sub.Raw, sub.Traffic, sub.UpdatedAt = draft.Raw, draft.Traffic, draft.UpdatedAt
		if draft.NamePrefix != e.prefix {
			retagNodes(doc, sub.ID, draft.NamePrefix)
		}
		if !slices.Equal(draft.Chain, e.chain) {
			setNodesChain(doc, sub.ID, draft.Chain)
		}
	}
	if e.fetched != nil {
		applyNodes(doc, sub.ID, e.fetched.result, e.fetched.skipped, nil, draft.UpdatedAt)
	}
	sub.LastError = draft.LastError
}

// committed records that the document now has the draft.
func (e *subEditor) committed() {
	e.fetched = nil
	e.prefix, e.chain = e.draft.NamePrefix, slices.Clone(e.draft.Chain)
}

func (e *subEditor) save() {
	if e.busy {
		e.w.env.Toast("请等待订阅更新完成")
		return
	}
	if strings.TrimSpace(e.draft.Tag) == "" {
		e.w.env.Toast("请填写订阅名称")
		return
	}
	if u, err := url.Parse(strings.TrimSpace(e.draft.URL)); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		e.w.env.Toast("请输入有效的 HTTP(S) 订阅链接")
		return
	}
	doc := e.w.store.Doc()
	var owners []string
	for _, o := range subscriptionNodes(doc, e.draft.ID) {
		owners = append(owners, o.ID)
	}
	if issue, ok := model.ChainError(doc, owners, e.draft.Chain); !ok {
		e.w.env.Toast("代理链：" + issue.Message)
		return
	}
	if !e.w.update(e.write) {
		return
	}
	e.committed()
	e.close()
}

// fetch downloads the subscription through the server and converts it,
// saving the draft with the nodes found.
func (e *subEditor) fetch() {
	requested := strings.TrimSpace(e.draft.URL)
	if requested == "" {
		e.msg = "请先填写订阅链接"
		return
	}
	if strings.TrimSpace(e.draft.Tag) == "" {
		e.msg = "请先填写订阅名称"
		return
	}
	e.cancelFetch()
	e.request++
	request := e.request
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.busy, e.msg = true, "拉取中…"
	userAgent := e.draft.UserAgent
	client := e.w.env.API
	env.Go(e.w.env, func(context.Context) (fetchOutcome, error) {
		return fetchSubscription(ctx, client, requested, userAgent)
	}, func(out fetchOutcome, err error) {
		if request != e.request || ctx.Err() != nil {
			return
		}
		e.cancel, e.busy = nil, false
		cancel()
		if strings.TrimSpace(e.draft.URL) != requested {
			if err == nil {
				e.msg = "订阅链接已变化，请重新更新"
			}
			return
		}
		id := e.draft.ID
		if out.fetched {
			e.draft.Raw, e.draft.Traffic = out.text, out.traffic
		}
		if err != nil {
			failure := out.phase + "失败（" + err.Error() + "）"
			e.draft.LastError, e.msg = failure, failure
			return
		}
		e.fetched = &out
		if len(out.result.Nodes) > 0 {
			e.draft.UpdatedAt, e.draft.LastError = time.Now().UnixMilli(), ""
		}
		// Preview the outcome on a copy of the document; saving applies it.
		work := model.Clone(e.w.store.Doc())
		e.write(work)
		e.msg = applyNodes(work, id, out.result, out.skipped, nil, e.draft.UpdatedAt)
		e.nodes = len(subscriptionNodes(work, id))
	})
}

// fetchOutcome is what a fetch brings back.
type fetchOutcome struct {
	phase   string
	fetched bool
	text    string
	traffic *model.Traffic
	result  subscription.Result
	skipped int
}

func (e *subEditor) delete(done func(error)) {
	e.cancelFetch()
	id := e.draft.ID
	if err := e.w.store.Update(func(doc *model.Doc) { removeSubscription(doc, id) }); err != nil {
		done(err)
		return
	}
	done(nil)
	e.close()
}

func (e *subEditor) view(c *ui.Context) {
	if !e.open {
		return
	}
	k := kit.T(c)
	doc := e.w.store.Doc()
	nodes := subscriptionNodes(doc, e.draft.ID)
	nodeCount := len(nodes)
	if e.fetched != nil {
		nodeCount = e.nodes
	}
	title := "编辑订阅"
	if e.add {
		title = "添加订阅"
	}
	open := true
	kit.Dialog(c, &open, title, 600, func() {
		ui.Grid(c).Columns(2).Gap(12).FillWidth().Shrink(0).Children(func() {
			ui.Column(c).Gap(5).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).MinWidth(0).Children(func() {
					fieldLabel(c, "订阅名称")
					if t := e.draft.Traffic; t != nil {
						used := subscription.FormatTraffic(t.Upload + t.Download)
						text := used + " / " + subscription.FormatTraffic(t.Total)
						if d := subscription.FormatExpireDate(t.Expire); d != "" {
							text += " · " + d + " 到期"
						}
						ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").SingleLine().
							Ellipsis("…").MinWidth(0)
					}
				})
				kit.Input(c, "tag", &e.draft.Tag, "").Label("订阅名称")
			})
			ui.Column(c).Gap(5).MinWidth(0).Children(func() {
				fieldLabel(c, "统一前缀")
				kit.Input(c, "prefix", &e.draft.NamePrefix, "例如 tass-").Label("统一前缀")
			})
		})
		owners := make([]string, len(nodes))
		for i, o := range nodes {
			owners[i] = o.ID
		}
		forms.ChainEditor(c, "chain", doc, &e.draft.Chain, owners, "各节点")
		ui.Column(c).Gap(5).Shrink(0).Children(func() {
			fieldLabel(c, "订阅链接")
			ui.Row(c).Gap(8).Children(func() {
				ui.Box(c).Grow(1).MinWidth(0).Children(func() {
					kit.Input(c, "url", &e.draft.URL, "https://example.com/sub?token=...").Label("订阅链接")
				})
				label := "更新订阅"
				if e.busy {
					label = "更新中…"
				}
				kit.Button(c, label, kit.ButtonOptions{Disabled: e.busy}).OnClick(e.fetch)
			})
			if e.msg != "" {
				color := k.Success
				if strings.Contains(e.msg, "失败") || strings.Contains(e.msg, "没有") {
					color = k.AccentText
				}
				ui.Text(c, e.msg).FontSize(kit.FontTable).TextColor(color).Wrap().Margin(3, 0, 0)
			}
		})
		ui.Column(c).Gap(5).Shrink(0).Children(func() {
			fieldLabel(c, "User-Agent")
			kit.Input(c, "ua", &e.draft.UserAgent, "clash-verge/v2.5.4").Label("User-Agent")
		})
		ui.Row(c).Gap(12).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Shrink(0).Children(func() {
			ui.Row(c).Gap(6).Grow(1).MinWidth(0).Children(func() {
				kit.Badge(c, fmt.Sprintf("%d 个节点", nodeCount), kit.SuccessTone)
				if e.draft.UpdatedAt > 0 {
					ui.Text(c, "· 更新于 "+timefmt.FormatMilli(e.draft.UpdatedAt, "2006/1/2 15:04:05")).
						FontSize(kit.FontTable).TextColor(k.Label3).SingleLine()
				}
			})
			if !e.add {
				del := kit.Button(c, "删除订阅", kit.ButtonOptions{Variant: kit.Danger, Small: true})
				if del.Clicked() {
					e.remove.Open = true
				}
				kit.ConfirmPopover(c, del, &e.remove, kit.ConfirmOptions{
					Title: "删除「" + e.draft.Tag + "」？", Message: fmt.Sprintf("该订阅及其 %d 个节点都会被删除。", len(nodes)),
					Confirm: "删除", Danger: true, Run: e.delete,
				})
			}
		})
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(e.close)
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary}).OnClick(e.save)
	})
	if !open {
		e.close()
	}
}
