package model

import (
	"math"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// The former TypeScript console validated hosts by parsing them with the
// WHATWG URL parser (new URL(...).hostname). These helpers reproduce the
// host part of that parser for special (http/https) URLs.

var idnaProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.ValidateLabels(false), idna.Transitional(false))

func forbiddenDomainRune(r rune) bool {
	if r <= 0x1f || r == 0x7f {
		return true
	}
	switch r {
	case ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|', '%':
		return true
	}
	return false
}

// whatwgHostname parses the host of a special URL as the WHATWG URL
// parser does and returns URL.hostname: lower-cased IDNA domains,
// normalized IPv4 numbers, bracketed compressed IPv6. ok is false when the
// parser would throw.
func whatwgHostname(host string) (string, bool) {
	if host == "" {
		return "", false
	}
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return "", false
		}
		addr, err := netip.ParseAddr(host[1 : len(host)-1])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", false
		}
		return "[" + formatIPv6(addr.AsSlice()) + "]", true
	}
	domain := host
	if strings.Contains(domain, "%") {
		// Percent-decoding is not needed: the callers reject % before.
		return "", false
	}
	if !isASCII(domain) {
		ascii, err := idnaProfile.ToASCII(domain)
		if err != nil {
			return "", false
		}
		domain = ascii
	} else {
		domain = strings.ToLower(domain)
	}
	if domain == "" {
		return "", false
	}
	for _, r := range domain {
		if forbiddenDomainRune(r) {
			return "", false
		}
	}
	if endsInNumber(domain) {
		v4, ok := parseWHATWGIPv4(domain)
		if !ok {
			return "", false
		}
		return v4, true
	}
	return domain, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func endsInNumber(domain string) bool {
	parts := strings.Split(domain, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := parseIPv4Number(last)
	return ok
}

func parseIPv4Number(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s, base = s[2:], 16
	} else if len(s) >= 2 && s[0] == '0' {
		s, base = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		var digits string
		switch base {
		case 16:
			digits = "0123456789abcdefABCDEF"
		case 8:
			digits = "01234567"
		default:
			digits = "0123456789"
		}
		if strings.Trim(s, digits) == "" {
			return math.Inf(1), true
		}
		return 0, false
	}
	return float64(n), true
}

func parseWHATWGIPv4(domain string) (string, bool) {
	parts := strings.Split(domain, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	numbers := make([]float64, len(parts))
	for i, p := range parts {
		n, ok := parseIPv4Number(p)
		if !ok {
			return "", false
		}
		numbers[i] = n
	}
	for _, n := range numbers[:len(numbers)-1] {
		if n > 255 {
			return "", false
		}
	}
	last := numbers[len(numbers)-1]
	if last >= math.Pow(256, float64(5-len(numbers))) {
		return "", false
	}
	ipv4 := last
	for i, n := range numbers[:len(numbers)-1] {
		ipv4 += n * math.Pow(256, float64(3-i))
	}
	v := uint32(ipv4)
	return strconv.Itoa(int(v>>24)) + "." + strconv.Itoa(int(v>>16&255)) + "." + strconv.Itoa(int(v>>8&255)) + "." + strconv.Itoa(int(v&255)), true
}

// formatIPv6 writes 16 bytes as the URL serializer does: lower-case hex,
// the first longest run of two or more zero pieces compressed.
func formatIPv6(bytes []byte) string {
	words := make([]string, 8)
	for i := range words {
		words[i] = strconv.FormatUint(uint64(bytes[i*2])<<8|uint64(bytes[i*2+1]), 16)
	}
	start, size := -1, 1
	for i := 0; i < len(words); {
		if words[i] != "0" {
			i++
			continue
		}
		end := i
		for end < len(words) && words[end] == "0" {
			end++
		}
		if end-i > size {
			start, size = i, end-i
		}
		i = end
	}
	if start < 0 {
		return strings.Join(words, ":")
	}
	return strings.Join(words[:start], ":") + "::" + strings.Join(words[start+size:], ":")
}

// whatwgAuthority splits the authority of a special URL into user info,
// host and port, as new URL does. ok is false when the parser would throw.
type whatwgAuthority struct {
	Username, Password string
	Hostname           string
	Port               string
}

func parseWHATWGAuthority(authority string) (whatwgAuthority, bool) {
	var out whatwgAuthority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		info := authority[:at]
		authority = authority[at+1:]
		user, pass, _ := strings.Cut(info, ":")
		out.Username, out.Password = percentEncodeUserinfo(user), percentEncodeUserinfo(pass)
	}
	host, port := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.Index(authority, "]")
		if end < 0 {
			return out, false
		}
		host = authority[:end+1]
		rest := authority[end+1:]
		if rest != "" {
			if rest[0] != ':' {
				return out, false
			}
			port = rest[1:]
		}
	} else if i := strings.LastIndex(authority, ":"); i >= 0 {
		host, port = authority[:i], authority[i+1:]
	}
	if port != "" {
		if strings.Trim(port, "0123456789") != "" {
			return out, false
		}
		n, err := strconv.Atoi(strings.TrimLeft(port, "0"))
		if port != "" && strings.TrimLeft(port, "0") == "" {
			n, err = 0, nil
		}
		if err != nil || n > 65535 {
			return out, false
		}
		out.Port = strconv.Itoa(n)
	}
	hostname, ok := whatwgHostname(host)
	if !ok {
		return out, false
	}
	out.Hostname = hostname
	return out, true
}

// percentEncodeUserinfo encodes user info as the URL serializer does.
func percentEncodeUserinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || strings.IndexByte(" \"#<>?`{}/:;=@[\\]^|", c) >= 0 {
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)|0x100, 16)[1:]))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
