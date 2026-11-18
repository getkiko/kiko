package rewrites

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Kind is a kind of rewrite rule, one tab of the workspace.
type Kind int

// The kinds, in the order of the tabs.
const (
	KindURL Kind = iota
	KindHeader
	KindMapLocal
	KindBody
)

// Kinds lists every kind in tab order.
var Kinds = []Kind{KindURL, KindHeader, KindMapLocal, KindBody}

// Label is the tab's name.
func (k Kind) Label() string {
	switch k {
	case KindHeader:
		return "请求头"
	case KindMapLocal:
		return "本地映射"
	case KindBody:
		return "正文"
	}
	return "URL 重写"
}

// idPrefix starts the ids of new rules of the kind.
func (k Kind) idPrefix() string {
	switch k {
	case KindHeader:
		return "header"
	case KindMapLocal:
		return "map"
	case KindBody:
		return "body"
	}
	return "url"
}

// URLPattern is the pattern that matches exactly url.
func URLPattern(url string) string { return "^" + regexp.QuoteMeta(url) + "$" }

// rulesOf returns the rewrite rules of doc, nil without any.
func rulesOf(doc *model.Doc) *model.HTTPRewrites {
	if doc == nil || doc.HTTPTools == nil {
		return nil
	}
	return doc.HTTPTools.Rewrites
}

// ensure returns the rewrite rules of d, made when absent.
func ensure(d *model.Doc) *model.HTTPRewrites {
	if d.HTTPTools == nil {
		d.HTTPTools = &model.HTTPTools{}
	}
	if d.HTTPTools.Rewrites == nil {
		d.HTTPTools.Rewrites = &model.HTTPRewrites{}
	}
	return d.HTTPTools.Rewrites
}

// MITMEnabled reports whether HTTPS decryption is on in doc.
func MITMEnabled(doc *model.Doc) bool {
	return doc != nil && doc.HTTPTools != nil && doc.HTTPTools.MITM != nil && doc.HTTPTools.MITM.Enabled
}

// RuleName resolves the name of a rule by id from the document, the id
// itself when the rule is gone or has no name, a pattern standing for it
// when it has no name.
func RuleName(doc *model.Doc, id string) string {
	r := rulesOf(doc)
	if r == nil || id == "" {
		return id
	}
	for _, u := range r.URL {
		if u.ID == id {
			return firstText(u.Name, u.Pattern, id)
		}
	}
	for _, h := range r.Headers {
		if h.ID == id {
			return firstText(h.Name, h.Pattern, id)
		}
	}
	for _, m := range r.MapLocal {
		if m.ID == id {
			return firstText(m.Name, m.Pattern, id)
		}
	}
	for _, b := range r.Body {
		if b.ID == id {
			return firstText(b.Name, b.Pattern, id)
		}
	}
	return id
}

func firstText(texts ...string) string {
	for _, t := range texts {
		if t != "" {
			return t
		}
	}
	return ""
}

// URLActionLabel is the action's name.
func URLActionLabel(action string) string {
	switch action {
	case model.URLActionReject:
		return "拒绝"
	case model.URLActionReject200:
		return "返回 200 空内容"
	case model.URLActionRejectImg:
		return "返回透明图片"
	case model.URLActionTinyGIF:
		return "返回 1×1 GIF"
	case model.URLActionRejectDict:
		return "返回 {}"
	case model.URLActionRejectArray:
		return "返回 []"
	case model.URLActionFound:
		return "302 跳转"
	case model.URLActionTemporary:
		return "307 跳转"
	case model.URLActionHeader:
		return "改写主机（内部）"
	}
	return action
}

// urlActions are the choices of a URL rewrite, in the order listed.
var urlActions = []string{
	model.URLActionReject, model.URLActionReject200, model.URLActionRejectImg, model.URLActionTinyGIF,
	model.URLActionRejectDict, model.URLActionRejectArray, model.URLActionFound, model.URLActionTemporary,
	model.URLActionHeader,
}

// HeaderActionLabel is the header operation's name.
func HeaderActionLabel(action string) string {
	switch action {
	case model.HeaderActionAdd:
		return "添加"
	case model.HeaderActionDelete:
		return "删除"
	case model.HeaderActionReplace:
		return "替换"
	case model.HeaderActionReplaceRegex:
		return "正则替换"
	}
	return action
}

// headerActions are the header operations, in the order listed.
var headerActions = []string{
	model.HeaderActionAdd, model.HeaderActionDelete, model.HeaderActionReplace, model.HeaderActionReplaceRegex,
}

// ScopeLabel is the side a header rule applies to.
func ScopeLabel(scope string) string {
	if scope == model.HeaderScopeResponse {
		return "响应"
	}
	return "请求"
}

func urlSummary(r *model.URLRewrite) string {
	text := URLActionLabel(r.Action)
	if (r.Action == model.URLActionFound || r.Action == model.URLActionTemporary || r.Action == model.URLActionHeader) && r.Target != "" {
		text += " → " + r.Target
	}
	return text
}

func headerSummary(r *model.HeaderRewrite) string {
	parts := make([]string, 0, len(r.Ops))
	for _, op := range r.Ops {
		if op == nil {
			continue
		}
		parts = append(parts, HeaderActionLabel(op.Action)+" "+op.Name)
	}
	return ScopeLabel(r.Scope) + " · " + strings.Join(parts, "、")
}

// BodyActionLabel is the body edit's name.
func BodyActionLabel(action string) string {
	switch action {
	case model.BodyActionReplaceRegex:
		return "正则替换"
	case model.BodyActionJSONSet:
		return "JSON 设置"
	case model.BodyActionJSONDelete:
		return "JSON 删除"
	case model.BodyActionJSONAppend:
		return "JSON 追加"
	}
	return action
}

// bodyActions are the body edit actions, in the order listed.
var bodyActions = []string{
	model.BodyActionReplaceRegex, model.BodyActionJSONSet, model.BodyActionJSONDelete, model.BodyActionJSONAppend,
}

func bodySummary(r *model.BodyRewrite) string {
	var parts []string
	for _, e := range r.Edits {
		if e == nil {
			continue
		}
		label := BodyActionLabel(e.Action)
		if !slices.Contains(parts, label) {
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, "、")
}

func mapSummary(r *model.MapLocal) string {
	parts := []string{fmt.Sprint(r.Status)}
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "Content-Type") && h.Value != "" {
			parts = append(parts, h.Value)
			break
		}
	}
	if n := r.BodyBytes(); n > 0 {
		parts = append(parts, kit.Bytes(float64(n)))
	} else {
		parts = append(parts, "空")
	}
	return strings.Join(parts, " · ")
}

// ruleRow is one rule as the table lists it.
type ruleRow struct {
	ID, Name, Pattern, Summary string
	Disabled                   bool
	// Issue is why the rule cannot take effect; empty when it can.
	Issue string
	// Scope and Edits are the body rules' side and number of edits.
	Scope string
	Edits int
}

// rowsOf lists the rules of one kind in their order.
func rowsOf(doc *model.Doc, kind Kind) []ruleRow {
	r := rulesOf(doc)
	if r == nil {
		return nil
	}
	var rows []ruleRow
	switch kind {
	case KindURL:
		for _, x := range r.URL {
			rows = append(rows, ruleRow{x.ID, x.Name, x.Pattern, urlSummary(x), x.Disabled, x.Issue(), "", 0})
		}
	case KindHeader:
		for _, x := range r.Headers {
			rows = append(rows, ruleRow{x.ID, x.Name, x.Pattern, headerSummary(x), x.Disabled, x.Issue(), "", 0})
		}
	case KindMapLocal:
		for _, x := range r.MapLocal {
			rows = append(rows, ruleRow{x.ID, x.Name, x.Pattern, mapSummary(x), x.Disabled, x.Issue(), "", 0})
		}
	case KindBody:
		for _, x := range r.Body {
			rows = append(rows, ruleRow{x.ID, x.Name, x.Pattern, bodySummary(x), x.Disabled, x.Issue(), x.Scope, len(x.Edits)})
		}
	}
	return rows
}

// count is how many rules of the kind the document holds.
func count(doc *model.Doc, kind Kind) int {
	r := rulesOf(doc)
	if r == nil {
		return 0
	}
	switch kind {
	case KindURL:
		return len(r.URL)
	case KindHeader:
		return len(r.Headers)
	case KindBody:
		return len(r.Body)
	}
	return len(r.MapLocal)
}

// title is what names a row: its name, else its pattern.
func (r ruleRow) title() string { return firstText(r.Name, r.Pattern, r.ID) }

// matches reports whether the search text, lower case, is in the row.
func (r ruleRow) matches(query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Name+"\n"+r.Pattern+"\n"+r.Summary), query)
}

// ruleList gives the operations the table needs on one kind's slice.
type ruleList struct {
	n           func() int
	id          func(i int) string
	swap        func(i, j int)
	remove      func(i int)
	duplicate   func(i int) string
	setDisabled func(i int, disabled bool)
}

func listOf(r *model.HTTPRewrites, kind Kind) ruleList {
	switch kind {
	case KindHeader:
		return ruleList{
			n:    func() int { return len(r.Headers) },
			id:   func(i int) string { return r.Headers[i].ID },
			swap: func(i, j int) { r.Headers[i], r.Headers[j] = r.Headers[j], r.Headers[i] },
			remove: func(i int) {
				r.Headers = append(r.Headers[:i:i], r.Headers[i+1:]...)
			},
			duplicate: func(i int) string {
				c := model.Clone(r.Headers[i])
				c.ID, c.Name = model.NewID(KindHeader.idPrefix()), copyName(c.Name)
				r.Headers = insertAt(r.Headers, i+1, c)
				return c.ID
			},
			setDisabled: func(i int, v bool) { r.Headers[i].Disabled = v },
		}
	case KindBody:
		return ruleList{
			n:    func() int { return len(r.Body) },
			id:   func(i int) string { return r.Body[i].ID },
			swap: func(i, j int) { r.Body[i], r.Body[j] = r.Body[j], r.Body[i] },
			remove: func(i int) {
				r.Body = append(r.Body[:i:i], r.Body[i+1:]...)
			},
			duplicate: func(i int) string {
				c := model.Clone(r.Body[i])
				c.ID, c.Name = model.NewID(KindBody.idPrefix()), copyName(c.Name)
				r.Body = insertAt(r.Body, i+1, c)
				return c.ID
			},
			setDisabled: func(i int, v bool) { r.Body[i].Disabled = v },
		}
	case KindMapLocal:
		return ruleList{
			n:    func() int { return len(r.MapLocal) },
			id:   func(i int) string { return r.MapLocal[i].ID },
			swap: func(i, j int) { r.MapLocal[i], r.MapLocal[j] = r.MapLocal[j], r.MapLocal[i] },
			remove: func(i int) {
				r.MapLocal = append(r.MapLocal[:i:i], r.MapLocal[i+1:]...)
			},
			duplicate: func(i int) string {
				c := model.Clone(r.MapLocal[i])
				c.ID, c.Name = model.NewID(KindMapLocal.idPrefix()), copyName(c.Name)
				r.MapLocal = insertAt(r.MapLocal, i+1, c)
				return c.ID
			},
			setDisabled: func(i int, v bool) { r.MapLocal[i].Disabled = v },
		}
	}
	return ruleList{
		n:    func() int { return len(r.URL) },
		id:   func(i int) string { return r.URL[i].ID },
		swap: func(i, j int) { r.URL[i], r.URL[j] = r.URL[j], r.URL[i] },
		remove: func(i int) {
			r.URL = append(r.URL[:i:i], r.URL[i+1:]...)
		},
		duplicate: func(i int) string {
			c := model.Clone(r.URL[i])
			c.ID, c.Name = model.NewID(KindURL.idPrefix()), copyName(c.Name)
			r.URL = insertAt(r.URL, i+1, c)
			return c.ID
		},
		setDisabled: func(i int, v bool) { r.URL[i].Disabled = v },
	}
}

func insertAt[T any](s []T, i int, v T) []T {
	s = append(s[:i:i], append([]T{v}, s[i:]...)...)
	return s
}

// copyName names a duplicate; a rule without a name stays without.
func copyName(name string) string {
	if name == "" {
		return ""
	}
	return name + " 副本"
}

// index finds the rule with id in the list, -1 when absent.
func (l ruleList) index(id string) int {
	for i := 0; i < l.n(); i++ {
		if l.id(i) == id {
			return i
		}
	}
	return -1
}

// move shifts the rule by delta places; false when it cannot move.
func moveRule(d *model.Doc, kind Kind, id string, delta int) bool {
	l := listOf(ensure(d), kind)
	i := l.index(id)
	j := i + delta
	if i < 0 || j < 0 || j >= l.n() {
		return false
	}
	l.swap(i, j)
	return true
}

// removeRule deletes the rule.
func removeRule(d *model.Doc, kind Kind, id string) bool {
	l := listOf(ensure(d), kind)
	i := l.index(id)
	if i < 0 {
		return false
	}
	l.remove(i)
	return true
}

// duplicateRule copies the rule below itself and returns the copy's id.
func duplicateRule(d *model.Doc, kind Kind, id string) string {
	l := listOf(ensure(d), kind)
	i := l.index(id)
	if i < 0 {
		return ""
	}
	return l.duplicate(i)
}

// disableRule turns the rule off or on.
func disableRule(d *model.Doc, kind Kind, id string, disabled bool) bool {
	l := listOf(ensure(d), kind)
	i := l.index(id)
	if i < 0 {
		return false
	}
	l.setDisabled(i, disabled)
	return true
}

// PatternTest is the result of trying a pattern on a URL.
type PatternTest struct {
	// Err is why the pattern cannot be tried; empty when it compiled.
	Err string
	// Hit reports a match; Groups are the capture groups $1..$n (the
	// whole match is not listed), "" for a group that did not take part.
	Hit    bool
	Groups []string
	// Names are the names of named groups by position, "" for unnamed.
	Names []string
	re    *regexp.Regexp
	match []int
	url   string
}

// TestPattern tries pattern on url with Go's regexp.
func TestPattern(pattern, url string) PatternTest {
	if strings.TrimSpace(pattern) == "" {
		return PatternTest{}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return PatternTest{Err: "匹配模式无效"}
	}
	t := PatternTest{re: re, url: url}
	m := re.FindStringSubmatchIndex(url)
	if m == nil {
		return t
	}
	t.Hit, t.match = true, m
	names := re.SubexpNames()
	for g := 1; g < len(m)/2; g++ {
		text := ""
		if m[2*g] >= 0 {
			text = url[m[2*g]:m[2*g+1]]
		}
		t.Groups = append(t.Groups, text)
		t.Names = append(t.Names, names[g])
	}
	return t
}

// Expand returns target with $1..$9 and ${name} replaced by the groups of
// the match; false without a match.
func (t PatternTest) Expand(target string) (string, bool) {
	if !t.Hit || t.re == nil {
		return "", false
	}
	return string(t.re.ExpandString(nil, target, t.url, t.match)), true
}

// IsText reports whether data can be kept as the text body of a mapping:
// valid UTF-8 without NUL bytes.
func IsText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

// hopHeaders are the fields a mapped response does not take over from a
// recorded one: framing, hop-by-hop and what the stored body no longer
// matches.
var hopHeaders = map[string]bool{
	"content-length": true, "transfer-encoding": true, "connection": true, "keep-alive": true,
	"upgrade": true, "te": true, "trailer": true, "proxy-connection": true, "proxy-authenticate": true,
	"proxy-authorization": true, "content-encoding": true,
}

// MapLocalFromResponse builds the local mapping that answers url as the
// recorded response did: headers without framing, hop-by-hop and
// encoding fields, the body as text when it is text within the limit,
// else base64. The error says why the body cannot be kept.
func MapLocalFromResponse(url string, status int, headers []model.HeaderField, body []byte) (model.MapLocal, error) {
	rule := model.MapLocal{Pattern: URLPattern(url), Status: status}
	if rule.Status < 200 || rule.Status > 599 || rule.Status == 206 || rule.Status == 304 {
		rule.Status = 200
	}
	for _, h := range headers {
		if hopHeaders[strings.ToLower(h.Name)] || strings.EqualFold(h.Name, "content-range") || strings.HasPrefix(h.Name, ":") {
			continue
		}
		if model.HeaderNameIssue(h.Name) != "" || model.HeaderValueIssue(h.Value) != "" {
			continue
		}
		rule.Headers = append(rule.Headers, model.HeaderField{Name: h.Name, Value: h.Value})
	}
	if len(body) > model.MaxMapLocalBody {
		return rule, fmt.Errorf("正文超过 %s", kit.Bytes(model.MaxMapLocalBody))
	}
	if IsText(body) {
		rule.Body = string(body)
	} else {
		rule.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	return rule, nil
}
