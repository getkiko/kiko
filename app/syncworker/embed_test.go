package syncworker

import (
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	for name, value := range map[string]string{"Source": Source, "Readme": Readme, "WranglerExample": WranglerExample} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if !strings.Contains(Source, "SYNC_TOKEN") || !strings.Contains(WranglerExample, "SYNC_KV") {
		t.Fatal("embedded worker and example must reference the bindings")
	}
	if strings.Contains(Source, "import ") || strings.Contains(Source, "export const") || !strings.Contains(Source, "const ADMIN_HTML =") {
		t.Fatal("copied worker source must include its management page without local imports")
	}
	for _, want := range []string{"const SHARE_UPLOAD_HTML =", "const SHARE_ADMIN_HTML =", "const SHARE_STYLE =", "/api/share/", "TURNSTILE_SECRET_KEY"} {
		if !strings.Contains(Source, want) {
			t.Fatalf("copied worker source must include configuration sharing: missing %q", want)
		}
	}
	if !strings.Contains(Source, "export default {") {
		t.Fatal("copied worker source must keep its default export")
	}
}

func TestCommands(t *testing.T) {
	commands := Commands("")
	if len(commands) != 4 || !slices.Contains(commands, "wrangler deploy") {
		t.Fatalf("commands=%v", commands)
	}
	if got := Commands(" my-worker ")[2]; !strings.HasSuffix(got, "--name my-worker") {
		t.Fatalf("secret command=%q", got)
	}
	for _, command := range commands {
		if strings.Contains(command, "tskey") || strings.Contains(strings.ToLower(command), "token=") {
			t.Fatalf("command must not embed secrets: %q", command)
		}
	}
}
