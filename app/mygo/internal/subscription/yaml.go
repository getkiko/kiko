package subscription

import (
	"errors"
	"strconv"
	"strings"
)

// The YAML subset of Clash configurations: block mappings and sequences
// by indentation, flow mappings and sequences ({…}, […]), plain, single-
// and double-quoted scalars, literal and folded block scalars, and
// comments. Plain scalars stay text (yamlPlain) so a password such as
// 0123 keeps its digits; readers convert them as they need.

// yamlPlain is an unquoted scalar, as written.
type yamlPlain string

type yamlLine struct {
	indent int
	text   string // without indentation and comment
	raw    string // the whole line, for block scalars
}

var errYAML = errors.New("invalid YAML")

// parseYAML reads a YAML document.
func parseYAML(src string) (any, error) {
	var lines []yamlLine
	for _, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		raw = strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimLeft(raw, " ")
		if trimmed == "---" || trimmed == "..." {
			continue
		}
		text := stripComment(trimmed)
		lines = append(lines, yamlLine{indent: len(raw) - len(trimmed), text: strings.TrimRight(text, " \t"), raw: raw})
	}
	p := &yamlParser{lines: lines}
	p.skipBlank()
	if p.i >= len(p.lines) {
		return nil, nil
	}
	v, err := p.block(p.lines[p.i].indent)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// stripComment drops a comment: a # at the start or after a space,
// outside quotes.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case c == '"' || c == '\'':
			if i == 0 || strings.IndexByte(" \t:,[{-", s[i-1]) >= 0 {
				quote = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

type yamlParser struct {
	lines []yamlLine
	i     int
}

func (p *yamlParser) skipBlank() {
	for p.i < len(p.lines) && p.lines[p.i].text == "" {
		p.i++
	}
}

// block reads the mapping or sequence whose entries start at indent.
func (p *yamlParser) block(indent int) (any, error) {
	p.skipBlank()
	if p.i >= len(p.lines) {
		return nil, nil
	}
	if isSeqItem(p.lines[p.i].text) {
		return p.sequence(indent)
	}
	if _, _, ok := splitKey(p.lines[p.i].text); ok {
		return p.mapping(indent, nil)
	}
	// A lone scalar or flow collection, possibly over several lines.
	text := p.lines[p.i].text
	p.i++
	return p.scalarOrFlow(text, indent)
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

func (p *yamlParser) sequence(indent int) (any, error) {
	out := []any{}
	for {
		p.skipBlank()
		if p.i >= len(p.lines) {
			return out, nil
		}
		line := p.lines[p.i]
		if line.indent != indent || !isSeqItem(line.text) {
			if line.indent > indent {
				return nil, errYAML
			}
			return out, nil
		}
		rest := strings.TrimLeft(strings.TrimPrefix(line.text, "-"), " ")
		if rest == "" {
			p.i++
			p.skipBlank()
			if p.i < len(p.lines) && p.lines[p.i].indent > indent {
				v, err := p.block(p.lines[p.i].indent)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			} else {
				out = append(out, nil)
			}
			continue
		}
		column := indent + (len(line.text) - len(rest))
		if _, _, ok := splitKey(rest); ok && rest[0] != '{' && rest[0] != '[' && rest[0] != '"' && rest[0] != '\'' {
			// A mapping starting on the item's line: its keys sit at the
			// column of the first.
			p.lines[p.i] = yamlLine{indent: column, text: rest, raw: line.raw}
			v, err := p.mapping(column, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		if isSeqItem(rest) {
			p.lines[p.i] = yamlLine{indent: column, text: rest, raw: line.raw}
			v, err := p.sequence(column)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		p.i++
		v, err := p.scalarOrFlow(rest, indent)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (p *yamlParser) mapping(indent int, out map[string]any) (any, error) {
	if out == nil {
		out = map[string]any{}
	}
	for {
		p.skipBlank()
		if p.i >= len(p.lines) {
			return out, nil
		}
		line := p.lines[p.i]
		if line.indent < indent {
			return out, nil
		}
		if line.indent > indent {
			return nil, errYAML
		}
		key, rest, ok := splitKey(line.text)
		if !ok {
			return nil, errYAML
		}
		p.i++
		if rest == "" {
			p.skipBlank()
			if p.i < len(p.lines) && (p.lines[p.i].indent > indent ||
				p.lines[p.i].indent == indent && isSeqItem(p.lines[p.i].text)) {
				v, err := p.block(p.lines[p.i].indent)
				if err != nil {
					return nil, err
				}
				out[key] = v
			} else {
				out[key] = nil
			}
			continue
		}
		v, err := p.scalarOrFlow(rest, indent)
		if err != nil {
			return nil, err
		}
		if key == "<<" {
			if m, ok := v.(map[string]any); ok {
				for k, mv := range m {
					if _, exists := out[k]; !exists {
						out[k] = mv
					}
				}
				continue
			}
		}
		out[key] = v
	}
}

// scalarOrFlow reads a value written after a key or a dash: a block
// scalar, a flow collection (continuing on following lines until it
// closes), or a scalar.
func (p *yamlParser) scalarOrFlow(text string, indent int) (any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "&") { // an anchor: aliases are not followed
		_, after, _ := strings.Cut(text, " ")
		text = strings.TrimSpace(after)
		if text == "" {
			p.skipBlank()
			if p.i < len(p.lines) && p.lines[p.i].indent > indent {
				return p.block(p.lines[p.i].indent)
			}
			return nil, nil
		}
	}
	if text == "|" || text == ">" || strings.HasPrefix(text, "|") && len(text) <= 3 || strings.HasPrefix(text, ">") && len(text) <= 3 {
		return p.blockScalar(text[0] == '>', indent), nil
	}
	if text[0] == '{' || text[0] == '[' {
		for !flowClosed(text) && p.i < len(p.lines) {
			text += " " + strings.TrimSpace(p.lines[p.i].text)
			p.i++
		}
		f := &flowParser{s: text}
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	if text[0] == '"' || text[0] == '\'' {
		for !quoteClosed(text) && p.i < len(p.lines) {
			text += " " + strings.TrimSpace(p.lines[p.i].text)
			p.i++
		}
		f := &flowParser{s: text}
		return f.quoted()
	}
	// Plain scalars may continue on more indented lines.
	for p.i < len(p.lines) && p.lines[p.i].text != "" && p.lines[p.i].indent > indent &&
		!isSeqItem(p.lines[p.i].text) {
		if _, _, ok := splitKey(p.lines[p.i].text); ok {
			break
		}
		text += " " + p.lines[p.i].text
		p.i++
	}
	return yamlPlain(text), nil
}

func (p *yamlParser) blockScalar(folded bool, indent int) string {
	var parts []string
	blockIndent := -1
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if line.text == "" && strings.TrimSpace(line.raw) == "" {
			parts = append(parts, "")
			p.i++
			continue
		}
		if line.indent <= indent {
			break
		}
		if blockIndent < 0 {
			blockIndent = line.indent
		}
		parts = append(parts, line.raw[min(blockIndent, len(line.raw)):])
		p.i++
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	sep := "\n"
	if folded {
		sep = " "
	}
	return strings.Join(parts, sep) + "\n"
}

func flowClosed(s string) bool {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return depth <= 0
}

func quoteClosed(s string) bool {
	q := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' && q == '"' {
			i++
			continue
		}
		if s[i] == q {
			if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			return true
		}
	}
	return false
}

// splitKey splits "key: value" or "key:" of a block mapping; ok is false
// when text is no mapping entry.
func splitKey(text string) (key, rest string, ok bool) {
	if text == "" || isSeqItem(text) {
		return "", "", false
	}
	if text[0] == '"' || text[0] == '\'' {
		f := &flowParser{s: text}
		k, err := f.quoted()
		if err != nil {
			return "", "", false
		}
		after := text[f.i:]
		if !strings.HasPrefix(after, ":") || len(after) > 1 && after[1] != ' ' && after[1] != '\t' {
			return "", "", false
		}
		return k, strings.TrimSpace(after[1:]), true
	}
	if text[0] == '{' || text[0] == '[' {
		return "", "", false
	}
	for i := 0; i < len(text); i++ {
		if text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t') {
			return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:]), true
		}
	}
	return "", "", false
}

// flowParser reads flow collections and quoted scalars.
type flowParser struct {
	s string
	i int
}

func (f *flowParser) space() {
	for f.i < len(f.s) && (f.s[f.i] == ' ' || f.s[f.i] == '\t') {
		f.i++
	}
}

func (f *flowParser) value() (any, error) {
	f.space()
	if f.i >= len(f.s) {
		return nil, nil
	}
	switch f.s[f.i] {
	case '{':
		f.i++
		m := map[string]any{}
		for {
			f.space()
			if f.i >= len(f.s) {
				return nil, errYAML
			}
			if f.s[f.i] == '}' {
				f.i++
				return m, nil
			}
			var key string
			if f.s[f.i] == '"' || f.s[f.i] == '\'' {
				k, err := f.quoted()
				if err != nil {
					return nil, err
				}
				key = k
			} else {
				key = strings.TrimSpace(f.plain(true))
			}
			f.space()
			var v any
			if f.i < len(f.s) && f.s[f.i] == ':' {
				f.i++
				var err error
				if v, err = f.value(); err != nil {
					return nil, err
				}
			}
			m[key] = v
			f.space()
			if f.i < len(f.s) && f.s[f.i] == ',' {
				f.i++
			}
		}
	case '[':
		f.i++
		list := []any{}
		for {
			f.space()
			if f.i >= len(f.s) {
				return nil, errYAML
			}
			if f.s[f.i] == ']' {
				f.i++
				return list, nil
			}
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
			f.space()
			if f.i < len(f.s) && f.s[f.i] == ',' {
				f.i++
			}
		}
	case '"', '\'':
		return f.quoted()
	}
	text := strings.TrimSpace(f.plain(false))
	if text == "" {
		return nil, nil
	}
	return yamlPlain(text), nil
}

// plain reads a plain scalar inside a flow collection, up to , ] } or,
// for a key, ": ".
func (f *flowParser) plain(key bool) string {
	start := f.i
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == ']' || c == '}' {
			break
		}
		if c == ':' && (f.i+1 == len(f.s) || strings.IndexByte(" \t,]}", f.s[f.i+1]) >= 0) {
			break
		}
		if key && c == ':' && f.i+1 < len(f.s) && f.s[f.i+1] == ' ' {
			break
		}
		f.i++
	}
	return f.s[start:f.i]
}

func (f *flowParser) quoted() (string, error) {
	q := f.s[f.i]
	f.i++
	var b strings.Builder
	for f.i < len(f.s) {
		c := f.s[f.i]
		if q == '\'' {
			if c == '\'' {
				if f.i+1 < len(f.s) && f.s[f.i+1] == '\'' {
					b.WriteByte('\'')
					f.i += 2
					continue
				}
				f.i++
				return b.String(), nil
			}
			b.WriteByte(c)
			f.i++
			continue
		}
		switch c {
		case '"':
			f.i++
			return b.String(), nil
		case '\\':
			if f.i+1 >= len(f.s) {
				return "", errYAML
			}
			e := f.s[f.i+1]
			f.i += 2
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case '/', '\\', '"', ' ':
				b.WriteByte(e)
			case 'x', 'u', 'U':
				size := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if f.i+size > len(f.s) {
					return "", errYAML
				}
				n, err := strconv.ParseUint(f.s[f.i:f.i+size], 16, 32)
				if err != nil {
					return "", errYAML
				}
				b.WriteRune(rune(n))
				f.i += size
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
			f.i++
		}
	}
	return "", errYAML
}
