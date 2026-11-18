package main

import (
	"errors"
	"net/http"
	"strconv"
)

func processForAction(r *http.Request) (int32, error) {
	pid, err := strconv.ParseInt(r.PathValue("pid"), 10, 32)
	started, err2 := strconv.ParseInt(r.URL.Query().Get("startedAt"), 10, 64)
	if err != nil || err2 != nil || pid <= 0 || started <= 0 {
		return 0, errors.New("进程身份无效")
	}
	actual, err := monitoredProcessStartedAt(r.Context(), int32(pid))
	if err != nil || actual != started {
		return 0, errors.New("进程身份已变更")
	}

	return int32(pid), nil
}
func registerProcessMonitorActions(mux *routeMux, ownerMaps ...map[string]string) {
	var owners map[string]string
	if len(ownerMaps) > 0 {
		owners = ownerMaps[0]
	}
	mux.handle("POST /api/v1/process-monitor/{pid}/reveal", routeWrite, "显示进程位置", func(w http.ResponseWriter, r *http.Request) {
		p, err := processForAction(r)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		path, err := monitoredProcessPath(r.Context(), p)
		if err != nil || path == "" {
			http.Error(w, "进程路径暂不可用", 404)
			return
		}
		if err := showInFileManager(r.Context(), path, true); err != nil {
			if errors.Is(err, errNoFileManager) {
				http.Error(w, "当前系统的文件管理器暂不可用", 501)
				return
			}
			http.Error(w, "打开文件位置失败："+err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Icons are derived from an existing PID, or an existing executable's
	// bundle (registerProcessPathIcon). Cache by bundle path so every helper
	// process can reuse the same PNG.
	icons := newBundleIconCache()
	registerProcessPathIcon(mux, icons, owners)
	mux.get("GET /api/v1/process-monitor/{pid}/icon", func(w http.ResponseWriter, r *http.Request) {
		p, err := processForAction(r)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		path, err := monitoredProcessPath(r.Context(), p)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		item := monitoredProcess{PID: p, Path: path}
		identifyProcessIcon(&item)
		identifyOwnedProcess(&item, owners)
		if item.AppPath == "" {
			w.WriteHeader(404)
			return
		}
		data, err := icons.icon(r.Context(), item.AppPath)
		if err != nil {
			w.WriteHeader(404)
			return
		}
		writeProcessIcon(w, data)
	})
}
