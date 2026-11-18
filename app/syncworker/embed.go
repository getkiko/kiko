// Package syncworker embeds the source and deployment instructions of the user-deployed Cloudflare KV sync Worker.
package syncworker

import (
	_ "embed"
	"strings"
)

//go:embed worker.js
var workerSource string

//go:embed admin-page.js
var adminPageSource string

//go:embed share-pages.js
var sharePagesSource string

// Source is self-contained so the UI's copy action also works in the Cloudflare editor:
// each local module import is replaced by the module's body with its exports made local.
var Source = inline(inline(workerSource,
	`import { ADMIN_HTML } from "./admin-page.js";`, adminPageSource),
	`import { SHARE_UPLOAD_HTML, SHARE_ADMIN_HTML } from "./share-pages.js";`, sharePagesSource)

func inline(source, importLine, module string) string {
	if !strings.Contains(source, importLine) {
		panic("syncworker: missing import " + importLine)
	}
	return strings.Replace(source, importLine, strings.ReplaceAll(module, "export const ", "const "), 1)
}

//go:embed README.md
var Readme string

//go:embed wrangler.jsonc.example
var WranglerExample string

// Commands returns the deployment commands shown to the user. They contain no secrets;
// the user generates the token and enters it via wrangler secret.
func Commands(workerName string) []string {
	workerName = strings.TrimSpace(workerName)
	if workerName == "" {
		workerName = "kiko-sync"
	}
	return []string{
		"wrangler kv namespace create KIKO_SYNC",
		"openssl rand -hex 32",
		"wrangler secret put ADMIN_TOKEN --name " + workerName,
		"wrangler deploy",
	}
}
