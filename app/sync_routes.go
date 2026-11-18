package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/syncbackend"
	"github.com/getkiko/kiko/app/syncvault"
	"github.com/getkiko/kiko/app/syncworker"
)

const syncMaxPasswordRunes = 256

func syncFail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}

// syncFailErr maps engine/backend errors to localized responses with a code;
// backend error messages have credentials stripped.
func syncFailErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, syncvault.ErrWrongPassword):
		syncFail(w, http.StatusUnauthorized, "wrong-password", "密码错误")
	case errors.Is(err, syncvault.ErrWrongKey):
		syncFail(w, http.StatusConflict, "key-mismatch", err.Error())
	case errors.Is(err, syncvault.ErrCorrupt):
		syncFail(w, http.StatusBadGateway, "corrupt", err.Error())
	case errors.Is(err, syncvault.ErrUnsupported), errors.Is(err, errSyncUnsupported):
		syncFail(w, http.StatusBadRequest, "unsupported", err.Error())
	case errors.Is(err, errSyncNotConfigured):
		syncFail(w, http.StatusBadRequest, "not-configured", err.Error())
	case errors.Is(err, errSyncLocked):
		syncFail(w, http.StatusConflict, "locked", err.Error())
	case errors.Is(err, errSyncVaultMissing):
		syncFail(w, http.StatusNotFound, "vault-missing", err.Error())
	case errors.Is(err, errSyncConfirm):
		syncFail(w, http.StatusBadRequest, "confirm-mismatch", err.Error())
	case errors.Is(err, errSyncNotAVault):
		syncFail(w, http.StatusBadGateway, "not-a-vault", err.Error())
	case errors.Is(err, errSyncVaultExists):
		syncFail(w, http.StatusConflict, "vault-exists", err.Error())
	case errors.Is(err, syncbackend.ErrUnauthorized):
		syncFail(w, http.StatusBadGateway, "unauthorized", err.Error())
	case errors.Is(err, syncbackend.ErrConflict):
		syncFail(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, syncbackend.ErrNotFound):
		syncFail(w, http.StatusNotFound, "not-found", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		syncFail(w, http.StatusGatewayTimeout, "timeout", "连接备份服务超时")
	default:
		syncFail(w, http.StatusBadGateway, "backend-error", err.Error())
	}
}

func syncPasswordProblem(w http.ResponseWriter, passwords ...string) bool {
	for _, p := range passwords {
		if utf8.RuneCountInString(p) > syncMaxPasswordRunes {
			syncFail(w, http.StatusBadRequest, "password-too-long", "密码过长（最多 256 个字符）")
			return true
		}
	}
	return false
}

func (s *server) registerSyncRoutes(m *routeMux) {
	m.get("GET /api/v1/sync/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.syncStatusView())
	})
	m.handle("PUT /api/v1/sync/config", routeWrite, "修改同步设置", s.syncPutConfig)
	m.handle("POST /api/v1/sync/test", routeRead, "", s.syncTest)
	m.handle("POST /api/v1/sync/unlock", routeOwn, "解锁同步", s.syncUnlockHandler)
	m.handle("POST /api/v1/sync/lock", routeWrite, "锁定同步", func(w http.ResponseWriter, r *http.Request) {
		s.syncLock()
		writeJSON(w, http.StatusOK, s.syncStatusView())
	})
	m.handle("POST /api/v1/sync/now", routeOwn, "立即同步", func(w http.ResponseWriter, r *http.Request) {
		res, err := s.syncOnce(r.Context())
		if err != nil {
			syncFailErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": s.syncStatusView(), "result": res})
	})
	m.handle("POST /api/v1/sync/auto", routeWrite, "修改自动同步", s.syncSetAuto)
	m.handle("POST /api/v1/sync/change-password", routeWrite, "修改同步密码", s.syncChangePasswordHandler)
	m.handle("POST /api/v1/sync/reset", routeOwn, "重置远端备份", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !body.Confirm {
			syncFail(w, http.StatusBadRequest, "confirm-required", "重置远端备份需要确认")
			return
		}
		if err := s.syncReset(r.Context()); err != nil {
			syncFailErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.syncStatusView())
	})
	m.get("GET /api/v1/sync/worker", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"source": syncworker.Source, "readme": syncworker.Readme, "wrangler": syncworker.WranglerExample,
			"commands": syncworker.Commands("kiko-sync"),
		})
	})
}

func (s *server) syncPutConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend  syncbackend.Config `json:"backend"`
		AutoSync *bool              `json:"autoSync"`
	}
	if !decode(w, r, &body) {
		return
	}
	sr := &s.sync
	sr.mu.Lock()
	old := sr.cfg.Backend
	sr.mu.Unlock()
	merged := body.Backend.MergeSecrets(old)
	if _, err := syncbackend.New(merged); err != nil {
		syncFail(w, http.StatusBadRequest, "invalid-config", err.Error())
		return
	}
	changed := !reflect.DeepEqual(old, merged)
	var exists bool
	if changed {
		// Connect once first: a wrong address or credentials means nothing is
		// saved, and it also tells whether the remote has a backup, so the UI
		// can tell "create new" from "enter password".
		be, err := sr.newBackend(merged)
		if err != nil {
			syncFail(w, http.StatusBadRequest, "invalid-config", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		exists, err = syncProbe(ctx, be)
		cancel()
		if err != nil {
			syncFailErr(w, err)
			return
		}
	}
	sr.mu.Lock()
	if changed {
		sr.st.VaultExists = &exists
	}
	sr.cfg.Backend = merged
	if body.AutoSync != nil {
		sr.cfg.AutoSync = *body.AutoSync
	}
	err := s.syncSaveConfigLocked()
	if err == nil {
		err = s.syncSaveStateLocked()
	}
	auto := sr.cfg.AutoSync
	sr.mu.Unlock()
	if err != nil {
		syncFail(w, http.StatusInternalServerError, "save-failed", "保存备份配置失败")
		return
	}
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	s.syncTrack(st)
	if auto {
		select {
		case sr.trigger <- struct{}{}:
		default:
		}
	}
	s.syncEmit()
	writeJSON(w, http.StatusOK, s.syncStatusView())
}

// syncProbe checks that the backend is reachable and the credentials are
// valid, and reports whether the remote already has a Kiko backup (no
// password needed).
func syncProbe(ctx context.Context, be syncbackend.Backend) (bool, error) {
	if err := be.Test(ctx); err != nil {
		return false, err
	}
	data, _, err := be.Get(ctx)
	switch {
	case errors.Is(err, syncbackend.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	if _, err = syncvault.ParseHeader(data); err != nil {
		return false, errSyncNotAVault
	}
	return true, nil
}

func (s *server) syncTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend *syncbackend.Config `json:"backend"`
	}
	if !decode(w, r, &body) {
		return
	}
	sr := &s.sync
	sr.mu.Lock()
	saved := sr.cfg.Backend
	sr.mu.Unlock()
	cfg, isSaved := saved, true
	if body.Backend != nil {
		cfg = body.Backend.MergeSecrets(saved)
		isSaved = reflect.DeepEqual(cfg, saved)
	}
	if cfg.Type == "" {
		syncFailErr(w, errSyncNotConfigured)
		return
	}
	be, err := sr.newBackend(cfg)
	if err != nil {
		syncFail(w, http.StatusBadRequest, "invalid-config", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	exists, err := syncProbe(ctx, be)
	if err != nil {
		syncFailErr(w, err)
		return
	}
	if isSaved {
		s.syncSetVaultExists(exists)
		s.syncEmit()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vaultExists": exists})
}

func (s *server) syncUnlockHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
		Create   bool   `json:"create"`
	}
	if !decode(w, r, &body) {
		return
	}
	if syncPasswordProblem(w, body.Password, body.Confirm) {
		return
	}
	if body.Create {
		if err := syncvault.CheckPassword(body.Password); err != nil {
			syncFail(w, http.StatusBadRequest, "weak-password", err.Error())
			return
		}
	}
	if err := s.syncUnlock(r.Context(), syncUnlockRequest{Password: body.Password, Confirm: body.Confirm, Create: body.Create}); err != nil {
		s.syncEmit()
		syncFailErr(w, err)
		return
	}
	s.syncEmit()
	writeJSON(w, http.StatusOK, s.syncStatusView())
}

func (s *server) syncSetAuto(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	sr := &s.sync
	sr.mu.Lock()
	sr.cfg.AutoSync = body.Enabled
	err := s.syncSaveConfigLocked()
	sr.mu.Unlock()
	if err != nil {
		syncFail(w, http.StatusInternalServerError, "save-failed", "保存备份配置失败")
		return
	}
	if body.Enabled {
		select {
		case sr.trigger <- struct{}{}:
		default:
		}
	}
	s.syncEmit()
	writeJSON(w, http.StatusOK, s.syncStatusView())
}

func (s *server) syncChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewPassword string `json:"newPassword"`
		Confirm     string `json:"confirm"`
	}
	if !decode(w, r, &body) {
		return
	}
	if syncPasswordProblem(w, body.NewPassword, body.Confirm) {
		return
	}
	if err := syncvault.CheckPassword(body.NewPassword); err != nil {
		syncFail(w, http.StatusBadRequest, "weak-password", err.Error())
		return
	}
	if err := s.syncChangePassword(r.Context(), body.NewPassword, body.Confirm); err != nil {
		syncFailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.syncStatusView())
}
