package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// scriptsProbeConfig carries the package L key; a core without it rejects
// the unknown top-level key.
const scriptsProbeConfig = `{"scripts":[]}`

// scriptsCronProbeConfig holds one disabled cron script: a core without cron
// support rejects the type before it looks at the enabled flag, a core with
// it checks only the shape of a disabled script and never reads the file.
const scriptsCronProbeConfig = `{"scripts":[{"tag":"p","type":"cron","cron":"* * * * *","enabled":false,"source_path":"http/probe.js"}]}`

// scriptsRuleProbeConfig holds one disabled script of each synchronous type:
// a core without package P rejects the types rule and dns before it looks at
// the enabled flag; a core with them checks only the shape of a disabled
// script and never reads the file.
const scriptsRuleProbeConfig = `{"scripts":[{"tag":"r","type":"rule","enabled":false,"source_path":"http/probe.js"},{"tag":"d","type":"dns","enabled":false,"source_path":"http/probe.js"}]}`

// maxScriptSourceBytes bounds one script text accepted from a client.
const maxScriptSourceBytes = 4 << 20

// scriptsSupported probes once whether the core accepts the top-level
// "scripts" option.
func (s *server) scriptsSupported() bool {
	s.mitm.scriptsProbeOnce.Do(func() {
		if s.singBox == "" || s.dataDir == "" {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "scripts-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(scriptsProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.scriptsSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.scriptsSupported
}

// scriptsCronSupported probes once whether the core accepts cron scripts.
func (s *server) scriptsCronSupported() bool {
	if !s.scriptsSupported() {
		return false
	}
	s.mitm.scriptsCronProbeOnce.Do(func() {
		path := filepath.Join(s.dataDir, "runtime", "scripts-cron-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(scriptsCronProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.scriptsCronSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.scriptsCronSupported
}

// scriptsRuleSupported probes once whether the core accepts rule and dns
// scripts together with the `script` rule condition and DNS action.
func (s *server) scriptsRuleSupported() bool {
	if !s.scriptsSupported() {
		return false
	}
	s.mitm.scriptsRuleProbeOnce.Do(func() {
		path := filepath.Join(s.dataDir, "runtime", "scripts-rule-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(scriptsRuleProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.scriptsRuleSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.scriptsRuleSupported
}

// scriptsRuleStrip removes what a core without package P does not know:
// the scripts of type rule and dns, and every route and DNS rule that uses a
// `script` condition or the `script` DNS action. A rule is dropped as a whole
// (also a logical rule holding one): taking out only the condition would
// widen it. It returns the number of scripts and rules removed.
func scriptsRuleStrip(config map[string]any) (scripts, rules int) {
	if list, ok := config["scripts"].([]any); ok {
		kept := make([]any, 0, len(list))
		for _, raw := range list {
			if item, ok := raw.(map[string]any); ok && (item["type"] == "rule" || item["type"] == "dns") {
				scripts++
				continue
			}
			kept = append(kept, raw)
		}
		if scripts > 0 {
			if len(kept) == 0 {
				delete(config, "scripts")
			} else {
				config["scripts"] = kept
			}
		}
	}
	if route, ok := config["route"].(map[string]any); ok {
		rules += scriptRulesDrop(route)
	}
	if dns, ok := config["dns"].(map[string]any); ok {
		rules += scriptRulesDrop(dns)
	}
	return scripts, rules
}

// scriptRulesDrop removes the rules of section that use scripts.
func scriptRulesDrop(section map[string]any) int {
	list, ok := section["rules"].([]any)
	if !ok {
		return 0
	}
	kept := make([]any, 0, len(list))
	for _, raw := range list {
		if scriptRuleUses(raw) {
			continue
		}
		kept = append(kept, raw)
	}
	removed := len(list) - len(kept)
	if removed > 0 {
		section["rules"] = kept
	}
	return removed
}

func scriptRuleUses(raw any) bool {
	rule, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := rule["script"]; ok {
		return true
	}
	if rule["action"] == "script" {
		return true
	}
	if sub, ok := rule["rules"].([]any); ok {
		for _, child := range sub {
			if scriptRuleUses(child) {
				return true
			}
		}
	}
	return false
}

// scriptsCronStrip removes the cron scripts from the scripts array for a
// core that does not know them and returns how many were removed.
func scriptsCronStrip(config map[string]any) int {
	list, ok := config["scripts"].([]any)
	if !ok {
		return 0
	}
	kept := make([]any, 0, len(list))
	for _, raw := range list {
		if item, ok := raw.(map[string]any); ok && item["type"] == "cron" {
			continue
		}
		kept = append(kept, raw)
	}
	removed := len(list) - len(kept)
	if removed == 0 {
		return 0
	}
	if len(kept) == 0 {
		delete(config, "scripts")
	} else {
		config["scripts"] = kept
	}
	return removed
}

// scriptsStrip removes the top-level scripts option for a core that does
// not know it and reports whether anything was removed.
func scriptsStrip(config map[string]any) bool {
	if _, ok := config["scripts"]; !ok {
		return false
	}
	delete(config, "scripts")
	return true
}

// scriptSourceDir is the directory of script sources, relative to the
// core's working directory (the data directory).
const scriptSourceDir = "http"

var scriptPathPattern = regexp.MustCompile(`^http/[0-9a-f]{64}\.js$`)

// scriptPruneAge is how long an unreferenced script file is kept: files of
// the previous configuration stay for a rollback.
const scriptPruneAge = 24 * time.Hour

// scriptsMaterialize writes the script texts carried in the "source" keys of
// the scripts array to <dataDir>/http/<sha256>.js, points source_path at
// the file and removes the key. The path always derives from the text, so a
// client cannot name another file.
func (s *server) scriptsMaterialize(config map[string]any) error {
	list, ok := config["scripts"].([]any)
	if !ok {
		return nil
	}
	dir := filepath.Join(s.dataDir, scriptSourceDir)
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text, ok := item["source"].(string)
		if !ok {
			// No text to write: only a content-addressed path of our own
			// layout may stay; anything else would let a client name a file.
			if path, _ := item["source_path"].(string); !scriptPathPattern.MatchString(path) {
				delete(item, "source_path")
			}
			continue
		}
		delete(item, "source")
		if len(text) > maxScriptSourceBytes {
			return errors.New("脚本过大")
		}
		sum := sha256.Sum256([]byte(text))
		name := hex.EncodeToString(sum[:]) + ".js"
		path := filepath.Join(dir, name)
		if existing, err := os.ReadFile(path); err != nil || !bytes.Equal(existing, []byte(text)) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("写入脚本失败: %w", err)
			}
			if err := atomicWrite(path, []byte(text), 0o600); err != nil {
				return fmt.Errorf("写入脚本失败: %w", err)
			}
		}
		item["source_path"] = scriptSourceDir + "/" + name
	}
	s.scriptsPrune(list, time.Now())
	return nil
}

// scriptsPrune deletes script files that are older than scriptPruneAge and
// referenced neither by the candidate scripts nor by the last applied
// runtime configuration. Failures are ignored.
func (s *server) scriptsPrune(current []any, now time.Time) {
	dir := filepath.Join(s.dataDir, scriptSourceDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, raw := range current {
		if item, ok := raw.(map[string]any); ok {
			if path, _ := item["source_path"].(string); path != "" {
				keep[path] = true
			}
		}
	}
	for _, name := range []string{"config.json", "candidate.json"} {
		data, err := os.ReadFile(filepath.Join(s.dataDir, "runtime", name))
		if err != nil {
			if name == "config.json" && !os.IsNotExist(err) {
				return
			}
			continue
		}
		for _, path := range scriptPathFinder.FindAllString(string(data), -1) {
			keep[path] = true
		}
	}
	for _, entry := range entries {
		path := scriptSourceDir + "/" + entry.Name()
		if entry.IsDir() || keep[path] || !scriptPathPattern.MatchString(path) {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > scriptPruneAge {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

var scriptPathFinder = regexp.MustCompile(`http/[0-9a-f]{64}\.js`)
