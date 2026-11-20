package overview

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestLiveAppliedMessage(t *testing.T) {
	cases := []struct {
		outcome store.InterruptResult
		want    string
	}{
		{store.InterruptResult{}, "已应用"},
		{store.InterruptResult{Closed: 3}, "已应用，已中断 3 条连接"},
		{store.InterruptResult{Closed: 3, Error: "核心未响应"}, "已应用，中断连接失败：核心未响应"},
	}
	for _, c := range cases {
		if got := liveAppliedMessage(c.outcome); got != c.want {
			t.Errorf("%+v: %q, want %q", c.outcome, got, c.want)
		}
	}
}

// Starting the core and reloading it restart its services, which closes the
// connections: the marks are spent.
func TestStartAndReloadClearTheMarks(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "已停止")
	f.store.SetInterrupt("rr-1", "a", "b", true)
	f.click(t, "启动")
	testkit.Until(t, f.tt, func() bool { return len(f.store.InterruptRules()) == 0 })

	f.store.SetInterrupt("rr-1", "a", "b", true)
	f.w.reload()
	testkit.Until(t, f.tt, func() bool { return len(f.store.InterruptRules()) == 0 })
}
