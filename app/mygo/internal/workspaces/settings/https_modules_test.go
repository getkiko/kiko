package settings

import (
	"fmt"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// moduleHostsDoc is a document with the user's own host entries and three
// modules: one enabled with confirmed and unconfirmed hosts, one that
// excludes a host, one disabled.
func moduleHostsDoc() *model.Doc {
	doc := mixedDoc()
	mod := func(id, name string, enabled bool, hosts *model.MITMHostnames) *model.HTTPModule {
		m := model.NewHTTPModule()
		m.ID, m.Name, m.Enabled = id, name, enabled
		m.Contents = model.ModuleContents{Hostnames: hosts}
		return m
	}
	ads := mod("mod_ads", "去广告", true, &model.MITMHostnames{Include: []string{"ads.example.com", "pay.example.com", "new.example.com"}})
	ads.Approved = model.ModuleApproved{Hostnames: []string{"ads.example.com", "pay.example.com"}}
	skip := mod("mod_skip", "排除银行", true, &model.MITMHostnames{Exclude: []string{"bank.example.com"}})
	off := mod("mod_off", "已停用", false, &model.MITMHostnames{Include: []string{"off.example.com"}})
	doc.HTTPTools = &model.HTTPTools{
		MITM: &model.MITMSettings{Enabled: true, Acknowledged: true, Hostnames: &model.MITMHostnames{
			Include: []string{"example.com", "bank.example.com"}, Exclude: []string{"pay.example.com"}}},
		Modules: []*model.HTTPModule{ads, skip, off},
	}
	return doc
}

func TestModuleHostRows(t *testing.T) {
	doc := moduleHostsDoc()
	show := func(rows []moduleHostRow) []string {
		var out []string
		for _, r := range rows {
			s := r.ModuleName + ":" + r.Host
			if r.Unconfirmed {
				s += ":待确认"
			}
			if r.Overridden {
				s += ":被排除"
			}
			out = append(out, s)
		}
		return out
	}
	// Includes: the disabled module adds none; an exclude of the user's own
	// (pay.example.com) wins over the module's include.
	got := show(moduleHostRows(doc, false))
	want := []string{"去广告:ads.example.com", "去广告:pay.example.com:被排除", "去广告:new.example.com:待确认"}
	if !slices.Equal(got, want) {
		t.Errorf("include rows %v, want %v", got, want)
	}
	if got, want := show(moduleHostRows(doc, true)), []string{"排除银行:bank.example.com"}; !slices.Equal(got, want) {
		t.Errorf("exclude rows %v, want %v", got, want)
	}
	// A module's exclude covers a user's include, which stays the user's
	// entry; a module include is covered by a module exclude as well.
	doc.HTTPTools.Modules[0].Contents.Hostnames.Include = append(doc.HTTPTools.Modules[0].Contents.Hostnames.Include, "bank.example.com")
	doc.HTTPTools.Modules[0].Approved.Hostnames = append(doc.HTTPTools.Modules[0].Approved.Hostnames, "bank.example.com")
	if got := show(moduleHostRows(doc, false)); !slices.Contains(got, "去广告:bank.example.com:被排除") {
		t.Errorf("module exclude does not override: %v", got)
	}
	if rows := moduleHostRows(mixedDoc(), false); rows != nil {
		t.Errorf("rows without modules: %v", rows)
	}
}

func TestHTTPSModuleHostsAreReadOnly(t *testing.T) {
	status := api.MITMStatus{Supported: true, CA: readyCA(true), Core: api.MITMCore{Connected: true, Enabled: true, AuthorityFingerprint: "ab"}}
	f, _ := newMITMFixture(t, status, moduleHostsDoc())
	var pages []string
	f.env.SetNavigator(func(page string, arg any) { pages = append(pages, fmt.Sprint(page, ":", arg)) })
	f.tt.Frame()
	for _, text := range []string{"ads.example.com", "new.example.com", "bank.example.com", "去广告", "排除银行", "待确认", "被排除"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	if f.tt.HasText("off.example.com") || f.tt.HasText("已停用") {
		t.Errorf("disabled module listed: %q", f.tt.Texts())
	}
	// The user's own entries keep their remove buttons; a module's host has
	// none, only the way to its module.
	if _, ok := f.tt.Find("移除 example.com"); !ok {
		t.Errorf("user host lost its remove button: %q", f.tt.Texts())
	}
	for _, host := range []string{"ads.example.com", "new.example.com"} {
		if _, ok := f.tt.Find("移除 " + host); ok {
			t.Errorf("module host %s can be removed", host)
		}
	}
	f.click("前往模块 去广告")
	if len(pages) != 1 || pages[0] != "rewrites:module:mod_ads" {
		t.Fatalf("navigation %v", pages)
	}
	// The stored lists stay the user's.
	m := f.mitmSettings()
	if !slices.Equal(hostList(m, false), []string{"example.com", "bank.example.com"}) || !slices.Equal(hostList(m, true), []string{"pay.example.com"}) {
		t.Errorf("stored lists changed: %v %v", hostList(m, false), hostList(m, true))
	}
	f.save("hosts-modules-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("hosts-modules-dark.png")
	f.tt.SetDark(false)
}
