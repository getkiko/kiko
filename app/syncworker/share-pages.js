// Share pages, kept separate for editing; the Go package inlines this into copied Worker source.
// __CSP_NONCE__ and __SHARE_CONFIG__ are replaced per request by worker.js.
const SHARE_STYLE = `:root{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#1d1d1f;background:#f5f5f7;color-scheme:light dark;--surface:#fff;--line:#e6e6e9;--muted:#6e6e73;--accent:#007aff;--danger:#c9252d;--fill:#f0f0f2}
*{box-sizing:border-box}body{margin:0}[hidden]{display:none!important}button,input{font:inherit}button{cursor:pointer;touch-action:manipulation}button:disabled{opacity:.5;cursor:default}
button,input[type=text],input[type=password],input:not([type]){min-height:44px;border-radius:11px}button{padding:0 15px;border:0;background:var(--fill);color:inherit;font-size:14px;font-weight:500}
button:focus-visible,input:focus-visible,label:focus-within{outline:3px solid var(--accent);outline-offset:2px}
input[type=text],input[type=password],input:not([type]){width:100%;min-width:0;padding:0 12px;border:1px solid var(--line);background:var(--surface);color:inherit;font-size:14px}
.primary{background:var(--accent);color:#fff}.danger{color:var(--danger)}
header{border-bottom:1px solid var(--line);background:var(--surface)}.bar{display:flex;align-items:center;gap:12px;max-width:760px;margin:0 auto;padding:12px 24px}.brand{font-weight:600}.bar a{margin-left:auto;color:var(--accent);font-size:13px;text-decoration:none}
main{max-width:760px;margin:32px auto;padding:0 24px 40px}h1{font-size:26px;margin:0 0 6px}p{color:var(--muted);font-size:13px;line-height:1.6}
.panel{background:var(--surface);border:1px solid var(--line);border-radius:14px;padding:20px;margin-top:16px}
.picker{display:flex;align-items:center;gap:12px;padding:14px;border:1px dashed var(--line);border-radius:11px;cursor:pointer}.picker span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}
.picker input{position:absolute;width:1px;height:1px;opacity:0}.confirm{display:flex;gap:8px;align-items:center;font-size:13px;margin:14px 0}.field{margin-top:12px}.field label{display:block;font-size:13px;margin-bottom:6px}
.actions{display:flex;align-items:center;gap:12px;margin-top:16px;flex-wrap:wrap}.status{font-size:13px;color:var(--muted)}.status.error,.error{color:var(--danger)}
#turnstile{margin-top:14px;min-height:65px}.row{display:flex;gap:8px}.row input{font:12px ui-monospace,SFMono-Regular,Menlo,monospace}
table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;padding:10px 6px;border-top:1px solid var(--line);vertical-align:middle}th{color:var(--muted);font-weight:500;border-top:0}
td a{color:var(--accent);word-break:break-all;font:12px ui-monospace,SFMono-Regular,Menlo,monospace;text-decoration:none}td.num{white-space:nowrap;color:var(--muted);font-variant-numeric:tabular-nums}
@media(prefers-color-scheme:dark){:root{color:#f5f5f7;background:#161618;--surface:#222225;--line:#39393d;--muted:#a1a1a6;--fill:#333336;--accent:#0a84ff;--danger:#ff6961}}
@media(max-width:540px){main{padding:0 16px 32px;margin-top:20px}.bar{padding:12px 16px}.row{flex-wrap:wrap}.row button{flex:1}th:nth-child(2),td:nth-child(2){display:none}}`;

export const SHARE_UPLOAD_HTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<meta name="robots" content="noindex">
<title>Kiko 配置分享</title>
<style nonce="__CSP_NONCE__">${SHARE_STYLE}</style>
</head>
<body>
<header><div class="bar"><span class="brand">Kiko 配置分享</span><a href="/share-admin">管理分享</a></div></header>
<main>
<h1>上传配置</h1>
<p>选择 Kiko 保存的配置文件，上传后获得分享链接。</p>
<form id="upload-form" class="panel">
  <label class="picker" for="config-file"><span id="file-detail">选择配置文件 · JSON · 最大 2 MB</span><b>选择文件</b><input id="config-file" type="file" accept=".json,application/json" required></label>
  <p>配置将以明文保存在此服务。持有链接的人可以下载文件，管理员可删除分享。</p>
  <label class="confirm"><input id="confirm-public" type="checkbox" required>我已确认文件内容适合公开分享</label>
  <div id="token-field" class="field" hidden><label for="upload-token">访问令牌</label><input id="upload-token" type="password" autocomplete="off" spellcheck="false" maxlength="256" placeholder="同步访问令牌或管理密钥"></div>
  <div id="turnstile" hidden></div>
  <div class="actions"><button id="upload-button" class="primary" type="submit" disabled>验证并上传</button><span id="status" class="status" role="status" aria-live="polite"></span></div>
</form>
<section id="result" class="panel" hidden>
  <h2>分享已创建</h2>
  <div class="row"><input id="result-link" aria-label="分享链接" readonly><button id="copy-link" type="button">复制链接</button></div>
</section>
</main>
<script nonce="__CSP_NONCE__">
const config = __SHARE_CONFIG__;
const el = id => document.getElementById(id);
let turnstileToken = '';
let widget = null;
let uploading = false;
function message(text, error = false) { el('status').textContent = text; el('status').classList.toggle('error', error); }
function refresh() {
  const file = el('config-file').files[0];
  el('file-detail').textContent = file ? file.name + ' · ' + file.size.toLocaleString('zh-CN') + ' 字节' : '选择配置文件 · JSON · 最大 2 MB';
  const tooLarge = file && file.size > config.maxBytes;
  if (tooLarge) message('文件超过 2 MB 限制', true); else if (!uploading) message('');
  const verified = config.siteKey ? !!turnstileToken : !!el('upload-token').value.trim();
  el('upload-button').disabled = uploading || !file || tooLarge || !el('confirm-public').checked || !verified;
}
function shareKey() {
  let bytes = '';
  for (const byte of crypto.getRandomValues(new Uint8Array(32))) bytes += String.fromCharCode(byte);
  return btoa(bytes).replace(/\\+/g, '-').replace(/\\//g, '_').replace(/=+$/, '');
}
if (config.siteKey) {
  el('turnstile').hidden = false;
  window.onTurnstileLoad = () => {
    widget = window.turnstile.render('#turnstile', {
      sitekey: config.siteKey, action: config.action,
      callback: token => { turnstileToken = token; refresh(); },
      'expired-callback': () => { turnstileToken = ''; message('验证已过期，请重新验证', true); refresh(); },
      'error-callback': () => { turnstileToken = ''; message('验证加载失败，请刷新页面重试', true); refresh(); },
    });
  };
  const script = document.createElement('script');
  script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=onTurnstileLoad&render=explicit';
  script.async = true;
  document.head.append(script);
} else {
  el('token-field').hidden = false;
}
el('upload-form').addEventListener('submit', async event => {
  event.preventDefault();
  const file = el('config-file').files[0];
  if (!file || uploading) return;
  let body;
  try {
    body = await file.text();
    if (new TextEncoder().encode(body).byteLength > config.maxBytes) throw new Error('文件超过 2 MB 限制');
    const value = JSON.parse(body);
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('配置 JSON 必须是对象');
  } catch (error) { message(error instanceof SyntaxError ? '配置 JSON 格式无效' : error.message, true); return; }
  uploading = true; refresh(); message('正在上传…'); el('result').hidden = true;
  const key = shareKey();
  const headers = { 'Content-Type': 'application/json' };
  if (config.siteKey) headers['X-Turnstile-Token'] = turnstileToken;
  else headers.Authorization = 'Bearer ' + el('upload-token').value.trim();
  try {
    const response = await fetch('/api/share/' + key, { method: 'PUT', headers, body });
    if (!response.ok) { const data = await response.json().catch(() => ({})); throw new Error(data.error || '上传失败（HTTP ' + response.status + '）'); }
    el('result-link').value = new URL('/' + key, location.origin).href;
    el('result').hidden = false;
    message('上传完成');
  } catch (error) { message(error.message || '上传失败，请重试', true); }
  finally {
    uploading = false;
    if (widget !== null) { turnstileToken = ''; window.turnstile.reset(widget); }
    refresh();
  }
});
el('copy-link').addEventListener('click', async () => {
  try { await navigator.clipboard.writeText(el('result-link').value); el('copy-link').textContent = '已复制'; setTimeout(() => { el('copy-link').textContent = '复制链接'; }, 1400); }
  catch { el('result-link').select(); message('请复制选中的链接', true); }
});
for (const id of ['config-file', 'confirm-public', 'upload-token']) el(id).addEventListener('input', refresh);
el('config-file').addEventListener('change', () => { el('result').hidden = true; refresh(); });
</script>
</body>
</html>`;

export const SHARE_ADMIN_HTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<meta name="robots" content="noindex">
<title>Kiko 分享管理</title>
<style nonce="__CSP_NONCE__">${SHARE_STYLE}</style>
</head>
<body>
<header><div class="bar"><span class="brand">Kiko 分享管理</span><a href="/share-upload">上传配置</a></div></header>
<main>
<section id="login-panel" class="panel">
  <h1>进入管理</h1><p>使用管理密钥查看和删除分享。</p>
  <form id="login-form"><div class="row"><input id="admin-key" type="password" autocomplete="current-password" required maxlength="256" aria-label="管理密钥" placeholder="输入管理密钥"><button class="primary">进入</button></div><p id="login-error" class="error" role="alert"></p></form>
</section>
<section id="dashboard" hidden>
  <div class="actions"><h1 id="heading">分享文件</h1><button id="logout" type="button">退出</button></div>
  <form id="delete-form" class="panel"><label for="delete-link">按链接删除</label><div class="row field"><input id="delete-link" autocomplete="off" spellcheck="false" placeholder="粘贴分享链接"><button class="danger">删除</button></div></form>
  <div class="panel"><table><thead><tr><th>分享链接</th><th>上传时间</th><th>大小</th><th></th></tr></thead><tbody id="rows"></tbody></table>
  <div class="actions"><span id="list-status" class="status" role="status" aria-live="polite"></span><button id="more" type="button" hidden>加载更多</button></div></div>
</section>
</main>
<script nonce="__CSP_NONCE__">
const el = id => document.getElementById(id);
let adminKey = '';
let cursor = null;
let count = 0;
const link = key => new URL('/' + key, location.origin).href;
function status(text, error = false) { el('list-status').textContent = text; el('list-status').classList.toggle('error', error); }
async function api(path, options = {}) {
  const response = await fetch(path, { ...options, cache: 'no-store', headers: { Authorization: 'Bearer ' + adminKey } });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || '请求失败（HTTP ' + response.status + '）');
  return data;
}
function size(value) { return typeof value === 'number' ? (value < 1000 ? value + ' B' : (value / 1000).toFixed(1) + ' KB') : '—'; }
function time(value) { return typeof value === 'number' ? new Date(value).toLocaleString('zh-CN') : '—'; }
function setCount(value) { count = value; el('heading').textContent = '分享文件 · ' + count; }
async function remove(key, row) {
  if (!confirm('删除此分享？\\n' + link(key))) return false;
  try { await api('/api/admin/shares/' + key, { method: 'DELETE' }); if (row) { row.remove(); setCount(Math.max(0, count - 1)); } status('已删除'); return true; }
  catch (error) { status(error.message, true); return false; }
}
function row(item) {
  const tr = document.createElement('tr');
  const a = document.createElement('a'); a.href = link(item.key); a.textContent = link(item.key); a.target = '_blank'; a.rel = 'noopener noreferrer';
  const cells = [a, time(item.uploadedAt), size(item.size)].map(value => { const td = document.createElement('td'); td.append(value); return td; });
  cells[1].className = cells[2].className = 'num';
  const action = document.createElement('td'); const button = document.createElement('button'); button.className = 'danger'; button.type = 'button'; button.textContent = '删除';
  button.addEventListener('click', () => remove(item.key, tr)); action.append(button);
  tr.append(...cells, action); return tr;
}
async function load(reset) {
  if (reset) { cursor = null; el('rows').replaceChildren(); setCount(0); }
  status('加载中…');
  const data = await api('/api/admin/shares' + (cursor ? '?cursor=' + encodeURIComponent(cursor) : ''));
  for (const item of data.items) el('rows').append(row(item));
  setCount(count + data.items.length);
  cursor = data.cursor; el('more').hidden = data.complete;
  status(count ? '' : '当前没有分享');
}
function logout() { adminKey = ''; el('rows').replaceChildren(); el('dashboard').hidden = true; el('login-panel').hidden = false; el('admin-key').focus(); }
el('login-form').addEventListener('submit', async event => {
  event.preventDefault(); adminKey = el('admin-key').value.trim(); el('login-error').textContent = '';
  try { await load(true); el('admin-key').value = ''; el('login-panel').hidden = true; el('dashboard').hidden = false; }
  catch (error) { adminKey = ''; el('login-error').textContent = error.message; }
});
el('logout').addEventListener('click', logout);
el('more').addEventListener('click', () => load(false).catch(error => status(error.message, true)));
el('delete-form').addEventListener('submit', event => {
  event.preventDefault();
  const value = el('delete-link').value.trim();
  let key = value;
  try { const url = new URL(value); key = url.origin === location.origin ? url.pathname.slice(1) : ''; } catch {}
  if (!/^[A-Za-z0-9_-]{43}$/.test(key)) { status('请输入此服务的有效分享链接', true); return; }
  const shown = [...el('rows').children].find(tr => tr.querySelector('a')?.href === link(key));
  remove(key, shown).then(done => { if (done) el('delete-link').value = ''; });
});
</script>
</body>
</html>`;
