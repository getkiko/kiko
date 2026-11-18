package logs

import "testing"

// A level index from the server outside the known levels names no level
// instead of panicking or naming another.
func TestLevelNameOutOfRange(t *testing.T) {
	for level, want := range map[int]string{-1: "未知", 0: "PANIC", 4: "INFO", len(Levels): "未知"} {
		if got := levelName(level); got != want {
			t.Errorf("levelName(%d) = %q, want %q", level, got, want)
		}
	}
}
