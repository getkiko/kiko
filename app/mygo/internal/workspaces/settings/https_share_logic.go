package settings

import (
	"fmt"
	"slices"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// This file is the pure logic of the LAN download page of the root
// certificate in Settings › HTTPS Decryption.

// shareReason says why the page cannot start, "" when it can.
func shareReason(s *api.MITMStatus) string {
	switch {
	case s == nil:
		return "正在读取"
	case s.CA.State == caBroken:
		return "证书已损坏"
	case s.CA.State != caReady:
		return "没有根证书"
	}
	return ""
}

// shareFingerprintText groups the fingerprint in fours; text that is not
// a SHA-256 fingerprint shows as it is.
func shareFingerprintText(text string) string {
	if fp, err := api.ParseFingerprint(text); err == nil {
		return api.GroupFingerprint(fp)
	}
	return text
}

// shareRemaining is the time left until expiresAt as m:ss, or h:mm:ss from
// an hour. A partial second counts as a whole one. expired is true at the
// deadline and after it, and when the time cannot be read.
func shareRemaining(expiresAt string, now time.Time) (text string, expired bool) {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "0:00", true
	}
	left := t.Sub(now)
	if left <= 0 {
		return "0:00", true
	}
	seconds := int((left + time.Second - 1) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60), false
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60), false
}

// shareAddress is the candidate to start on: chosen while it is still a
// candidate, else the first; "" without candidates.
func shareAddress(addresses []string, chosen string) string {
	if slices.Contains(addresses, chosen) {
		return chosen
	}
	if len(addresses) > 0 {
		return addresses[0]
	}
	return ""
}
