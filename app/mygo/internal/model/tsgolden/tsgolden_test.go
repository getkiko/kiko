package tsgolden

import "testing"

// Recorded rewrites only the recorded side: Go output that still has the old
// wording does not compare equal to the rewritten recording.
func TestRecordedRewritesOnlyTheRecording(t *testing.T) {
	options := Options{Recorded: func(fn string, v any) any { return CoreWording(v) }}
	old := []any{"sing-box 将拒绝启动"}
	want := canonical("f", []any{"sing-box 将拒绝启动"}, nil, options, true)
	if got := canonical("f", old, nil, options, false); got == want {
		t.Fatalf("old wording in the Go output passed: %s", got)
	}
	if got := canonical("f", []any{"核心将拒绝启动"}, nil, options, false); got != want {
		t.Fatalf("current wording %s, want %s", got, want)
	}
}
