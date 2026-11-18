// Kept separate for editing; the Go package inlines this into copied Worker source.
export const ADMIN_HTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>Kiko 同步管理</title>
<style nonce="__CSP_NONCE__">
:root{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#1d1d1f;background:#f5f5f7;color-scheme:light dark;--surface:#fff;--line:#e6e6e9;--muted:#6e6e73;--accent:#007aff;--danger:#c9252d;--fill:#f0f0f2;--glass:rgba(245,245,247,.86)}
*{box-sizing:border-box}body{margin:0}button,input{font:inherit}button{cursor:pointer;touch-action:manipulation}button:disabled{opacity:.5;cursor:default}button,input{min-height:44px;border-radius:11px}button{padding:0 15px;border:0;background:var(--fill);color:inherit;font-size:14px;font-weight:500}button:active{filter:brightness(.94)}button:focus-visible,input:focus-visible{outline:3px solid var(--accent);outline-offset:3px}.primary{background:var(--accent);color:white}.quiet{background:transparent}.danger{color:var(--danger);background:transparent}input{width:100%;padding:10px 13px;border:1px solid var(--line);background:var(--surface);color:inherit;font-size:16px}label{display:block;font-size:14px;font-weight:500;margin-bottom:8px}header{position:sticky;top:0;z-index:1;border-bottom:1px solid var(--line);background:var(--glass);backdrop-filter:blur(20px) saturate(160%)}.bar{max-width:800px;margin:auto;padding:16px 24px;display:flex;align-items:center;justify-content:space-between;gap:12px}.brand{font-size:17px;font-weight:600;letter-spacing:-.4px}main{max-width:800px;margin:40px auto;padding:0 24px max(40px,env(safe-area-inset-bottom))}h1{font-size:28px;letter-spacing:-.7px;margin:0 0 10px}h2{font-size:18px;letter-spacing:-.3px;margin:0}p{color:var(--muted);font-size:14px;line-height:1.65;margin:0 0 24px}.panel{background:var(--surface);border:1px solid var(--line);border-radius:18px;padding:24px}.login{max-width:420px;margin:70px auto}.login button{width:100%;margin-top:16px}.login p{margin-bottom:24px}.heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:30px 0 12px}.create{display:flex;gap:12px;align-items:end}.create .field{flex:1;min-width:0}.create button{flex-shrink:0}.tokens{list-style:none;padding:0;margin:0;background:var(--surface);border:1px solid var(--line);border-radius:18px;overflow:hidden}.token{padding:20px}.token+.token{border-top:1px solid var(--line)}.row{display:flex;align-items:center;gap:10px}.row h3{min-width:0;flex:1;font-size:16px;margin:0;overflow-wrap:anywhere}.state{font-size:12px;color:var(--muted);flex-shrink:0}.meta{color:var(--muted);font-size:12px;margin:3px 0 12px}.credential{display:flex;gap:8px;align-items:center}.credential input{font-family:ui-monospace,SFMono-Regular,monospace;font-size:13px;min-width:0;flex:1}.credential button{flex-shrink:0}.empty{padding:32px 20px;text-align:center;color:var(--muted);font-size:14px}.help{font-size:12px;margin-top:16px}.address{font-family:ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere;font-size:13px;color:inherit}.error{color:var(--danger);font-size:13px;margin-top:12px;margin-bottom:0;min-height:20px}dialog{max-width:400px;width:calc(100% - 40px);border:1px solid var(--line);border-radius:20px;padding:24px;background:var(--surface);color:inherit;box-shadow:0 12px 50px #0002}dialog::backdrop{background:#0004;backdrop-filter:blur(4px)}dialog h2{margin-bottom:12px}dialog .actions{display:flex;gap:8px;justify-content:flex-end}.toast{position:fixed;bottom:max(24px,env(safe-area-inset-bottom));left:50%;transform:translateX(-50%);max-width:calc(100% - 32px);border:1px solid var(--line);border-radius:14px;padding:12px 18px;background:var(--surface);box-shadow:0 4px 25px #0002;font-size:14px;z-index:3}[hidden]{display:none!important}
@media(prefers-color-scheme:dark){:root{color:#f5f5f7;background:#161618;--surface:#222225;--line:#39393d;--muted:#a1a1a6;--fill:#333336;--glass:rgba(22,22,24,.9);--accent:#0a84ff;--danger:#ff6961}}
@media(max-width:540px){main{padding:0 16px 32px;margin-top:24px}.bar{padding:12px 16px}.panel{padding:18px}.login{margin:48px auto}.token{padding:16px}.credential{flex-wrap:wrap}.credential input{flex-basis:100%}.credential button{flex:1}.create{gap:8px}h1{font-size:25px}}
@media(prefers-reduced-transparency:reduce){header{background:var(--surface);backdrop-filter:none}dialog::backdrop{backdrop-filter:none}}
.storage{color:var(--muted);font-size:13px;line-height:1.7;margin:12px 0 2px;font-variant-numeric:tabular-nums}.files{margin-top:4px}.files summary{cursor:pointer;color:var(--accent);font-size:13px;padding:10px 0;min-height:44px;width:fit-content}.files summary:focus-visible{outline:3px solid var(--accent);outline-offset:3px;border-radius:4px}.file-list{list-style:none;padding:0;margin:0}.file{padding:12px 0;border-top:1px solid var(--line)}.file-row{display:flex;align-items:baseline;justify-content:space-between;gap:16px}.file-name{font-size:13px;overflow-wrap:anywhere;min-width:0}.file-size{font-size:13px;white-space:nowrap;flex-shrink:0;font-variant-numeric:tabular-nums}.file-meta{color:var(--muted);font-size:12px;line-height:1.7;margin-top:4px;overflow-wrap:anywhere}.checksum{font-family:ui-monospace,SFMono-Regular,monospace;font-size:11px}.file-status{color:var(--muted);font-size:13px;padding:12px 0}.files .help{margin:8px 0 0}
</style>
</head>
<body>
<header><div class="bar"><span class="brand">Kiko 同步管理</span><button id="logout" class="quiet" hidden>退出</button></div></header>
<main>
<section id="login-panel" class="login panel">
  <h1>进入管理</h1><p>使用管理密钥创建和管理访问令牌。</p>
  <form id="login-form"><label for="admin-key">管理密钥</label><input id="admin-key" type="password" autocomplete="current-password" required maxlength="256" placeholder="输入管理密钥"><button id="login-button" class="primary">进入</button><p id="login-error" class="error" role="alert"></p></form>
</section>
<section id="dashboard" hidden>
  <h1>访问令牌</h1><p>每个令牌拥有独立的备份空间。同一用户的多台设备使用同一个令牌。</p>
  <div class="panel"><form id="create-form" class="create"><div class="field"><label for="token-name">令牌名称</label><input id="token-name" autocomplete="off" required maxlength="80" placeholder="例如：家人、小王"></div><button id="create-button" class="primary">创建令牌</button></form></div>
  <div class="heading"><h2 id="token-heading">已创建的令牌</h2><button id="refresh-button" class="quiet">刷新</button></div>
  <ul id="token-list" class="tokens"></ul>
  <p class="help">Kiko 中选择 Cloudflare KV，地址填写 <span id="sync-address" class="address"></span>，Token 填写对应的访问令牌。KV 更新通常需要几十秒传播；新建和撤销操作可能稍后在其他地区生效。</p>
  <p class="help">配置分享：在 Kiko 设置中把配置分享地址也填写为同一地址。<a href="/share-admin">管理分享</a></p>
</section>
</main>
<dialog id="revoke-dialog"><h2>撤销访问令牌</h2><p id="revoke-message"></p><div class="actions"><button id="revoke-cancel">取消</button><button id="revoke-confirm" class="danger">撤销</button></div></dialog>
<div id="toast" class="toast" role="status" aria-live="polite" hidden></div>
<script nonce="__CSP_NONCE__">
const el = id => document.getElementById(id);
let adminKey = '';
let tokens = [];
const expanded = new Set();
const objectDetails = new Map();
let revokeTarget = null;
let toastTimer;
function notify(message) { el('toast').textContent = message; el('toast').hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => { el('toast').hidden = true; }, 3500); }
function logout() { adminKey = ''; tokens = []; expanded.clear(); objectDetails.clear(); revokeTarget = null; render(); el('dashboard').hidden = true; el('logout').hidden = true; el('login-panel').hidden = false; el('admin-key').value = ''; el('login-error').textContent = ''; el('revoke-dialog').close(); el('admin-key').focus(); }
async function api(path = '', options = {}) {
  const key = adminKey;
  const response = await fetch('/admin/api/tokens' + path, { ...options, headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' } });
  if (key !== adminKey) throw new DOMException('已退出管理', 'AbortError');
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || '操作失败');
  return data;
}
function node(tag, text, className) { const item = document.createElement(tag); if (text !== undefined) item.textContent = text; if (className) item.className = className; return item; }
function formatBytes(value) {
  if (value === null || value === undefined) return '大小未知';
  if (value < 1024) return value.toLocaleString() + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB']; let size = value / 1024; let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return size.toLocaleString(undefined, { maximumFractionDigits: 2 }) + ' ' + units[unit];
}
function storageText(storage) {
  if (!storage) return '存储统计待刷新';
  const size = (storage.unknownSizeCount ? '已知大小 ' : '共 ') + formatBytes(storage.totalBytes);
  const updated = storage.lastUpdatedAt ? ' · 最近更新 ' + new Date(storage.lastUpdatedAt).toLocaleString() : '';
  return storage.objectCount + ' 个文件 · ' + size + (storage.unknownSizeCount ? ' · ' + storage.unknownSizeCount + ' 个大小未知' : '') + updated;
}
function renderObjects(content, data) {
  content.replaceChildren();
  if (!data.objects.length) { content.append(node('div', '该令牌暂未存储文件', 'file-status')); return; }
  const list = node('ul', undefined, 'file-list');
  for (const object of data.objects) {
    const file = node('li', undefined, 'file'); const row = node('div', undefined, 'file-row');
    row.append(node('span', object.name, 'file-name'), node('span', formatBytes(object.size), 'file-size'));
    const meta = node('div', object.updatedAt ? '更新于 ' + new Date(object.updatedAt).toLocaleString() : '更新时间未知', 'file-meta');
    if (object.etag) { meta.append(node('br'), node('span', 'ETag ' + object.etag, 'checksum')); }
    file.append(row, meta); list.append(file);
  }
  content.append(list, node('p', '文件数按当前存储文件统计；大小为加密后的文件大小。配置内容与配置数量由客户端解密后读取。', 'help'));
}
async function loadObjects(record, content, stats) {
  const cached = objectDetails.get(record.id);
  if (cached) { renderObjects(content, cached); return; }
  content.replaceChildren(node('div', '正在读取文件…', 'file-status'));
  try {
    const data = await api('/' + record.id + '/objects');
    if (!content.isConnected) return;
    objectDetails.set(record.id, data);
    const current = tokens.find(item => item.id === record.id); if (current) current.storage = data.storage;
    stats.textContent = storageText(data.storage); renderObjects(content, data);
  } catch (error) {
    if (error.name === 'AbortError' || !content.isConnected) return;
    content.replaceChildren(node('div', error.message, 'error'));
    const retry = node('button', '重试', 'quiet'); retry.addEventListener('click', () => loadObjects(record, content, stats)); content.append(retry);
  }
}
function update(record) { tokens = tokens.filter(item => item.id !== record.id); tokens.unshift(record); render(); }
function render() {
  const list = el('token-list'); list.replaceChildren();
  const active = tokens.filter(item => !item.revokedAt).length;
  el('token-heading').textContent = '已创建的令牌 · ' + active + ' 个可用';
  if (!tokens.length) { list.append(node('li', '还没有访问令牌，创建一个即可开始。', 'empty')); return; }
  for (const record of tokens) {
    const item = node('li', undefined, 'token');
    const row = node('div', undefined, 'row');
    row.append(node('h3', record.name), node('span', record.revokedAt ? '已撤销' : '可用', 'state'));
    if (!record.revokedAt) { const revoke = node('button', '撤销', 'danger'); revoke.addEventListener('click', () => { revokeTarget = record; el('revoke-message').textContent = '撤销「' + record.name + '」后，使用它的设备将失去同步权限，已保存的备份会保留。'; el('revoke-dialog').showModal(); }); row.append(revoke); }
    const meta = node('div', '创建于 ' + new Date(record.createdAt).toLocaleString(), 'meta');
    const credential = node('div', undefined, 'credential');
    const input = node('input'); input.type = 'password'; input.readOnly = true; input.value = record.token; input.setAttribute('aria-label', record.name + '的访问令牌'); input.autocomplete = 'off'; input.spellcheck = false;
    const show = node('button', '查看'); show.setAttribute('aria-pressed', 'false'); show.addEventListener('click', () => { const visible = input.type === 'password'; input.type = visible ? 'text' : 'password'; show.textContent = visible ? '隐藏' : '查看'; show.setAttribute('aria-pressed', String(visible)); });
    const copy = node('button', '复制'); copy.addEventListener('click', async () => { try { await navigator.clipboard.writeText(record.token); notify('访问令牌已复制'); } catch { input.type = 'text'; show.textContent = '隐藏'; show.setAttribute('aria-pressed', 'true'); input.focus(); input.select(); notify('请复制选中的令牌'); } });
    const stats = node('div', storageText(record.storage), 'storage');
    const files = node('details', undefined, 'files'); files.open = expanded.has(record.id);
    const content = node('div'); files.append(node('summary', '文件详情'), content);
    files.addEventListener('toggle', () => { if (files.open) { expanded.add(record.id); loadObjects(record, content, stats); } else expanded.delete(record.id); });
    credential.append(input, show, copy); item.append(row, meta, credential, stats, files); list.append(item);
  }
}
el('sync-address').textContent = location.origin;
el('login-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = el('login-button'); button.disabled = true; el('login-error').textContent = ''; adminKey = el('admin-key').value.trim();
  try { const data = await api(); tokens = data.tokens; render(); el('admin-key').value = ''; el('login-panel').hidden = true; el('dashboard').hidden = false; el('logout').hidden = false; el('token-name').focus(); }
  catch (error) { adminKey = ''; el('login-error').textContent = error.message; }
  finally { button.disabled = false; }
});
el('logout').addEventListener('click', logout);
el('create-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = el('create-button'); button.disabled = true;
  try { const record = await api('', { method: 'POST', body: JSON.stringify({ name: el('token-name').value.trim() }) }); update(record); el('token-name').value = ''; el('token-name').focus(); notify('访问令牌已创建'); }
  catch (error) { if (error.name !== 'AbortError') notify(error.message); }
  finally { button.disabled = false; }
});
el('refresh-button').addEventListener('click', async () => {
  const button = el('refresh-button'); button.disabled = true;
  try { const data = await api(); const known = new Map(tokens.map(item => [item.id, item])); for (const record of data.tokens) { const previous = known.get(record.id); known.set(record.id, { ...record, revokedAt: previous?.revokedAt || record.revokedAt }); } tokens = [...known.values()].sort((a, b) => b.createdAt - a.createdAt); objectDetails.clear(); render(); }
  catch (error) { if (error.name !== 'AbortError') notify(error.message); }
  finally { button.disabled = false; }
});
el('revoke-cancel').addEventListener('click', () => { revokeTarget = null; el('revoke-dialog').close(); });
el('revoke-dialog').addEventListener('click', event => { if (event.target === el('revoke-dialog')) { const rect = event.target.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) { revokeTarget = null; event.target.close(); } } });
el('revoke-confirm').addEventListener('click', async () => {
  if (!revokeTarget) return; const button = el('revoke-confirm'); button.disabled = true;
  try { const record = await api('/' + revokeTarget.id, { method: 'DELETE' }); update(record); revokeTarget = null; el('revoke-dialog').close(); notify('访问令牌已撤销'); }
  catch (error) { if (error.name !== 'AbortError') notify(error.message); }
  finally { button.disabled = false; }
});
</script>
</body>
</html>`;
