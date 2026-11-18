package subscription

import (
	"strconv"
	"strings"
)

// linkURL is a share link split as the WHATWG URL parser splits a URL of
// a non-special scheme: raw user info, host and port, the query as
// URLSearchParams and the raw fragment.
type linkURL struct {
	scheme             string
	username, password string
	host, port         string
	path               string
	query              query
	hash               string
}

// parseLinkURL splits a share link; ok is false where new URL throws: a
// port that is not a number up to 65535, or a host with a forbidden code
// point.
func parseLinkURL(raw string) (linkURL, bool) {
	var u linkURL
	i := strings.Index(raw, "://")
	if i <= 0 {
		return u, false
	}
	u.scheme, raw = strings.ToLower(raw[:i]), raw[i+3:]
	if h := strings.Index(raw, "#"); h >= 0 {
		u.hash, raw = raw[h+1:], raw[:h]
	}
	if q := strings.Index(raw, "?"); q >= 0 {
		u.query, raw = parseQuery(raw[q+1:]), raw[:q]
	}
	authority := raw
	if p := strings.Index(raw, "/"); p >= 0 {
		authority, u.path = raw[:p], raw[p:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo := authority[:at]
		authority = authority[at+1:]
		if c := strings.Index(userinfo, ":"); c >= 0 {
			u.username, u.password = userinfo[:c], userinfo[c+1:]
		} else {
			u.username = userinfo
		}
	}
	host := authority
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return u, false
		}
		u.host, host = host[:end+1], host[end+1:]
		if host != "" {
			if host[0] != ':' {
				return u, false
			}
			u.port = host[1:]
		}
	} else {
		if c := strings.LastIndex(host, ":"); c >= 0 {
			u.host, u.port = host[:c], host[c+1:]
		} else {
			u.host = host
		}
		if strings.ContainsAny(u.host, " \t<>?@[\\]^|/:#") {
			return u, false
		}
	}
	if u.port != "" {
		for _, r := range u.port {
			if r < '0' || r > '9' {
				return u, false
			}
		}
		if n, err := strconv.Atoi(u.port); err != nil || n > 65535 {
			return u, false
		} else {
			u.port = strconv.Itoa(n)
		}
	}
	if u.query == nil {
		u.query = query{}
	}
	return u, true
}

// query is URLSearchParams: ordered name-value pairs.
type query []queryPair

type queryPair struct{ name, value string }

// parseQuery reads application/x-www-form-urlencoded text.
func parseQuery(s string) query {
	q := query{}
	s = strings.TrimPrefix(s, "?")
	for _, part := range strings.Split(s, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		q = append(q, queryPair{formDecode(name), formDecode(value)})
	}
	return q
}

// formDecode decodes '+' as a space and percent escapes, leaving invalid
// escapes as they are.
func formDecode(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	if !strings.Contains(s, "%") {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(v))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "�")
}

func (q query) get(name string) string {
	for _, p := range q {
		if p.name == name {
			return p.value
		}
	}
	return ""
}

func (q query) has(name string) bool {
	for _, p := range q {
		if p.name == name {
			return true
		}
	}
	return false
}

// set replaces the first pair named name, dropping the others, or adds
// one at the end.
func (q *query) set(name, value string) {
	out := (*q)[:0:0]
	found := false
	for _, p := range *q {
		if p.name != name {
			out = append(out, p)
		} else if !found {
			found = true
			out = append(out, queryPair{name, value})
		}
	}
	if !found {
		out = append(out, queryPair{name, value})
	}
	*q = out
}

func (q *query) del(name string) {
	out := (*q)[:0:0]
	for _, p := range *q {
		if p.name != name {
			out = append(out, p)
		}
	}
	*q = out
}

// String serializes the pairs as URLSearchParams.toString does.
func (q query) String() string {
	var b strings.Builder
	for i, p := range q {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(formEncode(p.name))
		b.WriteByte('=')
		b.WriteString(formEncode(p.value))
	}
	return b.String()
}

func formEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)>>4, 16)+strconv.FormatUint(uint64(c)&15, 16)))
		}
	}
	return b.String()
}

// encodeURIComponent escapes s as JavaScript's encodeURIComponent.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)>>4, 16)+strconv.FormatUint(uint64(c)&15, 16)))
		}
	}
	return b.String()
}
