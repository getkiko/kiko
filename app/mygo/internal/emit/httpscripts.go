package emit

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ScriptSourcePath is the core-relative path of a script source: the file
// is named after the SHA-256 of its text, so equal sources share a file and
// a changed source never overwrites the one a running core compiled.
func ScriptSourcePath(source string) string {
	sum := sha256.Sum256([]byte(source))
	return "http/" + hex.EncodeToString(sum[:]) + ".js"
}

// scriptsConfig builds the core's top-level "scripts" array from the
// enabled, valid scripts; nil when none remains. Each item carries the
// script text in "source" besides "source_path": kiko-server writes the
// file into the core's working directory and removes the key before the
// core reads the configuration. Invalid scripts and duplicate tags are
// skipped with a warning.
func scriptsConfig(tools *model.HTTPTools) ([]any, []string) {
	if tools == nil || len(tools.Scripts) == 0 {
		return nil, nil
	}
	var items []any
	var warnings []string
	enabled := 0
	seen := map[string]bool{}
	for _, s := range tools.Scripts {
		if s == nil || s.Disabled {
			continue
		}
		skip := func(why string) {
			warnings = append(warnings, rewriteLabel("脚本", s.Name, s.ID)+why+"，已跳过")
		}
		if issue := s.Issue(); issue != "" {
			skip("无效（" + issue + "）")
			continue
		}
		tag := s.EffectiveTag()
		if seen[tag] {
			skip("的标识「" + tag + "」重复")
			continue
		}
		if enabled++; enabled > model.MaxScripts {
			skip("超过 256 个上限")
			continue
		}
		seen[tag] = true
		item := obj("tag", tag, "type", s.Type)
		if s.IsHTTP() {
			item.set("pattern", s.Pattern)
		}
		if s.Type == model.ScriptCron {
			item.set("cron", strings.TrimSpace(s.Cron))
		}
		item.set("source_path", ScriptSourcePath(s.Source))
		item.set("source", s.Source)
		if s.IsHTTP() {
			if s.RequiresBody {
				item.set("requires_body", true)
			}
			if s.BinaryBody {
				item.set("binary_body", true)
			}
			if s.MaxSize > 0 {
				item.set("max_size", strconv.Itoa(s.MaxSize/1024)+"KiB")
			}
		}
		if s.Timeout > 0 {
			if s.IsDecision() {
				item.set("timeout", strconv.Itoa(s.Timeout)+"ms")
			} else {
				item.set("timeout", strconv.Itoa(s.Timeout)+"s")
			}
		}
		if arg := strings.TrimSpace(s.Argument); arg != "" {
			item.set("argument", s.Argument)
		}
		items = append(items, item)
	}
	return items, warnings
}

// scriptTypesOf maps the tags of the emitted scripts items to their types.
func scriptTypesOf(items []any) map[string]string {
	types := make(map[string]string, len(items))
	for _, item := range items {
		if o, ok := item.(*object); ok {
			tag, _ := o.get("tag").(string)
			typ, _ := o.get("type").(string)
			types[tag] = typ
		}
	}
	return types
}

// scriptRefIssue explains what is wrong with the script tag of a rule
// condition or action: it must name an emitted script of type want.
func (e *emitter) scriptRefIssue(tag, want string) string {
	switch e.scriptTypes[tag] {
	case want:
		return ""
	case "":
		return "引用的脚本「" + tag + "」不存在、已停用或无效"
	}
	return "引用的脚本「" + tag + "」不是" + scriptTypeLabel(want)
}

func scriptTypeLabel(typ string) string {
	if typ == model.ScriptDNS {
		return "DNS 脚本"
	}
	return "规则脚本"
}

// scriptConditionTags returns the rule script tags of a `script` condition;
// a tag that names no emitted rule script is an error.
func (e *emitter) scriptConditionTags(v any, owner string) []string {
	var tags []string
	for _, tag := range uniqueStrings(splitList(v)) {
		if issue := e.scriptRefIssue(tag, model.ScriptRule); issue != "" {
			e.errors = append(e.errors, owner+issue)
			continue
		}
		tags = append(tags, tag)
	}
	return tags
}
