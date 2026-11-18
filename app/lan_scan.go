package main

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/getkiko/kiko/app/lanscan"
)

// The LAN scan runs on the machine that serves the request, so it is offered
// only on the loopback listener, which is how the desktop app (MyGo) reaches
// its own server. Paired remote devices would otherwise scan the server's
// network instead of their own.
var (
	lanScanner     *lanscan.Manager
	lanScannerOnce sync.Once
)

func lanScanManager() *lanscan.Manager {
	lanScannerOnce.Do(func() { lanScanner = lanscan.NewManager(lanscan.DefaultEnv) })
	return lanScanner
}

func registerLANScan(m *routeMux) {
	m.handle("POST /api/v1/lan-scan", routeOwn, "扫描局域网", startLANScan)
	m.get("GET /api/v1/lan-scan", pollLANScan)
	m.handle("POST /api/v1/lan-scan/cancel", routeOwn, "取消扫描", cancelLANScan)
}

func lanScanLocalOnly(w http.ResponseWriter, r *http.Request) bool {
	if requestClient(r).Remote {
		http.Error(w, "局域网扫描仅限本机", http.StatusForbidden)
		return false
	}
	return true
}

func startLANScan(w http.ResponseWriter, r *http.Request) {
	if !lanScanLocalOnly(w, r) {
		return
	}
	id, err := lanScanManager().Start()
	if errors.Is(err, lanscan.ErrBusy) {
		// The id of the scan in progress lets a client that lost track of
		// it (a restarted window) cancel it and start again.
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "id": lanScanManager().RunningID()})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func pollLANScan(w http.ResponseWriter, r *http.Request) {
	if !lanScanLocalOnly(w, r) {
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	snap, err := lanScanManager().Poll(r.URL.Query().Get("id"), since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func cancelLANScan(w http.ResponseWriter, r *http.Request) {
	if !lanScanLocalOnly(w, r) {
		return
	}
	if err := lanScanManager().Cancel(r.URL.Query().Get("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
