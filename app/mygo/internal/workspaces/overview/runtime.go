package overview

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// runtimeSync keeps the runtime shown current: events
// push it, and a query on showing the page, or when no event came for
// a while, recovers missed ones. A query's answer is dropped when an event
// arrived meanwhile or the page was left: the newer state wins.
type runtimeSync struct {
	revision int
	query    int
	pending  bool
	updated  time.Time
}

// runtimeQuery identifies a query: its own number and the revision it
// started at.
type runtimeQuery struct{ query, revision int }

// begin starts a query unless one is pending.
func (s *runtimeSync) begin() (runtimeQuery, bool) {
	if s.pending {
		return runtimeQuery{}, false
	}
	s.pending = true
	s.query++
	return runtimeQuery{s.query, s.revision}, true
}

// finish ends query q, reporting whether its answer, when ok, is still the
// newest state.
func (s *runtimeSync) finish(q runtimeQuery, ok bool, now time.Time) bool {
	if q.query == s.query {
		s.pending = false
	}
	if !ok || q.revision != s.revision {
		return false
	}
	s.update(now)
	return true
}

// update records a newer state, from an event or a control request.
func (s *runtimeSync) update(now time.Time) {
	s.revision++
	s.updated = now
}

// reset forgets the query in flight, as the page is left.
func (s *runtimeSync) reset() {
	s.revision++
	s.query++
	s.pending = false
	s.updated = time.Time{}
}

// stale reports whether no state came for three seconds.
func (s *runtimeSync) stale(now time.Time) bool { return now.Sub(s.updated) > 3*time.Second }

// statusText describes the core's state, "Reading status" before the first.
func statusText(runtime api.Runtime, known bool) string {
	if !known {
		return "正在读取状态"
	}
	switch runtime.State {
	case "running":
		return "运行中"
	case "stopped":
		return "已停止"
	case "starting":
		return "正在启动"
	case "reloading":
		if runtime.Reload != nil && runtime.Reload.Phase == "recovering" {
			return "正在恢复原配置"
		}
		return "正在重载 · 等待核心就绪"
	case "restarting":
		return "正在重启"
	case "degraded":
		if runtime.RestartInSeconds != nil && *runtime.RestartInSeconds != 0 {
			return "等待重试（" + strconv.FormatFloat(*runtime.RestartInSeconds, 'f', -1, 64) + " 秒）"
		}
		return "等待重试"
	case "failed":
		return "启动失败"
	case "stopping":
		return "正在停止"
	}
	return runtime.State
}

// usageBytes formats a byte count for usage: binary units up
// to PB, one decimal past bytes, grouped digits ("1,023.5 KB").
func usageBytes(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	unit := 0
	if value > 0 {
		unit = min(5, int(math.Floor(math.Log(value)/math.Log(1024))))
		unit = max(unit, 0)
	}
	scaled := value / math.Pow(1024, float64(unit))
	digits := 1
	if unit == 0 {
		digits = 0
	}
	return groupDigits(scaled, digits) + " " + []string{"B", "KB", "MB", "GB", "TB", "PB"}[unit]
}

// groupDigits formats v with at most digits decimals, dropping trailing
// zeros, and thousands separators, as toLocaleString("zh-CN") does.
func groupDigits(v float64, digits int) string {
	text := strconv.FormatFloat(v, 'f', digits, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	whole, fraction, _ := strings.Cut(text, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if text == "0" || whole == "-0" {
		sign = ""
	}
	if fraction != "" {
		return sign + whole + "." + fraction
	}
	return fmt.Sprint(sign, whole)
}
