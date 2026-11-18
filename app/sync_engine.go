package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/getkiko/kiko/app/syncbackend"
	"github.com/getkiko/kiko/app/syncvault"
)

const (
	syncPayloadFormat = "kiko-sync"
	// legacySyncPayloadFormat is the payload format written to backups before
	// the rename to Kiko; it is read as equivalent to syncPayloadFormat.
	legacySyncPayloadFormat = "sbcfg-sync"
	syncMaxProfiles         = 2000
	syncMaxAttempts         = 3
)

var (
	errSyncNotConfigured = errors.New("还没有配置备份后端")
	errSyncLocked        = errors.New("尚未输入备份密码，请先解锁")
	errSyncVaultMissing  = errors.New("远端还没有备份，请设置一个新的备份密码来创建")
	errSyncConfirm       = errors.New("两次输入的密码不一致")
	errSyncVaultExists   = errors.New("远端刚刚已被其他设备创建了备份，请重新输入密码解锁")
	errSyncUnsupported   = errors.New("备份内容的格式不受支持，请升级 Kiko")
	errSyncNotAVault     = errors.New("远端已有同名文件，但不是有效的 Kiko 备份")
)

type syncPayloadProfile struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	UpdatedAt int64           `json:"updatedAt"`
	Doc       json.RawMessage `json:"doc"`
}

type syncPayload struct {
	NetworkConfigs  []networkConfig      `json:"networkConfigs,omitempty"`
	Format          string               `json:"format"`
	Schema          int                  `json:"schema"`
	DeviceID        string               `json:"deviceId"`
	ExportedAt      int64                `json:"exportedAt"`
	Profiles        []syncPayloadProfile `json:"profiles"`
	Tombstones      []syncTombstone      `json:"tombstones"`
	ActiveProfileID string               `json:"activeProfileId,omitempty"`
}

func parseSyncPayload(b []byte) (*syncPayload, error) {
	var p syncPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", syncvault.ErrCorrupt, err)
	}
	if (p.Format != syncPayloadFormat && p.Format != legacySyncPayloadFormat) || p.Schema < 1 || p.Schema > 2 {
		return nil, errSyncUnsupported
	}
	if len(p.Profiles) > syncMaxProfiles || len(p.NetworkConfigs) > syncMaxProfiles {
		return nil, fmt.Errorf("%w：配置档案数量过多", errSyncUnsupported)
	}
	return &p, nil
}

func (p *syncPayload) mergeProfiles() []mergeProfile {
	out := make([]mergeProfile, 0, len(p.Profiles))
	for _, item := range p.Profiles {
		if item.ID == "" || len(item.Doc) == 0 {
			continue
		}
		out = append(out, mergeProfile{
			ID: item.ID, Name: item.Name, Doc: item.Doc, UpdatedAt: item.UpdatedAt, Hash: syncProfileHash(item.Name, item.Doc),
		})
	}
	raw, _ := json.Marshal(p.NetworkConfigs)
	out = append(out, networkMergeEntries(raw)...)
	return out
}

type syncSnapshot struct {
	store      storeState
	profiles   []mergeProfile
	tombstones []syncTombstone
	lastSynced map[string]string
	active     string
}

func activeProfileString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// syncSnapshot returns a local view consistent with the tracking state (it
// first refreshes tracking from the current store).
func (s *server) syncSnapshot() syncSnapshot {
	sr := &s.sync
	sr.trackMu.Lock()
	defer sr.trackMu.Unlock()
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	s.syncTrackLocked(st)
	sr.mu.Lock()
	defer sr.mu.Unlock()
	snap := syncSnapshot{
		store: st, tombstones: append([]syncTombstone(nil), sr.st.Tombstones...),
		lastSynced: make(map[string]string, len(sr.st.LastSynced)), active: activeProfileString(st.ActiveProfileID),
	}
	for id, hash := range sr.st.LastSynced {
		snap.lastSynced[id] = hash
	}
	for _, p := range storeSyncEntries(st) {
		track := sr.st.Profiles[p.ID]
		snap.profiles = append(snap.profiles, mergeProfile{ID: p.ID, Name: p.Name, Doc: p.Doc, UpdatedAt: track.UpdatedAt, Hash: track.Hash, Kind: entryKind(p.ID)})
	}
	return snap
}

func (s *server) syncBuildPayload(profiles []mergeProfile, tombstones []syncTombstone, active string) []byte {
	sr := &s.sync
	sr.mu.Lock()
	deviceID := sr.cfg.DeviceID
	sr.mu.Unlock()
	payload := syncPayload{
		Format: syncPayloadFormat, Schema: 2, DeviceID: deviceID, ExportedAt: sr.now().UnixMilli(),
		Tombstones: tombstones, ActiveProfileID: active,
	}
	profiles, payload.NetworkConfigs = splitNetworkEntries(profiles)
	for _, p := range profiles {
		payload.Profiles = append(payload.Profiles, syncPayloadProfile{ID: p.ID, Name: p.Name, UpdatedAt: p.UpdatedAt, Doc: p.Doc})
	}
	if payload.Profiles == nil {
		payload.Profiles = []syncPayloadProfile{}
	}
	if payload.Tombstones == nil {
		payload.Tombstones = []syncTombstone{}
	}
	b, _ := json.Marshal(payload)
	return b
}

// syncApplyLocal writes the merge result back to the local store. It returns
// false if the local store was edited meanwhile (revision changed), and the
// caller retries.
func (s *server) syncApplyLocal(snap syncSnapshot, merged mergeOutput) (bool, error) {
	sr := &s.sync
	sr.trackMu.Lock()
	defer sr.trackMu.Unlock()
	s.mu.Lock()
	if s.store.Revision != snap.store.Revision {
		s.mu.Unlock()
		return false, nil
	}
	allMerged := merged.Profiles
	merged.Profiles, _ = splitNetworkEntries(merged.Profiles)
	_, networkConfigs := splitNetworkEntries(allMerged)
	existing := map[string]storeProfile{}
	var order []string
	for _, p := range parseStoreProfiles(s.store.Profiles) {
		existing[p.ID] = p
		order = append(order, p.ID)
	}
	mergedByID := indexProfiles(merged.Profiles)
	var list []map[string]json.RawMessage
	appendProfile := func(m mergeProfile) {
		if old, ok := existing[m.ID]; ok && syncProfileHash(old.Name, old.Doc) == m.Hash {
			list = append(list, old.Raw)
			return
		}
		item := map[string]json.RawMessage{"doc": m.Doc}
		item["id"], _ = json.Marshal(m.ID)
		item["name"], _ = json.Marshal(m.Name)
		item["savedAt"], _ = json.Marshal(m.UpdatedAt)
		list = append(list, item)
	}
	done := map[string]bool{}
	for _, id := range order {
		if m, ok := mergedByID[id]; ok {
			appendProfile(m)
			done[id] = true
		}
	}
	for _, m := range merged.Profiles {
		if !done[m.ID] {
			appendProfile(m)
		}
	}
	profilesJSON, err := json.Marshal(list)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	next := s.store
	if list == nil {
		profilesJSON = []byte("[]")
	}
	next.NetworkConfigs, _ = json.Marshal(networkConfigs)
	next.Profiles = profilesJSON
	active := activeProfileString(next.ActiveProfileID)
	if m, ok := mergedByID[active]; ok {
		if old, had := existing[active]; !had || syncProfileHash(old.Name, old.Doc) != m.Hash {
			next.Doc = m.Doc
		}
	} else {
		var pick *mergeProfile
		for i := range merged.Profiles {
			if pick == nil || merged.Profiles[i].UpdatedAt > pick.UpdatedAt {
				pick = &merged.Profiles[i]
			}
		}
		if pick == nil {
			next.ActiveProfileID = nil
		} else {
			next.ActiveProfileID, next.Doc = pick.ID, pick.Doc
		}
	}
	// Profiles pulled from a device from before the Burrow rename may hold
	// holepunch entries.
	migrateBurrowStore(&next)
	next.Revision++
	if err = atomicJSON(s.syncPath("editor.json"), next); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.store = next
	s.mu.Unlock()

	sr.mu.Lock()
	sr.st.Profiles = make(map[string]syncProfileTrack, len(merged.Profiles))
	for _, m := range allMerged {
		sr.st.Profiles[m.ID] = syncProfileTrack{Hash: m.Hash, UpdatedAt: m.UpdatedAt}
	}
	sr.st.Tombstones = merged.Tombstones
	sr.st.TrackedRevision = next.Revision
	_ = s.syncSaveStateLocked()
	sr.mu.Unlock()
	s.emit("store", next)
	return true, nil
}

func (s *server) syncBackendFromConfig() (syncbackend.Backend, error) {
	sr := &s.sync
	sr.mu.Lock()
	cfg := sr.cfg.Backend
	sr.mu.Unlock()
	if cfg.Type == "" {
		return nil, errSyncNotConfigured
	}
	return sr.newBackend(cfg)
}

// syncDropKeys forgets this device's keys (the backend config is kept).
func (s *server) syncDropKeys() {
	sr := &s.sync
	sr.mu.Lock()
	sr.keys = nil
	if sr.debounce != nil {
		sr.debounce.Stop()
	}
	_ = os.Remove(s.syncPath(syncKeyFileName))
	sr.mu.Unlock()
}

func (s *server) syncOnce(ctx context.Context) (syncResult, error) {
	s.sync.syncMu.Lock()
	defer s.sync.syncMu.Unlock()
	return s.syncOnceLocked(ctx)
}

// syncOnceLocked requires the caller to hold syncMu.
func (s *server) syncOnceLocked(ctx context.Context) (syncResult, error) {
	sr := &s.sync
	sr.mu.Lock()
	sr.syncing = true
	sr.mu.Unlock()
	s.syncEmit()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var res syncResult
	var err error
	for attempt := 0; attempt < syncMaxAttempts; attempt++ {
		var retry bool
		res, retry, err = s.syncAttempt(ctx)
		if !retry {
			break
		}
		err = errors.New("多次尝试后仍与远端冲突，请稍后重试")
	}
	s.syncFinish(res, err)
	return res, err
}

func (s *server) syncFinish(res syncResult, err error) {
	sr := &s.sync
	sr.mu.Lock()
	sr.syncing = false
	if err == nil {
		sr.st.LastSyncedAt = sr.now().UnixMilli()
		sr.st.LastError = ""
		sr.st.LastResult = &res
		sr.failures, sr.nextTry = 0, time.Time{}
	} else {
		sr.st.LastError = err.Error()
		sr.failures++
		backoff := syncBackoffBase << min(sr.failures-1, 10)
		sr.nextTry = sr.now().Add(min(backoff, syncBackoffMax))
	}
	_ = s.syncSaveStateLocked()
	sr.mu.Unlock()
	s.syncEmit()
}

// syncAttempt runs one "pull -> merge -> write back locally -> push" round;
// retry is true when a concurrent modification was hit and the round should
// start over.
func (s *server) syncAttempt(ctx context.Context) (res syncResult, retry bool, err error) {
	sr := &s.sync
	sr.mu.Lock()
	keys, saved := sr.keys, sr.st.Header
	sr.mu.Unlock()
	be, err := s.syncBackendFromConfig()
	if err != nil {
		return res, false, err
	}
	if keys == nil {
		return res, false, errSyncLocked
	}
	data, etag, err := be.Get(ctx)
	remoteExists := true
	var hdr syncvault.Header
	var remote *syncPayload
	switch {
	case errors.Is(err, syncbackend.ErrNotFound):
		remoteExists = false
	case err != nil:
		return res, false, err
	default:
		plain, h, openErr := keys.Open(data)
		if errors.Is(openErr, syncvault.ErrWrongKey) {
			s.syncDropKeys()
			return res, false, openErr
		}
		if openErr != nil {
			return res, false, openErr
		}
		if remote, err = parseSyncPayload(plain); err != nil {
			return res, false, err
		}
		hdr = h
	}
	stray := s.syncStrayLegacy(ctx, be, keys, data, remoteExists)
	if stray != nil {
		defer func() {
			// A retried attempt has not written the merge yet: let it check
			// the old backup again instead of waiting for the next interval.
			if retry {
				stray.backup.RecheckLegacy()
			}
		}()
	}
	header := hdr
	if !remoteExists {
		if saved == nil {
			return res, false, errSyncLocked
		}
		header = *saved
	}

	snap := s.syncSnapshot()
	in := mergeInput{
		Local: snap.profiles, LocalTombstones: snap.tombstones, LastSynced: snap.lastSynced,
		Now: sr.now(), NewID: func() string { return newSyncID("prof_") },
	}
	if remote != nil {
		in.Remote, in.RemoteTombstones = remote.mergeProfiles(), remote.Tombstones
	}
	strayChanged := false
	if stray != nil && stray.payload != nil {
		// Updates a client from before the rename wrote to the old backup
		// after it was copied, merged against the copied version: a profile
		// only one side changed takes that side's version, one both changed
		// keeps the newer and a conflict copy of the other. Without the
		// copied version every differing profile counts as changed on both
		// sides. Conflict copies are named after the old backup, so merging
		// it again (another device, a retried attempt) adds none twice.
		combined := mergeProfiles(mergeInput{
			Local: in.Remote, LocalTombstones: in.RemoteTombstones,
			Remote: stray.payload.mergeProfiles(), RemoteTombstones: stray.payload.Tombstones,
			LastSynced: stray.baseline, Now: in.Now, NewID: in.NewID,
			ConflictAt: time.UnixMilli(stray.payload.ExportedAt),
			ConflictID: func(loser mergeProfile) string {
				sum := sha256.Sum256([]byte(stray.hash + "\x00" + loser.ID))
				return "prof_" + hex.EncodeToString(sum[:8])
			},
		})
		in.Remote, in.RemoteTombstones = combined.Profiles, combined.Tombstones
		strayChanged = combined.LocalChanged
	}
	merged := mergeProfiles(in)
	if strayChanged {
		merged.RemoteChanged = true
	}
	if merged.LocalChanged {
		applied, applyErr := s.syncApplyLocal(snap, merged)
		if applyErr != nil {
			return res, false, applyErr
		}
		if !applied {
			return res, true, nil
		}
	}

	revision, newEtag := hdr.Revision, etag
	if merged.RemoteChanged || !remoteExists {
		revision = hdr.Revision + 1
		if !remoteExists {
			sr.mu.Lock()
			revision = sr.st.RemoteRevision + 1
			sr.mu.Unlock()
		}
		active := snap.active
		if _, ok := indexProfiles(merged.Profiles)[active]; !ok {
			active = ""
		}
		env, sealErr := keys.Seal(header, revision, s.syncBuildPayload(merged.Profiles, merged.Tombstones, active))
		if sealErr != nil {
			return res, false, sealErr
		}
		cond := syncbackend.Cond{IfNoneMatch: !remoteExists}
		if remoteExists {
			cond.IfMatch = etag
		}
		newEtag, err = be.Put(ctx, env, cond)
		if errors.Is(err, syncbackend.ErrConflict) {
			return res, true, nil
		}
		if err != nil {
			return res, false, err
		}
		if remoteExists && etag == "" {
			// The backend returns no ETag and cannot do conditional writes: read
			// back to confirm no other device overwrote it at the same time.
			back, _, getErr := be.Get(ctx)
			if getErr != nil {
				return res, false, getErr
			}
			if _, h, openErr := keys.Open(back); openErr != nil || h.Revision != revision {
				return res, true, nil
			}
		}
	}

	// Merged and written: the old backup gets its moved marker. Until that
	// succeeds, the merged version is remembered so later checks that find it
	// unchanged only retry the marker; an old client writing it again makes
	// a new version, which is merged.
	mergedLegacy := ""
	if stray != nil && stray.backup.RetireLegacy(ctx, stray.data, stray.etag) != nil {
		mergedLegacy = stray.hash
	}

	sr.mu.Lock()
	if stray != nil {
		sr.st.LegacyMerged = mergedLegacy
	}
	sr.st.LastSynced = syncHashes(merged.Profiles)
	sr.st.RemoteRevision, sr.st.RemoteEtag = revision, newEtag
	sr.st.Header = &header
	exists := true
	sr.st.VaultExists = &exists
	sr.mu.Unlock()
	return merged.Result, false, nil
}

// syncStray is an old backup a client from before the rename wrote after it was
// migrated.
type syncStray struct {
	backup syncbackend.LegacyBackup
	data   []byte
	etag   string
	// hash identifies this version of the old backup.
	hash string
	// payload is its content to merge, nil when this version was merged
	// already and only the moved marker is missing.
	payload *syncPayload
	// baseline maps the profiles of the copied version to their hashes.
	baseline map[string]string
}

// syncStrayLegacy returns the old backup to merge or retire, nil when there is
// none. An old backup that cannot be read, or opened with this vault's keys,
// is skipped; the backend checks it again every interval and it stays intact.
func (s *server) syncStrayLegacy(ctx context.Context, be syncbackend.Backend, keys *syncvault.Keys, current []byte, remoteExists bool) *syncStray {
	backup, ok := be.(syncbackend.LegacyBackup)
	if !ok || !remoteExists {
		return nil
	}
	data, etag, err := backup.PendingLegacy(ctx, current)
	if err != nil || data == nil {
		return nil
	}
	sum := sha256.Sum256(data)
	stray := &syncStray{backup: backup, data: data, etag: etag, hash: hex.EncodeToString(sum[:])}
	s.sync.mu.Lock()
	merged := s.sync.st.LegacyMerged == stray.hash
	s.sync.mu.Unlock()
	if merged {
		return stray
	}
	plain, _, err := keys.Open(data)
	if err != nil {
		return nil
	}
	if stray.payload, err = parseSyncPayload(plain); err != nil {
		return nil
	}
	stray.baseline = map[string]string{}
	if base, err := backup.LegacyBase(ctx); err == nil && base != nil {
		if plain, _, err := keys.Open(base); err == nil {
			if payload, err := parseSyncPayload(plain); err == nil {
				for _, p := range payload.mergeProfiles() {
					stray.baseline[p.ID] = p.Hash
				}
			}
		}
	}
	return stray
}

type syncUnlockRequest struct {
	Password string
	Confirm  string
	Create   bool
}

// syncInstallKeys saves this device's keys and header and resets the sync
// baseline.
func (s *server) syncInstallKeys(keys *syncvault.Keys, hdr syncvault.Header, revision uint64, etag string, lastSynced map[string]string) error {
	sr := &s.sync
	if err := s.syncSaveKey(keys); err != nil {
		return err
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.keys = keys
	sr.st.Header = &hdr
	sr.st.RemoteRevision, sr.st.RemoteEtag = revision, etag
	exists := true
	sr.st.VaultExists = &exists
	if lastSynced == nil {
		lastSynced = map[string]string{}
	}
	sr.st.LastSynced = lastSynced
	sr.st.LastError = ""
	sr.failures, sr.nextTry = 0, time.Time{}
	return s.syncSaveStateLocked()
}

func (s *server) syncSetVaultExists(exists bool) {
	sr := &s.sync
	sr.mu.Lock()
	sr.st.VaultExists = &exists
	_ = s.syncSaveStateLocked()
	sr.mu.Unlock()
}

// syncUnlock unlocks with the password (or creates the vault when the remote
// has no backup). When a backup exists it syncs once right away; a sync
// failure is only recorded in the status.
func (s *server) syncUnlock(parent context.Context, req syncUnlockRequest) error {
	sr := &s.sync
	sr.syncMu.Lock()
	defer sr.syncMu.Unlock()
	be, err := s.syncBackendFromConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	data, etag, err := be.Get(ctx)
	if errors.Is(err, syncbackend.ErrNotFound) {
		s.syncSetVaultExists(false)
		if !req.Create {
			return errSyncVaultMissing
		}
		if err = syncvault.CheckPassword(req.Password); err != nil {
			return err
		}
		if req.Password != req.Confirm {
			return errSyncConfirm
		}
		keys, hdr, err := syncvault.Create(req.Password, "")
		if err != nil {
			return err
		}
		snap := s.syncSnapshot()
		env, err := keys.Seal(hdr, 1, s.syncBuildPayload(snap.profiles, snap.tombstones, snap.active))
		if err != nil {
			return err
		}
		newEtag, err := be.Put(ctx, env, syncbackend.Cond{IfNoneMatch: true})
		if errors.Is(err, syncbackend.ErrConflict) {
			return errSyncVaultExists
		}
		if err != nil {
			return err
		}
		if err = s.syncInstallKeys(keys, hdr, 1, newEtag, syncHashes(snap.profiles)); err != nil {
			return err
		}
		sr.mu.Lock()
		sr.st.LastSyncedAt = sr.now().UnixMilli()
		sr.st.LastResult = &syncResult{Pushed: len(snap.profiles)}
		_ = s.syncSaveStateLocked()
		sr.mu.Unlock()
		s.syncEmit()
		return nil
	}
	if err != nil {
		return err
	}
	hdr, err := syncvault.ParseHeader(data)
	if err != nil {
		return err
	}
	keys, err := syncvault.Unlock(req.Password, hdr)
	if err != nil {
		return err
	}
	plain, _, err := keys.Open(data)
	if err != nil {
		return err
	}
	revision := hdr.Revision
	if keys.Format() == syncvault.LegacyFormat {
		keys, hdr, revision, etag = s.syncUpgradeVault(ctx, be, req.Password, keys, hdr, plain, etag)
	}
	if err = s.syncInstallKeys(keys, hdr, revision, etag, nil); err != nil {
		return err
	}
	_, _ = s.syncOnceLocked(parent)
	return nil
}

// syncUpgradeVault re-encrypts a backup in the pre-rename format into the
// current format, using the same password and vaultId. If the write fails
// (e.g. a concurrent modification), the old format is kept, the backup stays
// usable, and the upgrade is retried on the next unlock.
func (s *server) syncUpgradeVault(ctx context.Context, be syncbackend.Backend, password string, keys *syncvault.Keys,
	hdr syncvault.Header, plain []byte, etag string) (*syncvault.Keys, syncvault.Header, uint64, string) {
	newKeys, newHdr, err := syncvault.Create(password, keys.VaultID())
	if err != nil {
		return keys, hdr, hdr.Revision, etag
	}
	revision := hdr.Revision + 1
	env, err := newKeys.Seal(newHdr, revision, plain)
	if err != nil {
		return keys, hdr, hdr.Revision, etag
	}
	newEtag, err := be.Put(ctx, env, syncbackend.Cond{IfMatch: etag})
	if err != nil {
		if s.logs != nil {
			s.logs.add("[kiko] 备份格式升级未完成，继续使用原格式: " + err.Error())
		}
		return keys, hdr, hdr.Revision, etag
	}
	return newKeys, newHdr, revision, newEtag
}

func (s *server) syncLock() {
	s.sync.syncMu.Lock()
	s.syncDropKeys()
	s.sync.syncMu.Unlock()
	s.syncEmit()
}

// syncReset deletes the remote backup and clears this device's keys and sync
// baseline; the backend config is kept.
func (s *server) syncReset(ctx context.Context) error {
	sr := &s.sync
	sr.syncMu.Lock()
	defer sr.syncMu.Unlock()
	be, err := s.syncBackendFromConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err = be.Delete(ctx); err != nil {
		return err
	}
	s.syncDropKeys()
	sr.mu.Lock()
	exists := false
	sr.st.Header, sr.st.RemoteRevision, sr.st.RemoteEtag = nil, 0, ""
	sr.st.LastSynced, sr.st.LastError, sr.st.LastResult, sr.st.LastSyncedAt = map[string]string{}, "", nil, 0
	sr.st.VaultExists = &exists
	_ = s.syncSaveStateLocked()
	sr.mu.Unlock()
	s.syncEmit()
	return nil
}

// syncChangePassword syncs once, then re-encrypts the whole backup with the
// new password (new salt, same vaultId).
func (s *server) syncChangePassword(ctx context.Context, newPassword, confirm string) error {
	sr := &s.sync
	sr.syncMu.Lock()
	defer sr.syncMu.Unlock()
	if err := syncvault.CheckPassword(newPassword); err != nil {
		return err
	}
	if newPassword != confirm {
		return errSyncConfirm
	}
	sr.mu.Lock()
	keys := sr.keys
	sr.mu.Unlock()
	if keys == nil {
		return errSyncLocked
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err := s.syncOnceLocked(ctx); err != nil {
		return err
	}
	be, err := s.syncBackendFromConfig()
	if err != nil {
		return err
	}
	data, etag, err := be.Get(ctx)
	if err != nil {
		return err
	}
	plain, oldHdr, err := keys.Open(data)
	if err != nil {
		if errors.Is(err, syncvault.ErrWrongKey) {
			s.syncDropKeys()
		}
		return err
	}
	newKeys, newHdr, err := syncvault.Create(newPassword, keys.VaultID())
	if err != nil {
		return err
	}
	revision := oldHdr.Revision + 1
	env, err := newKeys.Seal(newHdr, revision, plain)
	if err != nil {
		return err
	}
	cond := syncbackend.Cond{IfMatch: etag}
	newEtag, err := be.Put(ctx, env, cond)
	if errors.Is(err, syncbackend.ErrConflict) {
		return syncbackend.ErrConflict
	}
	if err != nil {
		return err
	}
	sr.mu.Lock()
	lastSynced := sr.st.LastSynced
	sr.mu.Unlock()
	if err = s.syncInstallKeys(newKeys, newHdr, revision, newEtag, lastSynced); err != nil {
		return err
	}
	s.syncEmit()
	return nil
}

// syncLoop runs while auto sync is on and the vault is unlocked: it pulls on
// start, pushes local changes after a debounce, and pulls periodically,
// backing off exponentially on failure.
func (s *server) syncLoop(ctx context.Context) {
	sr := &s.sync
	timer := time.NewTimer(syncStartDelay)
	ticker := time.NewTicker(syncInterval)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-ticker.C:
		case <-sr.trigger:
		}
		sr.mu.Lock()
		ready := sr.cfg.AutoSync && sr.keys != nil && sr.cfg.Backend.Type != ""
		wait := time.Until(sr.nextTry)
		sr.mu.Unlock()
		if !ready {
			continue
		}
		if wait > 0 {
			resetTimer(timer, wait)
			continue
		}
		if _, err := s.syncOnce(ctx); err != nil {
			sr.mu.Lock()
			delay := time.Until(sr.nextTry)
			sr.mu.Unlock()
			resetTimer(timer, max(delay, time.Second))
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func entryKind(id string) string {
	if len(id) >= len(networkSyncPrefix) && id[:len(networkSyncPrefix)] == networkSyncPrefix {
		return "network"
	}
	return ""
}
