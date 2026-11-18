package lanscan

import (
	"net/netip"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	maxTextRunes    = 64 // names and models
	maxServices     = 32 // services kept per host
	maxServiceRunes = 64
	maxFollowUp     = 4 // mDNS follow-up queries per source IP
)

// cleanText makes a device-supplied string safe to show: valid UTF-8, no
// control characters, collapsed whitespace, at most 64 characters.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
		case unicode.IsControl(r) || r == utf8.RuneError || unicode.Is(unicode.Cf, r):
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	out := b.String()
	if r := []rune(out); len(r) > maxTextRunes {
		out = strings.TrimSpace(string(r[:maxTextRunes]))
	}
	return out
}

// appendService adds v unless the list is full or v is empty or too long.
func appendService(list []string, v string) []string {
	if v == "" || len(list) >= maxServices || utf8.RuneCountInString(v) > maxServiceRunes {
		return list
	}
	return appendUnique(list, v)
}

// followLimiter bounds the unicast TXT follow-ups one scan sends: a fixed
// count per source IP, each instance at most once.
type followLimiter struct {
	mu   sync.Mutex
	n    map[string]int
	seen map[string]bool
}

func newFollowLimiter() *followLimiter {
	return &followLimiter{n: map[string]int{}, seen: map[string]bool{}}
}

func (f *followLimiter) allow(src netip.Addr, inst string) bool {
	ip := src.String()
	key := ip + "|" + strings.ToLower(inst)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[key] || f.n[ip] >= maxFollowUp {
		return false
	}
	f.seen[key] = true
	f.n[ip]++
	return true
}
