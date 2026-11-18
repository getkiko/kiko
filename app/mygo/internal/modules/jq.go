package modules

import (
	"encoding/json"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// jqEdits converts the jq subset that has a body edit counterpart:
// pipelines of del(PATH, …) and PATH = JSON, where PATH is built from
// .name, ."name", ["name"], [n], [] and .[]. Everything else is refused with
// a reason.
func jqEdits(expr string) ([]*model.BodyEdit, string) {
	var edits []*model.BodyEdit
	for _, step := range splitTop(expr, '|') {
		step = strings.TrimSpace(step)
		if step == "" || step == "." {
			continue
		}
		if strings.Contains(step, "|=") {
			return nil, "jq 的更新赋值（|=）不支持"
		}
		if inner, ok := strings.CutPrefix(step, "del("); ok && strings.HasSuffix(step, ")") {
			inner = strings.TrimSuffix(inner, ")")
			for _, part := range splitTop(inner, ',') {
				path, ok := jqPath(part)
				if !ok {
					return nil, "jq 路径「" + strings.TrimSpace(part) + "」不支持"
				}
				edits = append(edits, &model.BodyEdit{Action: model.BodyActionJSONDelete, Path: path})
			}
			continue
		}
		idx := assignIndex(step)
		if idx < 0 {
			return nil, "jq 表达式「" + shorten(step) + "」不支持"
		}
		path, ok := jqPath(step[:idx])
		value := strings.TrimSpace(step[idx+1:])
		if !ok || !json.Valid([]byte(value)) {
			return nil, "jq 表达式「" + shorten(step) + "」不支持"
		}
		edits = append(edits, &model.BodyEdit{Action: model.BodyActionJSONSet, Path: path, Value: value})
	}
	if len(edits) == 0 {
		return nil, "jq 表达式为空"
	}
	return edits, ""
}

// splitTop splits s at sep outside quotes and brackets. A "|" followed by
// "=" is not a separator.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			if sep == '|' && i+1 < len(s) && s[i+1] == '=' {
				continue
			}
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// assignIndex returns the index of the "=" of a plain assignment outside
// quotes and brackets, or -1.
func assignIndex(s string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == '=' && depth == 0:
			if i > 0 && strings.IndexByte("|+-*/%!<>=", s[i-1]) >= 0 {
				return -1
			}
			if i+1 < len(s) && s[i+1] == '=' {
				return -1
			}
			return i
		}
	}
	return -1
}

func isNameStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

// jqPath converts a jq path to the JSONPath subset of the body edits.
func jqPath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "." {
		return "$", true
	}
	if s == "" || s[0] != '.' {
		return "", false
	}
	var out strings.Builder
	out.WriteByte('$')
	i := 0
	for i < len(s) {
		switch s[i] {
		case '.':
			i++
			if i >= len(s) {
				return "", false
			}
			switch {
			case s[i] == '[':
			case s[i] == '"':
				end := strings.IndexByte(s[i+1:], '"')
				if end < 0 {
					return "", false
				}
				name := s[i+1 : i+1+end]
				if name == "" || strings.ContainsAny(name, `'\`) {
					return "", false
				}
				out.WriteString("['" + name + "']")
				i += end + 2
			case isNameStart(s[i]):
				start := i
				for i < len(s) && (isNameStart(s[i]) || s[i] >= '0' && s[i] <= '9') {
					i++
				}
				out.WriteString("." + s[start:i])
			default:
				return "", false
			}
		case '[':
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				return "", false
			}
			inner := strings.TrimSpace(s[i+1 : i+end])
			switch {
			case inner == "":
				out.WriteString("[*]")
			case isIndex(inner):
				out.WriteString("[" + inner + "]")
			case len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' && !strings.ContainsAny(inner[1:len(inner)-1], `"'\`) && len(inner) > 2:
				out.WriteString("['" + inner[1:len(inner)-1] + "']")
			default:
				return "", false
			}
			i += end + 1
		default:
			return "", false
		}
	}
	return out.String(), true
}

func isIndex(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" || len(s) > 9 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
