package overview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

func TestRuntimeDeadlines(t *testing.T) {
	if d := toggleDeadline(true); d.timeout != 5*time.Second || d.message != "停止请求超时（5 秒）" {
		t.Errorf("stop %+v", d)
	}
	if d := toggleDeadline(false); d.timeout != 20*time.Second || d.message != "启动超时（20 秒）" {
		t.Errorf("start %+v", d)
	}
	if d := reloadDeadline(); d.timeout != 45*time.Second || d.message != "配置重载超时（45 秒）" {
		t.Errorf("reload %+v", d)
	}
	if got := messageFor(context.DeadlineExceeded, reloadDeadline()); got != "配置重载超时（45 秒）" {
		t.Errorf("timeout message %q", got)
	}
}

func TestRuntimePowerState(t *testing.T) {
	cases := []struct {
		state   string
		desired bool
		want    powerState
	}{
		{"reloading", true, powerState{running: true, label: "停止"}},
		{"stopping", false, powerState{stopping: true, label: "正在停止"}},
		{"running", true, powerState{running: true, label: "停止"}},
		{"stopped", false, powerState{label: "启动"}},
		{"degraded", false, powerState{running: true, label: "停止"}},
	}
	for _, c := range cases {
		if got := runtimePowerState(c.state, c.desired); got != c.want {
			t.Errorf("%s/%v: %+v, want %+v", c.state, c.desired, got, c.want)
		}
	}
}

func busy() error {
	return &api.Error{Status: 409, Message: applyBusyMessage, Body: []byte(`{"error":"` + applyBusyMessage + `"}`)}
}

func TestRetryBusyApplyRetriesOnlyTheApplyLock(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	result, err := retryBusyApply(context.Background(), func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", busy()
		}
		return "applied", nil
	}, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if err != nil || result != "applied" || attempts != 3 {
		t.Fatalf("result %q %v after %d", result, err, attempts)
	}
	if len(delays) != 2 || delays[0] != 200*time.Millisecond || delays[1] != 400*time.Millisecond {
		t.Errorf("delays %v", delays)
	}
}

func TestRetryBusyApplyReportsOtherErrors(t *testing.T) {
	failure := &api.Error{Status: 409, Message: "核心进程状态已变化，配置重载已中止", Body: []byte(`{"error":"核心进程状态已变化，配置重载已中止"}`)}
	if got := errorMessage(failure); got != "核心进程状态已变化，配置重载已中止" {
		t.Errorf("message %q", got)
	}
	if got := errorMessage(errors.New(`{"error":"内嵌错误"}`)); got != "内嵌错误" {
		t.Errorf("JSON message %q", got)
	}
	attempts := 0
	_, err := retryBusyApply(context.Background(), func() (int, error) { attempts++; return 0, failure }, func(context.Context, time.Duration) error { return nil })
	if !errors.Is(err, failure) || attempts != 1 {
		t.Errorf("err %v after %d", err, attempts)
	}
	// It gives up after the last delay.
	attempts = 0
	_, err = retryBusyApply(context.Background(), func() (int, error) { attempts++; return 0, busy() }, func(context.Context, time.Duration) error { return nil })
	if err == nil || attempts != len(applyRetryDelays)+1 {
		t.Errorf("err %v after %d", err, attempts)
	}
}

func TestRuntimeSyncNewerPushWins(t *testing.T) {
	var s runtimeSync
	now := time.Unix(100, 0)
	revision, ok := s.begin()
	if !ok {
		t.Fatal("query not started")
	}
	if _, again := s.begin(); again {
		t.Error("two queries at once")
	}
	s.update(now) // an event arrives while the query runs
	if s.finish(revision, true, now) {
		t.Error("older answer overwrote the pushed state")
	}
	if _, ok := s.begin(); !ok {
		t.Error("query still pending after an event")
	}
}

func TestRuntimeSyncRefreshRecoversMissedEvents(t *testing.T) {
	var s runtimeSync
	now := time.Unix(100, 0)
	revision, _ := s.begin()
	if !s.finish(revision, true, now) {
		t.Fatal("answer dropped")
	}
	if s.stale(now.Add(time.Second)) || !s.stale(now.Add(4*time.Second)) {
		t.Error("staleness")
	}
}

func TestRuntimeSyncDropsAnswersOfALeftPage(t *testing.T) {
	var s runtimeSync
	first, _ := s.begin()
	s.reset()
	second, ok := s.begin()
	if !ok {
		t.Fatal("query not started after reopening")
	}
	if !s.finish(second, true, time.Now()) {
		t.Error("current answer dropped")
	}
	if s.finish(first, true, time.Now()) {
		t.Error("previous page's answer applied")
	}
}

func TestStatusText(t *testing.T) {
	three := 3.0
	cases := []struct {
		runtime api.Runtime
		known   bool
		want    string
	}{
		{api.Runtime{}, false, "正在读取状态"},
		{api.Runtime{State: "running"}, true, "运行中"},
		{api.Runtime{State: "reloading", Reload: &api.Reload{Phase: "recovering"}}, true, "正在恢复原配置"},
		{api.Runtime{State: "reloading"}, true, "正在重载 · 等待核心就绪"},
		{api.Runtime{State: "degraded", RestartInSeconds: &three}, true, "等待重试（3 秒）"},
		{api.Runtime{State: "degraded"}, true, "等待重试"},
		{api.Runtime{State: "custom"}, true, "custom"},
	}
	for _, c := range cases {
		if got := statusText(c.runtime, c.known); got != c.want {
			t.Errorf("%+v: %q, want %q", c.runtime, got, c.want)
		}
	}
}

func TestUsageBytes(t *testing.T) {
	cases := map[float64]string{0: "0 B", 512: "512 B", 1023: "1,023 B", 1536: "1.5 KB", 1024 * 1024: "1 MB",
		1023.5 * 1024: "1,023.5 KB", 12.34 * 1024 * 1024 * 1024: "12.3 GB"}
	for v, want := range cases {
		if got := usageBytes(v); got != want {
			t.Errorf("usageBytes(%v) = %q, want %q", v, got, want)
		}
	}
}

// formatUptime is kit.Uptime.
func TestFormatUptime(t *testing.T) {
	cases := map[float64]string{30: "30秒", 59: "59秒", 60: "1分钟", 20*60 + 30: "20分钟", 3600: "1小时",
		12*3600 + 20*60 + 30: "12小时20分钟", 86400: "1天", 2*86400 + 12*3600 + 20*60: "2天12小时", -1: "0秒"}
	for v, want := range cases {
		if got := kit.Uptime(v); got != want {
			t.Errorf("Uptime(%v) = %q, want %q", v, got, want)
		}
	}
}
