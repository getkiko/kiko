package model

import "strings"

// MaxCronLength is the longest cron expression the core accepts.
const MaxCronLength = 128

type cronField struct {
	min, max int
	names    []string // names[i] is the name of value min+offset
	offset   int
}

var (
	cronMonthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	cronDayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

	cronFields = [5]cronField{
		{min: 0, max: 59},
		{min: 0, max: 23},
		{min: 1, max: 31},
		{min: 1, max: 12, names: cronMonthNames, offset: 1},
		{min: 0, max: 7, names: cronDayNames, offset: 0},
	}
	cronFieldNames = [5]string{"分钟", "小时", "日", "月", "星期"}

	cronMacros = map[string]bool{
		"@hourly": true, "@daily": true, "@midnight": true, "@weekly": true,
		"@monthly": true, "@yearly": true, "@annually": true,
	}
)

// CronIssue explains why expr is not a usable cron expression; empty when
// valid. It mirrors the grammar of the core's script scheduler: five fields
// (minute hour day-of-month month day-of-week) or a macro such as @daily.
func CronIssue(expr string) string {
	if len(expr) > MaxCronLength {
		return "定时表达式过长"
	}
	for i := 0; i < len(expr); i++ {
		if c := expr[i]; c >= 0x80 || (c < 0x20 && c != '\t') || c == 0x7f {
			return "定时表达式含有无效字符"
		}
	}
	fields := strings.FieldsFunc(expr, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return "请填写定时表达式"
	}
	if strings.HasPrefix(fields[0], "@") {
		if len(fields) == 1 && cronMacros[strings.ToLower(fields[0])] {
			return ""
		}
		return "不支持的定时宏"
	}
	if len(fields) != 5 {
		return "定时表达式须为 5 段：分 时 日 月 星期"
	}
	for i, f := range fields {
		if !cronFieldValid(f, cronFields[i]) {
			return cronFieldNames[i] + "段无效"
		}
	}
	return ""
}

func cronFieldValid(s string, f cronField) bool {
	if s == "" {
		return false
	}
	for _, item := range strings.Split(s, ",") {
		if !cronItemValid(item, f) {
			return false
		}
	}
	return true
}

func cronItemValid(item string, f cronField) bool {
	rng, step, hasStep := strings.Cut(item, "/")
	if hasStep {
		n, ok := cronNumber(step, cronField{min: 0, max: 999})
		if !ok || n < 1 || n > f.max-f.min+1 {
			return false
		}
	}
	if rng == "*" {
		return true
	}
	if rng == "" || strings.HasPrefix(rng, "*") {
		return false
	}
	lo, hi, isRange := strings.Cut(rng, "-")
	a, ok := cronNumber(lo, f)
	if !ok {
		return false
	}
	if !isRange {
		// "n/step" is not allowed.
		return !hasStep
	}
	b, ok := cronNumber(hi, f)
	return ok && a <= b
}

// cronNumber parses a decimal number or, when the field has names, a
// case-insensitive name; the value must lie within the field's range.
func cronNumber(s string, f cronField) (int, bool) {
	if s == "" {
		return 0, false
	}
	if s[0] >= '0' && s[0] <= '9' {
		if len(s) > 3 {
			return 0, false
		}
		n := 0
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
			n = n*10 + int(s[i]-'0')
		}
		return n, n >= f.min && n <= f.max
	}
	ls := strings.ToLower(s)
	for i, name := range f.names {
		if name == ls {
			return i + f.offset, true
		}
	}
	return 0, false
}
