package forms

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// NetworkConfigSelect chooses, for each library slot of fields (general
// network, control connection, tunnel connection and advanced TLS configs),
// the shared config the entity uses, "Core default" for none, and
// summarizes what it sets. values are the entity's own; onChange receives
// the new references.
func NetworkConfigSelect(c *ui.Context, key any, fields []schema.Field, values map[string]any, refs model.NetworkConfigRefs,
	library []*model.NetworkConfig, platform string, onChange func(model.NetworkConfigRefs)) ui.Element {
	k := kit.T(c)
	effective := model.EffectiveNetworkFields(fields, values, refs, library, platform)
	problems := model.NetworkReferenceErrors(fields, refs, library, "当前项目")
	return ui.Column(c.Key(key)).Gap(12).Shrink(0).Children(func() {
		for _, slot := range orderedSlots(fields) {
			id := refs[slot]
			config := findNetworkConfig(library, id)
			var summary []string
			for _, f := range fields {
				if s, key, ok := model.NetworkField(f); ok && s == slot {
					if v, ok := effective[f.Key]; ok && v != nil {
						summary = append(summary, model.NetworkLabel(key)+" "+model.NetworkValueSummary(f.Key, v))
					}
				}
			}
			options := []kit.Option{{Value: "", Label: "核心默认"}}
			kind := model.NetworkSlotKind(fields, slot)
			for _, cfg := range library {
				if model.ConfigKind(cfg) == kind {
					options = append(options, kit.Option{Value: cfg.ID, Label: cfg.Name})
				}
			}
			label := model.NetworkSlotLabel(slot)
			ui.Column(c.Key(slot)).Gap(4).Children(func() {
				ui.Row(c).Gap(12).Children(func() {
					ui.Text(c, label).FontSize(kit.FontBody).Grow(1).MinWidth(0)
					value := id
					if kit.Select(c, "select", &value, options, kit.SelectOptions{Width: 200, Label: label, Placeholder: "核心默认"}).Changed() {
						next := model.NetworkConfigRefs{}
						for k, v := range refs {
							next[k] = v
						}
						if value == "" {
							delete(next, slot)
						} else {
							next[slot] = value
						}
						onChange(next)
					}
				})
				help := strings.Join(summary, " · ")
				switch {
				case id != "" && config == nil:
					help = "配置已缺失，请重新选择"
				case help == "":
					help = "使用核心默认网络参数"
				}
				ui.Text(c, help).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
				for _, problem := range problems {
					kit.FormError(c, problem)
				}
			})
		}
	})
}

// orderedSlots are the library slots of fields, TLS last.
func orderedSlots(fields []schema.Field) []string {
	slots := model.NetworkSlots(fields)
	slices.SortStableFunc(slots, func(a, b string) int {
		switch {
		case a == "tls" && b != "tls":
			return 1
		case b == "tls" && a != "tls":
			return -1
		}
		return 0
	})
	return slots
}

func findNetworkConfig(library []*model.NetworkConfig, id string) *model.NetworkConfig {
	for _, c := range library {
		if c.ID == id {
			return c
		}
	}
	return nil
}
