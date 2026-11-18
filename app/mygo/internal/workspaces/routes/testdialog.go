package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// testDialog runs a policy test on the server's running core
// (POST /policy/test).
type testDialog struct {
	open                     bool
	dns                      bool
	device, inbound          string
	sourceIP, domain, destIP string
	network, protocol        string
	sourcePort, destPort     string
	loading                  bool
	err                      string
	result                   *api.PolicyTestResult
	// resultHost is the host the result answers.
	resultHost string
	// resultSource is the device and port the result answers.
	resultSource mitmpolicy.Source
	cancel       context.CancelFunc
	// seq tells a reply from the request it answers.
	seq int
}

// firstHost is the first single IPv4 address of addresses.
func firstHost(addresses []string) string {
	for _, a := range addresses {
		v := strings.Split(a, "/")[0]
		if _, ok := model.ParseNetwork(v); ok && !strings.Contains(v, ":") {
			return v
		}
	}
	return ""
}

// testDevices are the scopes a test may come from: the ones in effect (the
// core answers with the active profile), all but Pre-rules.
func testDevices(doc *model.Doc) []string {
	var out []string
	for _, id := range model.PolicyOwners(doc) {
		if id != model.PreDeviceID {
			out = append(out, id)
		}
	}
	return out
}

// testInbounds are the enabled inbounds and endpoints.
func testInbounds(doc *model.Doc) []string {
	var out []string
	for _, in := range doc.Inbounds {
		if in.Enabled {
			out = append(out, in.Tag)
		}
	}
	for _, ep := range doc.Endpoints {
		if ep.Enabled {
			out = append(out, ep.Tag)
		}
	}
	return out
}

// sourceFor is a device's sample source address.
func (w *Workspace) sourceFor(doc *model.Doc, id string) string {
	if model.IsLocalScope(id) {
		var addresses []string
		for _, a := range w.store.LocalAddresses {
			if !strings.HasPrefix(a, "127.") {
				addresses = append(addresses, a)
			}
		}
		if host := firstHost(addresses); host != "" {
			return host
		}
		return "127.0.0.1"
	}
	if d := findDevice(doc, id); d != nil {
		return firstHost(d.Addresses)
	}
	return ""
}

func newTestDialog(w *Workspace, doc *model.Doc) *testDialog {
	devices := testDevices(doc)
	device := model.OtherDeviceID
	if len(devices) > 0 {
		device = devices[0]
	}
	for _, id := range devices {
		if id == w.owner {
			device = id
		}
	}
	inbound := ""
	for _, in := range doc.Inbounds {
		if in.Enabled && in.Type == "tun" {
			inbound = in.Tag
			break
		}
	}
	if inbound == "" {
		if list := testInbounds(doc); len(list) > 0 {
			inbound = list[0]
		}
	}
	t := &testDialog{open: true, dns: w.sec == sectionDNS, device: device, inbound: inbound, sourceIP: w.sourceFor(doc, device),
		network: "tcp", protocol: "tls", sourcePort: "53421", destPort: "443"}
	if t.dns {
		t.network, t.protocol, t.destPort = "udp", "dns", "53"
	}
	return t
}

// clear forgets the last result as the form changes.
func (t *testDialog) clear() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.loading, t.result, t.err = false, nil, ""
	t.seq++
}

// request validates the form into a test request.
func (t *testDialog) request() (api.PolicyTestRequest, error) {
	normalized := ""
	if strings.TrimSpace(t.domain) != "" {
		normalized = model.NormalizeDomainName(t.domain)
		if normalized == "" {
			return api.PolicyTestRequest{}, errors.New("请输入有效域名")
		}
	}
	dest := strings.TrimSpace(t.destIP)
	if dest != "" {
		if _, ok := model.ParseNetwork(dest); !ok || strings.Contains(t.destIP, "/") {
			return api.PolicyTestRequest{}, errors.New("请输入单个有效目标 IP 地址")
		}
	}
	if t.dns && normalized == "" {
		return api.PolicyTestRequest{}, errors.New("请输入查询域名")
	}
	if !t.dns && normalized == "" && dest == "" {
		return api.PolicyTestRequest{}, errors.New("请输入域名或目标 IP")
	}
	source := strings.TrimSpace(t.sourceIP)
	if _, ok := model.ParseNetwork(source); t.inbound == "" || !ok || strings.Contains(t.sourceIP, "/") {
		return api.PolicyTestRequest{}, errors.New("请选择进站并填写单个有效来源 IP")
	}
	port, err1 := strconv.Atoi(strings.TrimSpace(t.destPort))
	sport, err2 := strconv.Atoi(strings.TrimSpace(t.sourcePort))
	if err1 != nil || err2 != nil || port < 1 || port > 65535 || sport < 1 || sport > 65535 {
		return api.PolicyTestRequest{}, errors.New("端口范围为 1–65535")
	}
	req := api.PolicyTestRequest{Section: "route", Inbound: t.inbound, SourceIP: source, Domain: normalized, DestinationIP: dest,
		Network: t.network, Protocol: t.protocol, SourcePort: sport, DestinationPort: port}
	if t.dns {
		req.Section, req.DestinationIP = "dns", ""
	}
	return req, nil
}

// run sends the test.
func (w *Workspace) runTest(t *testDialog) {
	req, err := t.request()
	if err != nil {
		t.err, t.result = err.Error(), nil
		return
	}
	t.clear()
	t.loading = true
	t.resultHost, t.resultSource = "", mitmpolicy.Source{}
	if req.Protocol == "tls" {
		t.resultHost = req.Domain
		t.resultSource = mitmpolicy.Source{Device: t.device, Port: req.DestinationPort}
	}
	w.refreshTrust()
	seq := t.seq
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	env.Go(w.env, func(context.Context) (api.PolicyTestResult, error) {
		return w.env.API.TestPolicy(ctx, req)
	}, func(result api.PolicyTestResult, err error) {
		if t.seq != seq {
			return
		}
		t.loading, t.cancel = false, nil
		if err != nil {
			t.err = testError(err)
			return
		}
		t.result = &result
	})
}

// testError is a failed test's message: the server's error field, or the
// failure.
func testError(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		var body struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(apiErr.Body, &body) == nil && body.Error != "" {
			return body.Error
		}
	}
	if err.Error() == "" {
		return "测试失败"
	}
	return err.Error()
}

// testDialogView is the "Test routing rules" / "Test DNS routing" dialog.
func (w *Workspace) testDialogView(c *ui.Context, doc *model.Doc) {
	t := w.test
	title := "测试路由规则"
	if t.dns {
		title = "测试 DNS 分流"
	}
	input := func(key, label, placeholder string, value *string) {
		dialogRow(c, label, func() {
			if kit.Input(c, key, value, placeholder).Label(label).Changed() {
				t.clear()
			}
		})
	}
	kit.Dialog(c, &t.open, title, 0, func() {
		dialogRow(c, "测试设备", func() {
			var options []kit.Option
			for _, id := range testDevices(doc) {
				options = append(options, kit.Option{Value: id, Label: model.PolicyOwnerName(doc, id)})
			}
			if kit.Select(c, "device", &t.device, options, kit.SelectOptions{Label: "测试设备"}).Changed() {
				t.sourceIP = w.sourceFor(doc, t.device)
				t.clear()
			}
		})
		dialogRow(c, "进站", func() {
			var options []kit.Option
			for _, tag := range testInbounds(doc) {
				options = append(options, kit.Option{Value: tag, Label: tag})
			}
			if kit.Select(c, "inbound", &t.inbound, withUnknown(options, t.inbound), kit.SelectOptions{Label: "进站"}).Changed() {
				t.clear()
			}
		})
		input("source", "来源 IP", "192.168.1.10", &t.sourceIP)
		dialogRow(c, "域名", func() {
			if kit.Input(c, "domain", &t.domain, "www.example.com").Label("测试域名").Changed() {
				t.clear()
			}
		})
		if !t.dns {
			input("dest", "目标 IP", "自动解析域名", &t.destIP)
		}
		dialogRow(c, "传输协议", func() {
			if kit.Select(c, "network", &t.network, []kit.Option{{Value: "tcp", Label: "TCP"}, {Value: "udp", Label: "UDP"}}, kit.SelectOptions{Label: "传输协议"}).Changed() {
				switch {
				case t.dns:
					t.protocol = "dns"
				case t.network == "tcp":
					t.protocol = "tls"
				default:
					t.protocol = "quic"
				}
				t.clear()
			}
		})
		if !t.dns {
			dialogRow(c, "应用协议", func() {
				options := []kit.Option{{Value: "tls", Label: "TLS / HTTPS"}, {Value: "http", Label: "HTTP"}, {Value: "ssh", Label: "SSH"}, {Value: "quic", Label: "QUIC"}, {Value: "", Label: "其他"}}
				if kit.Select(c, "protocol", &t.protocol, options, kit.SelectOptions{Label: "应用协议"}).Changed() {
					t.clear()
				}
			})
		}
		input("sport", "来源端口", "", &t.sourcePort)
		input("dport", "目标端口", "", &t.destPort)
		if t.err != "" {
			kit.Notice(c, kit.DangerTone, "", t.err, nil)
		}
		if t.result != nil {
			mitm, show := t.mitmOutcome(doc, w.untrusted, *t.result)
			testResultView(c, *t.result, t.dns, func() {
				if show {
					mitmProperty(c, mitm)
				}
			})
		}
	}, func() {
		kit.Button(c, "关闭", kit.ButtonOptions{}).OnClick(func() { t.open = false })
		label := "运行测试"
		if t.loading {
			label = "测试中…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymPlay, Disabled: t.loading}).OnClick(func() {
			if !t.loading {
				w.runTest(t)
			}
		})
	})
	if w.test == t && !t.open {
		t.clear()
		w.test = nil
	}
}

// testResultView shows a test's outcome.
func testResultView(c *ui.Context, r api.PolicyTestResult, dns bool, extra func()) {
	k := kit.T(c)
	final := ""
	if len(r.OutboundPath) > 0 {
		final = r.OutboundPath[len(r.OutboundPath)-1]
	}
	action := final
	switch {
	case r.Action == "reject":
		action = "拒绝连接"
	case r.Action == "hijack-dns":
		action = "DNS 接管"
	case action == "":
		action = "默认出口"
	}
	heading := action
	if dns {
		heading = r.DNSServer
		if heading == "" {
			heading = "DNS 查询结果"
		}
	}
	ui.Column(c).Gap(5).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Label("测试结果").Children(func() {
		ui.Text(c, heading).FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label2).Margin(0, 0, 4)
		matched := "默认策略"
		if r.RuleIndex >= 0 {
			matched = fmt.Sprintf("第 %d 条", r.RuleIndex+1)
		}
		if r.Rule != "" {
			matched += " · " + r.Rule
		}
		kit.Property(c, "匹配规则", matched)
		if dns {
			server := r.DNSServer
			if server == "" {
				server = "核心默认"
			}
			kit.Property(c, "DNS 服务器", server)
			out := final
			if out == "" {
				out = r.DNSDetour
			}
			if out == "" {
				out = "协议默认"
			}
			kit.Property(c, "查询出口", out)
			if len(r.OutboundPath) > 1 {
				kit.Property(c, "出口路径", strings.Join(r.OutboundPath, " → "))
			}
			kit.Property(c, "响应状态", r.DNSRcode)
			addresses := strings.Join(r.ResolvedAddresses, "、")
			if addresses == "" {
				addresses = "无地址记录"
			}
			kit.Property(c, "解析地址", addresses)
		} else {
			if r.SelectedIP != "" {
				kit.Property(c, "使用目标 IP", r.SelectedIP)
			}
			kit.Property(c, "最终出口", action)
			if len(r.OutboundPath) > 1 {
				kit.Property(c, "出口路径", strings.Join(r.OutboundPath, " → "))
			}
			if extra != nil {
				extra()
			}
		}
		if len(r.ScriptMaybe) > 0 {
			kit.Property(c, "可能", strings.Join(r.ScriptMaybe, "、"))
		}
		if r.ConfigRevision != "" {
			kit.Property(c, "运行配置", r.ConfigRevision)
		}
		if ms, err := strconv.ParseFloat(r.TestedAt, 64); err == nil && ms > 0 {
			kit.Property(c, "测试时间", timefmt.FormatMilli(int64(ms), "2006/1/2 15:04:05"))
		}
	})
}
