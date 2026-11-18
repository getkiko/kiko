package scripts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

const (
	// storeMaxValue and storeMaxKey are the store's limits on a value and
	// a key, in bytes.
	storeMaxValue = 1 << 20
	storeMaxKey   = 256
	// storeRefresh is how often a visible store page reads the store again.
	storeRefresh = 5 * time.Second
	// slNarrow is the window width at and below which filters and
	// inspectors become overlays.
	slNarrow = 800
)

// storeValidate returns why an entry cannot be saved, "" when it can.
func storeValidate(namespace, key, value string) string {
	switch {
	case strings.TrimSpace(namespace) == "":
		return "请填写命名空间"
	case len(namespace) > storeMaxKey:
		return "命名空间超过 256 字节"
	case key == "":
		return "请填写键"
	case len(key) > storeMaxKey:
		return "键超过 256 字节"
	case len(value) > storeMaxValue:
		return "值超过 1 MiB"
	}
	return ""
}

// storePreview is a value on one line for a table cell: line breaks and
// runs of blanks become single spaces and the cut preview of a truncated
// value ends with an ellipsis.
func storePreview(value string, truncated bool) string {
	const limit = 400
	if len(value) > limit {
		value, truncated = value[:limit], true
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	value = strings.Join(strings.Fields(value), " ")
	if truncated {
		value += "…"
	}
	return value
}

// storeMatches reports whether an entry's key or value contains query,
// which is already lower case.
func storeMatches(item api.ScriptStoreItem, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(item.Key), query) || strings.Contains(strings.ToLower(item.Value), query)
}

// storeFilter returns the entries matching the search.
func storeFilter(items []api.ScriptStoreItem, search string) []api.ScriptStoreItem {
	query := strings.ToLower(strings.TrimSpace(search))
	if query == "" {
		return items
	}
	var out []api.ScriptStoreItem
	for _, item := range items {
		if storeMatches(item, query) {
			out = append(out, item)
		}
	}
	return out
}

// storeFormatJSON indents a value that is a JSON object or array.
func storeFormatJSON(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(trimmed), "", "  "); err != nil {
		return "", false
	}
	return out.String(), true
}

// storeNamespaceDetail is the count text of a namespace option.
func storeNamespaceDetail(ns api.ScriptNamespace) string {
	return fmt.Sprintf("%d · %s", ns.Entries, kit.Bytes(float64(ns.Bytes)))
}

// storeSelectedIndex returns the row of key among items, -1 when absent.
func storeSelectedIndex(items []api.ScriptStoreItem, key string) int {
	if key == "" {
		return -1
	}
	for i, item := range items {
		if item.Key == key {
			return i
		}
	}
	return -1
}

// slViewer is a read-only code view that loads a text when it changes.
type slViewer struct {
	state kit.CodeState
	key   string
	text  string
	set   bool
}

// show builds the view of text, a new document when key or text changed.
func (v *slViewer) show(c *ui.Context, key, text, language, label string, height float32) ui.Element {
	if !v.set || v.key != key || v.text != text {
		v.state.Reset(text)
		v.key, v.text, v.set = key, text, true
	}
	return kit.CodeEditor(c, &v.state, kit.CodeOptions{Language: language, ReadOnly: true, Label: label, Wrap: true, MinHeight: height}).Height(height)
}

// slSidePanel shows a panel over the window's left or right edge, closed by
// Escape or a press outside.
func slSidePanel(c *ui.Context, right bool, onClose func(), fn func()) {
	k := kit.T(c)
	var panel ui.Element
	ui.Overlay(c, func() {
		panel = ui.Row(c).Absolute().Top(0).Bottom(0).AlignItems(ui.Stretch).
			Shadow(0, 12, 40, 0, k.Shadow.Alpha(0.25)).Children(fn)
		if right {
			panel.Right(0)
		} else {
			panel.Left(0)
		}
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			onClose()
		}
	})
	if panel.PressedOutside() {
		onClose()
	}
}

// slTime is a time of day with milliseconds, "12:00:01.250".
func slTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return timefmt.FormatMilli(ms, "15:04:05.000")
}

// slViewerHeight is the height of a wrapped text view in the inspector,
// which fits about 30 characters a line: the text's lines plus a margin,
// between 64 and 320 DIPs.
func slViewerHeight(text string) float32 {
	const perLine = 30
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		lines += max(1, (utf8.RuneCountInString(line)+perLine-1)/perLine)
	}
	return float32(min(max(lines*18+16, 64), 320))
}

// restoreEOL gives an edited value the line endings of the original: the
// editor holds LF only. An untouched value is returned as loaded, and a
// value whose lines all ended in CRLF keeps CRLF.
func restoreEOL(orig, edited string) string {
	if !strings.Contains(orig, "\r") {
		return edited
	}
	lf := strings.ReplaceAll(strings.ReplaceAll(orig, "\r\n", "\n"), "\r", "\n")
	if lf == edited {
		return orig
	}
	if strings.Contains(strings.ReplaceAll(orig, "\r\n", ""), "\r") ||
		strings.Count(orig, "\r\n") != strings.Count(orig, "\n") {
		return edited
	}
	return strings.ReplaceAll(edited, "\n", "\r\n")
}
