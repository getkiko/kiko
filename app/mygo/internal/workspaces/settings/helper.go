package settings

import (
	"context"

	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// helperResult is the outcome of an install: whether it ran and the
// helper's state after it.
type helperResult struct {
	completed bool
	status    env.HelperStatus
	statusErr error
}

// loadHelper reads the macOS TUN helper's state; elsewhere the shell
// reports it is unavailable.
func (w *Workspace) loadHelper() {
	if w.env.Shell == nil || w.env.Shell.Platform() != "darwin" {
		return
	}
	env.Go(w.env, func(context.Context) (env.HelperStatus, error) {
		return w.env.Shell.HelperStatus()
	}, func(status env.HelperStatus, err error) {
		if err != nil {
			w.helperErr = errorText(err, "状态读取失败")
			return
		}
		w.helper, w.helperErr = &status, ""
	})
}

// installHelper installs or updates the helper.
func (w *Workspace) installHelper() {
	if w.helperBusy || w.env.Shell == nil {
		return
	}
	w.helperBusy = true
	env.Go(w.env, func(context.Context) (helperResult, error) {
		completed, err := w.env.Shell.InstallHelper()
		if err != nil {
			return helperResult{}, err
		}
		status, statusErr := w.env.Shell.HelperStatus()
		return helperResult{completed: completed, status: status, statusErr: statusErr}, nil
	}, func(result helperResult, err error) {
		w.helperBusy = false
		if err != nil {
			w.env.Toast(errorText(err, "特权助手安装失败"))
			return
		}
		if result.statusErr == nil {
			w.helper, w.helperErr = &result.status, ""
		}
		if result.completed {
			w.env.Toast("特权助手已安装，请重新应用配置")
		}
	})
}

// helperText is the helper row's state.
func (w *Workspace) helperText() string {
	switch {
	case w.helperBusy:
		return "正在安装…"
	case w.helperErr != "":
		return w.helperErr
	case w.helper == nil:
		return "正在检查…"
	case !w.helper.Installed:
		return "未安装"
	case w.helper.NeedsUpdate:
		return "需要更新"
	}
	return "已安装"
}

// helperAction is the helper row's button.
func (w *Workspace) helperAction() string {
	switch {
	case w.helperBusy:
		return "安装中…"
	case w.helper != nil && w.helper.Installed && w.helper.NeedsUpdate:
		return "更新"
	case w.helper != nil && w.helper.Installed:
		return "重新安装"
	}
	return "安装"
}

// errorText is err's message, fallback when it has none.
func errorText(err error, fallback string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fallback
}
