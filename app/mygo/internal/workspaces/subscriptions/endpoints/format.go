package endpoints

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// The pure logic of the endpoints' runtime display: formatting, Tailscale
// setup, ping and Taildrop tasks.

// stateTone is how a state looks: ok, warn, error or idle.
type stateTone int

const (
	toneIdle stateTone = iota
	toneOK
	toneWarn
	toneError
)

var (
	reErrorState = regexp.MustCompile(`error|fail|fatal`)
	reWarnState  = regexp.MustCompile(`connecting|starting|auth|login|needs|challenge|pending|stopping|reconnect`)
	reOKState    = regexp.MustCompile(`connected|running|started|ready|online|attached`)
)

// toneOf maps a backend state and an error to a tone.
func toneOf(state, err string) stateTone {
	text := strings.ToLower(state)
	switch {
	case err != "" || reErrorState.MatchString(text):
		return toneError
	case reWarnState.MatchString(text):
		return toneWarn
	case reOKState.MatchString(text):
		return toneOK
	}
	return toneIdle
}

var tailscaleStateLabels = map[string]string{
	"running": "运行中", "started": "已启动", "starting": "启动中", "stopping": "停止中", "stopped": "已停止",
	"connected": "已连接", "connecting": "连接中", "online": "在线", "offline": "离线", "ready": "就绪",
	"needslogin": "需要登录", "needsauth": "需要认证", "auth": "认证中", "authenticating": "认证中",
	"challenge": "等待认证", "pending": "等待中", "reconnecting": "重新连接中", "degraded": "连接降级",
	"error": "错误", "failed": "失败", "fatal": "严重错误",
}

var reStateSeparators = regexp.MustCompile(`[\s_-]+`)

// tailscaleStateLabel shows a Tailscale state code in Chinese.
func tailscaleStateLabel(stateText, backendState string) string {
	raw := stateText
	if raw == "" {
		raw = backendState
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "未知"
	}
	if label, ok := tailscaleStateLabels[reStateSeparators.ReplaceAllString(strings.ToLower(raw), "")]; ok {
		return label
	}
	return raw
}

// healthDiagnostic is a health message as the status shows it.
type healthDiagnostic struct {
	label, text     string
	connectionIssue bool
}

var reSSHACL = regexp.MustCompile(`(?i)tailscale ssh enabled, but access controls (?:don't|do not) allow anyone to access this device`)

func tailscaleHealthDiagnostic(message string) healthDiagnostic {
	if reSSHACL.MatchString(message) {
		return healthDiagnostic{label: "SSH 访问策略",
			text: "Tailscale SSH 已开启，当前 tailnet ACL 尚未授权用户访问此设备。请在 Tailscale 管理后台配置 SSH 访问规则。"}
	}
	return healthDiagnostic{label: "运行提示", text: message}
}

// stateLine is a state's text and tone.
type stateLine struct {
	text string
	tone stateTone
}

// staleAfter is how old a Tailscale observation may be; the core sends
// one every five seconds.
const staleAfter = 20 * time.Second

// tailscaleConnectionState is a Tailscale endpoint's state line.
func tailscaleConnectionState(s *api.TailscaleStatus, now time.Time) stateLine {
	if s.ObservedAt != "" {
		if ms, err := strconv.ParseFloat(s.ObservedAt, 64); err == nil && float64(now.UnixMilli())-ms > float64(staleAfter.Milliseconds()) {
			return stateLine{"状态已过期", toneWarn}
		}
	}
	online := s.Self != nil && s.Self.Online
	if s.BackendState == "Running" && s.Self != nil && !online {
		return stateLine{"控制连接已断开", toneWarn}
	}
	for _, message := range s.Health {
		if tailscaleHealthDiagnostic(message).connectionIssue {
			return stateLine{"连接异常", toneWarn}
		}
	}
	if s.BackendState == "Running" && online {
		return stateLine{"控制连接在线", toneOK}
	}
	return stateLine{tailscaleStateLabel(s.StateText, s.BackendState), toneOf(s.BackendState, "")}
}

// formatBytes formats a byte count: "512 B",
// "1.5 KB", "5.0 GB".
func formatBytes(n int64) string {
	size := float64(n)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatFloat(size, 'f', -1, 64) + " B"
	}
	digits := 1
	if size >= 100 {
		digits = 0
	}
	return strconv.FormatFloat(size, 'f', digits, 64) + " " + units[i]
}

// idNumber reads a USB vendor or product id: a JSON number as is,
// text as hexadecimal; ok is false when it does not parse.
func idNumber(v api.USBID) (int, bool) {
	if v.Numeric {
		f, err := strconv.ParseFloat(v.Text, 64)
		return int(f), err == nil
	}
	text := strings.TrimSpace(v.Text)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "0x"), "0X")
	end := 0
	for end < len(text) && strings.ContainsRune("0123456789abcdefABCDEF", rune(text[end])) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(text[:end], 16, 64)
	return int(n), err == nil
}

// hexID shows an id as four hexadecimal digits.
func hexID(v api.USBID) string {
	n, _ := idNumber(v)
	return fmt.Sprintf("%04x", n)
}

// deviceRow is an entry of a USB/IP service's devices.
type deviceRow struct {
	BusID, VendorID, ProductID, Serial string
}

// usbDeviceToRow matches a local device: by vendor, product and serial
// when it has a serial (a different port still matches), else also by
// bus id, to tell identical models apart.
func usbDeviceToRow(d api.USBLocalDevice) deviceRow {
	row := deviceRow{Serial: d.Serial}
	if n, ok := idNumber(d.VendorID); ok {
		row.VendorID = strconv.Itoa(n)
	}
	if n, ok := idNumber(d.ProductID); ok {
		row.ProductID = strconv.Itoa(n)
	}
	if row.Serial == "" {
		row.BusID = d.BusID
	}
	return row
}

// ---- tailscale-setup ----

// tailscaleKeysURL is the admin console's keys page.
const tailscaleKeysURL = "https://login.tailscale.com/admin/settings/keys"

// tailscaleLoginMode is "key" with an auth key filled in, else "link".
func tailscaleLoginMode(fields map[string]any) string {
	if key, ok := fields["auth_key"].(string); ok && strings.TrimSpace(key) != "" {
		return "key"
	}
	return "link"
}

// tailnetHostname is the name Tailscale gives a device of hostname, as
// its own clients do (dnsname.SanitizeHostname): lowercase, the first
// label without .local, .localdomain or .lan, other characters as "-",
// at most 63; "" without one.
func tailnetHostname(hostname string) string {
	name := strings.ToLower(strings.TrimSpace(hostname))
	for _, suffix := range []string{".local", ".localdomain", ".lan"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	label := []byte(name)
	for i, b := range label {
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
			label[i] = '-'
		}
	}
	name = strings.Trim(string(label), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// ---- tailscale-ping ----

// errPingTimeout and the others are pingOnce's failures.
var (
	errPingTimeout  = errors.New("Ping 超时，请重试")
	errPingNoResult = errors.New("Ping 未返回结果")
	errPingCanceled = errors.New("Ping 已取消")
)

// pingOnce runs a ping stream until its first result, bounding the whole
// operation, connecting included, by timeout. It cancels the stream as
// it settles; ctx ending cancels it with errPingCanceled.
func pingOnce[T any](ctx context.Context, start func(ctx context.Context, result func(T)) error, timeout time.Duration) (T, error) {
	var zero T
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan T, 1)
	done := make(chan error, 1)
	go func() {
		done <- start(ctx, func(v T) {
			select {
			case results <- v:
			default:
			}
		})
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-results:
		return v, nil
	case err := <-done:
		select {
		case v := <-results:
			return v, nil
		default:
		}
		if err == nil {
			return zero, errPingNoResult
		}
		return zero, err
	case <-timer.C:
		return zero, errPingTimeout
	case <-ctx.Done():
		return zero, errPingCanceled
	}
}

// ---- taildrop-tasks ----

// taildropActive reports a send still running.
func taildropActive(t api.TaildropSendTask) bool {
	return t.State == "uploading" || t.State == "sending"
}

// taildropTracker keeps every send, shared by the Tailscale sheets:
// progress and outcomes outlive any one sheet. onOutcome is told once of
// each send that completes or fails while watched.
type taildropTracker struct {
	tasks     map[string]api.TaildropSendTask
	order     []string
	notified  map[string]bool
	onOutcome func(api.TaildropSendTask)
}

func newTaildropTracker(onOutcome func(api.TaildropSendTask)) *taildropTracker {
	return &taildropTracker{tasks: map[string]api.TaildropSendTask{}, notified: map[string]bool{}, onOutcome: onOutcome}
}

// merge records a task unless an equal or newer revision is known; live
// marks an event, as opposed to a snapshot's history.
func (t *taildropTracker) merge(task api.TaildropSendTask, live bool) bool {
	previous, known := t.tasks[task.ID]
	if known && previous.Revision >= task.Revision {
		return false
	}
	if !known {
		t.order = append(t.order, task.ID)
	}
	t.tasks[task.ID] = task
	if (task.State == "completed" || task.State == "failed") && !t.notified[task.ID] {
		t.notified[task.ID] = true
		// Snapshots carry history; polling can also recover a missed outcome.
		if (live || known && taildropActive(previous)) && t.onOutcome != nil {
			t.onOutcome(task)
		}
	}
	return true
}

// snapshot lists the tasks in the order they were first seen.
func (t *taildropTracker) snapshot() []api.TaildropSendTask {
	out := make([]api.TaildropSendTask, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.tasks[id])
	}
	return out
}

// taildropOutcome is the toast of a finished send.
func taildropOutcome(task api.TaildropSendTask) string {
	names := make([]string, len(task.Files))
	for i, f := range task.Files {
		names[i] = f.Name
	}
	joined := strings.Join(names, "、")
	if task.State == "completed" {
		return "文件已发送：" + joined
	}
	if task.Error != "" {
		return "文件发送失败：" + task.Error
	}
	return "文件发送失败：" + joined
}
