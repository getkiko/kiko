// Package timefmt formats the times and durations the console shows, in
// one place for every workspace.
package timefmt

import (
	"fmt"
	"time"
)

// Duration is how long something lasted from start to end, or up to now
// while end is zero, in the inspectors' words (in Chinese): 42 seconds under a minute,
// 5 minutes, 1 hour 30 minutes, 1 day 2 hours. "—" for a zero start.
func Duration(start, end, now time.Time) string {
	if start.IsZero() {
		return "—"
	}
	if end.IsZero() {
		end = now
	}
	return Seconds(end.Sub(start).Seconds())
}

// Seconds formats a duration of seconds as Duration does; negative
// counts as 0.
func Seconds(seconds float64) string {
	s := int64(max(0, seconds))
	minutes := s / 60
	switch {
	case minutes < 1:
		return fmt.Sprintf("%d秒", s)
	case minutes < 60:
		return fmt.Sprintf("%d分钟", minutes)
	}
	hours, remainder := minutes/60, minutes%60
	if hours < 24 {
		if remainder > 0 {
			return fmt.Sprintf("%d小时%d分钟", hours, remainder)
		}
		return fmt.Sprintf("%d小时", hours)
	}
	if hours%24 > 0 {
		return fmt.Sprintf("%d天%d小时", hours/24, hours%24)
	}
	return fmt.Sprintf("%d天", hours/24)
}
