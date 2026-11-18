import test from "node:test";
import assert from "node:assert/strict";
import worker from "./worker.js";

function fakeKV() {
  const store = new Map();
  // Honors expirationTtl against Date.now(), which tests may move forward.
  const live = key => {
    const entry = store.get(key);
    if (entry && entry.expiresAt !== null && entry.expiresAt <= Date.now()) { store.delete(key); return undefined; }
    return entry;
  };
  return {
    store,
    async get(key, options) {
      const entry = live(key);
      if (!entry) return null;
      if (options?.type === "json") return JSON.parse(new TextDecoder().decode(entry.value));
      if (options?.type === "text") return new TextDecoder().decode(entry.value);
      return entry.value.slice().buffer;
    },
    async getWithMetadata(key) {
      const entry = live(key);
      return { value: entry ? entry.value.slice().buffer : null, metadata: entry ? entry.metadata : null };
    },
    async put(key, value, options) {
      const bytes = typeof value === "string" ? new TextEncoder().encode(value)
        : value instanceof Uint8Array ? value : new Uint8Array(value);
      if (options?.expirationTtl !== undefined && options.expirationTtl < 60) throw new Error("expirationTtl must be at least 60");
      const expiresAt = options?.expirationTtl ? Date.now() + options.expirationTtl * 1000 : null;
      store.set(key, { value: bytes.slice(), metadata: options?.metadata ?? null, expiresAt, ttl: options?.expirationTtl ?? null });
    },
    async delete(key) { store.delete(key); },
    async list({ prefix = "", cursor } = {}) {
      const keys = [...store.keys()].filter(key => key.startsWith(prefix) && live(key)).sort()
        .map(name => ({ name, metadata: store.get(name).metadata }));
      return { keys, list_complete: true, cursor };
    },
  };
}

const TOKEN = "s3cret-token";
const ADMIN = "management-key-for-tests";
const makeEnv = extra => ({ SYNC_KV: fakeKV(), SYNC_TOKEN: TOKEN, ...extra });
const call = (env, method, path, { token = TOKEN, headers = {}, body } = {}) => worker.fetch(new Request(`https://w.example${path}`, {
  method, body,
  headers: { ...(token === null ? {} : { Authorization: `Bearer ${token}` }), ...headers },
}), env);
const json = async response => JSON.parse(await response.text());

const createToken = async (env, name) => {
  const response = await call(env, "POST", "/admin/api/tokens", { token: ADMIN, body: JSON.stringify({ name }) });
  assert.equal(response.status, 201);
  return json(response);
};

test("rejects missing, wrong and malformed credentials", async () => {
  const env = makeEnv();
  assert.equal((await call(env, "GET", "/v1/objects/a", { token: null })).status, 401);
  assert.equal((await call(env, "GET", "/v1/objects/a", { token: "nope" })).status, 401);
  assert.equal((await call(env, "GET", "/v1/objects/a", { token: TOKEN + "x" })).status, 401);
  assert.equal((await call(env, "GET", "/v1/objects/a", { token: null, headers: { Authorization: `Basic ${TOKEN}` } })).status, 401);
});

test("reports a missing SYNC_TOKEN as a server error", async () => {
  const response = await call(makeEnv({ SYNC_TOKEN: undefined }), "GET", "/v1/objects/a");
  assert.equal(response.status, 500);
  assert.match((await json(response)).error, /SYNC_TOKEN/);
});

test("answers CORS preflight without credentials", async () => {
  const response = await call(makeEnv(), "OPTIONS", "/v1/objects/a", { token: null });
  assert.equal(response.status, 204);
  assert.equal(response.headers.get("Access-Control-Allow-Origin"), "*");
  assert.match(response.headers.get("Access-Control-Allow-Methods"), /PUT/);
  assert.match(response.headers.get("Access-Control-Allow-Headers"), /If-Match/);
  assert.match(response.headers.get("Access-Control-Allow-Headers"), /If-None-Match/);
  assert.equal(response.headers.get("Access-Control-Expose-Headers"), "ETag");
  assert.ok(response.headers.get("Access-Control-Max-Age"));
});

test("ALLOWED_ORIGIN overrides the CORS origin", async () => {
  const response = await call(makeEnv({ ALLOWED_ORIGIN: "http://127.0.0.1:9099" }), "OPTIONS", "/v1/objects/a", { token: null });
  assert.equal(response.headers.get("Access-Control-Allow-Origin"), "http://127.0.0.1:9099");
});

test("creates then updates an object and serves it back", async () => {
  const env = makeEnv();
  const created = await call(env, "PUT", "/v1/objects/vault.json", { body: "cipher-1" });
  assert.equal(created.status, 201);
  const etag1 = created.headers.get("ETag");
  assert.match(etag1, /^"[0-9a-f]{32}"$/);
  assert.ok(env.SYNC_KV.store.has("sbcfg/vault.json"));

  const read = await call(env, "GET", "/v1/objects/vault.json");
  assert.equal(read.status, 200);
  assert.equal(await read.text(), "cipher-1");
  assert.equal(read.headers.get("ETag"), etag1);
  assert.equal(read.headers.get("Content-Type"), "application/octet-stream");

  const updated = await call(env, "PUT", "/v1/objects/vault.json", { body: "cipher-2" });
  assert.equal(updated.status, 200);
  assert.notEqual(updated.headers.get("ETag"), etag1);
});

test("If-None-Match: * only allows the first write", async () => {
  const env = makeEnv();
  assert.equal((await call(env, "PUT", "/v1/objects/a", { body: "1", headers: { "If-None-Match": "*" } })).status, 201);
  assert.equal((await call(env, "PUT", "/v1/objects/a", { body: "2", headers: { "If-None-Match": "*" } })).status, 412);
  assert.equal(await (await call(env, "GET", "/v1/objects/a")).text(), "1");
});

test("If-Match must equal the current etag", async () => {
  const env = makeEnv();
  const etag = (await call(env, "PUT", "/v1/objects/a", { body: "1" })).headers.get("ETag");
  assert.equal((await call(env, "PUT", "/v1/objects/a", { body: "2", headers: { "If-Match": '"deadbeef"' } })).status, 412);
  assert.equal((await call(env, "PUT", "/v1/objects/a", { body: "3", headers: { "If-Match": etag } })).status, 200);
  assert.equal((await call(env, "PUT", "/v1/objects/missing", { body: "x", headers: { "If-Match": etag } })).status, 412);
  assert.equal(await (await call(env, "GET", "/v1/objects/a")).text(), "3");
});

test("returns 404 for missing objects and deletes existing ones", async () => {
  const env = makeEnv();
  assert.equal((await call(env, "GET", "/v1/objects/none")).status, 404);
  await call(env, "PUT", "/v1/objects/a", { body: "1" });
  assert.equal((await call(env, "DELETE", "/v1/objects/a")).status, 204);
  assert.equal((await call(env, "GET", "/v1/objects/a")).status, 404);
  assert.equal((await call(env, "DELETE", "/v1/objects/a")).status, 204);
});

test("rejects unsafe object names", async () => {
  const env = makeEnv();
  for (const name of ["a%2Fb", "%2E%2E%2Fx", "a b", "x".repeat(129), "%E4%B8%AD", "%"]) {
    const response = await call(env, "PUT", `/v1/objects/${name}`, { body: "x" });
    assert.equal(response.status, 400, `name ${name}`);
  }
  // URL normalization collapses . and .. into other paths, so they never reach name validation and are never written.
  for (const name of ["..", "."]) {
    const response = await call(env, "PUT", `/v1/objects/${name}`, { body: "x" });
    assert.ok(response.status >= 400, `name ${name}`);
  }
  assert.equal((await call(env, "PUT", "/v1/objects/", { body: "x" })).status, 405);
  assert.equal((await call(env, "GET", "/v1/other")).status, 404);
  assert.equal(env.SYNC_KV.store.size, 0);
  assert.equal((await call(env, "PUT", `/v1/objects/${"x".repeat(128)}`, { body: "x" })).status, 201);
});

test("rejects bodies larger than 10 MiB", async () => {
  const env = makeEnv();
  const big = new Uint8Array(10 * 1024 * 1024 + 1);
  assert.equal((await call(env, "PUT", "/v1/objects/big", { body: big })).status, 413);
  assert.equal(env.SYNC_KV.store.size, 0);
  const limit = new Uint8Array(10 * 1024 * 1024);
  assert.equal((await call(env, "PUT", "/v1/objects/edge", { body: limit })).status, 201);
});

test("lists objects by prefix with metadata", async () => {
  const env = makeEnv();
  await call(env, "PUT", "/v1/objects/vault.json", { body: "12345" });
  await call(env, "PUT", "/v1/objects/vault.bak", { body: "1" });
  await call(env, "PUT", "/v1/objects/other.txt", { body: "1" });
  const listed = await json(await call(env, "GET", "/v1/objects?prefix=vault"));
  assert.deepEqual(listed.keys.map(item => item.name), ["vault.bak", "vault.json"]);
  const vault = listed.keys.find(item => item.name === "vault.json");
  assert.equal(vault.size, 5);
  assert.match(vault.etag, /^[0-9a-f]{32}$/);
  assert.ok(vault.updatedAt > 0);
  assert.equal((await json(await call(env, "GET", "/v1/objects"))).keys.length, 3);
  assert.equal((await call(env, "DELETE", "/v1/objects")).status, 405);
});

test("every response carries CORS and no-store headers", async () => {
  const env = makeEnv();
  const responses = [
    await call(env, "GET", "/v1/objects/a", { token: null }),
    await call(env, "PUT", "/v1/objects/a", { body: "1" }),
    await call(env, "GET", "/v1/objects/a"),
    await call(env, "GET", "/v1/objects/none"),
    await call(env, "PUT", "/v1/objects/a", { body: "1", headers: { "If-None-Match": "*" } }),
    await call(env, "DELETE", "/v1/objects/a"),
  ];
  for (const response of responses) {
    assert.equal(response.headers.get("Cache-Control"), "no-store");
    assert.equal(response.headers.get("Access-Control-Allow-Origin"), "*");
    assert.equal(response.headers.get("Access-Control-Expose-Headers"), "ETag");
  }
});

test("management page is public, has CSP, and contains no credentials", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  for (const path of ["/", "/admin", "/admin/"]) {
    const response = await call(env, "GET", path, { token: null });
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("Cache-Control"), "no-store");
    assert.match(response.headers.get("Content-Security-Policy"), /frame-ancestors 'none'/);
    const html = await response.text();
    assert.match(html, /访问令牌/);
    assert.ok(!html.includes(ADMIN) && !html.includes(TOKEN));
    assert.ok(!html.includes("__CSP_NONCE__"));
    const nonce = /script-src 'nonce-([0-9a-f]+)'/.exec(response.headers.get("Content-Security-Policy"))[1];
    assert.ok(html.includes('nonce="' + nonce + '"'));
  }
});

test("management key gates every management request and access tokens grant only storage access", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const owner = await createToken(env, "用户 A");
  for (const token of [null, "wrong", TOKEN, owner.token]) {
    for (const [method, path] of [["GET", "/admin/api/tokens"], ["POST", "/admin/api/tokens"], ["DELETE", "/admin/api/tokens/" + owner.id], ["GET", "/admin/api/tokens/" + owner.id + "/objects"]]) {
      const response = await call(env, method, path, { token, body: method === "POST" ? '{"name":"x"}' : undefined });
      assert.equal(response.status, 401);
      assert.equal(response.headers.get("Cache-Control"), "no-store");
      assert.equal(response.headers.get("Access-Control-Allow-Origin"), "https://w.example");
      assert.ok(!(await response.text()).includes(owner.token));
    }
  }
  assert.equal((await call(env, "GET", "/v1/objects/a", { token: ADMIN })).status, 401);
  assert.equal((await call(env, "GET", "/admin/api/tokens", { token: ADMIN })).status, 200);
});

test("created tokens remain viewable and validate names and management routes", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const [a, b] = await Promise.all([createToken(env, "  家人  "), createToken(env, "同事")]);
  assert.equal(a.name, "家人");
  assert.match(a.token, /^sbcfg_[0-9a-f]{64}$/);
  assert.match(a.id, /^[0-9a-f]{64}$/);
  assert.notEqual(a.token, b.token);
  const result = await json(await call(env, "GET", "/admin/api/tokens", { token: ADMIN }));
  assert.equal(result.tokens.length, 2);
  assert.equal(result.tokens.find(item => item.id === a.id).token, a.token);
  for (const body of ['{', '{}', 'null', '{"name":null}', '{"name":"  "}', JSON.stringify({ name: "x".repeat(81) })]) {
    assert.equal((await call(env, "POST", "/admin/api/tokens", { token: ADMIN, body })).status, 400);
  }
  assert.equal((await call(env, "POST", "/admin/api/tokens", { token: ADMIN, body: "x".repeat(4097) })).status, 413);
  assert.equal((await call(env, "PUT", "/admin/api/tokens", { token: ADMIN })).status, 405);
  assert.equal((await call(env, "DELETE", "/admin/api/tokens/../a", { token: ADMIN })).status, 404);
});

test("two tokens isolate same-named backups, etags, probes, lists, and deletion", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const a = await createToken(env, "A");
  const b = await createToken(env, "B");
  const name = "/v1/objects/kiko-vault.json";
  await call(env, "PUT", name, { token: TOKEN, body: "legacy-cipher" });
  const written = await call(env, "PUT", name, { token: a.token, body: "A-cipher" });
  const etagA = written.headers.get("ETag");
  assert.equal((await call(env, "GET", name, { token: b.token })).status, 404);
  assert.equal((await call(env, "PUT", name, { token: b.token, body: "B-cipher", headers: { "If-Match": etagA } })).status, 412);
  assert.equal((await call(env, "PUT", name, { token: b.token, body: "B-cipher", headers: { "If-None-Match": "*" } })).status, 201);
  assert.equal(await (await call(env, "GET", name, { token: a.token })).text(), "A-cipher");
  assert.equal(await (await call(env, "GET", name, { token: b.token })).text(), "B-cipher");
  assert.equal(await (await call(env, "GET", name, { token: TOKEN })).text(), "legacy-cipher");
  await call(env, "PUT", "/v1/objects/kiko-write-test", { token: a.token, body: "ok" });
  const listA = await json(await call(env, "GET", "/v1/objects", { token: a.token }));
  const listB = await json(await call(env, "GET", "/v1/objects", { token: b.token }));
  assert.deepEqual(listA.keys.map(item => item.name), ["kiko-vault.json", "kiko-write-test"]);
  assert.deepEqual(listB.keys.map(item => item.name), ["kiko-vault.json"]);
  const legacy = await json(await call(env, "GET", "/v1/objects", { token: TOKEN }));
  assert.deepEqual(legacy.keys.map(item => item.name), ["kiko-vault.json"]);
  for (const wanted of ["../../sbcfg/", "../../sbcfg-admin/", "../../sbcfg-users/" + b.id + "/"]) {
    assert.deepEqual((await json(await call(env, "GET", "/v1/objects?prefix=" + encodeURIComponent(wanted), { token: a.token }))).keys, []);
  }
  await call(env, "DELETE", name, { token: a.token });
  assert.equal((await call(env, "GET", name, { token: a.token })).status, 404);
  assert.equal(await (await call(env, "GET", name, { token: b.token })).text(), "B-cipher");
  assert.equal(await (await call(env, "GET", name, { token: TOKEN })).text(), "legacy-cipher");
});

test("revocation blocks every data operation while retaining backups and other users", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const a = await createToken(env, "A");
  const b = await createToken(env, "B");
  await call(env, "PUT", "/v1/objects/a", { token: a.token, body: "cipher" });
  const revoked = await json(await call(env, "DELETE", "/admin/api/tokens/" + a.id, { token: ADMIN }));
  assert.ok(revoked.revokedAt > 0);
  assert.ok(env.SYNC_KV.store.has("sbcfg-users/" + a.id + "/a"));
  for (const method of ["GET", "PUT", "DELETE"]) {
    assert.equal((await call(env, method, "/v1/objects/a", { token: a.token, body: method === "PUT" ? "new" : undefined })).status, 401);
  }
  assert.equal((await call(env, "GET", "/v1/objects", { token: a.token })).status, 401);
  assert.equal((await call(env, "GET", "/v1/objects", { token: b.token })).status, 200);
  assert.equal((await call(env, "GET", "/v1/objects", { token: TOKEN })).status, 200);
  assert.equal((await call(env, "DELETE", "/admin/api/tokens/" + a.id, { token: ADMIN })).status, 200);
  assert.equal((await call(env, "DELETE", "/admin/api/tokens/" + "0".repeat(64), { token: ADMIN })).status, 404);
});

test("multi-user mode works with only an admin key configured", async () => {
  const env = makeEnv({ SYNC_TOKEN: undefined, ADMIN_TOKEN: ADMIN });
  const a = await createToken(env, "A");
  assert.equal((await call(env, "PUT", "/v1/objects/a", { token: a.token, body: "cipher" })).status, 201);
  assert.equal(await (await call(env, "GET", "/v1/objects/a", { token: a.token })).text(), "cipher");
  assert.equal((await call(env, "GET", "/v1/objects", { token: "sbcfg_" + "0".repeat(64) })).status, 401);
});

test("management API needs its dedicated secret when only legacy sync is configured", async () => {
  const response = await call(makeEnv(), "GET", "/admin/api/tokens", { token: TOKEN });
  assert.equal(response.status, 503);
  assert.match((await json(response)).error, /ADMIN_TOKEN/);
});

test("management storage statistics follow writes and deletes while isolating tenants and legacy data", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const a = await createToken(env, "A");
  const b = await createToken(env, "B");
  assert.deepEqual(a.storage, { objectCount: 0, totalBytes: 0, unknownSizeCount: 0, lastUpdatedAt: null });
  const detailPath = "/admin/api/tokens/" + a.id + "/objects";
  assert.deepEqual((await json(await call(env, "GET", detailPath, { token: ADMIN }))).objects, []);
  await call(env, "PUT", "/v1/objects/vault.json", { token: a.token, body: "密文" });
  const updated = await call(env, "PUT", "/v1/objects/vault.json", { token: a.token, body: "更新密文" });
  await call(env, "PUT", "/v1/objects/backup.json", { token: a.token, body: "abc" });
  await call(env, "PUT", "/v1/objects/vault.json", { token: b.token, body: "b" });
  await call(env, "PUT", "/v1/objects/legacy.json", { body: "legacy" });
  await env.SYNC_KV.put("sbcfg-users/" + "0".repeat(64) + "/orphan", "unknown owner", { metadata: { size: 13 } });
  const listed = await json(await call(env, "GET", "/admin/api/tokens", { token: ADMIN }));
  const stats = listed.tokens.find(token => token.id === a.id).storage;
  assert.equal(stats.objectCount, 2);
  assert.equal(stats.totalBytes, 15); // UTF-8 ciphertext bytes, and an overwrite counts once.
  assert.equal(stats.unknownSizeCount, 0);
  assert.ok(stats.lastUpdatedAt > 0);
  assert.equal(listed.tokens.find(token => token.id === b.id).storage.totalBytes, 1);
  const response = await call(env, "GET", detailPath, { token: ADMIN });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  const detail = await json(response);
  assert.deepEqual(detail.storage, stats);
  assert.deepEqual(detail.objects.map(object => object.name), ["backup.json", "vault.json"]);
  assert.equal(detail.objects[1].etag, updated.headers.get("ETag").slice(1, -1));
  assert.ok(!JSON.stringify(detail).includes("更新密文"));
  assert.equal((await call(env, "POST", detailPath, { token: ADMIN })).status, 405);
  assert.equal((await call(env, "GET", "/admin/api/tokens/" + "0".repeat(64) + "/objects", { token: ADMIN })).status, 404);
  await call(env, "DELETE", "/v1/objects/backup.json", { token: a.token });
  const revoked = await json(await call(env, "DELETE", "/admin/api/tokens/" + a.id, { token: ADMIN }));
  assert.equal(revoked.storage.objectCount, 1);
  assert.equal(revoked.storage.totalBytes, 12);
  const retained = await json(await call(env, "GET", detailPath, { token: ADMIN }));
  assert.deepEqual(retained.storage, revoked.storage);
  assert.deepEqual(retained.objects.map(object => object.name), ["vault.json"]);
});

test("storage reports paginate across empty pages and mark missing metadata as unknown", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const a = await createToken(env, "A");
  await env.SYNC_KV.put("sbcfg-users/" + a.id + "/old.json", "old");
  await call(env, "PUT", "/v1/objects/empty.json", { token: a.token, body: "" });
  await call(env, "PUT", "/v1/objects/new.json", { token: a.token, body: "1234" });
  const list = env.SYNC_KV.list;
  env.SYNC_KV.list = async ({ prefix, cursor }) => {
    if (cursor === undefined) return { keys: [], list_complete: false, cursor: "0" };
    const all = (await list({ prefix })).keys;
    const offset = Number(cursor);
    return { keys: all.slice(offset, offset + 1), list_complete: offset + 1 >= all.length, cursor: String(offset + 1) };
  };
  const report = await json(await call(env, "GET", "/admin/api/tokens", { token: ADMIN }));
  const storage = report.tokens[0].storage;
  assert.equal(storage.objectCount, 3);
  assert.equal(storage.totalBytes, 4);
  assert.equal(storage.unknownSizeCount, 1);
  const detail = await json(await call(env, "GET", "/admin/api/tokens/" + a.id + "/objects", { token: ADMIN }));
  assert.deepEqual(detail.storage, storage);
  assert.deepEqual(detail.objects.find(object => object.name === "old.json"), { name: "old.json", size: null, etag: "", updatedAt: null });
  assert.equal(detail.objects.find(object => object.name === "empty.json").size, 0);
});

// ---- Configuration sharing ----

const SHARE_KEY = "A".repeat(42) + "B";
const OTHER_KEY = "x".repeat(43);
const nthKey = i => String(i).padStart(43, "k");
const CONFIG = JSON.stringify({ profiles: [{ name: "家" }] });
const upload = (env, key, body, { headers = {}, token = null } = {}) =>
  call(env, "PUT", `/api/share/${key}`, { token, body, headers: { "Content-Type": "application/json", ...headers } });
const shareKeys = env => [...env.SYNC_KV.store.keys()].filter(key => key.startsWith("kiko-shares/"));

// Replaces global fetch with a fake Turnstile siteverify endpoint for the duration of fn.
async function withSiteverify(answer, fn) {
  const original = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: new URLSearchParams(init.body) });
    const form = new URLSearchParams(init.body);
    return new Response(JSON.stringify(answer(form)), { headers: { "Content-Type": "application/json" } });
  };
  try { return await fn(calls); } finally { globalThis.fetch = original; }
}

async function withClock(fn) {
  const original = Date.now;
  let now = original();
  Date.now = () => now;
  try { return await fn(ms => { now += ms; }); } finally { Date.now = original; }
}

const turnstileEnv = extra => makeEnv({ ADMIN_TOKEN: ADMIN, TURNSTILE_SITE_KEY: "site-key", TURNSTILE_SECRET_KEY: "secret-key", ...extra });
const goodAnswer = form => ({ success: form.get("response") === "good", hostname: "w.example", action: "kiko_config_share" });

test("share: Turnstile off accepts sync tokens and the admin key, then serves the file by link", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const user = await createToken(env, "A");
  const anonymous = await upload(env, SHARE_KEY, CONFIG);
  assert.equal(anonymous.status, 401);
  assert.match((await json(anonymous)).error, /访问令牌/);
  assert.equal((await upload(env, SHARE_KEY, CONFIG, { token: "wrong" })).status, 401);
  assert.equal((await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "anything" } })).status, 401);
  assert.equal(shareKeys(env).length, 0);

  const created = await upload(env, SHARE_KEY, CONFIG, { token: user.token });
  assert.equal(created.status, 201);
  const body = await json(created);
  assert.equal(body.uploaded, true);
  assert.equal(body.url, "https://w.example/" + SHARE_KEY);
  assert.equal((await upload(env, OTHER_KEY, CONFIG, { token: ADMIN })).status, 201);

  const download = await call(env, "GET", "/" + SHARE_KEY, { token: null });
  assert.equal(download.status, 200);
  assert.equal(await download.text(), CONFIG);
  assert.match(download.headers.get("Content-Type"), /application\/json/);
  assert.match(download.headers.get("Content-Disposition"), /attachment; filename="config\.kiko\.json"/);
  assert.equal(download.headers.get("Cache-Control"), "no-store");

  // A share is immutable: a repeated PUT is refused for every credential and the stored file is unchanged.
  const replaced = JSON.stringify({ v: 2 });
  for (const token of [user.token, ADMIN, TOKEN]) {
    const again = await upload(env, SHARE_KEY, replaced, { token });
    assert.equal(again.status, 409);
    assert.match((await json(again)).error, /已存在/);
  }
  assert.equal(await (await call(env, "GET", "/" + SHARE_KEY, { token: null })).text(), CONFIG);
  assert.equal((await call(env, "GET", "/" + "Z".repeat(43), { token: null })).status, 404);
});

test("share: Turnstile off without any secret refuses uploads", async () => {
  const env = makeEnv({ SYNC_TOKEN: undefined });
  const response = await upload(env, SHARE_KEY, CONFIG);
  assert.equal(response.status, 503);
  assert.equal(shareKeys(env).length, 0);
});

test("share: Turnstile on requires a verified token with the right hostname and action", async () => {
  const env = turnstileEnv();
  await withSiteverify(goodAnswer, async calls => {
    assert.equal((await upload(env, SHARE_KEY, CONFIG)).status, 400);
    assert.equal((await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "bad" } })).status, 403);
    assert.equal(shareKeys(env).length, 0);
    const ok = await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "good", "CF-Connecting-IP": "203.0.113.9" } });
    assert.equal(ok.status, 201);
    const last = calls.at(-1);
    assert.equal(last.url, "https://challenges.cloudflare.com/turnstile/v0/siteverify");
    assert.equal(last.body.get("secret"), "secret-key");
    assert.equal(last.body.get("remoteip"), "203.0.113.9");
    // An authorized credential skips the challenge.
    const before = calls.length;
    assert.equal((await upload(env, OTHER_KEY, CONFIG, { token: ADMIN })).status, 201);
    assert.equal(calls.length, before);
  });
  await withSiteverify(() => ({ success: true, hostname: "evil.example", action: "kiko_config_share" }), async () => {
    assert.equal((await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "good" } })).status, 403);
  });
  await withSiteverify(() => ({ success: true, hostname: "w.example", action: "other" }), async () => {
    assert.equal((await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "good" } })).status, 403);
  });
  await withSiteverify(() => ({ success: true, hostname: "share.example.org", action: "kiko_config_share" }), async () => {
    const custom = turnstileEnv({ TURNSTILE_HOSTNAMES: "example.org" });
    assert.equal((await upload(custom, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "good" } })).status, 201);
  });
  const original = globalThis.fetch;
  globalThis.fetch = async () => { throw new Error("offline"); };
  try {
    assert.equal((await upload(env, SHARE_KEY, CONFIG, { headers: { "X-Turnstile-Token": "good" } })).status, 502);
  } finally { globalThis.fetch = original; }
});

test("share: a half Turnstile configuration refuses uploads and the page reflects the mode", async () => {
  const half = makeEnv({ ADMIN_TOKEN: ADMIN, TURNSTILE_SECRET_KEY: "secret-key" });
  assert.equal((await upload(half, SHARE_KEY, CONFIG, { token: ADMIN })).status, 503);

  const on = await call(turnstileEnv(), "GET", "/share-upload", { token: null });
  assert.equal(on.status, 200);
  const onCSP = on.headers.get("Content-Security-Policy");
  assert.match(onCSP, /script-src 'nonce-[0-9a-f]+' https:\/\/challenges\.cloudflare\.com/);
  assert.match(onCSP, /frame-src https:\/\/challenges\.cloudflare\.com/);
  assert.match(onCSP, /frame-ancestors 'none'/);
  const onHTML = await on.text();
  assert.match(onHTML, /"siteKey":"site-key"/);
  assert.ok(!onHTML.includes("secret-key") && !onHTML.includes(ADMIN));
  assert.ok(!onHTML.includes("__CSP_NONCE__") && !onHTML.includes("__SHARE_CONFIG__"));

  const off = await call(makeEnv({ ADMIN_TOKEN: ADMIN }), "GET", "/share-upload", { token: null });
  assert.doesNotMatch(off.headers.get("Content-Security-Policy"), /challenges\.cloudflare\.com/);
  const offHTML = await off.text();
  assert.match(offHTML, /"siteKey":""/);
  assert.match(offHTML, /上传配置/);
  assert.ok(!offHTML.includes(TOKEN));
  assert.equal((await call(makeEnv(), "POST", "/share-upload", { token: null })).status, 405);
});

test("share: enforces the 2 MB limit, JSON objects and valid keys", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const pad = size => JSON.stringify({ p: "x".repeat(size - 8) });
  assert.equal(pad(2_000_000).length, 2_000_000);
  assert.equal((await upload(env, SHARE_KEY, pad(2_000_001), { token: ADMIN })).status, 413);
  assert.equal(shareKeys(env).length, 0);
  assert.equal((await upload(env, SHARE_KEY, pad(2_000_000), { token: ADMIN })).status, 201);
  for (const body of ["[]", "null", "{", "\"x\""]) {
    assert.equal((await upload(env, OTHER_KEY, body, { token: ADMIN })).status, 400, body);
  }
  assert.equal((await call(env, "PUT", `/api/share/${OTHER_KEY}`, { token: ADMIN, body: CONFIG, headers: { "Content-Type": "text/plain" } })).status, 415);
  for (const key of ["short", "A".repeat(44), "A".repeat(42) + "%2B"]) {
    assert.equal((await upload(env, key, CONFIG, { token: ADMIN })).status, 404, key);
  }
  assert.equal((await call(env, "GET", `/api/share/${SHARE_KEY}`, { token: ADMIN })).status, 405);
});

test("share: rate limits each address to 5 uploads per minute", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  await withClock(async advance => {
    const from = ip => ({ token: ADMIN, headers: { "CF-Connecting-IP": ip } });
    for (let i = 0; i < 5; i++) assert.equal((await upload(env, nthKey(i), CONFIG, from("198.51.100.1"))).status, 201);
    // A refused duplicate is answered before the quota and does not use it up.
    assert.equal((await upload(env, nthKey(0), CONFIG, from("198.51.100.2"))).status, 409);
    const limited = await upload(env, nthKey(5), CONFIG, from("198.51.100.1"));
    assert.equal(limited.status, 429);
    assert.match((await json(limited)).error, /5 次/);
    assert.equal((await upload(env, nthKey(6), CONFIG, from("198.51.100.2"))).status, 201);
    const counters = [...env.SYNC_KV.store.entries()].filter(([key]) => key.startsWith("kiko-share-rate/"));
    assert.ok(counters.length >= 2);
    assert.ok(counters.every(([key, entry]) => entry.ttl === 120 && !key.includes("198.51.100")));
    advance(60_000);
    assert.equal((await upload(env, nthKey(7), CONFIG, from("198.51.100.1"))).status, 201);
  });
});

test("share: SHARE_TTL_DAYS expires shares through KV expirationTtl", async () => {
  await withClock(async advance => {
    const forever = makeEnv({ ADMIN_TOKEN: ADMIN });
    await upload(forever, SHARE_KEY, CONFIG, { token: ADMIN });
    assert.equal(forever.SYNC_KV.store.get("kiko-shares/" + SHARE_KEY).ttl, null);

    const env = makeEnv({ ADMIN_TOKEN: ADMIN, SHARE_TTL_DAYS: "7" });
    const created = await json(await upload(env, SHARE_KEY, CONFIG, { token: ADMIN }));
    const entry = env.SYNC_KV.store.get("kiko-shares/" + SHARE_KEY);
    assert.equal(entry.ttl, 7 * 86400);
    assert.equal(entry.metadata.expiresAt, created.expiresAt);
    advance(7 * 86400 * 1000 - 1000);
    assert.equal((await call(env, "GET", "/" + SHARE_KEY, { token: null })).status, 200);
    advance(1000);
    const gone = await call(env, "GET", "/" + SHARE_KEY, { token: null });
    assert.equal(gone.status, 404);
    assert.match((await json(gone)).error, /过期/);

    // Expired metadata is honored even while KV has not removed the value yet.
    await env.SYNC_KV.put("kiko-shares/" + OTHER_KEY, CONFIG, { metadata: { uploadedAt: 1, size: 2, expiresAt: Date.now() - 1 } });
    assert.equal((await call(env, "GET", "/" + OTHER_KEY, { token: null })).status, 404);
  });
});

test("share: CORS allows same-origin, configured and native requests only", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN, SHARE_ALLOWED_ORIGINS: "https://app.example" });
  const preflight = await call(env, "OPTIONS", `/api/share/${SHARE_KEY}`, { token: null, headers: { Origin: "https://w.example" } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get("Access-Control-Allow-Origin"), "https://w.example");
  assert.match(preflight.headers.get("Access-Control-Allow-Headers"), /X-Turnstile-Token/);
  assert.equal((await upload(env, SHARE_KEY, CONFIG, { token: ADMIN, headers: { Origin: "https://app.example" } })).status, 201);
  const foreign = await upload(env, SHARE_KEY, CONFIG, { token: ADMIN, headers: { Origin: "https://evil.example" } });
  assert.equal(foreign.status, 403);
  assert.equal(foreign.headers.get("Access-Control-Allow-Origin"), null);
});

test("share: admin lists and deletes shares with ADMIN_TOKEN only", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const user = await createToken(env, "A");
  await upload(env, SHARE_KEY, CONFIG, { token: ADMIN });
  for (const token of [null, "wrong", TOKEN, user.token]) {
    assert.equal((await call(env, "GET", "/api/admin/shares", { token })).status, 401);
    assert.equal((await call(env, "DELETE", "/api/admin/shares/" + SHARE_KEY, { token })).status, 401);
  }
  const listed = await json(await call(env, "GET", "/api/admin/shares", { token: ADMIN }));
  assert.equal(listed.complete, true);
  assert.deepEqual(listed.items.map(item => item.key), [SHARE_KEY]);
  assert.equal(listed.items[0].size, CONFIG.length + 2); // "家" is three UTF-8 bytes.
  assert.ok(listed.items[0].uploadedAt > 0);
  assert.equal(listed.items[0].expiresAt, null);
  assert.equal((await call(env, "DELETE", "/api/admin/shares/bad", { token: ADMIN })).status, 400);
  assert.equal((await call(env, "DELETE", "/api/admin/shares/" + SHARE_KEY, { token: ADMIN })).status, 200);
  assert.equal((await call(env, "GET", "/" + SHARE_KEY, { token: null })).status, 404);
  assert.equal((await call(makeEnv(), "GET", "/api/admin/shares", { token: TOKEN })).status, 503);

  const page = await call(env, "GET", "/share-admin", { token: null });
  assert.equal(page.status, 200);
  assert.match(await page.text(), /分享管理/);
  const redirect = await call(env, "GET", "/manage", { token: null });
  assert.equal(redirect.status, 308);
  assert.equal(redirect.headers.get("Location"), "https://w.example/share-admin");
});

test("share and sync routes do not interfere", async () => {
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const user = await createToken(env, "A");
  await call(env, "PUT", "/v1/objects/kiko-vault.json", { token: user.token, body: "cipher" });
  await call(env, "PUT", "/v1/objects/legacy.json", { body: "legacy" });
  await upload(env, SHARE_KEY, CONFIG, { token: user.token });

  // Shares never show up in, or count towards, sync listings and storage statistics.
  assert.deepEqual((await json(await call(env, "GET", "/v1/objects", { token: user.token }))).keys.map(item => item.name), ["kiko-vault.json"]);
  assert.deepEqual((await json(await call(env, "GET", "/v1/objects", { token: TOKEN }))).keys.map(item => item.name), ["legacy.json"]);
  const tokens = await json(await call(env, "GET", "/admin/api/tokens", { token: ADMIN }));
  assert.equal(tokens.tokens[0].storage.objectCount, 1);
  // Sync objects are not reachable through share links or the share admin list.
  const shares = await json(await call(env, "GET", "/api/admin/shares", { token: ADMIN }));
  assert.deepEqual(shares.items.map(item => item.key), [SHARE_KEY]);
  assert.equal((await call(env, "GET", "/v1/objects/" + SHARE_KEY, { token: user.token })).status, 404);
  // A 43-character sync object name stays a sync object, and other unknown paths still require auth.
  assert.equal((await call(env, "PUT", "/v1/objects/" + OTHER_KEY, { token: user.token, body: "c" })).status, 201);
  assert.equal((await call(env, "GET", "/" + OTHER_KEY, { token: null })).status, 404);
  assert.equal((await call(env, "GET", "/v1/objects/kiko-vault.json", { token: null })).status, 401);
  assert.equal((await call(env, "GET", "/unknown", { token: null })).status, 401);
  // Sync CORS is unchanged.
  const preflight = await call(env, "OPTIONS", "/v1/objects/a", { token: null });
  assert.equal(preflight.headers.get("Access-Control-Allow-Origin"), "*");
  // Deleting the share leaves sync data in place.
  await call(env, "DELETE", "/api/admin/shares/" + SHARE_KEY, { token: ADMIN });
  assert.equal(await (await call(env, "GET", "/v1/objects/kiko-vault.json", { token: user.token })).text(), "cipher");
});

test("copied single-file worker includes sharing", async () => {
  const { readFile, writeFile, mkdtemp } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const path = await import("node:path");
  const read = name => readFile(new URL(name, import.meta.url), "utf8");
  const local = source => source.replaceAll("export const ", "const ");
  // Mirrors embed.go's inlining of local modules.
  const adminPage = await read("./admin-page.js");
  const sharePages = await read("./share-pages.js");
  const source = (await read("./worker.js"))
    .replace('import { ADMIN_HTML } from "./admin-page.js";', () => local(adminPage))
    .replace('import { SHARE_UPLOAD_HTML, SHARE_ADMIN_HTML } from "./share-pages.js";', () => local(sharePages));
  assert.ok(!/^import /m.test(source));
  const dir = await mkdtemp(path.join(tmpdir(), "kiko-worker-"));
  const file = path.join(dir, "worker.mjs");
  await writeFile(file, source);
  const single = (await import(file)).default;
  const env = makeEnv({ ADMIN_TOKEN: ADMIN });
  const response = await single.fetch(new Request(`https://w.example/api/share/${SHARE_KEY}`, {
    method: "PUT", body: CONFIG, headers: { "Content-Type": "application/json", Authorization: `Bearer ${ADMIN}` },
  }), env);
  assert.equal(response.status, 201);
  assert.equal((await single.fetch(new Request("https://w.example/share-upload"), env)).status, 200);
});
