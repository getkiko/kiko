package main

import (
	"crypto/x509"
	"html/template"
	"net/http"
	"strings"
)

// The download page is self-contained HTML with no scripts or external resources; dynamic content is escaped by html/template.

type mitmSharePageData struct {
	CommonName    string
	Fingerprint   string
	NotBefore     string
	NotAfter      string
	Open          string
	AndroidIntent template.URL
}

var mitmSharePageTemplate = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Kiko 根证书</title>
<style>
:root{--bg:#f5f5f7;--card:#fff;--text:#1d1d1f;--sub:#6e6e73;--line:#d2d2d7;--accent:#0a64d8;--onaccent:#fff;--code:#f0f0f3}
@media(prefers-color-scheme:dark){:root{--bg:#000;--card:#1c1c1e;--text:#f5f5f7;--sub:#a1a1a6;--line:#3a3a3c;--accent:#4c9cff;--onaccent:#00122b;--code:#2c2c2e}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif}
main{max-width:640px;margin:0 auto;padding:20px 16px 40px}
h1{font-size:22px;margin:8px 0 16px}
section,details{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:14px 16px;margin:0 0 12px}
summary{font-weight:600;cursor:pointer}
dl{margin:0;display:grid;grid-template-columns:auto 1fr;gap:6px 12px}
dt{color:var(--sub)}dd{margin:0;min-width:0}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;word-break:break-all}
.files{display:flex;flex-wrap:wrap;gap:8px}
.files a,.btn{display:inline-block;min-height:44px;line-height:44px;padding:0 18px;border-radius:22px;background:var(--accent);color:var(--onaccent);text-decoration:none;font-weight:600}
.files a.alt{background:transparent;color:var(--accent);border:1px solid var(--accent);line-height:42px}
ol{margin:10px 0 0;padding-left:22px}li{margin:4px 0}
pre{margin:6px 0;padding:8px 10px;background:var(--code);border-radius:8px;overflow-x:auto;font-size:13px}
.note{color:var(--sub);font-size:13px;margin:8px 0 0}
</style></head><body><main>
<h1>Kiko 根证书</h1>
<section><dl>
<dt>名称</dt><dd>{{.CommonName}}</dd>
<dt>SHA-256</dt><dd class="mono">{{.Fingerprint}}</dd>
<dt>有效期</dt><dd>{{.NotBefore}} 至 {{.NotAfter}}</dd>
</dl></section>
<section><div class="files">
<a href="ca.mobileconfig">iOS 描述文件</a>
<a href="ca.crt">Android .crt</a>
<a href="ca.cer" class="alt">.cer</a>
<a href="ca.pem" class="alt">.pem</a>
<a href="ca.p12" class="alt">.p12</a>
</div></section>
<details{{if eq .Open "ios"}} open{{end}}><summary>iPhone / iPad</summary><ol>
<li>下载描述文件并允许。</li>
<li>设置 &gt; 通用 &gt; VPN 与设备管理，安装「Kiko Root CA」。</li>
<li>设置 &gt; 通用 &gt; 关于本机 &gt; 证书信任设置，打开「Kiko Root CA」的完全信任。</li>
</ol></details>
<details{{if eq .Open "android"}} open{{end}}><summary>Android</summary><ol>
<li>下载 .crt 文件。</li>
<li>设置 &gt; 安全 &gt; 加密与凭据 &gt; 安装证书 &gt; CA 证书，选择文件。</li>
</ol>
<p><a class="btn" href="{{.AndroidIntent}}">打开安全设置</a></p>
<p class="note">目标 API 24 及以上的应用默认不信任用户安装的 CA。</p></details>
<details{{if eq .Open "macos"}} open{{end}}><summary>macOS</summary><ol>
<li>下载 .cer，双击添加到「系统」钥匙串，设为始终信任。</li>
</ol>
<pre>sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "Kiko Root CA.cer"</pre></details>
<details{{if eq .Open "windows"}} open{{end}}><summary>Windows</summary><ol>
<li>下载 .cer，以管理员身份运行：</li>
</ol>
<pre>certutil -addstore -f Root "Kiko Root CA.cer"</pre></details>
<details{{if eq .Open "linux"}} open{{end}}><summary>Linux</summary><ol>
<li>下载 .crt 后执行：</li>
</ol>
<pre>sudo cp "Kiko Root CA.crt" /usr/local/share/ca-certificates/kiko-root-ca.crt
sudo update-ca-certificates</pre>
<pre>sudo cp "Kiko Root CA.crt" /etc/pki/ca-trust/source/anchors/kiko-root-ca.crt
sudo update-ca-trust</pre></details>
</main></body></html>
`))

// mitmShareOS picks the platform expanded by default from the User-Agent; it returns empty when it cannot tell.
func mitmShareOS(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios"
	case strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Windows"):
		return "windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "macos"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "linux"
	}
	return ""
}

func mitmShareFingerprint(fp string) string {
	fp = strings.ToUpper(fp)
	var parts []string
	for i := 0; i+2 <= len(fp); i += 2 {
		parts = append(parts, fp[i:i+2])
	}
	return strings.Join(parts, " ")
}

func (s *server) mitmSharePage(w http.ResponseWriter, r *http.Request, root *x509.Certificate) {
	data := mitmSharePageData{
		CommonName:    root.Subject.CommonName,
		Fingerprint:   mitmShareFingerprint(certFingerprint(root.Raw)),
		NotBefore:     root.NotBefore.UTC().Format("2006-01-02"),
		NotAfter:      root.NotAfter.UTC().Format("2006-01-02"),
		Open:          mitmShareOS(r.UserAgent()),
		AndroidIntent: template.URL("intent:#Intent;action=android.settings.SECURITY_SETTINGS;end"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_ = mitmSharePageTemplate.Execute(w, data)
}
