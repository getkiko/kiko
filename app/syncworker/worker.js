// Kiko sync Worker: stores only ciphertext encrypted by the client. KV is eventually consistent, so If-Match is best effort, not a true CAS.
import { ADMIN_HTML } from "./admin-page.js";
import { SHARE_UPLOAD_HTML, SHARE_ADMIN_HTML } from "./share-pages.js";

const PREFIX = "sbcfg/";
const TOKEN_PREFIX = "sbcfg-admin/tokens/";
const USER_PREFIX = "sbcfg-users/";
const MAX_BODY = 10 * 1024 * 1024;
const NAME_RE = /^[A-Za-z0-9._-]{1,128}$/;

function corsHeaders(env) {
  return {
    "Access-Control-Allow-Origin": env.ALLOWED_ORIGIN || "*",
    "Access-Control-Allow-Methods": "GET,POST,PUT,DELETE,OPTIONS",
    "Access-Control-Allow-Headers": "Authorization, Content-Type, If-Match, If-None-Match",
    "Access-Control-Expose-Headers": "ETag",
    "Access-Control-Max-Age": "86400",
    "Cache-Control": "no-store",
  };
}

function reply(env, status, body, extra) {
  return new Response(body ?? null, { status, headers: { ...corsHeaders(env), ...extra } });
}

function fail(env, status, message) {
  return reply(env, status, JSON.stringify({ error: message }), { "Content-Type": "application/json" });
}

async function digest(text) {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
}

// Compare the SHA-256 digests of the two tokens, which have a fixed length, to avoid leaking the token's length and prefix.
async function tokenMatches(given, expected) {
  const [a, b] = await Promise.all([digest(given), digest(expected)]);
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

function bearer(request) {
  const header = request.headers.get("Authorization") || "";
  return header.startsWith("Bearer ") ? header.slice(7).trim() : "";
}

function quote(etag) {
  return `"${etag}"`;
}

function unquote(value) {
  return value.trim().replace(/^W\//, "").replace(/^"|"$/g, "");
}

async function newEtag(bytes) {
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(hash.slice(0, 16), byte => byte.toString(16).padStart(2, "0")).join("");
}

async function readBody(request, limit = MAX_BODY) {
  const declared = Number(request.headers.get("Content-Length"));
  if (Number.isFinite(declared) && declared > limit) return null;
  if (!request.body) return new Uint8Array();
  const reader = request.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > limit) { await reader.cancel(); return null; }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  return bytes;
}

function objectInfo(item, prefix) {
  const meta = item.metadata || {};
  return {
    name: item.name.slice(prefix.length),
    size: Number.isSafeInteger(meta.size) && meta.size >= 0 ? meta.size : null,
    etag: typeof meta.etag === "string" ? meta.etag : "",
    updatedAt: Number.isSafeInteger(meta.updatedAt) && meta.updatedAt > 0 ? meta.updatedAt : null,
  };
}

function emptyStorage() {
  return { objectCount: 0, totalBytes: 0, unknownSizeCount: 0, lastUpdatedAt: null };
}

function addObject(storage, object) {
  storage.objectCount++;
  if (object.size === null) storage.unknownSizeCount++;
  else storage.totalBytes += object.size;
  if (object.updatedAt !== null) storage.lastUpdatedAt = Math.max(storage.lastUpdatedAt || 0, object.updatedAt);
}

// Scan metadata once for all tenants; ciphertext values stay in KV.
async function tokenStorage(env, tokens) {
  const summaries = new Map(tokens.map(token => [token.id, emptyStorage()]));
  if (!tokens.length) return summaries;
  let cursor;
  do {
    const page = await env.SYNC_KV.list({ prefix: USER_PREFIX, cursor });
    for (const item of page.keys) {
      const id = item.name.slice(USER_PREFIX.length).split("/", 1)[0];
      const storage = summaries.get(id);
      if (storage) addObject(storage, objectInfo(item, USER_PREFIX + id + "/"));
    }
    cursor = page.list_complete ? undefined : page.cursor;
  } while (cursor);
  return summaries;
}

async function tokenObjects(env, id) {
  const prefix = USER_PREFIX + id + "/";
  const objects = [];
  const storage = emptyStorage();
  let cursor;
  do {
    const page = await env.SYNC_KV.list({ prefix, cursor });
    for (const item of page.keys) {
      const object = objectInfo(item, prefix);
      objects.push(object);
      addObject(storage, object);
    }
    cursor = page.list_complete ? undefined : page.cursor;
  } while (cursor);
  return { objects, storage };
}

async function listObjects(env, url, prefix) {
  const wanted = url.searchParams.get("prefix") || "";
  const keys = [];
  let cursor;
  do {
    const page = await env.SYNC_KV.list({ prefix: prefix + wanted, cursor });
    for (const item of page.keys) {
      const meta = item.metadata || {};
      keys.push({ name: item.name.slice(prefix.length), size: meta.size ?? 0, etag: meta.etag ?? "", updatedAt: meta.updatedAt ?? 0 });
    }
    cursor = page.list_complete ? undefined : page.cursor;
  } while (cursor);
  return reply(env, 200, JSON.stringify({ keys }), { "Content-Type": "application/json" });
}

async function putObject(env, request, name, prefix) {
  const key = prefix + name;
  const current = await env.SYNC_KV.getWithMetadata(key, { type: "arrayBuffer" });
  const exists = current.value !== null && current.value !== undefined;
  const currentEtag = exists ? String(current.metadata?.etag ?? "") : "";
  const ifNoneMatch = request.headers.get("If-None-Match");
  const ifMatch = request.headers.get("If-Match");
  if (ifNoneMatch !== null && ifNoneMatch.trim() === "*" && exists) return fail(env, 412, "object already exists");
  if (ifMatch !== null && (!exists || unquote(ifMatch) !== currentEtag)) return fail(env, 412, "etag mismatch");
  const body = await readBody(request);
  if (!body) return fail(env, 413, "body too large (max 10 MiB)");
  const etag = await newEtag(body);
  await env.SYNC_KV.put(key, body, { metadata: { etag, size: body.length, updatedAt: Date.now() } });
  return reply(env, exists ? 200 : 201, null, { ETag: quote(etag) });
}

function jsonReply(env, status, data) {
  return reply(env, status, JSON.stringify(data), { "Content-Type": "application/json" });
}

function hex(bytes) {
  return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
}

async function storagePrefix(env, token) {
  if (!token || token.length > 256) return null;
  if (env.SYNC_TOKEN && await tokenMatches(token, env.SYNC_TOKEN)) return PREFIX;
  if (!/^sbcfg_[0-9a-f]{64}$/.test(token)) return null;
  const id = hex(await digest(token));
  const record = await env.SYNC_KV.get(TOKEN_PREFIX + id, { type: "json", cacheTtl: 30 });
  if (!record || record.revokedAt || !(await tokenMatches(token, record.token))) return null;
  return USER_PREFIX + id + "/";
}

async function adminAPI(request, env, url) {
  // Admin responses are same-origin, with no cross-origin credential access.
  const privateEnv = { ALLOWED_ORIGIN: url.origin };
  if (!env.ADMIN_TOKEN) return fail(privateEnv, 503, "请先配置 ADMIN_TOKEN 管理密钥");
  const key = bearer(request);
  if (!key || key.length > 256 || !(await tokenMatches(key, env.ADMIN_TOKEN))) return fail(privateEnv, 401, "管理密钥无效");

  if (url.pathname === "/admin/api/tokens") {
    if (request.method === "GET") {
      const tokens = [];
      let cursor;
      do {
        const page = await env.SYNC_KV.list({ prefix: TOKEN_PREFIX, cursor });
        const records = await Promise.all(page.keys.map(item => env.SYNC_KV.get(item.name, { type: "json", cacheTtl: 30 })));
        tokens.push(...records.filter(Boolean));
        cursor = page.list_complete ? undefined : page.cursor;
      } while (cursor);
      tokens.sort((a, b) => b.createdAt - a.createdAt || a.id.localeCompare(b.id));
      const summaries = await tokenStorage(env, tokens);
      return jsonReply(privateEnv, 200, { tokens: tokens.map(token => ({ ...token, storage: summaries.get(token.id) })) });
    }
    if (request.method === "POST") {
      const bytes = await readBody(request, 4096);
      if (!bytes) return fail(privateEnv, 413, "请求过大");
      let body;
      try { body = JSON.parse(new TextDecoder().decode(bytes)); } catch { return fail(privateEnv, 400, "请求格式错误"); }
      const name = typeof body?.name === "string" ? body.name.trim() : "";
      if (!name || name.length > 80) return fail(privateEnv, 400, "请填写 1–80 个字符的令牌名称");
      const token = "sbcfg_" + hex(crypto.getRandomValues(new Uint8Array(32)));
      const id = hex(await digest(token));
      const record = { id, name, token, createdAt: Date.now(), revokedAt: null };
      await env.SYNC_KV.put(TOKEN_PREFIX + id, JSON.stringify(record));
      return jsonReply(privateEnv, 201, { ...record, storage: emptyStorage() });
    }
    return fail(privateEnv, 405, "method not allowed");
  }

  const objectsMatch = /^\/admin\/api\/tokens\/([0-9a-f]{64})\/objects$/.exec(url.pathname);
  if (objectsMatch) {
    if (request.method !== "GET") return fail(privateEnv, 405, "method not allowed");
    const record = await env.SYNC_KV.get(TOKEN_PREFIX + objectsMatch[1], { type: "json", cacheTtl: 30 });
    if (!record) return fail(privateEnv, 404, "令牌不存在");
    return jsonReply(privateEnv, 200, await tokenObjects(env, objectsMatch[1]));
  }

  const match = /^\/admin\/api\/tokens\/([0-9a-f]{64})$/.exec(url.pathname);
  if (match) {
    if (request.method !== "DELETE") return fail(privateEnv, 405, "method not allowed");
    const record = await env.SYNC_KV.get(TOKEN_PREFIX + match[1], { type: "json", cacheTtl: 30 });
    if (!record) return fail(privateEnv, 404, "令牌不存在");
    if (!record.revokedAt) {
      record.revokedAt = Date.now();
      await env.SYNC_KV.put(TOKEN_PREFIX + record.id, JSON.stringify(record));
    }
    const { storage } = await tokenObjects(env, match[1]);
    return jsonReply(privateEnv, 200, { ...record, storage });
  }
  return fail(privateEnv, 404, "not found");
}

// ---- Configuration sharing ----
// Public, link-addressed configuration files: PUT /api/share/:key stores one, GET /:key downloads it.
// The 43-character key (32 random bytes, URL-safe base64) is the only credential for downloading. A share is written
// once: a PUT to an existing key is refused with 409, so a link holder can never replace the file. Every share uses a new key.
const SHARE_PREFIX = "kiko-shares/";
const SHARE_RATE_PREFIX = "kiko-share-rate/";
const SHARE_MAX_BYTES = 2_000_000;
const SHARE_UPLOADS_PER_MINUTE = 5;
const SHARE_KEY_RE = /^[A-Za-z0-9_-]{43}$/;
const SHARE_ACTION = "kiko_config_share";
const TURNSTILE_VERIFY_URL = "https://challenges.cloudflare.com/turnstile/v0/siteverify";

function shareHeaders(extra) {
  return {
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    "X-Robots-Tag": "noindex, nofollow, noarchive",
    "Referrer-Policy": "no-referrer",
    ...extra,
  };
}

function shareJSON(status, data, extra) {
  return new Response(JSON.stringify(data), { status, headers: shareHeaders({ "Content-Type": "application/json; charset=utf-8", ...extra }) });
}

function shareError(status, message, extra) {
  return shareJSON(status, { error: message }, extra);
}

function splitList(value) {
  return String(value || "").split(/[\s,]+/).filter(Boolean);
}

// Turnstile is on only when both keys are set; a half configuration refuses uploads instead of silently opening them.
function turnstileMode(env) {
  if (env.TURNSTILE_SITE_KEY && env.TURNSTILE_SECRET_KEY) return "on";
  if (env.TURNSTILE_SITE_KEY || env.TURNSTILE_SECRET_KEY) return "misconfigured";
  return "off";
}

function turnstileAction(env) {
  return /^[A-Za-z0-9_-]{1,32}$/.test(env.TURNSTILE_ACTION || "") ? env.TURNSTILE_ACTION : SHARE_ACTION;
}

function shareTTLSeconds(env) {
  const days = Number(env.SHARE_TTL_DAYS);
  return Number.isInteger(days) && days > 0 && days <= 3650 ? days * 86400 : 0;
}

// Browsers may call the share API from this origin or from SHARE_ALLOWED_ORIGINS; native clients send no Origin.
function shareCORS(request, env, url) {
  const origin = request.headers.get("Origin");
  if (!origin) return {};
  if (origin !== url.origin && !splitList(env.SHARE_ALLOWED_ORIGINS).includes(origin)) return null;
  return {
    "Access-Control-Allow-Origin": origin,
    "Access-Control-Allow-Methods": "PUT, OPTIONS",
    "Access-Control-Allow-Headers": "Authorization, Content-Type, X-Turnstile-Token",
    "Access-Control-Max-Age": "600",
    Vary: "Origin",
  };
}

async function verifyTurnstile(request, env, url, token) {
  const body = new URLSearchParams({ secret: env.TURNSTILE_SECRET_KEY, response: token });
  const ip = request.headers.get("CF-Connecting-IP");
  if (ip) body.set("remoteip", ip);
  let result;
  try {
    const response = await fetch(TURNSTILE_VERIFY_URL, { method: "POST", body });
    if (!response.ok) return 502;
    result = await response.json();
  } catch {
    return 502;
  }
  const hostnames = splitList(env.TURNSTILE_HOSTNAMES);
  if (!hostnames.length) hostnames.push(url.hostname);
  const hostname = typeof result?.hostname === "string" ? result.hostname : "";
  const hostOK = hostnames.some(allowed => hostname === allowed || hostname.endsWith("." + allowed));
  const expected = turnstileAction(env);
  const actionOK = result?.action === expected || (expected === "test" && !result?.action);
  return result?.success === true && hostOK && actionOK ? 200 : 403;
}

// A sync access token, the legacy SYNC_TOKEN or ADMIN_TOKEN authorizes uploads without Turnstile.
async function shareCredentialValid(env, token) {
  if (!token || token.length > 256) return false;
  if (env.ADMIN_TOKEN && await tokenMatches(token, env.ADMIN_TOKEN)) return true;
  return (await storagePrefix(env, token)) !== null;
}

// Best-effort per-IP minute counter; KV has no atomic increment, so concurrent uploads may slip past the limit.
async function consumeShareQuota(request, env) {
  const ip = request.headers.get("CF-Connecting-IP") || "unknown";
  const secret = env.SHARE_IP_HASH_SECRET || env.ADMIN_TOKEN || env.SYNC_TOKEN || env.TURNSTILE_SECRET_KEY || "kiko-share";
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const ipHash = hex(new Uint8Array(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(ip)))).slice(0, 32);
  const counterKey = SHARE_RATE_PREFIX + ipHash + "/" + Math.floor(Date.now() / 60_000);
  const used = Number(await env.SYNC_KV.get(counterKey, { type: "text" })) || 0;
  if (used >= SHARE_UPLOADS_PER_MINUTE) return false;
  // 120 s covers the current minute; KV requires expirationTtl >= 60.
  await env.SYNC_KV.put(counterKey, String(used + 1), { expirationTtl: 120 });
  return true;
}

async function uploadShare(request, env, url, key, cors) {
  if (!SHARE_KEY_RE.test(key)) return shareError(404, "分享链接无效", cors);
  const mode = turnstileMode(env);
  if (mode === "misconfigured") return shareError(503, "上传验证配置不完整：需要同时设置 TURNSTILE_SITE_KEY 和 TURNSTILE_SECRET_KEY", cors);
  if ((request.headers.get("Content-Type") || "").split(";")[0].trim().toLowerCase() !== "application/json") {
    return shareError(415, "上传内容必须是 JSON", cors);
  }
  const bytes = await readBody(request, SHARE_MAX_BYTES);
  if (!bytes) return shareError(413, "配置文件超过 2 MB 限制", cors);
  try {
    const value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    if (!value || typeof value !== "object" || Array.isArray(value)) return shareError(400, "配置 JSON 必须是对象", cors);
  } catch {
    return shareError(400, "配置 JSON 格式无效", cors);
  }

  if (!(await shareCredentialValid(env, bearer(request)))) {
    if (mode === "off") {
      if (!env.ADMIN_TOKEN && !env.SYNC_TOKEN) return shareError(503, "分享上传尚未配置：请设置 ADMIN_TOKEN 或 Turnstile", cors);
      return shareError(401, "请填写有效的访问令牌", cors);
    }
    const token = (request.headers.get("X-Turnstile-Token") || "").trim();
    if (!token || token.length > 2048) return shareError(400, "请完成上传验证", cors);
    const verified = await verifyTurnstile(request, env, url, token);
    if (verified === 502) return shareError(502, "上传验证暂时不可用，请重试", cors);
    if (verified !== 200) return shareError(403, "上传验证失败，请重新验证", cors);
  }

  // Shares are immutable. KV is eventually consistent, so two concurrent first uploads to one key may both pass this
  // check; keys are random 32-byte values chosen by the uploader, so that only affects the uploader's own link.
  const existing = await env.SYNC_KV.getWithMetadata(SHARE_PREFIX + key, { type: "arrayBuffer" });
  if (existing.value !== null && existing.value !== undefined) return shareError(409, "该分享链接已存在，请重新分享以获得新链接", cors);
  if (!(await consumeShareQuota(request, env))) return shareError(429, "每分钟最多上传 5 次，请稍后重试", cors);
  const now = Date.now();
  const ttl = shareTTLSeconds(env);
  const metadata = { uploadedAt: now, size: bytes.length };
  const options = { metadata };
  if (ttl) { metadata.expiresAt = now + ttl * 1000; options.expirationTtl = ttl; }
  await env.SYNC_KV.put(SHARE_PREFIX + key, bytes, options);
  return shareJSON(201, { uploaded: true, url: url.origin + "/" + key, expiresAt: metadata.expiresAt ?? null }, cors);
}

async function downloadShare(env, key) {
  const found = await env.SYNC_KV.getWithMetadata(SHARE_PREFIX + key, { type: "arrayBuffer" });
  const expiresAt = found.metadata?.expiresAt;
  // KV expiry is the real deletion; the metadata check also covers its propagation lag.
  if (found.value === null || found.value === undefined || (Number.isFinite(expiresAt) && expiresAt <= Date.now())) {
    return shareError(404, "配置文件不存在或已过期");
  }
  return new Response(found.value, {
    headers: shareHeaders({
      "Content-Type": "application/json; charset=utf-8",
      "Content-Disposition": "attachment; filename=\"config.kiko.json\"",
      "Content-Security-Policy": "default-src 'none'; base-uri 'none'; frame-ancestors 'none'",
      "Access-Control-Allow-Origin": "*",
    }),
  });
}

async function shareAdminAPI(request, env, url) {
  if (!env.ADMIN_TOKEN) return shareError(503, "请先配置 ADMIN_TOKEN 管理密钥");
  const key = bearer(request);
  if (!key || key.length > 256 || !(await tokenMatches(key, env.ADMIN_TOKEN))) return shareError(401, "管理密钥无效");
  if (url.pathname === "/api/admin/shares") {
    if (request.method !== "GET") return shareError(405, "method not allowed", { Allow: "GET" });
    const cursor = url.searchParams.get("cursor") || undefined;
    if (cursor && cursor.length > 2048) return shareError(400, "分页游标无效");
    const page = await env.SYNC_KV.list({ prefix: SHARE_PREFIX, limit: 50, cursor });
    const items = page.keys.flatMap(item => {
      const shareKey = item.name.slice(SHARE_PREFIX.length);
      if (!SHARE_KEY_RE.test(shareKey)) return [];
      const meta = item.metadata || {};
      const int = value => Number.isSafeInteger(value) ? value : null;
      return [{ key: shareKey, size: int(meta.size), uploadedAt: int(meta.uploadedAt), expiresAt: int(meta.expiresAt) }];
    });
    return shareJSON(200, { items, cursor: page.list_complete ? null : page.cursor ?? null, complete: !!page.list_complete });
  }
  const match = /^\/api\/admin\/shares\/([^/]+)$/.exec(url.pathname);
  if (!match) return shareError(404, "not found");
  if (request.method !== "DELETE") return shareError(405, "method not allowed", { Allow: "DELETE" });
  if (!SHARE_KEY_RE.test(match[1])) return shareError(400, "分享链接格式无效");
  await env.SYNC_KV.delete(SHARE_PREFIX + match[1]);
  return shareJSON(200, { deleted: true });
}

function sharePage(html, replacements, turnstile) {
  const nonce = hex(crypto.getRandomValues(new Uint8Array(16)));
  let body = html.replaceAll("__CSP_NONCE__", nonce);
  for (const [name, value] of Object.entries(replacements)) body = body.replace(name, () => value);
  const challenges = turnstile ? " https://challenges.cloudflare.com" : "";
  return new Response(body, {
    headers: shareHeaders({
      "Content-Type": "text/html; charset=utf-8",
      "Content-Security-Policy": "default-src 'none'; script-src 'nonce-" + nonce + "'" + challenges + "; style-src 'nonce-" + nonce + "'; connect-src 'self'" +
        (turnstile ? "; frame-src https://challenges.cloudflare.com" : "") + "; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
      "Permissions-Policy": "camera=(), microphone=(), geolocation=()",
    }),
  });
}

// Returns a response for share routes, or null to fall through to sync routes.
async function handleShare(request, env, url) {
  const path = url.pathname;
  if (path === "/share-upload" || path === "/share-admin") {
    if (request.method !== "GET" && request.method !== "HEAD") return shareError(405, "method not allowed", { Allow: "GET" });
    if (path === "/share-admin") return sharePage(SHARE_ADMIN_HTML, {}, false);
    const turnstile = turnstileMode(env) === "on";
    const config = { siteKey: turnstile ? env.TURNSTILE_SITE_KEY : "", action: turnstileAction(env), maxBytes: SHARE_MAX_BYTES };
    return sharePage(SHARE_UPLOAD_HTML, { __SHARE_CONFIG__: JSON.stringify(config).replace(/</g, "\\u003c") }, turnstile);
  }
  if (path === "/manage") return Response.redirect(new URL("/share-admin", url).href, 308);
  if (path === "/api/admin/shares" || path.startsWith("/api/admin/shares/")) return shareAdminAPI(request, env, url);
  const api = /^\/api\/share\/([^/]+)$/.exec(path);
  if (api) {
    const cors = shareCORS(request, env, url);
    if (!cors) return shareError(403, "来源不受支持");
    if (request.method === "OPTIONS") return new Response(null, { status: 204, headers: shareHeaders(cors) });
    if (request.method === "PUT") return uploadShare(request, env, url, api[1], cors);
    return shareError(405, "method not allowed", { ...cors, Allow: "PUT, OPTIONS" });
  }
  const download = /^\/([A-Za-z0-9_-]{43})$/.exec(path);
  if (download && request.method === "GET") return downloadShare(env, download[1]);
  return null;
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const shared = await handleShare(request, env, url);
    if (shared) return shared;
    if (url.pathname.startsWith("/admin/api/")) return adminAPI(request, env, url);
    if (["/", "/admin", "/admin/"].includes(url.pathname)) {
      if (request.method === "GET") {
        const nonce = hex(crypto.getRandomValues(new Uint8Array(16)));
        return reply(env, 200, ADMIN_HTML.replaceAll("__CSP_NONCE__", nonce), {
          "Content-Type": "text/html; charset=utf-8",
          "Content-Security-Policy": "default-src 'none'; script-src 'nonce-" + nonce + "'; style-src 'nonce-" + nonce + "'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
          "X-Content-Type-Options": "nosniff",
          "Referrer-Policy": "no-referrer",
        });
      }
    }
    if (request.method === "OPTIONS") return reply(env, 204, null);
    if (!env.SYNC_TOKEN && !env.ADMIN_TOKEN) return fail(env, 500, "SYNC_TOKEN or ADMIN_TOKEN is not configured (wrangler secret put)");
    const prefix = await storagePrefix(env, bearer(request));
    if (!prefix) return fail(env, 401, "unauthorized");

    if (url.pathname === "/v1/objects" || url.pathname === "/v1/objects/") {
      return request.method === "GET" ? listObjects(env, url, prefix) : fail(env, 405, "method not allowed");
    }
    const match = /^\/v1\/objects\/([^/]*)$/.exec(url.pathname);
    if (!match) return fail(env, 404, "not found");
    let name;
    try { name = decodeURIComponent(match[1]); } catch { return fail(env, 400, "invalid name"); }
    if (!NAME_RE.test(name) || name === "." || name === "..") return fail(env, 400, "invalid name");

    const key = prefix + name;
    switch (request.method) {
      case "GET": {
        const found = await env.SYNC_KV.getWithMetadata(key, { type: "arrayBuffer" });
        if (found.value === null || found.value === undefined) return fail(env, 404, "not found");
        return reply(env, 200, found.value, { "Content-Type": "application/octet-stream", ETag: quote(String(found.metadata?.etag ?? "")) });
      }
      case "PUT":
        return putObject(env, request, name, prefix);
      case "DELETE":
        await env.SYNC_KV.delete(key);
        return reply(env, 204, null);
      default:
        return fail(env, 405, "method not allowed");
    }
  },
};
