package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// Document operations of the workspace, the subscriptions', inbounds' and
// outbounds' part. Each changes the copy that
// Store.Update hands it.

// topLevelKeys are the keys Doc.OutboundOrder orders: manual outbounds'
// ids and "sub:" subscription keys, in their current order.
func topLevelKeys(doc *model.Doc) []string {
	var known []string
	for _, o := range doc.Outbounds {
		if o.SubID == "" {
			known = append(known, o.ID)
		}
	}
	for _, s := range doc.Subscriptions {
		known = append(known, "sub:"+s.ID)
	}
	order := doc.OutboundOrder
	if order == nil {
		order = known
	}
	var out []string
	for _, key := range order {
		if slices.Contains(known, key) {
			out = append(out, key)
		}
	}
	for _, key := range known {
		if !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// reorderTopLevelTo moves the outbounds list's item key to targetKey's
// place (reorderTopLevelOutboundTo).
func reorderTopLevelTo(doc *model.Doc, key, targetKey string) {
	order := topLevelKeys(doc)
	from, to := slices.Index(order, key), slices.Index(order, targetKey)
	if from < 0 || to < 0 || from == to {
		return
	}
	item := order[from]
	order = slices.Delete(order, from, from+1)
	order = slices.Insert(order, to, item)
	doc.OutboundOrder = order
}

// reorderInbound moves inbound id to targetID's place.
func reorderInbound(doc *model.Doc, id, targetID string) {
	if id == targetID {
		return
	}
	from := slices.IndexFunc(doc.Inbounds, func(i *model.Inbound) bool { return i.ID == id })
	to := slices.IndexFunc(doc.Inbounds, func(i *model.Inbound) bool { return i.ID == targetID })
	if from < 0 || to < 0 {
		return
	}
	item := doc.Inbounds[from]
	doc.Inbounds = slices.Delete(doc.Inbounds, from, from+1)
	doc.Inbounds = slices.Insert(doc.Inbounds, to, item)
}

// removeSubscription deletes a subscription with its nodes, pruning
// them from group members, defaults and the outbounds order.
func removeSubscription(doc *model.Doc, id string) { model.RemoveSubscription(doc, id) }

// removeOutbound deletes an outbound, pruning it from group members,
// defaults and the outbounds order.
func removeOutbound(doc *model.Doc, id string) { model.RemoveOutbound(doc, id) }

// removeInbound deletes an inbound, keeping detours to it dangling.
func removeInbound(doc *model.Doc, id string) {
	doc.Inbounds = slices.DeleteFunc(doc.Inbounds, func(i *model.Inbound) bool { return i.ID == id })
}

// putOutbound replaces the outbound with o's id, or appends o.
func putOutbound(doc *model.Doc, o *model.Outbound) {
	if i := slices.IndexFunc(doc.Outbounds, func(x *model.Outbound) bool { return x.ID == o.ID }); i >= 0 {
		doc.Outbounds[i] = o
		return
	}
	doc.Outbounds = append(doc.Outbounds, o)
}

// putInbound replaces the inbound with in's id, or appends in.
func putInbound(doc *model.Doc, in *model.Inbound) {
	if i := slices.IndexFunc(doc.Inbounds, func(x *model.Inbound) bool { return x.ID == in.ID }); i >= 0 {
		doc.Inbounds[i] = in
		return
	}
	doc.Inbounds = append(doc.Inbounds, in)
}

// findSubscription returns the subscription with id, or nil.
func findSubscription(doc *model.Doc, id string) *model.Subscription {
	for _, s := range doc.Subscriptions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// subscriptionNodes are the outbounds fetched by subscription id.
func subscriptionNodes(doc *model.Doc, id string) []*model.Outbound {
	var out []*model.Outbound
	for _, o := range doc.Outbounds {
		if o.SubID == id {
			out = append(out, o)
		}
	}
	return out
}

// retagNodes renames a subscription's nodes for a new name prefix: each
// becomes prefix + its name in the subscription, numbered when another
// outbound has the tag (updateNamePrefix).
func retagNodes(doc *model.Doc, subID, prefix string) {
	taken := map[string]bool{}
	for _, o := range doc.Outbounds {
		if o.SubID != subID {
			taken[o.Tag] = true
		}
	}
	for _, o := range doc.Outbounds {
		if o.SubID != subID {
			continue
		}
		original := jsv.StringOr(o.Fields["__subscription_name"], o.Tag)
		base := prefix + original
		tag := base
		for suffix := 2; taken[tag]; suffix++ {
			tag = fmt.Sprintf("%s %d", base, suffix)
		}
		taken[tag] = true
		o.Tag = tag
		if o.Fields == nil {
			o.Fields = model.Fields{}
		}
		o.Fields["__subscription_name"] = original
	}
}

// setNodesDetour applies a subscription's detour to all its nodes; an
// empty id removes it.
func setNodesDetour(doc *model.Doc, subID, detour string) {
	for _, o := range doc.Outbounds {
		if o.SubID != subID {
			continue
		}
		if o.Fields == nil {
			o.Fields = model.Fields{}
		}
		if detour == "" {
			delete(o.Fields, "detour")
		} else {
			o.Fields["detour"] = detour
		}
	}
}

// applyNodes merges fetched nodes into a subscription's outbounds and
// reports the outcome. Nodes keep the id,
// enabled state and network configs of the node they update
// (matchNodes), so references to them stay; group members and defaults
// whose node disappeared are removed. patch updates the subscription, which also gets updatedAt
// and loses lastError. Without nodes the document stays as it is.
func applyNodes(doc *model.Doc, subID string, result subscription.Result, conversionSkipped int,
	patch func(*model.Subscription), now int64) string {
	skipped := conversionSkipped + result.Skipped
	if len(result.Nodes) == 0 {
		if skipped > 0 {
			return fmt.Sprintf("没有解析到支持的节点（跳过 %d 个）", skipped)
		}
		return "没有解析到任何节点"
	}
	sub := findSubscription(doc, subID)
	if sub != nil {
		old := subscriptionNodes(doc, subID)
		prevs := matchNodes(old, result.Nodes)
		taken := map[string]bool{}
		var others []*model.Outbound
		for _, o := range doc.Outbounds {
			if o.SubID != subID {
				taken[o.Tag] = true
				others = append(others, o)
			}
		}
		fresh := make([]*model.Outbound, 0, len(result.Nodes))
		kept := map[string]bool{}
		for i, node := range result.Nodes {
			prev := prevs[i]
			tag := sub.NamePrefix + node.Name
			if taken[tag] {
				suffix := 2
				for taken[fmt.Sprintf("%s %d", tag, suffix)] {
					suffix++
				}
				tag = fmt.Sprintf("%s %d", tag, suffix)
			}
			taken[tag] = true
			fields := schema.DefaultFields(schema.OutboundSchema[node.Type])
			for k, v := range node.Fields {
				fields[k] = v
			}
			fields["__key"] = node.Key
			fields["__subscription_name"] = node.Name
			if sub.Detour.Valid() {
				fields["detour"] = sub.Detour.ID()
			}
			o := &model.Outbound{Entity: model.Entity{ID: model.NewID("out"), Tag: tag, Enabled: true},
				Kind: "outbound", Type: node.Type, SubID: subID, Fields: fields}
			if prev != nil {
				o.ID, o.Enabled, o.NetworkConfigRefs = prev.ID, prev.Enabled, prev.NetworkConfigRefs
			}
			kept[o.ID] = true
			fresh = append(fresh, o)
		}
		var removed []string
		for _, o := range old {
			if !kept[o.ID] {
				removed = append(removed, o.ID)
			}
		}
		doc.Outbounds = append(others, fresh...)
		model.PruneOutboundMembers(doc, removed...)
		if patch != nil {
			patch(sub)
		}
		sub.UpdatedAt, sub.LastError = now, ""
	}
	msg := fmt.Sprintf("解析到 %d 个节点", len(result.Nodes))
	if skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 个不支持的协议", skipped)
	}
	return msg
}

// fetchSubscription downloads the subscription at link through the
// server, userAgent replacing the default User-Agent unless empty, and
// converts it. The outcome tells the phase that failed, and keeps the
// body and traffic of a download that converted to nothing.
func fetchSubscription(ctx context.Context, client *api.Client, link, userAgent string) (fetchOutcome, error) {
	headers := map[string]string{}
	for k, v := range subscriptionHeaders {
		headers[k] = v
	}
	if ua := strings.TrimSpace(userAgent); ua != "" {
		headers["User-Agent"] = ua
	}
	var out fetchOutcome
	res, err := client.Fetch(ctx, link, headers)
	if err == nil && (res.Status < 200 || res.Status >= 300) {
		err = errors.New(res.Body)
		if strings.TrimSpace(res.Body) == "" {
			err = fmt.Errorf("HTTP %d", res.Status)
		}
	}
	if err != nil {
		out.phase = "拉取"
		return out, err
	}
	out.fetched, out.text, out.traffic = true, res.Body, subscription.ParseInfo(res.SubscriptionUserinfo)
	out.phase = "转换"
	conv, err := subscription.Convert(res.Body)
	if err != nil {
		return out, err
	}
	parsed, ok := subscription.ParseSingBoxConfig(conv.Config)
	if !ok {
		return out, errors.New("转换结果缺少有效的 sing-box outbounds")
	}
	out.result, out.skipped = parsed, conv.Skipped
	return out, nil
}

// refreshSubscription fetches subscription id again and saves what it
// brings, as "Update subscription" then Save in its editor would: the body and traffic
// downloaded, the nodes (applyNodes), or the failure in LastError. One
// refresh of a subscription runs at a time (subsSection.refreshing); a
// subscription gone or given another link meanwhile keeps what it has.
func refreshSubscription(w *Workspace, id string) {
	sub := findSubscription(w.store.Doc(), id)
	if sub == nil || w.subs.refreshing[id] {
		return
	}
	requested := strings.TrimSpace(sub.URL)
	if requested == "" {
		w.env.Toast("请先填写订阅链接")
		return
	}
	if w.subs.refreshing == nil {
		w.subs.refreshing = map[string]bool{}
	}
	w.subs.refreshing[id] = true
	client, userAgent := w.env.API, sub.UserAgent
	env.Go(w.env, func(ctx context.Context) (fetchOutcome, error) {
		return fetchSubscription(ctx, client, requested, userAgent)
	}, func(out fetchOutcome, err error) {
		delete(w.subs.refreshing, id)
		if x := findSubscription(w.store.Doc(), id); x == nil || strings.TrimSpace(x.URL) != requested {
			return
		}
		msg := ""
		saved := w.update(func(d *model.Doc) {
			x := findSubscription(d, id)
			if x == nil {
				return
			}
			if out.fetched {
				x.Raw, x.Traffic = out.text, out.traffic
			}
			if err != nil {
				x.LastError = out.phase + "失败（" + err.Error() + "）"
				return
			}
			msg = applyNodes(d, id, out.result, out.skipped, nil, time.Now().UnixMilli())
		})
		// A save that failed toasted its error; the nodes it brought are
		// not kept.
		if saved {
			w.env.Toast(msg)
		}
	})
}

// matchNodes pairs each fetched node with the stored node it updates, or
// nil for a new one. A stored node is used once: by the first fetched
// node with its key, else, when the key changed (a rotated password, a
// new port), by a fetched node of the same type and name. Each stored
// node keeps one identity, so duplicate keys in a fetch do not share an
// id.
func matchNodes(old []*model.Outbound, nodes []subscription.Node) []*model.Outbound {
	prevs := make([]*model.Outbound, len(nodes))
	claimed := map[*model.Outbound]bool{}
	byKey := map[string][]*model.Outbound{}
	for _, o := range old {
		key := jsv.StringOr(o.Fields["__key"], "")
		byKey[key] = append(byKey[key], o)
	}
	for i, node := range nodes {
		for _, o := range byKey[node.Key] {
			if !claimed[o] {
				prevs[i], claimed[o] = o, true
				break
			}
		}
	}
	for i, node := range nodes {
		if prevs[i] != nil {
			continue
		}
		for _, o := range old {
			if !claimed[o] && o.Type == node.Type && jsv.StringOr(o.Fields["__subscription_name"], "") == node.Name {
				prevs[i], claimed[o] = o, true
				break
			}
		}
	}
	return prevs
}

// importedOutbounds turns share links read from the clipboard into
// outbounds with tags unique among the document's
// (importFromClipboard).
func importedOutbounds(doc *model.Doc, nodes []subscription.Node) []*model.Outbound {
	var taken []string
	for _, o := range doc.Outbounds {
		taken = append(taken, o.Tag)
	}
	out := make([]*model.Outbound, 0, len(nodes))
	for _, node := range nodes {
		base := node.Name
		if base == "" {
			base = node.Type
		}
		tag := model.UniqueTag(base, taken)
		taken = append(taken, tag)
		o := model.MakeOutbound(node.Type, tag)
		for k, v := range node.Fields {
			o.Fields[k] = v
		}
		out = append(out, o)
	}
	return out
}
