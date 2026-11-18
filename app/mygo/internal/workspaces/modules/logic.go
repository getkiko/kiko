// Package modules is the Modules tab of the Rewrites workspace: the installed
// Surge and Kiko modules as cards, the dialogs that install, update and
// inspect them, and the update schedule. Conversion is internal/modules;
// this package downloads what a module refers to, asks the user before
// anything sensitive takes effect and writes the result to the document.
package modules

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/modules"
)

// Download limits of one conversion: how many files, how many bytes in
// all, how many at once and how long each may take.
const (
	MaxFetches   = 64
	MaxFetchSize = 8 << 20
	FetchWorkers = 4
	FetchTimeout = 45 * time.Second
)

// Doc is the document the tab reads and writes (store.Store).
type Doc interface {
	Doc() *model.Doc
	Update(fn func(doc *model.Doc)) error
}

// Fetcher downloads a URL through kiko-server.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// errNoFetchPublic is the answer when the remote server predates the
// public-only download.
var errNoFetchPublic = errors.New("远端服务端版本过旧，无法下载脚本")

// APIFetcher downloads a module's own address through the server's /fetch: 30
// seconds, 16 MiB. The user typed that address, so a LAN, NAS or Tailscale
// address works; its redirects must lead to public addresses (a server
// without the capability follows them freely). A status other than 2xx is an
// error.
func APIFetcher(client *api.Client) Fetcher {
	return func(ctx context.Context, url string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
		defer cancel()
		if info, err := client.Info(ctx); err == nil && info.Capabilities["fetchPublicRedirects"] {
			return fetchBody(client.FetchPublicRedirects(ctx, url))
		}
		return fetchBody(client.Fetch(ctx, url, nil))
	}
}

// APIPublicFetcher downloads what a module declares (scripts and mapping
// data) from public addresses only, so a module cannot make the machine read
// its local network. A server without the fetchPublic capability would ignore
// the restriction; the download is refused then.
func APIPublicFetcher(client *api.Client) Fetcher {
	return func(ctx context.Context, url string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
		defer cancel()
		if info, err := client.Info(ctx); err == nil && !info.Capabilities["fetchPublic"] {
			return nil, errNoFetchPublic
		}
		return fetchBody(client.FetchPublic(ctx, url))
	}
}

func fetchBody(res api.FetchResult, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status > 299 {
		return nil, fmt.Errorf("HTTP %d", res.Status)
	}
	return []byte(res.Body), nil
}

// Failure is a download that did not come back.
type Failure struct {
	URL    string
	Reason string
}

func (f Failure) String() string { return f.URL + "：" + f.Reason }

// Input is what a conversion starts from: the module text or a Kiko
// file, the address it came from and the argument values to apply.
type Input struct {
	// Text is the module text; Data a file, which Kiko JSON is told
	// from a Surge text by its first character.
	Text string
	Data []byte
	// BaseURL resolves the relative script paths of a Surge module.
	BaseURL   string
	Arguments map[string]string
}

// Converted is a module text turned into contents, with what its
// downloads did.
type Converted struct {
	// Raw is the text to keep for converting again; empty for a Kiko
	// file, which carries its content.
	Raw    string
	Result *modules.Result
	Update model.ModuleUpdate
	// Failures are the downloads that failed; their items moved to the
	// unsupported list.
	Failures []Failure
	Fetched  int
	Bytes    int
}

// Contents are the converted contents.
func (c *Converted) Contents() model.ModuleContents { return c.Result.Contents }

// FileText returns the text of a module file and whether it is a Kiko
// file (JSON).
func FileText(data []byte) (text string, kiko bool) {
	trimmed := strings.TrimLeft(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), " \t\r\n")
	return string(data), strings.HasPrefix(trimmed, "{")
}

// Download fetches a module's own text with the fetcher of its main address.
func Download(ctx context.Context, fetch Fetcher, address string) ([]byte, error) {
	body, err := fetch(ctx, address)
	if err != nil {
		return nil, err
	}
	if len(body) > modules.MaxKikoSize {
		return nil, modules.ErrTooLarge
	}
	return body, nil
}

// Convert converts the input and downloads the scripts and mapping data it
// lists: at most MaxFetches files and MaxFetchSize bytes in all, FetchWorkers at
// a time. A download that fails is listed in Failures and its item moves to
// the unsupported list. progress, when set, hears of each finished download
// (from any goroutine).
func Convert(ctx context.Context, fetch Fetcher, in Input, progress func(done, total int)) (*Converted, error) {
	out := &Converted{}
	if _, kiko := FileText(in.Data); in.Data != nil && kiko {
		res, err := modules.ParseKiko(in.Data)
		if err != nil {
			return nil, err
		}
		out.Result = res
		out.Update = res.ModuleUpdate("")
		return out, nil
	}
	text := in.Text
	if in.Data != nil {
		text = string(in.Data)
	}
	res, err := modules.ParseSurge(text, modules.Options{Arguments: in.Arguments, BaseURL: in.BaseURL})
	if err != nil {
		return nil, err
	}
	out.Raw, out.Result = text, res
	fetched, failures, bytes := download(ctx, fetch, res.Fetches, progress)
	out.Failures, out.Bytes, out.Fetched = failures, bytes, len(fetched)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := res.Resolve(fetched); err != nil {
		return nil, err
	}
	out.Update = res.ModuleUpdate(text)
	return out, nil
}

// download fetches the list with the limits; the failures come back in
// the order of the list.
func download(ctx context.Context, fetch Fetcher, list []modules.Fetch, progress func(done, total int)) (map[string][]byte, []Failure, int) {
	fetched := map[string][]byte{}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		failures = map[string]string{}
		total    int
		done     int
	)
	sem := make(chan struct{}, FetchWorkers)
	report := func() {
		if progress != nil {
			progress(done, len(list))
		}
	}
	for i, f := range list {
		if i >= MaxFetches {
			mu.Lock()
			failures[f.URL] = fmt.Sprintf("超过 %d 个文件的下载上限", MaxFetches)
			done++
			mu.Unlock()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(f modules.Fetch) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := fetch(ctx, f.URL)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failures[f.URL] = reason(err)
			case total+len(body) > MaxFetchSize:
				failures[f.URL] = "超过 " + fmt.Sprint(MaxFetchSize>>20) + " MiB 的总大小上限"
			default:
				total += len(body)
				fetched[f.URL] = body
			}
			done++
			report()
		}(f)
	}
	wg.Wait()
	var out []Failure
	for _, f := range list {
		if why, bad := failures[f.URL]; bad {
			out = append(out, Failure{URL: f.URL, Reason: why})
		}
	}
	return fetched, out, total
}

// reason is a download error in a few words.
func reason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "超时"
	case errors.Is(err, context.Canceled):
		return "已取消"
	}
	return err.Error()
}

// ---- sources

// SourceLabel is where a module came from: the host of its address, or
// "Local file".
func SourceLabel(m *model.HTTPModule) string {
	if m.Source.Kind != model.ModuleSourceURL {
		return "本地文件"
	}
	if u, err := url.Parse(m.Source.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return m.Source.URL
}

// fallbackName names a module that declares no name: the file's name
// without its extension.
func fallbackName(address, file string) string {
	name := file
	if name == "" {
		if u, err := url.Parse(address); err == nil {
			name = path.Base(u.Path)
		}
	}
	name = strings.TrimSuffix(name, path.Ext(name))
	if name == "" || name == "." || name == "/" {
		return "模块"
	}
	return name
}

// ---- document changes

// NewModule makes the module an install adds: enabled, with everything in
// its contents confirmed. The caller has asked for that confirmation when
// NeedsApproval of the contents is not empty.
func NewModule(conv *Converted, source model.ModuleSource, file string, now time.Time) *model.HTTPModule {
	m := model.NewHTTPModule()
	m.Source = source
	if source.Kind != model.ModuleSourceURL {
		m.UpdateInterval = 0
	}
	u := conv.Update
	m.Name, m.Description, m.Version = u.Name, u.Description, u.Version
	if strings.TrimSpace(m.Name) == "" {
		m.Name = fallbackName(source.URL, file)
	}
	m.Arguments, m.Raw, m.Hash = u.Arguments, u.Raw, model.HashModuleRaw(u.Raw)
	m.Contents, m.Unsupported = u.Contents, u.Unsupported
	m.UpdatedAt, m.LastCheckedAt = now.UnixMilli(), now.UnixMilli()
	m.Enabled = true
	m.ApproveContents(u.Contents)
	return m
}

// Outcome is what an update did to a module.
type Outcome int

const (
	// Unchanged: only the check time moved.
	Unchanged Outcome = iota
	// Changed: the new contents took effect.
	Changed
	// Awaiting: the new contents add hosts or scripts and wait for the
	// user.
	Awaiting
)

// applyUpdate gives the module a converted text through Accept and tells what
// came of it: a text, argument or content change is Changed even when the
// module text itself kept its hash. Only the fields of the update change: the
// module's switch and approvals stay as the document has them now.
func applyUpdate(m *model.HTTPModule, u model.ModuleUpdate, now time.Time) Outcome {
	if strings.TrimSpace(u.Name) == "" {
		u.Name = m.Name
	}
	res := m.Accept(u, now.UnixMilli())
	switch {
	case res.Pending:
		return Awaiting
	case res.Applied:
		return Changed
	}
	return Unchanged
}

// Due reports whether the module is checked at now: a module from an
// address, enabled, with an interval that has passed since the last
// check.
func Due(m *model.HTTPModule, now time.Time) bool {
	if m == nil || !m.Enabled || m.Source.Kind != model.ModuleSourceURL || m.UpdateInterval <= 0 {
		return false
	}
	last := max(m.LastCheckedAt, m.UpdatedAt)
	if last == 0 {
		return true
	}
	return now.Sub(time.UnixMilli(last)) >= time.Duration(m.UpdateInterval)*time.Hour
}

// ArgumentValues are the values the module's arguments hold, by name; an
// argument without a value is left to its default.
func ArgumentValues(args []model.ModuleArgument) map[string]string {
	values := map[string]string{}
	for _, a := range args {
		if a.Value != "" {
			values[a.Name] = a.Value
		}
	}
	return values
}

// ---- display lines

// Item is one line of the content lists: the text and a short note.
type Item struct {
	Text, Note string
}

// ContentGroup is a titled list of items.
type ContentGroup struct {
	Title string
	Items []Item
}

// Groups lists a module's contents by category, in the order hostnames,
// URL rewrites, header rewrites, body rewrites, local mappings, scripts and
// rules. Empty categories are left out.
func Groups(c model.ModuleContents) []ContentGroup {
	var out []ContentGroup
	add := func(title string, items []Item) {
		if len(items) > 0 {
			out = append(out, ContentGroup{Title: title, Items: items})
		}
	}
	var hosts []Item
	if h := c.Hostnames; h != nil {
		for _, x := range h.Include {
			hosts = append(hosts, Item{Text: x})
		}
		for _, x := range h.Exclude {
			hosts = append(hosts, Item{Text: x, Note: "排除"})
		}
	}
	add("主机名", hosts)
	if rw := c.Rewrites; rw != nil {
		var urls, headers, bodies, maps []Item
		for _, r := range rw.URL {
			note := r.Action
			if r.Target != "" {
				note += " → " + r.Target
			}
			urls = append(urls, Item{Text: r.Pattern, Note: note})
		}
		for _, r := range rw.Headers {
			headers = append(headers, Item{Text: r.Pattern, Note: fmt.Sprintf("%s · %d 项", r.Scope, len(r.Ops))})
		}
		for _, r := range rw.Body {
			bodies = append(bodies, Item{Text: r.Pattern, Note: fmt.Sprintf("%s · %d 项", r.Scope, len(r.Edits))})
		}
		for _, r := range rw.MapLocal {
			maps = append(maps, Item{Text: r.Pattern, Note: fmt.Sprint(r.Status)})
		}
		add("URL 重写", urls)
		add("请求头 / 响应头", headers)
		add("正文改写", bodies)
		add("本地映射", maps)
	}
	var scripts []Item
	for _, s := range c.Scripts {
		scripts = append(scripts, Item{Text: ScriptTitle(s), Note: ScriptNote(s)})
	}
	add("脚本", scripts)
	var rules []Item
	for _, r := range c.Rules {
		note := r.Action
		if r.NoResolve {
			note += " · no-resolve"
		}
		rules = append(rules, Item{Text: r.Type + "," + r.Value, Note: note})
	}
	add("规则", rules)
	return out
}

// ScriptTitle is a script's name, or its tag.
func ScriptTitle(s *model.HTTPScript) string {
	if s.Name != "" {
		return s.Name
	}
	return s.EffectiveTag()
}

// ScriptNote is a script's type, the head of its SHA-256 and its size,
// joined by middle dots (http-response, 3f2a9c1d, 4.2 KiB).
func ScriptNote(s *model.HTTPScript) string {
	return fmt.Sprintf("%s · %s · %s", s.Type, ShortSHA(model.ScriptSHA256(s)), size(len(s.Source)))
}

// ShortSHA is the head of a SHA-256 shown to tell scripts apart.
func ShortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func size(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// UnsupportedText renders the unsupported lines for copying.
func UnsupportedText(items []model.UnsupportedItem) string {
	var b strings.Builder
	for _, u := range items {
		b.WriteString(u.Section)
		if u.Line > 0 {
			fmt.Fprintf(&b, " 第 %d 行", u.Line)
		}
		b.WriteString("：" + u.Reason)
		if u.Text != "" {
			b.WriteString("\n  " + u.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ContentText renders the contents and the unsupported lines as text for
// copying.
func ContentText(m *model.HTTPModule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", m.Name)
	for _, g := range Groups(m.Contents) {
		fmt.Fprintf(&b, "\n[%s]\n", g.Title)
		for _, it := range g.Items {
			b.WriteString(it.Text)
			if it.Note != "" {
				b.WriteString("  # " + it.Note)
			}
			b.WriteByte('\n')
		}
	}
	if len(m.Unsupported) > 0 {
		b.WriteString("\n[未支持]\n" + UnsupportedText(m.Unsupported))
	}
	return b.String()
}
