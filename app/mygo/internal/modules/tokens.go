package modules

import (
	"errors"
	"regexp"
	"strings"
)

var errUnterminated = errors.New("引号未闭合")

// nextToken reads one whitespace-delimited token from s. A token starting
// with a double or single quote runs to the closing quote that is followed
// by whitespace or the end of the text; \" inside double quotes is an
// escaped quote; without a closing quote the token is plain text up to the
// next whitespace. The remainder (without leading whitespace) is returned.
func nextToken(s string) (token, rest string, err error) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", nil
	}
	if q := s[0]; q == '"' || q == '\'' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' && q == '"' && i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			if c == q && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
				return b.String(), strings.TrimLeft(s[i+1:], " \t"), nil
			}
			b.WriteByte(c)
		}
		// No closing quote: the quote belongs to the token ("ad":true).
	}
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, "", nil
	}
	return s[:i], strings.TrimLeft(s[i:], " \t"), nil
}

// fields splits a line into tokens with nextToken.
func fields(s string) ([]string, error) {
	var out []string
	for {
		tok, rest, err := nextToken(s)
		if err != nil {
			return nil, err
		}
		if tok == "" && rest == "" && strings.TrimSpace(s) == "" {
			return out, nil
		}
		out = append(out, tok)
		s = rest
	}
}

// splitComma splits s at commas outside double quotes.
func splitComma(s string) []string {
	var out []string
	start := 0
	quoted := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// unquote removes one pair of surrounding double quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	return s
}

type param struct {
	key, value string
	hasValue   bool
}

var paramBoundary = regexp.MustCompile(`^\s+[A-Za-z][A-Za-z0-9_-]*=`)

// scanParams reads key=value parameters separated by whitespace. A value
// starting with a double quote runs to the closing quote that is followed by
// the end of the text or by whitespace and another "key=": that lets JSON
// text with bare quotes appear inside data="...". \" is an escaped quote.
func scanParams(s string) ([]param, error) {
	var out []param
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out, nil
		}
		end := strings.IndexAny(s, "= \t")
		if end < 0 {
			out = append(out, param{key: strings.ToLower(s)})
			return out, nil
		}
		if s[end] != '=' {
			out = append(out, param{key: strings.ToLower(s[:end])})
			s = s[end:]
			continue
		}
		key := strings.ToLower(s[:end])
		s = s[end+1:]
		if s != "" && s[0] == '"' {
			closed := -1
			var b strings.Builder
			for i := 1; i < len(s); i++ {
				c := s[i]
				if c == '\\' && i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				if c == '"' {
					tail := s[i+1:]
					if strings.TrimSpace(tail) == "" || paramBoundary.MatchString(tail) {
						closed = i + 1
						break
					}
				}
				b.WriteByte(c)
			}
			if closed < 0 {
				return nil, errUnterminated
			}
			out = append(out, param{key: key, value: b.String(), hasValue: true})
			s = s[closed:]
			continue
		}
		stop := strings.IndexAny(s, " \t")
		if stop < 0 {
			stop = len(s)
		}
		out = append(out, param{key: key, value: s[:stop], hasValue: true})
		s = s[stop:]
	}
}

// shorten cuts text for the unsupported list.
func shorten(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

var commaBoundary = regexp.MustCompile(`^\s*,\s*[A-Za-z][A-Za-z0-9_-]*\s*=`)

// scanScriptParams reads the comma-separated key=value list of a script
// line. A value ends at a comma that is followed by another "key=" (or at
// the end), so commas inside patterns and JSON arguments stay in the value;
// a value in double quotes ends at the closing quote followed by such a
// comma or the end.
func scanScriptParams(s string) ([]param, error) {
	var out []param
	for {
		s = strings.TrimLeft(s, " \t,")
		if s == "" {
			return out, nil
		}
		eq, comma := strings.IndexByte(s, '='), strings.IndexByte(s, ',')
		if eq < 0 || comma >= 0 && comma < eq {
			end := comma
			if end < 0 {
				end = len(s)
			}
			out = append(out, param{key: strings.ToLower(strings.TrimSpace(s[:end]))})
			s = s[end:]
			continue
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		if s != "" && s[0] == '"' {
			closed := -1
			for i := 1; i < len(s); i++ {
				if s[i] == '"' {
					if tail := s[i+1:]; strings.TrimSpace(tail) == "" || commaBoundary.MatchString(tail) {
						closed = i
						break
					}
				}
			}
			if closed < 0 {
				return nil, errUnterminated
			}
			out = append(out, param{key: key, value: s[1:closed], hasValue: true})
			s = s[closed+1:]
			continue
		}
		end := len(s)
		for i := 0; i < len(s); i++ {
			if s[i] == ',' && commaBoundary.MatchString(s[i:]) {
				end = i
				break
			}
		}
		out = append(out, param{key: key, value: strings.TrimSpace(s[:end]), hasValue: true})
		s = s[end:]
	}
}
