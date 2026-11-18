package endpoints

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// editor adds or edits an endpoint or a USB/IP service on a copy, saved
// as one change. Tailscale endpoints the USB/IP guide or wizard creates
// meanwhile are saved with it.
type editor struct {
	s *Section

	open     bool
	isNew    bool
	endpoint *model.Endpoint
	service  *model.Service
	// pending are endpoints created while editing.
	pending    []*model.Endpoint
	generation int
	formValid  bool

	guide   guideView
	devices devicesField
	udpPath udpPathField
	login   tailscaleLogin
}

func (e *editor) begin() {
	if !e.open {
		e.s.store.BeginEdit()
	}
	e.open = true
	e.formValid = true
	e.generation++
	e.guide = guideView{}
	e.devices = devicesField{}
	e.udpPath = udpPathField{}
	e.login = tailscaleLogin{}
}

func (e *editor) draftID() string {
	switch {
	case !e.open:
		return ""
	case e.service != nil:
		return e.service.ID
	case e.endpoint != nil:
		return e.endpoint.ID
	}
	return ""
}

// addsTailscale reports whether saving pending and endpoint would add a
// Tailscale endpoint to a document that already has one.
func addsTailscale(doc *model.Doc, pending []*model.Endpoint, endpoint *model.Endpoint) bool {
	added := append([]*model.Endpoint(nil), pending...)
	if endpoint != nil && !slices.ContainsFunc(doc.Endpoints, func(x *model.Endpoint) bool { return x.ID == endpoint.ID }) {
		added = append(added, endpoint)
	}
	count := len(tailscaleEndpoints(doc, false))
	for _, x := range added {
		if x.Type == "tailscale" {
			if count > 0 {
				return true
			}
			count++
		}
	}
	return false
}

// editDoc is the document with the endpoints created while editing.
func (e *editor) editDoc(doc *model.Doc) *model.Doc {
	if len(e.pending) == 0 {
		return doc
	}
	next := *doc
	next.Endpoints = append(append([]*model.Endpoint(nil), doc.Endpoints...), e.pending...)
	return &next
}

// addEndpoint opens a new endpoint of typ, unless the document refuses
// it (a second Tailscale endpoint), and reports whether it did.
func (e *editor) addEndpoint(doc *model.Doc, typ string) bool {
	if problem := model.CanAddEndpoint(doc, typ); problem != "" {
		e.s.env.Toast(problem)
		return false
	}
	e.begin()
	e.isNew, e.service, e.pending = true, nil, nil
	tag := model.UniqueTag(typ, allTags(doc))
	if typ == "tailscale" {
		tag = model.TailscaleTagFor(doc, "")
	}
	e.endpoint = model.MakeEndpoint(typ, tag, doc.IPv6Enabled)
	return true
}

// quickAddTailscale adds a Tailscale endpoint that logs in by link, named
// as Tailscale names the server's system hostname, and opens its login.
func (e *editor) quickAddTailscale(doc *model.Doc) {
	if !e.addEndpoint(doc, "tailscale") {
		return
	}
	e.login.show(e.endpoint.Fields)
}

func (e *editor) addService(doc *model.Doc, typ string) {
	platform := e.s.platform(doc)
	if !schema.ServiceAvailable(typ, platform) {
		e.s.env.Toast(schema.ServiceUnavailableReason(typ, platform))
		return
	}
	var taken []string
	for _, x := range doc.Services {
		taken = append(taken, x.Tag)
	}
	e.begin()
	e.isNew, e.endpoint, e.pending = true, nil, nil
	e.service = model.MakeService(typ, model.UniqueTag(typ, taken))
}

// createFromPlan opens the wizard's service, with its new endpoint.
func (e *editor) createFromPlan(plan usbIPPlan) {
	e.begin()
	e.isNew, e.endpoint, e.pending = true, nil, nil
	if plan.Endpoint != nil {
		e.pending = []*model.Endpoint{plan.Endpoint}
	}
	e.service = plan.Service
}

func (e *editor) edit(kind, id string) {
	doc := e.s.store.Doc()
	if kind == kindService {
		for _, x := range doc.Services {
			if x.ID == id {
				e.begin()
				e.isNew, e.endpoint, e.pending, e.service = false, nil, nil, model.Clone(x)
			}
		}
		return
	}
	for _, x := range doc.Endpoints {
		if x.ID == id {
			e.begin()
			e.isNew, e.service, e.pending, e.endpoint = false, nil, nil, model.Clone(x)
		}
	}
}

func (e *editor) close() {
	if e.open {
		e.s.store.EndEdit()
	}
	e.open, e.endpoint, e.service, e.pending = false, nil, nil, nil
	if e.s.confirm.inEditor {
		e.s.confirm = confirmation{}
	}
}

// resetDraft restores the draft's default fields.
func (e *editor) resetDraft() {
	doc := e.s.store.Doc()
	if e.service != nil {
		e.service.Fields = model.MakeService(e.service.Type, e.service.Tag).Fields
	} else if e.endpoint != nil {
		e.endpoint.Fields = model.MakeEndpoint(e.endpoint.Type, e.endpoint.Tag, doc.IPv6Enabled).Fields
	}
	e.generation++
}

func (e *editor) fullSchema() []schema.Field {
	if e.service != nil {
		return schema.ServiceSchema[e.service.Type]
	}
	return schema.EndpointSchema[e.endpoint.Type]
}

func withoutDevices(fields []schema.Field) []schema.Field {
	return withoutField(fields, "devices")
}

// withoutField leaves out the field an editor shows by itself.
func withoutField(fields []schema.Field, key string) []schema.Field {
	var out []schema.Field
	for _, f := range fields {
		if f.Key != key {
			out = append(out, f)
		}
	}
	return out
}

func (e *editor) save(doc *model.Doc) {
	if !e.formValid {
		e.s.env.Toast("请修正 JSON 格式")
		return
	}
	platform := e.s.platform(doc)
	library := e.s.store.NetworkConfigs()
	var tag, id string
	var fields model.Fields
	var refs model.NetworkConfigRefs
	var tagError string
	if e.service != nil {
		tag, id, fields, refs = e.service.Tag, e.service.ID, e.service.Fields, e.service.NetworkConfigRefs
		tagError = model.ServiceTagValidationError(doc, tag, id)
	} else {
		tag, id, fields, refs = e.endpoint.Tag, e.endpoint.ID, e.endpoint.Fields, e.endpoint.NetworkConfigRefs
		tagError = model.TagValidationError(e.editDoc(doc), tag, id)
	}
	if tagError != "" {
		e.s.env.Toast(tagError)
		return
	}
	if addsTailscale(doc, e.pending, e.endpoint) {
		e.s.env.Toast(model.TailscaleLimitMessage)
		return
	}
	full := e.fullSchema()
	effective := model.EffectiveNetworkFields(full, fields, refs, library, platform)
	if errors := schema.FormValidationErrors(withoutDevices(full), effective, platform); len(errors) > 0 {
		e.s.env.Toast(errors[0].Field.Label + "：" + errors[0].Message)
		return
	}
	if e.endpoint != nil && e.endpoint.Type == "tailscale" {
		if problem, _ := udpPathIssues(e.editDoc(doc), effective); problem != "" {
			e.s.env.Toast(problem)
			return
		}
		pruneUDPPathRules(e.endpoint.Fields)
	}
	if e.service != nil {
		if !schema.ServiceAvailable(e.service.Type, platform) {
			e.s.env.Toast(schema.ServiceUnavailableReason(e.service.Type, platform))
			return
		}
		if !(e.service.Type == "usbip-server" && fields["provider"] == "dynamic") {
			if problem := schema.UsbDevicesError(e.service.Type, fields); problem != "" {
				e.s.env.Toast(problem)
				return
			}
		}
	}
	service, endpoint, pending := e.service, e.endpoint, e.pending
	err := e.s.store.Update(func(doc *model.Doc) {
		doc.Endpoints = append(doc.Endpoints, pending...)
		if service != nil {
			for i, x := range doc.Services {
				if x.ID == service.ID {
					doc.Services[i] = service
					return
				}
			}
			doc.Services = append(doc.Services, service)
			return
		}
		for i, x := range doc.Endpoints {
			if x.ID == endpoint.ID {
				doc.Endpoints[i] = endpoint
				return
			}
		}
		doc.Endpoints = append(doc.Endpoints, endpoint)
	})
	if err != nil {
		e.s.env.Toast(err.Error())
		return
	}
	e.close()
}

func (e *editor) view(c *ui.Context, doc *model.Doc) {
	if !e.open || e.endpoint == nil && e.service == nil {
		return
	}
	open := true
	tag, typ := "", ""
	if e.service != nil {
		tag, typ = e.service.Tag, e.service.Type
	} else {
		tag, typ = e.endpoint.Tag, e.endpoint.Type
	}
	name := tag
	if name == "" || typ == "tailscale" {
		name = typeLabel(typ)
	}
	verb := "编辑"
	if e.isNew {
		verb = "添加"
	}
	kit.Dialog(c, &open, verb+" "+name, 600, func() { e.form(c, doc) }, func() {
		if !e.isNew {
			kind := kindEndpoint
			if e.service != nil {
				kind = kindService
			}
			id := e.draftID()
			reset := kit.Button(c, "恢复默认", kit.ButtonOptions{Small: true})
			if reset.Clicked() {
				e.s.askConfirm("reset", kind, id, tag, true)
			}
			if e.s.confirm.inEditor && e.s.confirm.action == "reset" && e.s.confirm.state.Open {
				e.s.confirmPopover(c, reset)
			}
			remove := kit.Button(c, "删除", kit.ButtonOptions{Small: true, Variant: kit.Danger})
			if remove.Clicked() {
				e.s.askConfirm("delete", kind, id, tag, true)
			}
			if e.s.confirm.inEditor && e.s.confirm.action == "delete" && e.s.confirm.state.Open {
				e.s.confirmPopover(c, remove)
			}
		}
		ui.Spacer(c)
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(e.close)
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() { e.save(doc) })
	})
	if !open && e.open {
		e.close()
	}
}

func (e *editor) form(c *ui.Context, doc *model.Doc) {
	k := kit.T(c)
	platform := e.s.platform(doc)
	editDoc := e.editDoc(doc)
	var fields []schema.Field
	var values model.Fields
	var refs model.NetworkConfigRefs
	var setRefs func(model.NetworkConfigRefs)
	var tag *string
	var trailing func()
	var accessory func(string) func()
	var placeholder func(string) string
	exclude := ""
	if e.service != nil {
		service := e.service
		if service.Fields == nil {
			service.Fields = model.Fields{}
		}
		if !schema.ServiceAvailable(service.Type, platform) {
			note(c, kit.WarningTone, schema.ServiceUnavailableReason(service.Type, platform)+"；切换到支持的目标平台后才会输出该服务。")
		}
		if service.Type == "usbip-server" && service.Fields["provider"] == "dynamic" {
			note(c, kit.Neutral, "dynamic 模式的设备由外部 gRPC 客户端在运行时注入，需要同时启用 api 服务。")
		}
		e.guide.view(c, e, editDoc, service)
		provider := "default"
		if v, ok := service.Fields["provider"].(string); ok && v != "" {
			provider = v
		}
		if service.Type == "usbip-client" || provider == "default" {
			e.devices.view(c, e.s, service)
		}
		fields = withoutDevices(schema.ServiceSchema[service.Type])
		values, refs, tag = service.Fields, service.NetworkConfigRefs, &service.Tag
		setRefs = func(r model.NetworkConfigRefs) { service.NetworkConfigRefs = r }
	} else {
		endpoint := e.endpoint
		if endpoint.Fields == nil {
			endpoint.Fields = model.Fields{}
		}
		fields = schema.EndpointSchema[endpoint.Type]
		values, refs, tag, exclude = endpoint.Fields, endpoint.NetworkConfigRefs, &endpoint.Tag, endpoint.ID
		setRefs = func(r model.NetworkConfigRefs) { endpoint.NetworkConfigRefs = r }
		if endpoint.Type == "tailscale" {
			accessory = func(key string) func() {
				if key != "auth_key" {
					return nil
				}
				return func() {
					kit.Button(c, "登录", kit.ButtonOptions{Icon: kit.SymKeyRound}).OnClick(func() { e.login.show(endpoint.Fields) })
				}
			}
			placeholder = func(key string) string {
				if key == "hostname" {
					return e.s.systemHostname()
				}
				return ""
			}
			e.login.view(c, e, endpoint)
			full := fields
			fields = withoutField(fields, udpPathPeersKey)
			trailing = func() {
				effective := model.EffectiveNetworkFields(full, endpoint.Fields, endpoint.NetworkConfigRefs, e.s.store.NetworkConfigs(), platform)
				e.udpPath.view(c, e.s, editDoc, endpoint, effective)
			}
		}
	}
	result := forms.Form(c, []any{"fields", e.draftID(), e.generation}, fields, values, forms.Context{
		Doc:            editDoc,
		NetworkConfigs: e.s.store.NetworkConfigs(),
		NetworkRefs:    refs,
		OnNetworkRefs:  setRefs,
		ExcludeRef:     exclude,
		Leading: func() {
			// The Tailscale endpoint keeps its fixed tag.
			if e.endpoint != nil && e.endpoint.Type == "tailscale" {
				return
			}
			ui.Column(c).Gap(6).Shrink(0).Children(func() {
				ui.Text(c, "名称").FontSize(kit.FontBody).TextColor(k.Label2)
				kit.Input(c, "tag", tag, "").Label("名称")
			})
		},
		Trailing:    trailing,
		Accessory:   accessory,
		Placeholder: placeholder,
	})
	e.formValid = result.Valid
}
