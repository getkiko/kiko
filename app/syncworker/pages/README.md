# Deploying the Kiko sync backend to pages.dev

This guide adds a `https://<project>.pages.dev` address in front of an existing sync Worker, for devices that can reach Cloudflare Pages more reliably than `workers.dev`. The KV namespace, secrets, encrypted backups, share links and API paths all stay the same. The Worker provides the admin page, multi-user tokens and configuration sharing.

Request flow:

```text
Kiko → https://<project>.pages.dev → Pages Functions
     → Cloudflare internal service binding → kiko-sync Worker → KV
```

Pages Functions forward every request through a service binding, so the Worker still handles authentication and KV access. After deploying, update the sync address on each device.

## 1. Prepare the sync Worker

You need a deployed sync Worker, for example `kiko-sync`, with `SYNC_KV` configured. Current versions use an `ADMIN_TOKEN` admin key and access tokens created in the admin page; older deployments can keep using `SYNC_TOKEN`. If you have not deployed the Worker yet, follow the [sync Worker guide](../README.md) first.

The Pages project and the Worker must be in the same Cloudflare account. Keep the access token you use in Kiko at hand; you will enter it again later.

## 2. Install and sign in to Wrangler

With Node.js installed:

```sh
npm install -g wrangler@latest
wrangler login
wrangler whoami
```

Check that you are signed in to the account that owns the Worker, then check that the Worker exists:

```sh
wrangler deployments list --name kiko-sync
wrangler secret list --name kiko-sync
```

The second command should list `ADMIN_TOKEN` or `SYNC_TOKEN` (names only).

## 3. Prepare the Pages files

In this repository, from the root:

```sh
cd app/syncworker/pages
```

Or create a directory with these three files:

```text
pages/
├── wrangler.jsonc
├── functions/
│   └── [[path]].js
└── public/
    └── _routes.json
```

`wrangler.jsonc`:

```json
{
  "name": "kiko-sync",
  "pages_build_output_dir": "./public",
  "compatibility_date": "2026-10-10",
  "services": [
    { "binding": "SYNC_BACKEND", "service": "kiko-sync" }
  ]
}
```

`name` is the new Pages project name; `services[0].service` is the name of the existing Worker. They can differ, for example a Pages project `my-kiko-sync` in front of the Worker `kiko-sync`. `--project-name` in the commands below must match the Pages project name.

`functions/[[path]].js`:

```js
export function onRequest({ request, env }) {
  return env.SYNC_BACKEND.fetch(request);
}
```

`public/_routes.json`:

```json
{
  "version": 1,
  "include": ["/*"],
  "exclude": []
}
```

Secrets and access tokens stay with the Worker; Pages only hands requests to it.

## 4. Create the project and deploy

From the `pages` directory:

```sh
wrangler pages project create kiko-sync --production-branch main
wrangler pages deploy public --project-name kiko-sync --branch main --commit-dirty=true
```

If the project already exists, run only the deploy command. `--branch main` publishes to the production branch.

Wrangler prints a deployment URL such as `https://<deployment-id>.kiko-sync.pages.dev`. Look up the project's stable domain with:

```sh
wrangler pages project list
```

Use the stable project domain, for example `https://kiko-sync.pages.dev`, for sync. Cloudflare may assign a domain with a suffix; use whatever it shows.

## 5. Verify from the target device

Replace the address with your own Pages domain and run this on a device that runs Kiko:

```sh
curl --connect-timeout 5 --max-time 15 -i \
  https://kiko-sync.pages.dev/v1/objects/kiko-vault.json
```

The request carries no credentials, so the expected answer is HTTP `401` with `{"error":"unauthorized"}`. That shows the device can reach Pages and Pages is calling the Worker's authentication.

## 6. Configure Kiko

Open the Pages root address (for example `https://kiko-sync.pages.dev`), sign in with the admin key, and create an access token for each user. Every token has its own backup space; all devices of one user share one token. The admin page can also view, copy and revoke tokens.

In Kiko, open **Settings › Backup & Sharing › Encrypted backup & sync** and choose Cloudflare KV:

1. Enter the Pages domain as the address, for example `https://kiko-sync.pages.dev`. The domain alone is enough; no path is needed.
2. Enter the access token created in the admin page. Existing clients can keep their previous token to keep using their existing backup space.
3. Click **Test connection** and save once it succeeds.
4. Unlock with your backup password, then sync.

Repeat these steps on every device. Devices using the same access token share that token's backups.

For configuration sharing, enter the same Pages domain under **Settings › Backup & Sharing › Configuration share address**. Uploads go through `https://kiko-sync.pages.dev/share-upload` and links look like `https://kiko-sync.pages.dev/<key>`. If you use Turnstile, add the Pages domain to the widget's hostnames. See [Sharing](../README.md#sharing).

## Updating

After changing the Pages entry point, redeploy from the `pages` directory:

```sh
wrangler pages deploy public --project-name kiko-sync --branch main --commit-dirty=true
```

After changing the sync logic, run `wrangler deploy` in the Worker's directory. The service binding always calls the Worker's published version.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| Deploy says the bound service does not exist | Check that `services[0].service` matches the Worker name and that Wrangler is signed in to the right account. |
| Project creation says the name is taken | Run `wrangler pages project list`; deploy to your existing project, or pick another name. |
| Requests return `unauthorized` | Test from Kiko with the access token; `401` in a browser without a token is expected. |
| `SYNC_TOKEN is not configured` | Set `SYNC_TOKEN` on the Worker and use the same token in Kiko. |
| The admin page asks for `ADMIN_TOKEN` | Run `wrangler secret put ADMIN_TOKEN --name kiko-sync`. |
| A newly created token fails to authenticate | KV changes take time to propagate; test again later. |
| Share upload says verification failed | Add the Pages domain to the Turnstile widget's hostnames, or list it in `TURNSTILE_HOSTNAMES`. |
| The device times out reaching Pages | Run the `curl` from step 5 on that device and check DNS and HTTPS reachability of the Pages domain. |
