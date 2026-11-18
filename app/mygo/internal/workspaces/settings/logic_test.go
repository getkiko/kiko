package settings

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// Backup sheet validation and wording.

func TestWebDAVValidation(t *testing.T) {
	config := emptyBackend(api.SyncWebDAV)
	if got := validateBackend(config).Errors["webdav.url"]; got != "请填写 WebDAV 地址" {
		t.Errorf("empty url: %q", got)
	}
	config.WebDAV.URL = "http://nas.lan/dav"
	if got := validateBackend(config).Errors["webdav.url"]; !strings.Contains(got, "默认只允许 https") {
		t.Errorf("http: %q", got)
	}
	config.WebDAV.AllowInsecure = true
	insecure := validateBackend(config)
	if len(insecure.Errors) != 0 || len(insecure.Warnings) != 1 {
		t.Errorf("insecure: %+v", insecure)
	}
	config.WebDAV.URL = "https://dav.example.com/remote.php/dav"
	config.WebDAV.Username = "me"
	if got := validateBackend(config).Errors["webdav.password"]; got != "填写了用户名，请同时填写密码" {
		t.Errorf("password: %q", got)
	}
	config.WebDAV.Password = secretMask
	if v := validateBackend(config); len(v.Errors) != 0 || len(v.Warnings) != 0 {
		t.Errorf("valid: %+v", v)
	}
}

func TestS3Validation(t *testing.T) {
	config := emptyBackend(api.SyncS3)
	empty := validateBackend(config).Errors
	for _, key := range []string{"s3.endpoint", "s3.bucket", "s3.accessKey", "s3.secretKey"} {
		if empty[key] == "" {
			t.Errorf("missing %s", key)
		}
	}
	config.S3.Endpoint, config.S3.Bucket, config.S3.AccessKey, config.S3.SecretKey = "https://s3.example.com", "my-bucket", "AK", secretMask
	if errs := validateBackend(config).Errors; len(errs) != 0 {
		t.Errorf("valid: %v", errs)
	}
	config.S3.Bucket = "bad bucket!"
	if !strings.Contains(validateBackend(config).Errors["s3.bucket"], "只能包含") {
		t.Error("bucket name")
	}
	config.S3.Bucket = "ok-bucket"
	config.S3.Region = " "
	if validateBackend(config).Errors["s3.region"] == "" {
		t.Error("region")
	}
}

func TestKVValidation(t *testing.T) {
	config := emptyBackend(api.SyncKV)
	errs := validateBackend(config).Errors
	if errs["kv.url"] == "" || errs["kv.token"] == "" {
		t.Errorf("empty kv: %v", errs)
	}
	config.KV = &api.SyncKVConfig{URL: "https://sync.me.workers.dev", Token: "t"}
	if errs := validateBackend(config).Errors; len(errs) != 0 {
		t.Errorf("valid: %v", errs)
	}
	if parseHTTPURL("ftp://x") != nil || parseHTTPURL("not a url") != nil {
		t.Error("parseHTTPURL accepts non-http")
	}
}

func TestWithDefaults(t *testing.T) {
	merged := withDefaults(&api.SyncBackendConfig{Type: api.SyncS3, S3: &api.SyncS3Config{Endpoint: "https://e"}}, api.SyncS3)
	if merged.S3.Endpoint != "https://e" || merged.S3.Region != "auto" {
		t.Errorf("merged %+v", merged.S3)
	}
	if got := withDefaults(&api.SyncBackendConfig{Type: api.SyncKV, KV: &api.SyncKVConfig{URL: "u", Token: "t"}}, api.SyncWebDAV); got.Type != api.SyncWebDAV {
		t.Errorf("type change: %+v", got)
	}
	if withDefaults(nil, api.SyncKV).KV.URL != "" {
		t.Error("empty kv")
	}
}

func TestPasswordStrength(t *testing.T) {
	score := func(p string) int { s, _, _ := passwordStrength(p); return s }
	if score("") != 0 || score("abc123") != 0 || score("password123") != 0 {
		t.Error("weak passwords")
	}
	if score("aaaaaaaaaaaa") >= 2 {
		t.Error("repetition")
	}
	if _, _, tone := passwordStrength("correcthorse"); tone != toneFair {
		t.Errorf("correcthorse tone %v", tone)
	}
	if score("Correct-Horse-Battery-9") != 4 {
		t.Error("strong")
	}
}

func TestValidateNewPassword(t *testing.T) {
	if validateNewPassword("short", "short") != "密码至少需要 8 位" {
		t.Error("short")
	}
	if validateNewPassword("long-enough-1", "long-enough-2") != "两次输入的密码不一致" {
		t.Error("mismatch")
	}
	if validateNewPassword("long-enough-1", "long-enough-1") != "" {
		t.Error("valid")
	}
}

func TestFormatAgoAndResult(t *testing.T) {
	now := time.UnixMilli(1_000_000_000_000)
	ms := now.UnixMilli()
	cases := map[int64]string{ms - 10_000: "刚刚", ms - 5*60_000: "5 分钟前", ms - 3*3_600_000: "3 小时前", ms - 2*86_400_000: "2 天前", 0: ""}
	for ts, want := range cases {
		if got := formatAgo(ts, now); got != want {
			t.Errorf("formatAgo(%d) = %q, want %q", ts, got, want)
		}
	}
	if got := formatSyncResult(&api.SyncResult{Pulled: 2, Pushed: 1, Conflicts: 1}); got != "拉取 2 个、推送 1 个、冲突副本 1 个" {
		t.Errorf("result %q", got)
	}
	if formatSyncResult(&api.SyncResult{}) != "已是最新" || formatSyncResult(nil) != "已是最新" {
		t.Error("up to date")
	}
}

func TestSyncSummary(t *testing.T) {
	now := time.UnixMilli(1_000_000_000_000)
	base := api.SyncStatus{Configured: true, State: api.SyncUnlocked, AutoSync: true, DeviceID: "d"}
	with := func(fn func(s *api.SyncStatus)) *api.SyncStatus { s := base; fn(&s); return &s }
	if _, tone := syncSummary(nil, now); tone != syncIdle {
		t.Error("nil")
	}
	check := func(status *api.SyncStatus, want string, tone syncTone) {
		t.Helper()
		got, gotTone := syncSummary(status, now)
		if !strings.HasPrefix(got, want) || gotTone != tone {
			t.Errorf("summary %q %v, want %q %v", got, gotTone, want, tone)
		}
	}
	check(with(func(s *api.SyncStatus) { s.Configured, s.State = false, api.SyncUnconfigured }), "未配置", syncIdle)
	check(with(func(s *api.SyncStatus) { s.State = api.SyncLocked }), "已锁定", syncWarn)
	check(with(func(s *api.SyncStatus) { s.State = api.SyncNeedsSetup }), "还没有云端备份", syncWarn)
	check(with(func(s *api.SyncStatus) { s.Syncing = true }), "正在同步…", syncIdle)
	check(with(func(s *api.SyncStatus) { s.LastError = "超时" }), "同步出错：超时", syncError)
	check(with(func(s *api.SyncStatus) { s.LastSyncedAt = now.UnixMilli() - 120_000 }), "上次同步 2 分钟前 · 自动同步已开启", syncOK)
	check(with(func(s *api.SyncStatus) { s.AutoSync = false }), "已解锁 · 尚未同步", syncIdle)
}

func TestSyncErrorMessage(t *testing.T) {
	coded := &api.Error{Status: 401, Message: "x", Body: []byte(`{"code":"wrong-password","error":"x"}`)}
	if syncErrorMessage(coded) != "密码错误，请重新输入" {
		t.Error("code")
	}
	if syncErrorMessage(&api.Error{Status: 502, Message: "连接被拒绝"}) != "连接被拒绝" {
		t.Error("server text")
	}
	if syncErrorMessage(errors.New("boom")) != "boom" || syncErrorMessage(nil) != "操作失败，请稍后重试" {
		t.Error("fallback")
	}
}

func TestWebDAVPaths(t *testing.T) {
	for dir, path := range map[string]string{"kiko": "kiko/kiko-vault.json", " /a/b/ ": "a/b/kiko-vault.json", "": "kiko-vault.json"} {
		if got := webdavDirToPath(dir); got != path {
			t.Errorf("dirToPath(%q) = %q", dir, got)
		}
	}
	for path, dir := range map[string]string{"kiko/kiko-vault.json": "kiko", "a/b/kiko-vault.json": "a/b", "kiko-vault.json": "", "legacy-dir": "legacy-dir",
		// Paths saved before the rename to Kiko end in sbcfg-vault.json.
		"sbcfg/sbcfg-vault.json": "sbcfg", "sbcfg-vault.json": ""} {
		if got := webdavPathToDir(path); got != dir {
			t.Errorf("pathToDir(%q) = %q", path, got)
		}
	}
	fromServer := withDefaults(&api.SyncBackendConfig{Type: api.SyncWebDAV, WebDAV: &api.SyncWebDAVConfig{URL: "https://x", Path: "backup/kiko-vault.json"}}, api.SyncWebDAV)
	if fromServer.WebDAV.Path != "backup" {
		t.Errorf("shown path %q", fromServer.WebDAV.Path)
	}
	if wire := toWireBackend(fromServer); wire.WebDAV.Path != "backup/kiko-vault.json" || fromServer.WebDAV.Path != "backup" {
		t.Errorf("wire path %q", wire.WebDAV.Path)
	}
	if toWireBackend(emptyBackend(api.SyncKV)).Type != api.SyncKV {
		t.Error("kv wire")
	}
}

// System proxy commands and addresses.

func inbound(id, typ string, enabled bool, fields model.Fields) *model.Inbound {
	if fields == nil {
		fields = model.Fields{}
	}
	return &model.Inbound{Entity: model.Entity{ID: id, Tag: id, Enabled: enabled}, Kind: "inbound", Type: typ, Fields: fields}
}

func TestProxyEndpoint(t *testing.T) {
	doc := &model.Doc{Inbounds: []*model.Inbound{
		inbound("a", "mixed", false, model.Fields{"listen_port": 1.0}),
		inbound("b", "mixed", true, model.Fields{"listen": "0.0.0.0", "listen_port": 7890.0}),
		inbound("c", "mixed", true, model.Fields{"listen_port": 7891.0}),
		inbound("t", "tun", false, nil),
	}}
	if firstMixedInbound(doc).ID != "b" {
		t.Error("first mixed")
	}
	if e, ok := proxyEndpointOf(doc, ""); !ok || e.Host != "127.0.0.1" || e.Port != "7890" || e.HasAuth {
		t.Errorf("endpoint %+v", e)
	}
	if hasEnabledTun(doc) {
		t.Error("disabled tun")
	}
	doc.Inbounds = append(doc.Inbounds, inbound("t2", "tun", true, nil))
	if !hasEnabledTun(doc) {
		t.Error("enabled tun")
	}
	if _, ok := proxyEndpointOf(&model.Doc{Inbounds: []*model.Inbound{inbound("s", "socks", true, model.Fields{"listen_port": 1080.0})}}, ""); ok {
		t.Error("socks endpoint")
	}
	if _, ok := proxyEndpointOf(&model.Doc{Inbounds: []*model.Inbound{inbound("m", "mixed", true, nil)}}, ""); ok {
		t.Error("no port")
	}
	concrete := &model.Doc{Inbounds: []*model.Inbound{inbound("m", "mixed", true, model.Fields{"listen": "192.168.1.2", "listen_port": "7890"})}}
	if e, _ := proxyEndpointOf(concrete, ""); e.Host != "192.168.1.2" || e.Port != "7890" {
		t.Errorf("concrete %+v", e)
	}
	if e, _ := proxyEndpointOf(concrete, " 10.0.0.1 "); e.Host != "10.0.0.1" {
		t.Errorf("override %+v", e)
	}
}

func TestShellCommand(t *testing.T) {
	plain := proxyEndpoint{Host: "127.0.0.1", Port: "7890"}
	want := map[string]string{
		"bash":       "export http_proxy=http://127.0.0.1:7890 https_proxy=http://127.0.0.1:7890 all_proxy=socks5://127.0.0.1:7890",
		"fish":       "set -gx http_proxy http://127.0.0.1:7890; set -gx https_proxy http://127.0.0.1:7890; set -gx all_proxy socks5://127.0.0.1:7890",
		"cmd":        "set http_proxy=http://127.0.0.1:7890&& set https_proxy=http://127.0.0.1:7890&& set all_proxy=socks5://127.0.0.1:7890",
		"powershell": `$env:http_proxy="http://127.0.0.1:7890"; $env:https_proxy="http://127.0.0.1:7890"; $env:all_proxy="socks5://127.0.0.1:7890"`,
	}
	for shell, command := range want {
		if got := shellCommand(shell, plain); got != command {
			t.Errorf("%s: %q", shell, got)
		}
	}
	auth := proxyEndpoint{Host: "::1", Port: "1", HasAuth: true, Username: "u s", Password: "p@!"}
	if got := shellCommand("bash", auth); !strings.Contains(got, "http_proxy=http://u%20s:p%40%21@[::1]:1 ") {
		t.Errorf("auth: %q", got)
	}
}

func TestEffectiveBypass(t *testing.T) {
	if got := effectiveBypass(false, []string{"a", "a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("custom %v", got)
	}
	merged := effectiveBypass(true, []string{"localhost", "example.com"})
	if len(merged) != len(defaultProxyBypass)+1 || merged[len(merged)-1] != "example.com" {
		t.Errorf("merged %v", merged)
	}
	if got := splitBypass("a\nb, c;;d"); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("split %v", got)
	}
}

func TestSetGlobalIPv6(t *testing.T) {
	doc := &model.Doc{Platform: "linux", Inbounds: []*model.Inbound{
		inbound("tun", "tun", true, model.Fields{"address": []any{"172.18.0.1/30"}}),
		inbound("mixed", "mixed", true, model.Fields{"listen": "0.0.0.0"}),
	}}
	doc.DNS.Servers = []*model.DNSServer{{ID: "d", DomainStrategy: "prefer_ipv6"}}
	setGlobalIPv6(doc, true)
	if !doc.IPv6Enabled || doc.DNS.Strategy != "prefer_ipv6" || doc.Inbounds[1].Fields["listen"] != "::" {
		t.Errorf("enable: %+v %v", doc.DNS, doc.Inbounds[1].Fields)
	}
	if got := doc.Inbounds[0].Fields["address"]; !reflect.DeepEqual(got, []any{"172.18.0.1/30", "fdfe:dcba:9876::1/126"}) {
		t.Errorf("tun address %v", got)
	}
	setGlobalIPv6(doc, false)
	if doc.DNS.Strategy != "prefer_ipv4" || doc.DNS.Servers[0].DomainStrategy != "ipv4_only" || doc.Inbounds[1].Fields["listen"] != "0.0.0.0" {
		t.Errorf("disable: %+v", doc.DNS)
	}
	if got := doc.Inbounds[0].Fields["address"]; !reflect.DeepEqual(got, []any{"172.18.0.1/30"}) {
		t.Errorf("tun address %v", got)
	}
}

// Rules of the network and TLS config libraries.

func TestLibrarySectionsAndFields(t *testing.T) {
	fields := []schema.Field{
		{Key: "tls.insecure", Label: "允许不安全 insecure", Type: schema.Bool},
		{Key: "tls.utls.fingerprint", Label: "指纹 fingerprint", Type: schema.Select, Hint: "fingerprint"},
		{Key: "tls.reality.short_id", Label: "short_id", Type: schema.Text},
		{Key: "tls.fragment", Label: "分片", Type: schema.Bool, Platforms: []string{"linux", "darwin"}, Adv: true, Group: "tls"},
	}
	// Templates no longer hold uTLS or REALITY (5e0c953): sections come
	// from the TLS keys they still hold.
	sectioned := append([]schema.Field{
		{Key: "tls.handshake_timeout", Label: "握手超时", Type: schema.Duration},
		{Key: "tls.ech.enabled", Label: "启用 ECH", Type: schema.Bool},
	}, fields[0], fields[3])
	var names []string
	for _, s := range tlsSections(sectioned) {
		names = append(names, s.name)
	}
	if !reflect.DeepEqual(names, []string{"连接安全", "握手与伪装", "ECH", "分片与内核 TLS"}) {
		t.Errorf("sections %v", names)
	}
	shown := editorFields(fields, []string{"tls.utls.fingerprint", "tls.reality.short_id", "tls.fragment"})
	if shown[0].Label != "指纹" || shown[0].Hint != "" || shown[1].Label != "短 ID" {
		t.Errorf("labels %+v", shown)
	}
	if shown[2].Hint != "适用 Linux、macOS" || shown[2].Platforms != nil || shown[2].Adv {
		t.Errorf("platform field %+v", shown[2])
	}
}

func TestNetworkUsageAndCheck(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Outbounds = []*model.Outbound{{Entity: model.Entity{ID: "o", Tag: "节点", Enabled: true,
		NetworkConfigRefs: model.NetworkConfigRefs{"tls": "net1"}}, Type: "vless", Fields: model.Fields{}}}
	profiles := usageProfiles(nil, "", doc)
	uses := model.NetworkUses(profiles, "net1")
	if len(uses) != 1 || uses[0].Label != "当前配置 / 出站 / 节点 / 高级 TLS 配置" {
		t.Errorf("uses %+v", uses)
	}
	old := []*model.NetworkConfig{{ID: "net1", Name: "a", Kind: model.NetworkConfigTLSClient, Fields: model.Fields{}}}
	if checkLibrary(nil, old, profiles) == nil {
		t.Error("removing a used config passed")
	}
	if err := checkLibrary(old, old, profiles); err != nil {
		t.Errorf("unchanged library refused: %v", err)
	}
	if uniqueConfigName("a", old) != "a 2" {
		t.Error("unique name")
	}
}

func TestCoreVersionDropsProgramName(t *testing.T) {
	for in, want := range map[string]string{
		"sing-box version 1.14.4\n\nEnvironment: go1.25": "1.14.4",
		"kiko-core version 1.14.4-kiko":                  "1.14.4-kiko",
		"1.14.0":                                         "1.14.0",
	} {
		if got := coreVersion(in); got != want {
			t.Errorf("coreVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
