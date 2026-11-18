package settings

import (
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// This file is the pure logic of Settings › HTTPS Decryption: when the switch may
// turn on, the host lists and the stored settings.

// CA states of GET /mitm/status.
const (
	caAbsent = "absent"
	caReady  = "ready"
	caBroken = "broken"
)

// mitmBlocker says why decryption cannot turn on, "" when it can. A nil
// status (not read yet) blocks as well.
func mitmBlocker(s *api.MITMStatus) string {
	switch {
	case s == nil:
		return "正在读取"
	case !s.Supported:
		return "核心不支持"
	case s.CA.State == caBroken:
		return "证书已损坏"
	case s.CA.State != caReady:
		return "没有根证书"
	case s.CA.TrustUnknown:
		return "信任状态未知"
	case !s.CA.Trusted:
		return "根证书未信任"
	}
	return ""
}

// mitmTrustText is the trust state in a word.
func mitmTrustText(ca api.MITMCA) string {
	switch {
	case !ca.TrustSupported:
		return "不支持"
	case ca.TrustUnknown:
		return "未知"
	case ca.Trusted:
		return "已信任"
	}
	return "未信任"
}

// mitmCoreText is the core's side of the certificate.
func mitmCoreText(s *api.MITMStatus) string {
	switch {
	case !s.Supported:
		return "不支持"
	case !s.Core.Connected:
		return "未连接"
	case s.Core.Pending:
		return "等待中"
	case s.Core.AuthorityFingerprint != "" || s.Core.Enabled:
		return "已生效"
	}
	return "未下发"
}

// mitmDate shows an RFC 3339 time as a local date.
func mitmDate(text string) string {
	t, err := time.Parse(time.RFC3339, text)
	if err != nil || t.IsZero() {
		return "-"
	}
	return timefmt.Format(t, "2006-01-02")
}

// mitmSettingsOf reads the stored settings, defaults when absent.
func mitmSettingsOf(doc *model.Doc) model.MITMSettings {
	if doc == nil || doc.HTTPTools == nil || doc.HTTPTools.MITM == nil {
		return model.MITMSettings{QUIC: model.MITMQUICBlock}
	}
	m := *doc.HTTPTools.MITM
	if m.QUIC != model.MITMQUICAllow {
		m.QUIC = model.MITMQUICBlock
	}
	return m
}

// editMITM applies fn to the document's MITM settings, creating them.
func editMITM(d *model.Doc, fn func(m *model.MITMSettings)) {
	if d.HTTPTools == nil {
		d.HTTPTools = &model.HTTPTools{}
	}
	if d.HTTPTools.MITM == nil {
		d.HTTPTools.MITM = &model.MITMSettings{}
	}
	fn(d.HTTPTools.MITM)
}

// The host list helpers live in mitmpolicy, which the HTTP request
// inspector shares.
var (
	hostList          = mitmpolicy.HostList
	setHostList       = mitmpolicy.SetHostList
	normalizeMITMHost = mitmpolicy.NormalizeHost
	hasHost           = mitmpolicy.HasHost
	addHost           = mitmpolicy.AddHost
	removeHost        = mitmpolicy.RemoveHost
	excludeHost       = mitmpolicy.ExcludeHost
)

// exportFormat is one entry of the export menu.
type exportFormat struct {
	// Format is the value of the export request, also the file extension.
	Format string
	// Label is the menu item.
	Label string
	// Filter names the type in the save panel.
	Filter string
}

// exportFormats are the formats of the export menu, in menu order.
var exportFormats = []exportFormat{
	{api.MITMFormatPEM, "PEM", "PEM"},
	{api.MITMFormatCER, "CER", "CER"},
	{api.MITMFormatCRT, "CRT（Android）", "CRT"},
	{api.MITMFormatP12, "PKCS#12", "PKCS#12"},
	{api.MITMFormatMobileConfig, "iOS 描述文件", "描述文件"},
}

// mitmExportName is the file name of an exported certificate.
func mitmExportName(ca api.MITMCA, format string) string {
	name := strings.NewReplacer(" ", "-", "/", "-").Replace(strings.TrimSpace(ca.CommonName))
	if name == "" {
		name = "kiko-root-ca"
	}
	return name + "." + format
}

// skipRow is a line of Failed Hosts: a host the core skips, or one whose
// interception failed and is not skipped (yet).
type skipRow struct {
	Host string
	// Detail is the count, the remaining skip time and the reason in one line.
	Detail string
	// Skipped is true for an entry of the core's skip table.
	Skipped bool
}

// skipReasonText maps a core reason to its label; unknown ones show as
// they are.
func skipReasonText(reason string) string {
	return mitmpolicy.ReasonText(reason)
}

// skipRemaining says how long a skip lasts.
func skipRemaining(left time.Duration) string {
	switch {
	case left <= 0:
		return "即将恢复"
	case left < time.Minute:
		return "剩余不足 1 分钟"
	case left < time.Hour:
		return "剩余 " + strconv.Itoa(int((left+time.Minute-1)/time.Minute)) + " 分钟"
	}
	return "剩余 " + strconv.Itoa(int((left+time.Hour-1)/time.Hour)) + " 小时"
}

// skipRows lists the skip table, then the failures it does not hold.
func skipRows(s *api.MITMStatus, now time.Time) []skipRow {
	if s == nil {
		return nil
	}
	var rows []skipRow
	seen := map[string]bool{}
	for _, e := range s.Skip {
		host := strings.ToLower(strings.TrimSpace(e.Host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		parts := []string{skipRemaining(e.Until.Sub(now))}
		if reason := skipReasonText(firstText(e.Reason, e.Source)); reason != "" {
			parts = append(parts, reason)
		}
		rows = append(rows, skipRow{Host: e.Host, Detail: strings.Join(parts, " · "), Skipped: true})
	}
	for _, f := range s.Failures {
		host := strings.ToLower(strings.TrimSpace(f.Host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		parts := []string{strconv.Itoa(f.Count) + " 次"}
		if f.LastError != "" {
			parts = append(parts, f.LastError)
		}
		rows = append(rows, skipRow{Host: f.Host, Detail: strings.Join(parts, " · ")})
	}
	return rows
}

func firstText(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// upstreamOf reads the upstream options, empty when absent.
func upstreamOf(m model.MITMSettings) model.MITMUpstream {
	if m.Upstream == nil {
		return model.MITMUpstream{}
	}
	return *m.Upstream
}

// editUpstream applies fn to the upstream options and drops them when
// nothing is left.
func editUpstream(m *model.MITMSettings, fn func(u *model.MITMUpstream)) {
	u := upstreamOf(*m)
	fn(&u)
	if u.Fingerprint == model.MITMFingerprintChrome {
		u.Fingerprint = ""
	}
	if u.Fingerprint == "" && len(u.SkipVerify) == 0 && len(u.CustomRoots) == 0 {
		m.Upstream = nil
		return
	}
	m.Upstream = &u
}

// builtinShown is the count beside the Built-in Exclusions switch: the table size less
// the entries switched off, and nothing while the table is off.
func builtinShown(total int, m model.MITMSettings, on bool) int {
	if !on {
		return 0
	}
	return max(0, total-len(m.DisabledBuiltin))
}

// setDeviceScope puts a device in or out of the decrypted devices.
func setDeviceScope(m *model.MITMSettings, id string, on bool) {
	m.Devices = removeHost(m.Devices, id)
	if on {
		m.Devices = append(m.Devices, id)
	}
	if len(m.Devices) == 0 {
		m.Devices, m.NoLocal = nil, false
	}
}
