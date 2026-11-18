// Package scripts is the script tab of the Rewrites workspace: the list of the
// user's HTTP scripts with its editor dialog and test area, and the
// store and log pages the list opens. The editor (Editors) is shared with
// the HTTP inspector, which opens it from a record.
package scripts

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// Doc is the document the workspace and the editors read and change;
// store.Store is one.
type Doc interface {
	Doc() *model.Doc
	Update(fn func(doc *model.Doc)) error
}

// editGuard is a Doc that holds the server's changes back while an editor
// is open (store.Store).
type editGuard interface {
	BeginEdit()
	EndEdit()
}

// types are the script types in the order of the editor's choice.
var types = []string{model.ScriptHTTPRequest, model.ScriptHTTPResponse, model.ScriptGeneric, model.ScriptCron, model.ScriptRule, model.ScriptDNS}

// TypeLabel is the script type's name.
func TypeLabel(t string) string {
	switch t {
	case model.ScriptHTTPRequest:
		return "请求"
	case model.ScriptHTTPResponse:
		return "响应"
	case model.ScriptGeneric:
		return "通用"
	case model.ScriptCron:
		return "定时"
	case model.ScriptRule:
		return "规则"
	case model.ScriptDNS:
		return "DNS"
	}
	return t
}

// DecisionLabel words the decision of a rule or dns script trial run.
func DecisionLabel(typ, decision string) string {
	switch {
	case decision == "":
		return ""
	case typ == model.ScriptRule && decision == "true":
		return "匹配"
	case typ == model.ScriptRule && decision == "false":
		return "不匹配"
	case decision == "pass":
		return "继续下一条规则"
	case strings.HasPrefix(decision, "server "):
		return "转发到 " + strings.TrimPrefix(decision, "server ")
	}
	return decision
}

// ActionLabel names what a script run did.
func ActionLabel(action string) string {
	switch action {
	case api.ScriptActionModify:
		return "已修改"
	case api.ScriptActionRespond:
		return "已应答"
	case api.ScriptActionAbort:
		return "已中止"
	case api.ScriptActionNone:
		return "未修改"
	case api.ScriptActionError:
		return "脚本出错"
	case api.ScriptActionTimeout:
		return "脚本超时"
	case api.ScriptActionLimit:
		return "超出资源限制"
	case api.ScriptActionPaused:
		return "已暂停"
	case api.ScriptActionSkipped:
		return "未运行"
	}
	return action
}

// Duration formats a run time given in microseconds.
func Duration(us int64) string {
	if us < 1000 {
		return strconv.FormatInt(us, 10) + " µs"
	}
	if us < 1_000_000 {
		return strconv.FormatFloat(float64(us)/1000, 'f', 1, 64) + " ms"
	}
	return strconv.FormatFloat(float64(us)/1_000_000, 'f', 2, 64) + " s"
}

// NextRunText is a scheduled time as the list shows it: the time of day
// for today, with "Tomorrow" for tomorrow, else the date (with the year when it is
// not this one). Empty when ms is not set.
func NextRunText(ms int64, now time.Time) string {
	if ms <= 0 {
		return ""
	}
	now = timefmt.In(now)
	t := time.UnixMilli(ms).In(now.Location())
	day := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location()) }
	switch days := int(day(t).Sub(day(now)).Hours()/24 + 0.5); {
	case days == 0:
		return t.Format("15:04")
	case days == 1:
		return "明天 " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// scriptsOf lists the scripts of doc.
func scriptsOf(doc *model.Doc) []*model.HTTPScript {
	if doc == nil || doc.HTTPTools == nil {
		return nil
	}
	return doc.HTTPTools.Scripts
}

// Count is how many scripts doc holds.
func Count(doc *model.Doc) int { return len(scriptsOf(doc)) }

// MITMEnabled reports whether HTTPS decryption is on in doc.
func MITMEnabled(doc *model.Doc) bool {
	return doc != nil && doc.HTTPTools != nil && doc.HTTPTools.MITM != nil && doc.HTTPTools.MITM.Enabled
}

// Find returns the script of doc whose effective tag is tag.
func Find(doc *model.Doc, tag string) *model.HTTPScript {
	if tag == "" {
		return nil
	}
	for _, s := range scriptsOf(doc) {
		if s != nil && s.EffectiveTag() == tag {
			return s
		}
	}
	return nil
}

func firstText(texts ...string) string {
	for _, t := range texts {
		if t != "" {
			return t
		}
	}
	return ""
}

// ensure returns the tools of d, made when absent.
func ensure(d *model.Doc) *model.HTTPTools {
	if d.HTTPTools == nil {
		d.HTTPTools = &model.HTTPTools{}
	}
	return d.HTTPTools
}

// scriptRow is a row of the list with its place in the whole list.
type scriptRow struct {
	*model.HTTPScript
	pos   int
	issue string
}

// title names a row: its name, else its tag.
func (r scriptRow) title() string { return firstText(r.Name, r.EffectiveTag()) }

// matches reports whether the search text, lower case, is in the row.
func (r scriptRow) matches(query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Name+"\n"+r.EffectiveTag()+"\n"+r.Pattern+"\n"+r.Cron), query)
}

// rowsOf lists the scripts of doc as rows.
func rowsOf(doc *model.Doc) []scriptRow {
	list := scriptsOf(doc)
	rows := make([]scriptRow, 0, len(list))
	for i, s := range list {
		if s == nil {
			continue
		}
		rows = append(rows, scriptRow{HTTPScript: s, pos: i, issue: s.Issue()})
	}
	return rows
}

func indexOf(list []*model.HTTPScript, id string) int {
	for i, s := range list {
		if s != nil && s.ID == id {
			return i
		}
	}
	return -1
}

// copyName names a duplicate; a script without a name stays without.
func copyName(name string) string {
	if name == "" {
		return ""
	}
	return name + " 副本"
}

// uniqueTag returns tag, or tag with a counter, so that no script of list
// has it as effective tag.
func uniqueTag(list []*model.HTTPScript, tag string) string {
	taken := func(t string) bool {
		for _, s := range list {
			if s != nil && s.EffectiveTag() == t {
				return true
			}
		}
		return false
	}
	if !taken(tag) {
		return tag
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		candidate := tag
		if len(candidate)+len(suffix) > model.MaxScriptTag {
			candidate = candidate[:model.MaxScriptTag-len(suffix)]
		}
		if candidate += suffix; !taken(candidate) {
			return candidate
		}
	}
}

// duplicateScript copies the script below itself and returns the copy's id.
func duplicateScript(d *model.Doc, id string) string {
	t := ensure(d)
	i := indexOf(t.Scripts, id)
	if i < 0 || len(t.Scripts) >= model.MaxScripts {
		return ""
	}
	c := model.Clone(t.Scripts[i])
	c.ID, c.Name = model.NewID("script"), copyName(c.Name)
	if t.Scripts[i].Tag != "" {
		c.Tag = uniqueTag(t.Scripts, t.Scripts[i].Tag)
	}
	t.Scripts = append(t.Scripts[:i+1:i+1], append([]*model.HTTPScript{c}, t.Scripts[i+1:]...)...)
	return c.ID
}

func disableScript(d *model.Doc, id string, disabled bool) bool {
	t := ensure(d)
	i := indexOf(t.Scripts, id)
	if i < 0 {
		return false
	}
	t.Scripts[i].Disabled = disabled
	return true
}

func moveScript(d *model.Doc, id string, delta int) bool {
	t := ensure(d)
	i := indexOf(t.Scripts, id)
	j := i + delta
	if i < 0 || j < 0 || j >= len(t.Scripts) {
		return false
	}
	t.Scripts[i], t.Scripts[j] = t.Scripts[j], t.Scripts[i]
	return true
}

func removeScript(d *model.Doc, id string) bool {
	t := ensure(d)
	i := indexOf(t.Scripts, id)
	if i < 0 {
		return false
	}
	t.Scripts = append(t.Scripts[:i:i], t.Scripts[i+1:]...)
	return true
}

// PatternTest is the result of trying a pattern on a URL.
type PatternTest struct {
	// Err is why the pattern cannot be tried; empty when it compiled.
	Err string
	// Tried reports that there was a pattern and a URL to try.
	Tried bool
	Hit   bool
}

// TestPattern tries pattern on url with Go's regexp, as the core matches
// the full request URL.
func TestPattern(pattern, url string) PatternTest {
	if strings.TrimSpace(pattern) == "" || strings.TrimSpace(url) == "" {
		return PatternTest{}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return PatternTest{Err: "匹配模式无效"}
	}
	return PatternTest{Tried: true, Hit: re.MatchString(url)}
}

// templates are the sources a new script starts from, by type.
func template(typ string) string {
	switch typ {
	case model.ScriptHTTPResponse:
		return "// $response.headers['X-Script'] = '1'\n$done({})\n"
	case model.ScriptGeneric:
		return "console.log('hello')\n$done({})\n"
	case model.ScriptRule:
		return "// $request: domain, ip, port, network, protocol, inbound, sourceIp, processName\n$done($request.domain.endsWith('.example.com'))\n"
	case model.ScriptDNS:
		return "// $request: domain, queryType, inbound, sourceIp, processName\n$done({ addresses: ['203.0.113.1'], ttl: 60 })\n"
	case model.ScriptCron:
		return "console.log('tick')\n$notification.post('定时脚本', '', new Date().toString())\n$done()\n"
	}
	return "// $request.headers['X-Script'] = '1'\n$done({})\n"
}

// sourceIssue is the problem of the source alone, as the model words it.
func sourceIssue(source string) string {
	probe := &model.HTTPScript{Tag: "t", Type: model.ScriptGeneric, Source: source}
	return probe.Issue()
}
