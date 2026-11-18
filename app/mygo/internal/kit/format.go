package kit

import (
	"fmt"
	"math"
	"time"
)

// Bytes formats a byte count for display: "512 B", "1.5 KB", "120 MB".
func Bytes(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return fmt.Sprint(n)
	}
	if n < 1024 {
		return fmt.Sprintf("%.0f B", math.Max(0, n))
	}
	unit := min(4, int(math.Floor(math.Log(n)/math.Log(1024))))
	v := n / math.Pow(1024, float64(unit))
	digits := 1
	if v >= 100 {
		digits = 0
	}
	return fmt.Sprintf("%.*f %s", digits, v, []string{"B", "KB", "MB", "GB", "TB"}[unit])
}

// Rate formats bytes per second.
func Rate(n float64) string { return Bytes(n) + "/s" }

// Uptime formats seconds in its two largest units, as "3 days 4 hours"
// (in Chinese), dropping a zero second unit.
func Uptime(seconds float64) string {
	s := int64(math.Max(0, math.Floor(seconds)))
	switch {
	case s >= 86400:
		days, hours := s/86400, s%86400/3600
		if hours > 0 {
			return fmt.Sprintf("%d天%d小时", days, hours)
		}
		return fmt.Sprintf("%d天", days)
	case s >= 3600:
		hours, minutes := s/3600, s%3600/60
		if minutes > 0 {
			return fmt.Sprintf("%d小时%d分钟", hours, minutes)
		}
		return fmt.Sprintf("%d小时", hours)
	case s >= 60:
		return fmt.Sprintf("%d分钟", s/60)
	}
	return fmt.Sprintf("%d秒", s)
}

// Ago formats a time relative to now: "just now", "5 minutes ago".
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	seconds := math.Max(0, math.Round(now.Sub(t).Seconds()))
	if seconds < 45 {
		return "刚刚"
	}
	minutes := math.Round(seconds / 60)
	if minutes < 60 {
		return fmt.Sprintf("%.0f 分钟前", minutes)
	}
	hours := math.Round(minutes / 60)
	if hours < 24 {
		return fmt.Sprintf("%.0f 小时前", hours)
	}
	return fmt.Sprintf("%.0f 天前", math.Round(hours/24))
}

// Count formats an integer with thousands separators.
func Count(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + Count(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
