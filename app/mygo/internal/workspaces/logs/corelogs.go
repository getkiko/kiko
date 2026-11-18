package logs

import (
	"regexp"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// Levels are the core's log levels, most severe first.
var Levels = []string{"PANIC", "FATAL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"}

// maxCoreLogs bounds the kept core log history.
const maxCoreLogs = 3000

// Entry is a log line with its level.
type Entry struct {
	ID    int
	Level int
	Text  string
}

// Plain strips terminal escape sequences, for search, copy and export.
func Plain(text string) string { return csiPattern.ReplaceAllString(text, "") }

// AppendCoreLogs adds an update of the core's log stream; a reset
// replaces the history. The history keeps the latest 3,000 lines.
func AppendCoreLogs(current []Entry, update api.CoreLogUpdate, nextID func() int) []Entry {
	entries := current
	if update.Reset {
		entries = nil
	}
	next := slices.Clone(entries)
	for _, m := range update.Messages {
		level := m.Level.Number
		if !m.Level.IsNumber {
			level = max(0, slices.Index(Levels, m.Level.Name))
		}
		next = append(next, Entry{ID: nextID(), Level: level, Text: m.Message})
	}
	if len(next) > maxCoreLogs {
		next = next[len(next)-maxCoreLogs:]
	}
	return next
}

// The application prefixes include sbcfg, the server's prefix before the
// rename to Kiko, which servers not yet upgraded still print.
var (
	applicationPrefix            = regexp.MustCompile(`^\[(?:kiko|sbcfg|usbip)\](?:\s|$)`)
	timestampedApplicationPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\S+\s+\[(?:kiko|sbcfg|usbip)\](?:\s|$)`)
	appLevel                     = regexp.MustCompile(`(?i)\b(PANIC|FATAL|ERROR|WARN|INFO|DEBUG|TRACE)\b`)
)

// IsApplicationLog reports whether a server log line is kiko's own (or
// USB/IP's), not the core's output; local diagnostics always are.
func IsApplicationLog(text string, local bool) bool {
	return local || applicationPrefix.MatchString(text) || timestampedApplicationPrefix.MatchString(text)
}

// appLogLevel is an application line's level from its text, INFO when
// it names none.
func appLogLevel(text string) int {
	if m := appLevel.FindStringSubmatch(Plain(text)); m != nil {
		return slices.Index(Levels, strings.ToUpper(m[1]))
	}
	return 4
}
