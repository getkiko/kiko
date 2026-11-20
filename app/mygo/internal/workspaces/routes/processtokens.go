package routes

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// processClipKind is the ClipKind of the process field's tokens.
const processClipKind = "process"

// processTokenID identifies a process tag among a field's tokens.
func processTokenID(tag model.ProcessTag) string {
	return fmt.Sprint(tag.Path, tag.Index, tag.Field)
}

// processPrefixes name each process kind before its value as a copy
// writes it ("path:/usr/bin/curl"), for a paste to read the kind back;
// the field names read as well.
var processPrefixes = []struct{ prefix, kind string }{
	{"app:", "application"}, {"name:", model.ProcessTypeName}, {"path:", model.ProcessTypePath}, {"regex:", model.ProcessTypeRegex},
	{"application:", "application"}, {"process_name:", model.ProcessTypeName},
	{"process_path_regex:", model.ProcessTypeRegex}, {"process_path:", model.ProcessTypePath},
}

// processClip is a selection as a copy writes it, its kind first.
func processClip(item model.ProcessSelection) string {
	item = model.PlainProcessSelection(item)
	switch {
	case item.Type == model.ProcessTypeName:
		return "name:" + item.Value
	case item.Type == model.ProcessTypePath:
		return "path:" + item.Value
	}
	return "regex:" + item.Value
}

// processPrefixed splits a value whose kind is named before it.
func processPrefixed(value string) (kind, rest string, ok bool) {
	for _, p := range processPrefixes {
		if len(value) > len(p.prefix) && strings.EqualFold(value[:len(p.prefix)], p.prefix) {
			return p.kind, strings.TrimSpace(value[len(p.prefix):]), true
		}
	}
	return "", value, false
}

// reProcessRegex tells a value that reads as a regex rather than a name or
// a path.
var reProcessRegex = regexp.MustCompile(`^\^|\$$|\\[.dswDSW]|\.\*|\.\+|[()\[\]|]`)

// looksLikeRegex reports whether a typed value reads as a regex: an
// absolute path (a Windows one holds "\W", "(x86)") is a path unless
// anchored as a regex is.
func looksLikeRegex(value string) bool {
	if reAbsolutePath.MatchString(value) {
		return strings.HasPrefix(value, "^") || strings.HasPrefix(value, "(?")
	}
	return reProcessRegex.MatchString(value)
}

// processEntry parses a typed or pasted process value as the kind chosen
// (name, path, regex or application; "" infers it), or why it cannot.
func processEntry(value, kind string) (model.ProcessSelection, string) {
	value = strings.TrimSpace(value)
	if kind == "" {
		kind, value, _ = processPrefixed(value)
	}
	if kind == "" {
		switch {
		case reAbsolutePath.MatchString(value) && !looksLikeRegex(value):
			kind = model.ProcessTypePath
		case looksLikeRegex(value):
			kind = model.ProcessTypeRegex
		default:
			kind = model.ProcessTypeName
		}
	}
	if value == "" {
		return model.ProcessSelection{}, "请填写进程名称、路径或正则"
	}
	switch kind {
	case "application":
		value = strings.TrimRight(value, "/")
		if !reAbsolutePath.MatchString(value) {
			return model.ProcessSelection{}, "请填写应用的完整路径"
		}
		name := strings.TrimSuffix(filepath.Base(strings.ReplaceAll(value, `\`, "/")), ".app")
		return model.AppSelection(value, name), ""
	case model.ProcessTypePath:
		if !reAbsolutePath.MatchString(value) {
			return model.ProcessSelection{}, "请填写完整的可执行文件路径"
		}
	case model.ProcessTypeRegex:
		if _, err := regexp.Compile(value); err != nil {
			return model.ProcessSelection{}, "正则表达式无效"
		}
	}
	return model.ProcessSelection{Source: model.ProcessSourceCustom, Type: kind, Value: value}, ""
}

// processSuggestions are the kinds a typed process value could be added
// as: a path or a name first, then a regex, and a whole application for a
// bundle path.
func processSuggestions(draft string) []kit.TokenSuggestion {
	if draft == "" {
		return nil
	}
	var out []kit.TokenSuggestion
	if _, _, ok := processPrefixed(draft); ok {
		// A kind named first: that kind only.
		if item, problem := processEntry(draft, ""); problem == "" {
			out = append(out, kit.TokenSuggestion{ID: "typed", Label: model.ProcessSelectionLabel(item), Detail: model.ProcessSelectionType(item)})
		}
		return out
	}
	abs := reAbsolutePath.MatchString(draft)
	if abs && strings.HasSuffix(strings.TrimRight(draft, "/"), ".app") {
		out = append(out, kit.TokenSuggestion{ID: "application", Label: model.AppSelection(draft, "").Value, Detail: "应用目录内的进程 · 正则"})
	}
	if abs {
		out = append(out, kit.TokenSuggestion{ID: model.ProcessTypePath, Label: draft, Detail: "路径"})
	} else {
		out = append(out, kit.TokenSuggestion{ID: model.ProcessTypeName, Label: draft, Detail: "名称"})
	}
	regex := kit.TokenSuggestion{ID: model.ProcessTypeRegex, Label: draft, Detail: "正则"}
	if looksLikeRegex(draft) {
		return append([]kit.TokenSuggestion{regex}, out...)
	}
	return append(out, regex)
}

// processTokens is the process TAG input: typed names, paths and regexes,
// a click on a chip edits it in the picker, and its chips copy and paste
// as the other fields' do.
func (w *Workspace) processTokens(c *ui.Context, e *ruleEditor, value *model.Condition, tags []model.ProcessTag, path []int, commit func(*model.Condition)) {
	s := &e.cond
	key := pathKey(path) + ":process"
	group := value
	if group == nil {
		group = model.EmptyProcessCondition()
	}
	query := s.queries[key]
	draft := strings.TrimSpace(query)
	tokens := make([]kit.Token, 0, len(tags))
	byID := map[string]model.ProcessTag{}
	for _, tag := range tags {
		id := processTokenID(tag)
		byID[id] = tag
		detail := ""
		if tag.Excluded {
			detail = "排除"
		}
		plain := model.PlainProcessSelection(tag.ProcessSelection)
		tokens = append(tokens, kit.Token{ID: id, Label: model.ProcessSelectionLabel(plain), Badge: model.ProcessSelectionType(plain), Detail: detail,
			Title: plain.Value, Editable: true, Text: processClip(plain)})
	}
	// add adds the selections, each one not there yet.
	add := func(items []model.ProcessSelection) {
		next := group
		for _, item := range items {
			next = model.AddProcessSelection(next, item)
		}
		if next != group {
			commit(next)
		}
	}
	remove := func(ids []string) {
		next := group
		for i := len(tags) - 1; i >= 0; i-- {
			if id := processTokenID(tags[i]); slices.Contains(ids, id) {
				next = model.ChangeProcessTag(next, tags[i], nil)
			}
		}
		commit(next)
	}
	input := kit.TokenInput(c, "process-tokens", &query, kit.TokenInputOptions{
		Label: "进程", Placeholder: "进程名、可执行文件路径或正则，回车添加", Error: s.errors[key], Tokens: tokens,
		Suggestions: processSuggestions(draft),
		Commit: func(suggestion string) bool {
			if draft == "" {
				return false
			}
			if suggestion == "typed" {
				suggestion = ""
			}
			item, problem := processEntry(draft, suggestion)
			if problem != "" {
				s.errors[key] = problem
				return false
			}
			add([]model.ProcessSelection{item})
			s.errors[key] = ""
			return true
		},
		Remove:    func(id string) { remove([]string{id}) },
		RemoveAll: func(ids []string) { remove(ids); s.errors[key] = "" },
		// Paths and regexes may hold spaces and commas: a paste splits
		// at lines only.
		Split:    processSplit,
		ClipKind: processClipKind,
		Whole:    func(value string) bool { _, _, ok := processPrefixed(value); return ok },
		Paste: func(values []string) {
			var items []model.ProcessSelection
			var bad []string
			for _, v := range values {
				item, problem := processEntry(v, "")
				if problem != "" {
					bad = append(bad, v)
					continue
				}
				items = append(items, item)
			}
			add(items)
			s.errors[key] = pasteProblem(bad)
		},
		TokenClick: func(id string, _ ui.Element) {
			if tag, ok := byID[id]; ok {
				e.cond.process = newProcessPicker(path, &tag)
			}
		},
	})
	if input.Changed() {
		s.errors[key] = ""
	}
	s.queries[key] = query
}

// processSplit splits a pasted list of processes at its lines.
func processSplit(text string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if line = strings.TrimSpace(line); line != "" && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
}
