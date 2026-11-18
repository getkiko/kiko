package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestUsageHidesLegacyCoreFlag(t *testing.T) {
	fs := flag.NewFlagSet("kiko-server", flag.ContinueOnError)
	var binary string
	fs.StringVar(&binary, "core", "", "kiko-core absolute path (required)")
	fs.StringVar(&binary, legacyCoreFlag, "", "")
	fs.String("listen", "127.0.0.1:9099", "listen address")
	var out bytes.Buffer
	fs.SetOutput(&out)
	printUsage(fs)
	usage := out.String()
	if !strings.Contains(usage, "-core") || !strings.Contains(usage, `(default "127.0.0.1:9099")`) || strings.Contains(usage, "sing-box") {
		t.Fatalf("usage:\n%s", usage)
	}
	if err := fs.Parse([]string{"--sing-box", "/opt/kiko/kiko-core"}); err != nil || binary != "/opt/kiko/kiko-core" {
		t.Fatalf("legacy flag: %q %v", binary, err)
	}
}
