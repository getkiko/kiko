// Package tsgolden replays calls recorded from the former TypeScript
// console against the Go port.
//
// The recordings are frozen: each Go package keeps its calls in
// testdata/ts-calls.jsonl.gz. The recorder and the TypeScript it ran were
// removed together with that console.
//
// Results are compared as JSON after both sides pass through the same Go
// type, so representation differences (omitted empty fields) do not count,
// and ids the call created (random in both implementations) are replaced
// by placeholders in order of appearance.
package tsgolden

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Call is one recorded call.
type Call struct {
	Fn   string            `json:"fn"`
	Args []json.RawMessage `json:"args"`
	// R is the result; nil when the function returned undefined.
	R json.RawMessage `json:"r"`
	// Throw is the message of the error the function threw.
	Throw string `json:"throw"`
}

// Arg decodes argument i into v; a missing or undefined argument leaves v
// unchanged and returns false.
func (c Call) Arg(i int, v any) bool {
	if i >= len(c.Args) {
		return false
	}
	raw := c.Args[i]
	if strings.TrimSpace(string(raw)) == `{"__undef":true}` {
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		panic(fmt.Sprintf("%s arg %d: %v", c.Fn, i, err))
	}
	return true
}

// Has reports whether argument i was passed and not undefined.
func (c Call) Has(i int) bool {
	return i < len(c.Args) && strings.TrimSpace(string(c.Args[i])) != `{"__undef":true}`
}

// Load reads the recorded calls of a package.
func Load(t *testing.T, path string) []Call {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("recorded calls: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var calls []Call
	blobs := map[string]json.RawMessage{}
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var line struct {
			Blob  string          `json:"blob"`
			Value json.RawMessage `json:"value"`
			Call
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("recorded call: %v", err)
		}
		if line.Blob != "" {
			blobs[line.Blob] = line.Value
			continue
		}
		c := line.Call
		for i, raw := range c.Args {
			var ref struct {
				Blob string `json:"$blob"`
			}
			if json.Unmarshal(raw, &ref) == nil && ref.Blob != "" {
				c.Args[i] = blobs[ref.Blob]
			}
		}
		calls = append(calls, c)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return calls
}

// Adapter runs a recorded call in Go. It returns the result (whose type
// the recorded result is decoded into for comparison) or an error, the
// Go counterpart of a throw.
type Adapter func(c Call) (any, error)

// Options tune a replay.
type Options struct {
	// Normalize rewrites both decoded results before comparing.
	Normalize func(fn string, v any) any
	// Recorded rewrites only the recorded result, before Normalize: for
	// output the Go port changed on purpose, such as CoreWording, so the
	// Go result is still checked as it is.
	Recorded func(fn string, v any) any
	// Fixed lists the TypeScript bugs the Go port fixes on purpose. A call
	// a fix matches is checked by the fix's own Check instead of against
	// the recorded result, which shows the bug.
	Fixed []Fix
}

// Fix is a TypeScript bug the Go port fixes: the recorded calls it
// matches no longer replay as recorded.
type Fix struct {
	// Bug describes the TypeScript bug and the Go behavior.
	Bug string
	// Fn is the recorded function.
	Fn string
	// Match reports whether a call shows the bug.
	Match func(c Call) bool
	// Check verifies the Go result of a matching call, returning what is
	// wrong with it or "".
	Check func(c Call, got any, err error) string
	// Want, instead of Check, rewrites the recorded result of a matching
	// call to the Go behavior (got is the Go result); the call is then
	// compared as any other.
	Want func(c Call, got any) json.RawMessage
}

// Replay runs every call with an adapter and reports mismatches. It
// returns how many calls ran per function.
func Replay(t *testing.T, calls []Call, adapters map[string]Adapter, options Options) map[string]int {
	t.Helper()
	ran := map[string]int{}
	failures := map[string]int{}
	for _, c := range calls {
		adapter, ok := adapters[c.Fn]
		if !ok {
			continue
		}
		ran[c.Fn]++
		got, err := run(adapter, c)
		// Every fix with a Want that matches rewrites the recorded result
		// in turn, so independent fixes of one call compose; without one,
		// the first matching Check decides.
		var check *Fix
		wanted := false
		for i := range options.Fixed {
			fix := &options.Fixed[i]
			if fix.Fn != c.Fn || !fix.Match(c) {
				continue
			}
			if fix.Want != nil {
				c.R = fix.Want(c, got)
				wanted = true
			} else if check == nil {
				check = fix
			}
		}
		if !wanted && check != nil {
			if message := check.Check(c, got, err); message != "" {
				report(t, failures, c, "fixed TypeScript bug ("+check.Bug+"): "+message)
			}
			continue
		}
		if c.Throw != "" {
			if err == nil || err.Error() != c.Throw {
				report(t, failures, c, fmt.Sprintf("want throw %q, got %v", c.Throw, err))
			}
			continue
		}
		if err != nil {
			report(t, failures, c, "unexpected error: "+err.Error())
			continue
		}
		want := decodeLike(c.R, got)
		inputs := idsIn(c.Args)
		gotJSON := canonical(c.Fn, got, inputs, options, false)
		wantJSON := canonical(c.Fn, want, inputs, options, true)
		if gotJSON != wantJSON {
			report(t, failures, c, "\n got: "+gotJSON+"\nwant: "+wantJSON)
		}
	}
	return ran
}

func run(adapter Adapter, c Call) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return adapter(c)
}

func report(t *testing.T, failures map[string]int, c Call, message string) {
	t.Helper()
	failures[c.Fn]++
	if failures[c.Fn] > 3 {
		return
	}
	args, _ := json.Marshal(c.Args)
	if len(args) > 1500 {
		args = append(args[:1500], "…"...)
	}
	t.Errorf("%s(%s): %s", c.Fn, args, message)
}

// decodeLike decodes raw into a value of got's type.
func decodeLike(raw json.RawMessage, got any) any {
	if got == nil {
		var v any
		if raw != nil {
			_ = json.Unmarshal(raw, &v)
		}
		return v
	}
	typ := reflect.TypeOf(got)
	ptr := reflect.New(typ)
	if raw != nil && string(raw) != "null" {
		if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
			return fmt.Sprintf("<cannot decode %s into %s: %v>", raw, typ, err)
		}
	}
	return ptr.Elem().Interface()
}

var reID = regexp.MustCompile(`^(?:rg|dg|rr|dr|pg|net|in|out|ep|svc|dns|prof|x)_[0-9a-f]{8}$`)

func idsIn(args []json.RawMessage) map[string]bool {
	ids := map[string]bool{}
	for _, raw := range args {
		var v any
		_ = json.Unmarshal(raw, &v)
		collect(v, ids)
	}
	return ids
}

func collect(v any, ids map[string]bool) {
	switch x := v.(type) {
	case string:
		ids[x] = true
	case []any:
		for _, e := range x {
			collect(e, ids)
		}
	case map[string]any:
		for k, e := range x {
			ids[k] = true
			collect(e, ids)
		}
	}
}

// canonical writes v as sorted JSON with new ids replaced in order.
// recorded marks the recorded result, which options.Recorded rewrites.
func canonical(fn string, v any, inputs map[string]bool, options Options, recorded bool) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	var generic any
	_ = json.Unmarshal(raw, &generic)
	generic = scrubTimes(generic)
	if recorded && options.Recorded != nil {
		generic = options.Recorded(fn, generic)
	}
	if options.Normalize != nil {
		generic = options.Normalize(fn, generic)
	}
	names := map[string]string{}
	generic = renameIDs(generic, inputs, names)
	out, _ := json.Marshal(generic)
	return string(out)
}

func scrubTimes(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = scrubTimes(x[i])
		}
	case map[string]any:
		for k, e := range x {
			if k == "updatedAt" || k == "lastUpdatedAt" || k == "savedAt" {
				x[k] = 0
				continue
			}
			x[k] = scrubTimes(e)
		}
	}
	return v
}

func renameIDs(v any, inputs map[string]bool, names map[string]string) any {
	switch x := v.(type) {
	case string:
		if reID.MatchString(x) && !inputs[x] {
			if _, ok := names[x]; !ok {
				names[x] = fmt.Sprintf("<id %d>", len(names)+1)
			}
			return names[x]
		}
		return x
	case []any:
		for i := range x {
			x[i] = renameIDs(x[i], inputs, names)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[renameIDs(k, inputs, names).(string)] = renameIDs(x[k], inputs, names)
		}
		return out
	}
	return v
}

// Summary logs how many recorded calls ran per function and fails when a
// function expected to have recordings has none.
func Summary(t *testing.T, ran map[string]int, adapters map[string]Adapter) {
	t.Helper()
	var names []string
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	for _, name := range names {
		total += ran[name]
		if ran[name] == 0 {
			t.Logf("no recorded calls of %s", name)
		}
	}
	t.Logf("replayed %d recorded calls of %d functions", total, len(names))
}

// SortLeaves orders the rules of condition groups made only of leaves.
func SortLeaves(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = SortLeaves(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = SortLeaves(e)
		}
		if rules, ok := x["rules"].([]any); ok && x["kind"] == "group" {
			leaves := true
			for _, r := range rules {
				if m, ok := r.(map[string]any); !ok || m["kind"] != "match" {
					leaves = false
				}
			}
			if leaves {
				sortByJSON(rules)
			}
		}
	}
	return v
}

// SortLogicalSingles orders the rules of sing-box logical rules made of
// single-field predicates (an "or" rule compiled from a flat match).
func SortLogicalSingles(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = SortLogicalSingles(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = SortLogicalSingles(e)
		}
		if rules, ok := x["rules"].([]any); ok && x["type"] == "logical" {
			singles := true
			for _, r := range rules {
				if m, ok := r.(map[string]any); !ok || len(m) != 1 {
					singles = false
				}
			}
			if singles {
				sortByJSON(rules)
			}
		}
	}
	return v
}

func sortByJSON(list []any) {
	sort.SliceStable(list, func(i, j int) bool {
		a, _ := json.Marshal(list[i])
		b, _ := json.Marshal(list[j])
		return string(a) < string(b)
	})
}

var reSummarySeparator = regexp.MustCompile(` · | 或 | 且 `)

// SortSummary orders the parts of a condition summary.
func SortSummary(s string) string {
	parts := reSummarySeparator.Split(strings.Trim(s, "（）"), -1)
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// coreWording maps the messages recorded from the TypeScript console, which
// named the running core sing-box, to the current wording.
var coreWording = strings.NewReplacer(
	"sing-box 将无法接收流量", "核心将无法接收流量",
	"sing-box 将拒绝启动", "核心将拒绝启动",
	"只用 sing-box 时连接会超时", "只用 Kiko 时连接会超时",
	"须在 sing-box 支持范围内", "须在核心支持范围内",
)

// CoreWording rewrites the recorded messages that named the running core
// sing-box to the current wording, in every string of v. It belongs in
// Options.Recorded: the Go output must already use the current wording.
func CoreWording(v any) any {
	switch x := v.(type) {
	case string:
		return coreWording.Replace(x)
	case []any:
		for i := range x {
			x[i] = CoreWording(x[i])
		}
	case map[string]any:
		for k, item := range x {
			x[k] = CoreWording(item)
		}
	}
	return v
}
