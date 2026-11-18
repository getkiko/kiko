// Package modules converts Surge modules (.sgmodule) and Kiko modules
// (.kikomodule) to the module model. The functions are pure: they do no
// network access; downloads of scripts and mapping data are listed in
// Result.Fetches and filled back with Result.Resolve.
package modules

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Parser limits.
const (
	maxLine     = 256 << 10
	maxSections = 64
	maxEntries  = 16384
)

// Errors of ParseSurge.
var (
	ErrTooLarge  = errors.New("模块内容超过 1 MiB")
	ErrNotModule = errors.New("不是有效的模块文件")
	ErrTooMany   = errors.New("模块的条目过多")
)

// Options are the inputs of a conversion.
type Options struct {
	// Arguments overrides the defaults of the module's arguments.
	Arguments map[string]string
	// BaseURL resolves relative script-path values (the module's own URL).
	BaseURL string
}

// Fetch kinds.
const (
	FetchScript = "script"
	FetchData   = "data"
)

// Fetch is a download the conversion needs: a script text or the data of a
// local mapping.
type Fetch struct {
	URL  string
	Kind string
}

type pendingScript struct {
	url    string
	script *model.HTTPScript
	line   int
	text   string
}

type pendingMap struct {
	url  string
	rule *model.MapLocal
	line int
	text string
}

// Result is a converted module.
type Result struct {
	Name        string
	Description string
	Version     string
	Arguments   []model.ModuleArgument
	Contents    model.ModuleContents
	Unsupported []model.UnsupportedItem
	Fetches     []Fetch

	scripts []pendingScript
	maps    []pendingMap
	dropped int
}

// ModuleUpdate returns the result as an update of the module whose text is
// raw.
func (r *Result) ModuleUpdate(raw string) model.ModuleUpdate {
	return model.ModuleUpdate{Name: r.Name, Description: r.Description, Version: r.Version, Arguments: r.Arguments,
		Raw: raw, Contents: r.Contents, Unsupported: r.Unsupported}
}

func (r *Result) unsupported(section string, line int, reason, text string) {
	if len(r.Unsupported) >= model.MaxModuleUnsupported {
		r.dropped++
		return
	}
	r.Unsupported = append(r.Unsupported, model.UnsupportedItem{Section: section, Line: line, Reason: reason, Text: shorten(text)})
}

func (r *Result) addFetch(url, kind string) {
	for _, f := range r.Fetches {
		if f.URL == url {
			return
		}
	}
	r.Fetches = append(r.Fetches, Fetch{URL: url, Kind: kind})
}

// Section names.
const (
	secMITM    = "MITM"
	secURL     = "URL Rewrite"
	secHeader  = "Header Rewrite"
	secBody    = "Body Rewrite"
	secMapLoc  = "Map Local"
	secScript  = "Script"
	secRule    = "Rule"
	secGeneral = "General"
)

var sectionNames = map[string]string{
	"mitm": secMITM, "url rewrite": secURL, "header rewrite": secHeader, "body rewrite": secBody,
	"map local": secMapLoc, "script": secScript, "rule": secRule,
}

var argRef = regexp.MustCompile(`\{\{\{([^{}\r\n]{1,64})\}\}\}`)

type metadata struct {
	name, desc, version, argLine, argDesc string
}

func scanMetadata(lines []string) metadata {
	var md metadata
	for _, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#!")
		if !ok {
			continue
		}
		key, value, found := strings.Cut(rest, "=")
		if !found {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "name":
			if md.name == "" {
				md.name = value
			}
		case "desc", "description":
			if md.desc == "" {
				md.desc = value
			}
		case "version":
			if md.version == "" {
				md.version = value
			}
		case "arguments":
			if md.argLine == "" {
				md.argLine = value
			}
		case "arguments-desc":
			if md.argDesc == "" {
				md.argDesc = value
			}
		}
	}
	return md
}

// parseArguments reads "A:default,B:default". A part without a colon
// continues the previous default; double quotes protect commas.
func parseArguments(line, desc string) []model.ModuleArgument {
	var args []model.ModuleArgument
	if strings.TrimSpace(line) != "" {
		for _, part := range splitComma(line) {
			name, def, found := strings.Cut(part, ":")
			if !found {
				if len(args) > 0 {
					args[len(args)-1].Default += "," + strings.TrimSpace(part)
				}
				continue
			}
			name = strings.TrimSpace(name)
			if model.ModuleArgumentNameIssue(name) != "" || len(args) >= model.MaxModuleArguments {
				continue
			}
			dup := false
			for _, a := range args {
				dup = dup || a.Name == name
			}
			if dup {
				continue
			}
			args = append(args, model.ModuleArgument{Name: name, Default: unquote(def)})
		}
	}
	if desc != "" {
		for _, entry := range strings.Split(desc, `\n`) {
			entry = strings.TrimSpace(entry)
			for i := range args {
				for _, sep := range []string{":", "："} {
					if text, ok := strings.CutPrefix(entry, args[i].Name+sep); ok {
						args[i].Description = strings.TrimSpace(text)
					}
				}
			}
		}
	}
	for i := range args {
		if len(args[i].Default) > model.MaxModuleArgumentValue {
			args[i].Default = args[i].Default[:model.MaxModuleArgumentValue]
		}
	}
	return args
}

// argumentValueIssue explains why an argument value cannot be substituted.
func argumentValueIssue(value string) string {
	if len(value) > model.MaxModuleArgumentValue {
		return "参数值过长"
	}
	if !utf8.ValidString(value) {
		return "参数值含有无效字符"
	}
	for _, r := range value {
		if r == '\r' || r == '\n' || r == 0 || r < 0x20 && r != '\t' || r == 0x7f {
			return "参数值含有换行或控制字符"
		}
	}
	return ""
}

// ParseSurge converts the text of a Surge module. Lines it cannot map are
// listed in Result.Unsupported. Call Resolve afterwards (with nil when there
// is nothing to download) to validate the whole content.
func ParseSurge(text string, opts Options) (*Result, error) {
	if len(text) > model.MaxModuleRaw {
		return nil, ErrTooLarge
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, ErrNotModule
	}
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	lines := strings.Split(text, "\n")
	md := scanMetadata(lines)
	res := &Result{Name: md.name, Description: md.desc, Version: md.version}
	res.Arguments = parseArguments(md.argLine, md.argDesc)
	values := map[string]string{}
	for i := range res.Arguments {
		a := &res.Arguments[i]
		values[a.Name] = a.Default
		if v, ok := opts.Arguments[a.Name]; ok {
			if issue := argumentValueIssue(v); issue != "" {
				return nil, errors.New("参数「" + a.Name + "」：" + issue)
			}
			a.Value = v
			if v != "" {
				values[a.Name] = v
			}
		}
	}
	substitute := func(line string) string {
		if len(values) == 0 || !strings.Contains(line, "{{{") {
			return line
		}
		return argRef.ReplaceAllStringFunc(line, func(m string) string {
			if v, ok := values[m[3:len(m)-3]]; ok {
				return v
			}
			return m
		})
	}

	c := &converter{res: res, opts: opts, rewrites: &model.HTTPRewrites{}, tags: map[string]bool{}}
	section, sections, entries := "", 0, 0
	recognized := md.name != "" || md.argLine != ""
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		if len(line) > maxLine {
			res.unsupported(section, lineNo, "行过长", "")
			continue
		}
		// Section headers are recognised before substitution, so an
		// argument value can never open a section.
		if len(line) > 2 && line[0] == '[' && line[len(line)-1] == ']' {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if sections++; sections > maxSections {
				return nil, ErrTooMany
			}
			if canon, ok := sectionNames[strings.ToLower(name)]; ok {
				section = canon
				recognized = true
			} else {
				section = "?" + name
				recognized = true
				res.unsupported(name, lineNo, "整节未支持", line)
			}
			continue
		}
		if section == "" {
			continue
		}
		if strings.HasPrefix(section, "?") {
			continue
		}
		if entries++; entries > maxEntries {
			return nil, ErrTooMany
		}
		line = substitute(line)
		if len(line) > maxLine {
			res.unsupported(section, lineNo, "行过长", "")
			continue
		}
		c.line(section, lineNo, line)
	}
	if !recognized {
		return nil, ErrNotModule
	}
	c.finish()
	return res, nil
}

type converter struct {
	res      *Result
	opts     Options
	include  []string
	exclude  []string
	seenHost map[string]bool
	rewrites *model.HTTPRewrites
	tags     map[string]bool
	nextID   int
	// scriptByName and scriptRules resolve SCRIPT rules to script tags.
	scriptByName map[string]*model.HTTPScript
	scriptRules  []pendingScriptRule
}

func (c *converter) id(prefix string) string {
	c.nextID++
	return prefix + itoa(c.nextID)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (c *converter) line(section string, no int, line string) {
	switch section {
	case secMITM:
		c.mitm(no, line)
	case secURL:
		c.urlRewrite(no, line)
	case secHeader:
		c.headerRewrite(no, line)
	case secBody:
		c.bodyRewrite(no, line)
	case secMapLoc:
		c.mapLocal(no, line)
	case secScript:
		c.script(no, line)
	case secRule:
		c.rule(no, line)
	}
}

func (c *converter) finish() {
	c.resolveScriptRules()
	r := c.res
	if len(c.include) > 0 || len(c.exclude) > 0 {
		r.Contents.Hostnames = &model.MITMHostnames{Include: c.include, Exclude: c.exclude}
	}
	if !c.rewrites.Empty() {
		r.Contents.Rewrites = c.rewrites
	}
	if r.dropped > 0 {
		r.Unsupported = append(r.Unsupported, model.UnsupportedItem{Section: "", Reason: "另有 " + itoa(r.dropped) + " 项未列出"})
	}
}

// capRewrites reports whether a rewrite list is full.
func (c *converter) full(n int) bool { return n >= model.MaxModuleRewrites }
