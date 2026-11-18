package emit

import (
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func stringList(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// mitmConfig builds the core's top-level "mitm" section; nil when HTTPS
// decryption is off. Settings at their default are not written, so a
// document that never touched them emits the package-E shape. The capture
// section is always written; saveBody turns on body capture.
func mitmConfig(doc *model.Doc) (*object, []string) {
	var tools *model.HTTPTools
	if doc != nil {
		tools = doc.HTTPTools
	}
	if tools == nil || tools.MITM == nil || !tools.MITM.Enabled {
		return nil, nil
	}
	mitm := tools.MITM
	config := obj("enabled", true, "certificate", obj("source", "managed"))
	hostnames := obj()
	var warnings []string
	var include, exclude []string
	if mitm.Hostnames != nil {
		include, exclude = mitm.Hostnames.Include, mitm.Hostnames.Exclude
	}
	moduleInclude, moduleExclude, moduleWarnings := moduleHostnames(tools)
	warnings = append(warnings, moduleWarnings...)
	include = append(slices.Clone(include), moduleInclude...)
	exclude = append(slices.Clone(exclude), moduleExclude...)
	for _, list := range []struct {
		key, label string
		raw        []string
	}{{"include", "包含", include}, {"exclude", "排除", exclude}} {
		result := model.NormalizeMITMHostnames(list.raw)
		for _, host := range result.Invalid {
			warnings = append(warnings, "HTTPS 解密"+list.label+"列表中的「"+host+"」不是有效主机名，已跳过")
		}
		if len(result.Hosts) > 0 {
			hosts := make([]any, len(result.Hosts))
			for i, host := range result.Hosts {
				hosts[i] = host
			}
			hostnames.set(list.key, hosts)
		}
	}
	if !mitm.BuiltinExcludeOn() {
		hostnames.set("builtin_exclude", false)
	}
	if mitm.BuiltinExcludeOn() && len(mitm.DisabledBuiltin) > 0 {
		var ids []string
		for _, id := range mitm.DisabledBuiltin {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			hostnames.set("disabled_builtin", stringList(ids))
		}
	}
	if len(hostnames.keys) > 0 {
		config.set("hostnames", hostnames)
	}
	sources, sourceWarnings := mitmSources(doc, mitm)
	if len(sources) > 0 {
		config.set("sources", stringList(sources))
	}
	warnings = append(warnings, sourceWarnings...)
	if mitm.NoLocal && len(sources) == 0 {
		// Nothing but this machine would match: leave decryption off
		// rather than fall back to the default local scope.
		return nil, append(warnings, "HTTPS 解密没有可用的设备地址，已关闭")
	}
	if up := mitm.Upstream; up != nil {
		upstream := obj()
		if up.Fingerprint != "" && up.Fingerprint != model.MITMFingerprintChrome {
			if model.ValidMITMFingerprint(up.Fingerprint) {
				upstream.set("fingerprint", up.Fingerprint)
			} else {
				warnings = append(warnings, "HTTPS 解密的指纹「"+up.Fingerprint+"」无效，已跳过")
			}
		}
		skip := model.NormalizeMITMHostnames(up.SkipVerify)
		for _, host := range skip.Invalid {
			warnings = append(warnings, "HTTPS 解密跳过证书校验列表中的「"+host+"」不是有效主机名，已跳过")
		}
		if len(skip.Hosts) > 0 {
			upstream.set("skip_verify", stringList(skip.Hosts))
		}
		var roots []string
		for _, path := range up.CustomRoots {
			if path = strings.TrimSpace(path); path != "" && !slices.Contains(roots, path) {
				roots = append(roots, path)
			}
		}
		if len(roots) > 0 {
			upstream.set("custom_roots", stringList(roots))
		}
		if len(upstream.keys) > 0 {
			config.set("upstream", upstream)
		}
	}
	if mitm.QUIC == model.MITMQUICAllow {
		config.set("quic", "allow")
	}
	if minutes := mitm.PinningSkip(); minutes != model.MITMDefaultPinningSkipMinutes {
		config.set("pinning_skip_duration", strconv.Itoa(minutes)+"m")
	}
	rewrite, rewriteWarnings := rewriteConfig(mergedRewrites(tools))
	if rewrite != nil {
		config.set("http_rewrite", rewrite)
	}
	warnings = append(warnings, rewriteWarnings...)
	capture := obj("enabled", true, "body", mitm.SaveBody, "max_body", "512KiB", "max_memory", "64MiB", "max_records", 5000)
	if persist := mitm.Persist; persist != nil {
		// The directory is added when the runtime configuration is built.
		limits := persist.Limits()
		section := obj("enabled", persist.Enabled, "max_bytes", strconv.Itoa(limits.MaxMB)+"MiB", "max_records", limits.MaxRecords,
			"retention", strconv.Itoa(limits.RetentionDays*24)+"h")
		if persist.Body {
			section.set("body", true)
		}
		if len(limits.RedactHeaders) > 0 {
			section.set("redact_headers", stringList(limits.RedactHeaders))
		}
		for _, name := range limits.Invalid {
			warnings = append(warnings, "抓取存储的脱敏头名「"+name+"」无效，已跳过")
		}
		capture.set("persist", section)
	}
	config.set("capture", capture)
	return config, warnings
}

// mitmSources maps the selected gateway devices to the core's sources:
// "local" for this machine and the devices' addresses. Nothing is written
// for the default (this machine only).
func mitmSources(doc *model.Doc, mitm *model.MITMSettings) ([]string, []string) {
	if len(mitm.Devices) == 0 && !mitm.NoLocal {
		return nil, nil
	}
	var sources, warnings []string
	if !mitm.NoLocal {
		sources = append(sources, "local")
	}
	for _, id := range mitm.Devices {
		var device *model.RouteDevice
		if doc != nil {
			for _, d := range doc.Route.Devices {
				if d != nil && d.ID == id {
					device = d
					break
				}
			}
		}
		if device == nil {
			warnings = append(warnings, "HTTPS 解密的设备「"+id+"」不存在，已跳过")
			continue
		}
		for _, address := range device.Addresses {
			address = strings.TrimSpace(address)
			if !model.ValidNetwork(address) {
				warnings = append(warnings, "HTTPS 解密设备「"+device.Name+"」的地址「"+address+"」无效，已跳过")
				continue
			}
			if !slices.Contains(sources, address) {
				sources = append(sources, address)
			}
		}
	}
	if len(sources) == 0 {
		// Every selected source was invalid. An empty list reads as the
		// default local scope in the core; mitmConfig turns decryption
		// off when NoLocal is set.
		return nil, warnings
	}
	return sources, warnings
}

// rewriteLabel names a rule in warnings.
func rewriteLabel(kind, name, id string) string {
	if name = strings.TrimSpace(name); name == "" {
		name = id
	}
	return "HTTPS 解密的" + kind + "「" + name + "」"
}

// rewriteConfig builds the core's "http_rewrite" section from the enabled,
// valid rules; nil when none remains. Invalid rules and map-local rules past
// the total body limit are skipped with a warning.
func rewriteConfig(rw *model.HTTPRewrites) (*object, []string) {
	if rw.Empty() {
		return nil, nil
	}
	var warnings []string
	skip := func(kind, name, id, why string) {
		warnings = append(warnings, rewriteLabel(kind, name, id)+why+"，已跳过")
	}
	var urls, headers, locals, bodies []any
	for _, r := range rw.URL {
		if r == nil || r.Disabled {
			continue
		}
		if issue := r.Issue(); issue != "" {
			skip("URL 改写规则", r.Name, r.ID, "无效（"+issue+"）")
			continue
		}
		item := obj()
		if r.ID != "" {
			item.set("id", r.ID)
		}
		item.set("pattern", r.Pattern)
		item.set("action", r.Action)
		if r.Action == model.URLActionFound || r.Action == model.URLActionTemporary || r.Action == model.URLActionHeader {
			item.set("target", strings.TrimSpace(r.Target))
		}
		urls = append(urls, item)
	}
	for _, r := range rw.Headers {
		if r == nil || r.Disabled {
			continue
		}
		if issue := r.Issue(); issue != "" {
			skip("头改写规则", r.Name, r.ID, "无效（"+issue+"）")
			continue
		}
		ops := make([]any, 0, len(r.Ops))
		for _, op := range r.Ops {
			item := obj("action", op.Action, "name", op.Name)
			switch op.Action {
			case model.HeaderActionAdd, model.HeaderActionReplace:
				item.set("value", op.Value)
			case model.HeaderActionReplaceRegex:
				item.set("regex", op.Regex)
				item.set("value", op.Value)
			}
			ops = append(ops, item)
		}
		item := obj()
		if r.ID != "" {
			item.set("id", r.ID)
		}
		item.set("pattern", r.Pattern)
		item.set("scope", r.Scope)
		item.set("operations", ops)
		headers = append(headers, item)
	}
	for _, r := range rw.Body {
		if r == nil || r.Disabled {
			continue
		}
		if issue := r.Issue(); issue != "" {
			skip("正文改写规则", r.Name, r.ID, "无效（"+issue+"）")
			continue
		}
		edits := make([]any, 0, len(r.Edits))
		for _, e := range r.Edits {
			item := obj("action", e.Action)
			if e.Action == model.BodyActionReplaceRegex {
				item.set("regex", e.Regex)
				item.set("value", e.Value)
			} else {
				item.set("path", e.Path)
				if e.Action != model.BodyActionJSONDelete {
					item.set("value", e.Value)
				}
			}
			edits = append(edits, item)
		}
		item := obj()
		if r.ID != "" {
			item.set("id", r.ID)
		}
		item.set("pattern", r.Pattern)
		item.set("scope", r.Scope)
		var types []string
		for _, ct := range r.ContentTypes {
			if ct = strings.TrimSpace(ct); ct != "" {
				types = append(types, ct)
			}
		}
		if len(types) > 0 {
			item.set("content_types", stringList(types))
		}
		item.set("edits", edits)
		bodies = append(bodies, item)
	}
	maxBody := 0
	if len(bodies) > 0 && rw.MaxBodySize != 0 && rw.MaxBodySize != model.DefaultBodySize {
		if issue := model.MaxBodySizeIssue(rw.MaxBodySize); issue != "" {
			warnings = append(warnings, "HTTPS 解密的正文缓冲上限无效（"+issue+"），已使用默认值")
		} else {
			maxBody = rw.MaxBodySize
		}
	}
	total := 0
	for _, r := range rw.MapLocal {
		if r == nil || r.Disabled {
			continue
		}
		if issue := r.Issue(); issue != "" {
			skip("本地映射规则", r.Name, r.ID, "无效（"+issue+"）")
			continue
		}
		size := r.BodyBytes()
		if total+size > model.MaxMapLocalBodyTotal {
			skip("本地映射规则", r.Name, r.ID, "使正文总量超过 4 MiB")
			continue
		}
		total += size
		item := obj()
		if r.ID != "" {
			item.set("id", r.ID)
		}
		item.set("pattern", r.Pattern)
		item.set("status", r.Status)
		if len(r.Headers) > 0 {
			fields := make([]any, len(r.Headers))
			for i, f := range r.Headers {
				fields[i] = obj("name", f.Name, "value", f.Value)
			}
			item.set("headers", fields)
		}
		if r.BodyBase64 != "" {
			item.set("body_base64", r.BodyBase64)
		} else if r.Body != "" {
			item.set("body", r.Body)
		}
		locals = append(locals, item)
	}
	if len(urls)+len(headers)+len(locals)+len(bodies) == 0 {
		return nil, warnings
	}
	out := obj()
	if len(urls) > 0 {
		out.set("url", urls)
	}
	if len(headers) > 0 {
		out.set("headers", headers)
	}
	if len(locals) > 0 {
		out.set("map_local", locals)
	}
	if len(bodies) > 0 {
		out.set("body", bodies)
	}
	if maxBody > 0 {
		out.set("max_body_size", strconv.Itoa(maxBody/1024)+"KB")
	}
	return out, warnings
}
