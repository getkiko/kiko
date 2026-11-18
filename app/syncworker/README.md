# Kiko sync and share backend (Cloudflare KV)

A self-hosted Cloudflare Worker that provides two services from one deployment and one KV namespace:

- **Encrypted sync**: a multi-user backend that stores Kiko's encrypted configuration backups. For sync the Worker only ever sees ciphertext encrypted by the client; it cannot read your configuration.
- **Configuration sharing**: public share links for Kiko configuration files (Overview › current configuration › ⋯ › File › Share current configuration). Shared files are stored in plain text and anyone with the link can download them.

Kiko does not ship a public sync or share service. Deploy this Worker to your own Cloudflare account and enter the same address in Kiko for both. (WebDAV and S3 sync backends are also supported and need no Worker.)

## Deploy

1. Install and sign in to Wrangler (requires Node.js):

   ```sh
   npm install -g wrangler
   wrangler login
   ```

2. Create a directory containing `worker.js`, `admin-page.js`, `share-pages.js` and the `wrangler.jsonc` below. In this repository all three source files are in this directory. Kiko's "Copy Worker code" action produces a single-file version with the admin and share pages built in; with that version you only need `worker.js`.

3. Create a KV namespace and put the `id` it prints into `kv_namespaces[0].id` in `wrangler.jsonc`:

   ```sh
   wrangler kv namespace create KIKO_SYNC
   ```

4. Generate an admin key, keep it somewhere safe, and set it as the Worker's `ADMIN_TOKEN`:

   ```sh
   openssl rand -hex 32
   wrangler secret put ADMIN_TOKEN
   ```

5. Optional, recommended when the share page is public: turn on [Cloudflare Turnstile](https://developers.cloudflare.com/turnstile/) for share uploads. In the Cloudflare dashboard create a Turnstile widget (Managed mode) for the Worker's hostname (and the Pages or custom domain, if you use one), then put the site key in `vars.TURNSTILE_SITE_KEY` in `wrangler.jsonc` and the secret key in a Worker secret:

   ```sh
   wrangler secret put TURNSTILE_SECRET_KEY
   ```

   See [Sharing](#sharing) for what changes with and without Turnstile.

6. Deploy:

   ```sh
   wrangler deploy
   ```

7. Open the Worker URL (or a Pages URL, see below), enter the admin key from step 4, and create an access token for each user.

8. In Kiko, open **Settings › Backup & Sharing**:
   - **Encrypted backup & sync**: choose Cloudflare KV, enter the backend's address (for example `https://kiko-sync.<account>.workers.dev`) and the access token created in the admin page, and click **Test connection**. All devices of one user share the same access token.
   - **Configuration share address**: enter the same address, origin only with no path (for example `https://kiko-sync.<account>.workers.dev`). Kiko opens `<address>/share-upload` in the browser when you share a configuration.

## Upgrading an existing deployment

1. Update the Worker with the new `worker.js`, `admin-page.js` and `share-pages.js` (or the single-file version), keeping the existing `SYNC_KV` binding. Sharing needs no new binding: it uses the same KV namespace under its own key prefixes.
2. Run `wrangler secret put ADMIN_TOKEN --name kiko-sync` to set a separate admin key, then `wrangler deploy`.
3. Open the existing Worker or Pages address and sign in with the admin key to create access tokens.

If `SYNC_TOKEN` is still set, the old token keeps access to the original `kiko/` backup space. Each new token gets its own empty space. Existing Pages projects that use a service binding call the updated Worker automatically.

## Managing access tokens

Open the backend's root URL or `/admin` and enter `ADMIN_TOKEN`:

- **Create**: enter a name to generate a random access token with its own storage space.
- **View / Copy**: tokens can be viewed and copied again later.
- **Storage**: each token shows its file count, total ciphertext size and last update. Expand the file details to see each file's name, size, update time and ETag; use **Refresh** to update.
- **Revoke**: removes sync access for that token and keeps its existing backups.
- **Sign out**: clears the admin key and token list from page memory; reloading requires signing in again.

The admin key manages tokens; access tokens are used by Kiko to sync. Every token maps to a separate space, and reads, writes, deletes, listings and connection tests are confined to it.

Access tokens are stored recoverably in the KV admin records so the admin page can show them again. Admin records and user backups use separate key prefixes, and the admin API requires the admin key. Uploaded configurations remain ciphertext.

Statistics come from KV list metadata, so configuration contents stay encrypted. One file can hold several configurations: the admin page counts stored objects, and only the client knows the configuration count after decrypting. Files without metadata show an unknown size or update time, and the total size only counts known sizes. Revoked tokens keep their files and statistics visible. KV list propagation delay also affects how quickly statistics refresh.

## Sharing

The share upload page is at `/share-upload`. Kiko saves the configuration file, opens that page in the browser, and you pick the file there. The page creates a random link `https://<address>/<43-character key>`; opening the link downloads the file as `config.kiko.json`.

How uploads are authorized depends on whether Turnstile is configured:

| Mode | Configuration | Who can upload |
| --- | --- | --- |
| Turnstile off (default) | no `TURNSTILE_*` variables | Only holders of a credential for this deployment: the upload page asks for a sync access token (created in the admin page), the legacy `SYNC_TOKEN`, or `ADMIN_TOKEN`. Suited to a personal deployment. |
| Turnstile on | `TURNSTILE_SITE_KEY` var **and** `TURNSTILE_SECRET_KEY` secret | Anyone who passes the Turnstile check on the upload page. Requests with a valid credential skip the check. Recommended when you give the share page to other people. |

Setting only one of the two Turnstile keys refuses all uploads until the other is set, so a half configuration never opens uploads silently.

In both modes:

- A file must be a JSON object of at most 2,000,000 bytes, sent as `application/json`.
- Each client IP may upload 5 times per UTC minute. The counter is best effort (KV has no atomic increment) and stores an HMAC of the IP, never the IP itself. Set `SHARE_IP_HASH_SECRET` to a random secret to use a dedicated HMAC key; otherwise `ADMIN_TOKEN` (or `SYNC_TOKEN`) is used.
- A share cannot be changed once uploaded: a `PUT` to an existing link returns `409`, whoever sends it, so a link holder can only download the file. To share a changed configuration, upload it again; the upload page creates a new link each time.
- Shares are kept until deleted. Set `vars.SHARE_TTL_DAYS` (1–3650) to have KV delete each share that many days after its upload.
- Browsers may call the upload API only from the Worker's own origin or from origins listed in `vars.SHARE_ALLOWED_ORIGINS` (space or comma separated). Requests without an `Origin` header, such as native clients, are accepted.

Optional Turnstile settings: `TURNSTILE_HOSTNAMES` (space-separated hostnames accepted in the Turnstile result; defaults to the hostname of the request, which is already right for workers.dev, Pages and custom domains) and `TURNSTILE_ACTION` (defaults to `kiko_config_share`; Cloudflare's test keys report `test`).

### Managing shares

Open `/share-admin` (or `/manage`) and enter `ADMIN_TOKEN` to list shares with their upload time and size, and to delete a share from the list or by pasting its link. Deleting a share is permanent. The sync admin page links to it.

## Example wrangler.jsonc

```jsonc
{
  "name": "kiko-sync",
  "main": "worker.js",
  "compatibility_date": "2026-10-10",
  "kv_namespaces": [
    { "binding": "SYNC_KV", "id": "<KV_NAMESPACE_ID>" }
  ],
  // Optional; remove what you do not use.
  "vars": {
    "TURNSTILE_SITE_KEY": "<TURNSTILE_SITE_KEY>",
    "SHARE_TTL_DAYS": "30"
  }
}
```

Secrets, set with `wrangler secret put <NAME>`:

| Name | Required | Purpose |
| --- | --- | --- |
| `ADMIN_TOKEN` | yes | Admin key for the sync and share admin pages; also authorizes share uploads |
| `TURNSTILE_SECRET_KEY` | with `TURNSTILE_SITE_KEY` | Turns on Turnstile for share uploads |
| `SHARE_IP_HASH_SECRET` | no | Dedicated HMAC key for the upload rate limit |
| `SYNC_TOKEN` | no | Legacy single-user sync token |

See also [wrangler.jsonc.example](wrangler.jsonc.example).

## Serving from pages.dev

An existing Worker can also be reached through a Cloudflare Pages address, which helps on networks where `workers.dev` is unreachable. Follow the [pages.dev deployment guide](pages/README.md). Pages forwards requests to the Worker through an internal service binding, so the same KV namespace, access tokens and share links keep working; use the Pages address as both the sync address and the share address.

## API

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/objects/:name` | Read an object; returns `ETag` |
| `PUT` | `/v1/objects/:name` | Write an object; supports `If-Match` / `If-None-Match: *`; 201 on create, 200 on update |
| `DELETE` | `/v1/objects/:name` | Delete an object |
| `GET` | `/v1/objects?prefix=` | List objects: `{keys:[{name,size,etag,updatedAt}]}` |

Sync requests need `Authorization: Bearer <access token>`; the legacy `SYNC_TOKEN` is still accepted for existing clients. `name` may contain only letters, digits, `.`, `_` and `-`, up to 128 characters. Objects are limited to 10 MiB.

Share routes:

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/share-upload` | none | Upload page |
| `PUT` | `/api/share/:key` | Turnstile token in `X-Turnstile-Token`, or `Authorization: Bearer <credential>` | Store a JSON configuration under a 43-character URL-safe key; `201 {"uploaded":true,"url":...,"expiresAt":...}` |
| `OPTIONS` | `/api/share/:key` | none | CORS preflight |
| `GET` | `/:key` | none | Download a shared configuration as an attachment |
| `GET` | `/share-admin`, `/manage` | none | Share admin page (`/manage` redirects) |
| `GET` | `/api/admin/shares?cursor=` | `ADMIN_TOKEN` | List shares, 50 per page: `{items:[{key,size,uploadedAt,expiresAt}],cursor,complete}` |
| `DELETE` | `/api/admin/shares/:key` | `ADMIN_TOKEN` | Delete a share |

Share errors are JSON `{"error":"..."}` with a message Kiko shows to the user.

The sync admin API requires `Authorization: Bearer <ADMIN_TOKEN>`:

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/admin/api/tokens` | List tokens, including their values |
| `GET` | `/admin/api/tokens/:id/objects` | File metadata and storage statistics for a token (revoked tokens included) |
| `POST` | `/admin/api/tokens` | Create a token from JSON `{"name":"user name"}` |
| `DELETE` | `/admin/api/tokens/:id` | Revoke a token, keeping its backups |

## Security and limits

- **End-to-end encryption**: configurations are encrypted in Kiko with your password before upload; the Worker and Cloudflare only see ciphertext. Tokens only protect access to the storage and cannot decrypt anything.
- **Access token scope**: a token can read, write and delete its own space. If one leaks, revoke it in the admin page; a new token gets a fresh space, and you can recreate the backup from a device. Rotate the legacy `SYNC_TOKEN` with `wrangler secret put SYNC_TOKEN`.
- **Admin key scope**: the admin key can view and create all access tokens. Rotating it with `wrangler secret put ADMIN_TOKEN` leaves existing access tokens valid.
- **Token propagation**: KV replication and read caching delay token creation, display and revocation, usually by tens of seconds and sometimes longer. If a new token fails to authenticate, test again later.
- **KV is eventually consistent**: reads in another region can lag writes by up to about 60 seconds. `If-Match` is best effort, not a true compare-and-swap; Kiko also checks the version stored in the file, and under extreme concurrency may create a conflict copy rather than lose data.
- A KV value can be up to 25 MiB; this Worker limits sync objects to 10 MiB and shared configurations to 2,000,000 bytes.
- **Shared configurations are not encrypted**: they are stored in plain text, and the link is the only credential. Check a configuration for passwords, keys and internal addresses before sharing it. A new upload can take up to about 60 seconds to be visible in other regions.
- **KV write quota**: each share upload costs two KV writes (the file and the rate counter). Cloudflare's free plan allows 1,000 KV writes per day, shared with sync.
- **KV key layout**: sync uses `sbcfg/`, `sbcfg-users/<token id>/` and `sbcfg-admin/tokens/` (names kept for compatibility with existing deployments); sharing uses `kiko-shares/<key>` and `kiko-share-rate/<ip hmac>/<minute>`. The prefixes never overlap, so sync listings and storage statistics never include shares.
- To restrict browser origins, set `vars.ALLOWED_ORIGIN` in `wrangler.jsonc`. The Kiko desktop app sends requests from its local server and is not affected.
