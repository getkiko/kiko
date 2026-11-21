package forms

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

// OutboundSelectOptions describe an OutboundSelect.
type OutboundSelectOptions struct {
	// Label names the choice, as "Target outbound".
	Label string
	// Lead are the choices before the outbounds, as "Not loaded" or "Core
	// default" for "".
	Lead []kit.Option
	// Exclude leaves these outbounds out (the entity itself).
	Exclude []string
	// Only, when set, keeps the outbounds it reports true for.
	Only func(id string) bool
	// Tags makes the values runtime tags (id → tag) rather than ids;
	// outbounds without one are left out.
	Tags map[string]string
	// Width fixes the trigger's width; it fills the row when 0.
	Width float32
	// Disabled greys the choice; NoTargets offers only Lead.
	Disabled, NoTargets bool
}

// TargetOptions are the outbounds and endpoints a reference may choose,
// as every outbound choice shows them: enabled ones (nodes only of
// enabled subscriptions) in the outbounds' order: the name, the protocol
// tag and the server address, as a subscription's node row shows them.
func TargetOptions(doc *model.Doc, exclude ...string) []kit.Option {
	if doc == nil {
		return nil
	}
	subs := map[string]*model.Subscription{}
	for _, s := range doc.Subscriptions {
		subs[s.ID] = s
	}
	var out []kit.Option
	for _, t := range model.AllOutboundTargets(doc) {
		if !t.Enabled() || slices.Contains(exclude, t.ID()) {
			continue
		}
		if t.Outbound != nil && t.Outbound.SubID != "" {
			if s := subs[t.Outbound.SubID]; s == nil || !s.Enabled {
				continue
			}
		}
		out = append(out, kit.Option{Value: t.ID(), Label: t.Tag(), Tag: nodeview.ProtocolTag(t.Type()), Detail: targetAddress(t)})
	}
	return out
}

// staleTarget is the option of a chosen outbound no longer offered: its
// name when it is only disabled, marked for fixing.
func staleTarget(doc *model.Doc, id string) kit.Option {
	if doc != nil {
		for _, t := range model.AllOutboundTargets(doc) {
			if t.ID() == id {
				detail := "需修正"
				if !t.Enabled() {
					detail = "已关闭 · 需修正"
				}
				return kit.Option{Value: id, Label: t.Tag(), Detail: detail}
			}
		}
	}
	return kit.Option{Value: id, Label: id, Detail: "需修正引用"}
}

// OutboundSelect is the one searchable choice of an outbound or endpoint,
// used wherever one is chosen: the name, its protocol tag and address in
// the list, searchable by any of them; a choice no longer offered stays,
// marked for fixing. It returns the trigger, whose Changed reports a new
// *value.
func OutboundSelect(c *ui.Context, key any, doc *model.Doc, value *string, o OutboundSelectOptions) ui.Element {
	options := slices.Clone(o.Lead)
	if !o.NoTargets {
		for _, t := range TargetOptions(doc, o.Exclude...) {
			if o.Only != nil && !o.Only(t.Value) {
				continue
			}
			if o.Tags != nil {
				if t.Value = o.Tags[t.Value]; t.Value == "" {
					continue
				}
				// The runtime tag tells apart outbounds of one name
				// ("香港", "香港 2").
				t.Label = t.Value
			}
			options = append(options, t)
		}
	}
	if v := *value; v != "" && !slices.ContainsFunc(options, func(x kit.Option) bool { return x.Value == v }) {
		stale := staleTarget(doc, v)
		if o.Tags != nil {
			stale = kit.Option{Value: v, Label: v, Detail: "需修正引用"}
		}
		options = append(options, stale)
	}
	return kit.SearchSelect(c, key, value, options, kit.SearchSelectOptions{
		Label: o.Label, Placeholder: "搜索出口名称、协议或地址", Width: o.Width, Disabled: o.Disabled,
	})
}
