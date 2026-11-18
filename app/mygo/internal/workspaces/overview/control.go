package overview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// deadline bounds a runtime request, with the message shown when it runs
// out.
type deadline struct {
	timeout time.Duration
	message string
}

// toggleDeadline is the deadline of stopping or of starting the core.
func toggleDeadline(running bool) deadline {
	if running {
		return deadline{5 * time.Second, "停止请求超时（5 秒）"}
	}
	return deadline{20 * time.Second, "启动超时（20 秒）"}
}

// reloadDeadline covers validation, API readiness and a bounded rollback.
func reloadDeadline() deadline { return deadline{45 * time.Second, "配置重载超时（45 秒）"} }

// errorMessage is what a failed runtime request shows: the server's
// "error" field, also when the message itself is such JSON.
func errorMessage(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		var body struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(apiErr.Body, &body) == nil && body.Error != "" {
			return body.Error
		}
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &body) == nil && body.Error != "" {
		return body.Error
	}
	return err.Error()
}

// messageFor is errorMessage, or the deadline's message when it ran out.
func messageFor(err error, d deadline) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return d.message
	}
	return errorMessage(err)
}

// applyBusyMessage is the server's answer while another apply runs.
const applyBusyMessage = "配置正在应用，请稍后重试"

func isApplyInProgress(err error) bool {
	return api.IsStatus(err, http.StatusConflict) && errorMessage(err) == applyBusyMessage
}

// applyRetryDelays are the waits between tries of an apply the server is
// busy with: about 30 seconds in all.
var applyRetryDelays = append([]time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond},
	slices.Repeat([]time.Duration{1600 * time.Millisecond}, 15)...)

// sleep waits d unless ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryBusyApply runs apply, trying again while the server reports that
// another configuration is being applied; other errors return at once.
func retryBusyApply[T any](ctx context.Context, apply func() (T, error), wait func(context.Context, time.Duration) error) (T, error) {
	if wait == nil {
		wait = sleep
	}
	for attempt := 0; ; attempt++ {
		var zero T
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := apply()
		if err == nil {
			return value, nil
		}
		if !isApplyInProgress(err) || attempt >= len(applyRetryDelays) {
			return zero, err
		}
		if err := wait(ctx, applyRetryDelays[attempt]); err != nil {
			return zero, err
		}
	}
}

// powerState is what the power button shows for a runtime state.
type powerState struct {
	running, stopping bool
	label             string
}

func runtimePowerState(state string, desiredRunning bool) powerState {
	stopping := state == "stopping"
	running := desiredRunning || slices.Contains([]string{"starting", "running", "reloading", "restarting", "degraded"}, state)
	label := "启动"
	switch {
	case stopping:
		label = "正在停止"
	case running:
		label = "停止"
	}
	return powerState{running: running, stopping: stopping, label: label}
}
