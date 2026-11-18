// Package store holds the document the console edits and keeps it in
// step with kiko-server's store (GET/PUT /api/v1/store).
//
// All methods run on the UI thread. Saves and loads run on goroutines and
// post their results back through env.Env.Post.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Store is the console's document and its saved profiles.
type Store struct {
	env *env.Env

	doc            *model.Doc
	profiles       []*model.Profile
	activeProfile  string
	networkConfigs []*model.NetworkConfig
	revision       uint64
	// platform is the host's, from GET /info; edits project onto it.
	platform string

	// Loaded reports the first load finished; LoadError why it failed.
	Loaded    bool
	LoadError string
	// LocalAddresses are the host's interface addresses.
	LocalAddresses []string

	version int
	editing int
	// editLibrary is the library when the outermost editor opened, and
	// editDirty whether the document changed while editing: a store that
	// changed remotely meanwhile is then merged instead of adopted.
	editLibrary   []*model.NetworkConfig
	editDirty     bool
	pendingRemote *api.ServerStore
	expectedEcho  uint64
	saving        bool
	saveAgain     bool
	// SaveError is the last save's failure, cleared by a successful one.
	SaveError string
	// LockWait is set while a save waits for another device's operation
	// (the server's write gate answered 423): "Waiting for X to finish", in Chinese.
	LockWait    string
	lockRetries int
	lockGen     int

	// Latency holds the last speed test's results by outbound id.
	Latency         map[string]api.LatencyResult
	LatencyProgress LatencyProgress
	// latencyGen counts the results written to Latency (LatencyGen).
	latencyGen int
	// latencyJobs are the outbounds with a test waiting or running by
	// id (Testing), latencyQueue those waiting, latencyActive the
	// running ones.
	latencyJobs map[string]*latencyJob
	// latencyBatches are the outbounds each batch entry asked to test
	// (MeasureBatch) whose tests have not ended yet, by the entry's key.
	latencyBatches map[string]map[string]bool
	latencyQueue   []*latencyJob
	latencyActive  int
	// latencyEpoch counts ResetLatency calls.
	latencyEpoch int
	// latencyHistory keeps each outbound's latest measured latencies,
	// oldest first (LatencyHistory).
	latencyHistory map[string][]float64
	// latencyHistoryMode is the Mode of the results in latencyHistory.
	latencyHistoryMode map[string]string

	listeners []func()
	loading   bool

	// applying an apply is in flight; ApplyError the last one's failure.
	applying   bool
	ApplyError string
	// interrupted is what the last live apply did to the connections of
	// the rules marked for interruption (InterruptOutcome).
	interrupted InterruptResult
	applied     appliedState
}

// LatencyProgress counts the results of the running test-all run
// (MeasureLatency); a test of some outbounds does not touch it.
type LatencyProgress struct {
	Completed, Total int
	Running          bool
}

// New returns an empty store. Call Load.
func New(e *env.Env) *Store {
	s := &Store{env: e, doc: model.EmptyDoc(""), Latency: map[string]api.LatencyResult{}}
	e.Server.Subscribe(func(event api.Event) {
		var remote api.ServerStore
		if event.Decode(&remote) == nil {
			s.handleRemote(&remote, false)
		}
	}, "store")
	e.Server.OnReconnect(func() {
		s.Resync()
		s.RefreshApplied()
	})
	return s
}

// Doc returns the current document, projected onto the host platform. Do
// not modify it: change it through Update.
func (s *Store) Doc() *model.Doc { return model.ProjectRuntimeDoc(s.doc, s.platform) }

// Version changes with every change of the document, for views that cache
// what they derive from it.
func (s *Store) Version() int { return s.version }

// Platform is the host's document platform.
func (s *Store) Platform() string { return s.platform }

// NetworkConfigs is the shared library of network and TLS configs.
func (s *Store) NetworkConfigs() []*model.NetworkConfig { return s.networkConfigs }

// Profiles are the saved profiles.
func (s *Store) Profiles() []*model.Profile { return s.profiles }

// ActiveProfileID is the profile the document belongs to.
func (s *Store) ActiveProfileID() string { return s.activeProfile }

// OnChange calls fn after every change of the document.
func (s *Store) OnChange(fn func()) { s.listeners = append(s.listeners, fn) }

func (s *Store) changed() {
	s.version++
	for _, fn := range s.listeners {
		fn()
	}
}

// Load reads the store from the server, completing and migrating it.
func (s *Store) Load() {
	if s.loading {
		return
	}
	s.loading = true
	type loaded struct {
		info      api.ServerInfo
		remote    api.ServerStore
		addresses []string
	}
	env.Go(s.env, func(ctx context.Context) (loaded, error) {
		var v loaded
		var err error
		if v.info, err = s.env.API.Info(ctx); err != nil {
			return v, err
		}
		if v.remote, err = s.env.API.Store(ctx); err != nil {
			return v, err
		}
		v.addresses, _ = s.env.API.LocalAddresses(ctx)
		return v, nil
	}, func(v loaded, err error) {
		s.loading = false
		if err != nil {
			s.LoadError = err.Error()
			return
		}
		s.LoadError = ""
		s.LocalAddresses = v.addresses
		s.platform = model.ServerPlatform(v.info.OS)
		s.revision = v.remote.Revision
		s.adopt(&v.remote, true)
		s.Loaded = true
		s.RefreshApplied()
		// A store event that arrived while the load was in flight was not
		// handled before Loaded: take it now when it is newer.
		if latest, ok := s.env.Server.Latest["store"]; ok {
			var remote api.ServerStore
			if latest.Decode(&remote) == nil {
				s.handleRemote(&remote, false)
			}
		}
	})
}

// Resync catches up with the server after its event stream connected
// again, as after a restart of the server: a store that never loaded loads,
// a save that failed meanwhile is written again (a conflict then adopts the
// server's store), and otherwise the server's store is read and handled as
// a remote change, which an open editor defers. UI thread.
func (s *Store) Resync() {
	switch {
	case !s.Loaded:
		s.Load()
	case s.saving:
		// The save in flight answers for itself.
	case s.SaveError != "":
		s.save()
	default:
		env.Go(s.env, func(ctx context.Context) (api.ServerStore, error) {
			return s.env.API.Store(ctx)
		}, func(remote api.ServerStore, err error) {
			if err == nil {
				s.handleRemote(&remote, false)
			}
		})
	}
}

// decodeProfiles reads profiles, dropping entries without an id or a
// document, and completes their documents.
func decodeProfiles(raw json.RawMessage) []*model.Profile { return model.DecodeProfiles(raw) }

// decodeDoc reads a version 1 document and completes it.
func decodeDoc(raw json.RawMessage) *model.Doc { return model.DecodeDoc(raw) }

func decodeLibrary(raw json.RawMessage) []*model.NetworkConfig {
	var library []*model.NetworkConfig
	_ = json.Unmarshal(raw, &library)
	return library
}

// adopt takes the server's store as the document, its profiles and the
// library, as adoptRemote and the first load do. first creates a profile
// when there is none.
func (s *Store) adopt(remote *api.ServerStore, first bool) {
	profiles := decodeProfiles(remote.Profiles)
	active := ""
	if remote.ActiveProfileID != nil {
		active = *remote.ActiveProfileID
	}
	active = pickActiveProfile(profiles, active)
	var doc *model.Doc
	for _, p := range profiles {
		if p.ID == active {
			doc = model.Clone(p.Doc)
		}
	}
	if doc == nil {
		doc = decodeDoc(remote.Doc)
	}
	if doc == nil {
		doc = model.EmptyDoc(s.platform)
	}
	persist := false
	if first && len(profiles) == 0 {
		profile := &model.Profile{ID: model.NewID("prof"), Name: "未命名配置", SavedAt: time.Now().UnixMilli(), Doc: model.Clone(doc)}
		profiles = []*model.Profile{profile}
		active = profile.ID
		persist = true
	}
	library := decodeLibrary(remote.NetworkConfigs)
	before := fingerprint(doc, profiles, library)
	doc, profiles = withDNSPresets(doc, profiles, active)
	doc, profiles, library = model.MigrateNetworkStore(doc, profiles, library, active)
	if fingerprint(doc, profiles, library) != before {
		persist = true
	}
	s.revision = remote.Revision
	s.pendingRemote = nil
	s.doc, s.profiles, s.networkConfigs, s.activeProfile = doc, profiles, library, active
	s.changed()
	if persist {
		s.save()
	}
}

// withDNSPresets completes the document and the profiles' documents with
// the DNS presets they lack (model.AddDNSPresets); the active profile
// takes the document's servers, so both keep the same ids.
func withDNSPresets(doc *model.Doc, profiles []*model.Profile, active string) (*model.Doc, []*model.Profile) {
	if doc != nil {
		doc = model.AddDNSPresets(doc)
	}
	var out []*model.Profile
	for i, p := range profiles {
		if p.Doc == nil || p.Doc.DNS.PresetsVersion >= model.DNSPresetsVersion {
			continue
		}
		if out == nil {
			out = slices.Clone(profiles)
		}
		copied := *p
		if p.ID == active && doc != nil {
			copied.Doc = model.Clone(doc)
		} else {
			copied.Doc = model.AddDNSPresets(p.Doc)
		}
		out[i] = &copied
	}
	if out == nil {
		out = profiles
	}
	return doc, out
}

func fingerprint(values ...any) string {
	raw, _ := json.Marshal(values)
	return string(raw)
}

func pickActiveProfile(profiles []*model.Profile, remote string) string {
	for _, p := range profiles {
		if p.ID == remote && remote != "" {
			return remote
		}
	}
	if len(profiles) > 0 {
		return profiles[0].ID
	}
	return ""
}

// handleRemote decides what to do with a store that changed elsewhere:
// ignore our own save's echo and older revisions, wait while an editor is
// open, and otherwise adopt it. It returns the decision.
func (s *Store) handleRemote(remote *api.ServerStore, conflict bool) string {
	if !s.Loaded {
		return "ignore"
	}
	decision := decideRemote(s.revision, s.expectedEcho, s.editing > 0, conflict, remote.Revision)
	switch decision {
	case "ignore":
	case "defer":
		if s.pendingRemote == nil || remote.Revision > s.pendingRemote.Revision {
			s.pendingRemote = remote
		}
	default:
		s.adopt(remote, false)
	}
	return decision
}

func decideRemote(local, expectedEcho uint64, editing, conflict bool, remote uint64) string {
	if remote <= local {
		return "ignore"
	}
	if !conflict && expectedEcho != 0 && remote == expectedEcho {
		return "ignore"
	}
	if editing {
		return "defer"
	}
	return "adopt"
}

// BeginEdit marks an editor open: changes of the server's store wait until
// EndEdit, so they do not replace what the user is editing.
func (s *Store) BeginEdit() {
	if s.editing == 0 {
		s.editLibrary = model.Clone(s.networkConfigs)
		s.editDirty = false
	}
	s.editing++
}

// EndEdit closes an editor. A store that changed meanwhile is adopted, or,
// when the document was changed while editing, merged: the edited profile
// stays local, other profiles and the library follow the server, keeping
// configs added locally.
func (s *Store) EndEdit() {
	if s.editing > 0 {
		s.editing--
	}
	if s.editing > 0 || s.pendingRemote == nil {
		return
	}
	pending := s.pendingRemote
	s.pendingRemote = nil
	if !s.editDirty {
		s.adopt(pending, false)
		return
	}
	s.networkConfigs = model.RebaseNetworkConfigs(s.editLibrary, s.networkConfigs, decodeLibrary(pending.NetworkConfigs))
	s.profiles = rebaseProfiles(s.profiles, s.activeProfile, decodeProfiles(pending.Profiles))
	s.revision = pending.Revision
	s.editLibrary, s.editDirty = nil, false
	s.changed()
	s.save()
}

// rebaseProfiles merges profiles after a remote change: the active one
// stays local, the others follow remote, and profiles only local remain.
func rebaseProfiles(local []*model.Profile, activeID string, remote []*model.Profile) []*model.Profile {
	byID := map[string]*model.Profile{}
	for _, p := range local {
		byID[p.ID] = p
	}
	var merged []*model.Profile
	remoteIDs := map[string]bool{}
	for _, p := range remote {
		remoteIDs[p.ID] = true
		if p.ID == activeID && byID[p.ID] != nil {
			merged = append(merged, byID[p.ID])
		} else {
			merged = append(merged, p)
		}
	}
	for _, p := range local {
		if !remoteIDs[p.ID] {
			merged = append(merged, p)
		}
	}
	return merged
}

// Editing reports whether an editor is open.
func (s *Store) Editing() bool { return s.editing > 0 }

// ErrDependencies is the reason Update refused a change.
var ErrDependencies = errors.New("配置引用不完整")

// Update changes the document through fn, which receives a copy to modify,
// and saves it. A change that breaks the configuration's dependencies is
// refused with the first problem as the error.
func (s *Store) Update(fn func(doc *model.Doc)) error {
	next := model.Clone(s.Doc())
	fn(next)
	// Edits use the host's defaults; the stored document keeps its own
	// platform.
	next.Platform = s.doc.Platform
	// Inline network and TLS fields move into the shared library.
	next, library := model.MigrateNetworkDoc(next, s.networkConfigs, model.DefaultNetworkProfileName)
	if problems := emit.Emit(next, emit.Options{NetworkConfigs: library}).DependencyErrors; len(problems) > 0 {
		return fmt.Errorf("%w：%s", ErrDependencies, problems[0])
	}
	if len(library) != len(s.networkConfigs) {
		s.networkConfigs = library
	}
	s.doc = next
	s.markDirty()
	s.syncActiveProfile()
	s.changed()
	s.save()
	return nil
}

// syncActiveProfile keeps the active profile's copy current.
func (s *Store) syncActiveProfile() {
	for i, p := range s.profiles {
		if p.ID == s.activeProfile {
			copied := *p
			copied.Doc = model.Clone(s.doc)
			copied.SavedAt = time.Now().UnixMilli()
			s.profiles[i] = &copied
		}
	}
}

// SetProfiles replaces the saved profiles and the active one, and saves.
// A doc, when given, becomes the document.
func (s *Store) SetProfiles(profiles []*model.Profile, activeID string, doc *model.Doc) {
	if doc != nil {
		doc = model.Clone(doc)
	}
	doc, profiles = withDNSPresets(doc, profiles, activeID)
	s.profiles = profiles
	if s.activeProfile != "" && activeID != s.activeProfile {
		s.ClearInterrupts()
	}
	s.activeProfile = activeID
	if doc != nil {
		s.doc = doc
	}
	s.markDirty()
	s.changed()
	s.save()
}

// markDirty records a change made while an editor is open, so that a
// remote store deferred meanwhile is merged instead of replacing it.
func (s *Store) markDirty() {
	if s.editing > 0 {
		s.editDirty = true
	}
}

// SetNetworkConfigs replaces the library and saves it.
func (s *Store) SetNetworkConfigs(library []*model.NetworkConfig) {
	s.networkConfigs = library
	s.markDirty()
	s.changed()
	s.save()
}

// Saving reports a save in flight.
func (s *Store) Saving() bool { return s.saving }

// save writes the store at its revision. Saves do not overlap: a change
// during a save is written after it.
func (s *Store) save() {
	if s.saving {
		s.saveAgain = true
		return
	}
	if s.pendingRemote != nil && s.editing > 0 {
		return
	}
	s.saving = true
	s.lockGen++ // a retry still waiting is this save now
	value, err := s.serverStore()
	if err != nil {
		s.saving = false
		s.SaveError = err.Error()
		return
	}
	s.expectedEcho = s.revision + 1
	env.Go(s.env, func(ctx context.Context) (api.ServerStore, error) {
		return s.env.API.SaveStore(ctx, value)
	}, func(saved api.ServerStore, err error) {
		s.saving = false
		s.expectedEcho = 0
		var apiErr *api.Error
		switch {
		case err == nil:
			// A newer store adopted while the save was in flight keeps
			// its revision.
			s.revision = max(s.revision, saved.Revision)
			s.SaveError = ""
			s.lockRetries, s.LockWait = 0, ""
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusLocked && s.lockRetries < maxLockRetries:
			// Another device holds the server's write gate: keep the
			// changes and try again after 1, 2, 4… seconds.
			delay := lockBackoff << s.lockRetries
			s.lockRetries++
			s.LockWait = lockWaitText(apiErr)
			s.lockGen++
			gen := s.lockGen
			s.saveAgain = false // the retry writes the latest document
			go func() {
				time.Sleep(delay)
				s.env.Post(func() {
					if gen == s.lockGen && !s.saving {
						s.save()
					}
				})
			}()
			return
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict:
			// The reply is the server's current store: catch up and adopt it.
			s.SaveError = ""
			s.lockRetries, s.LockWait = 0, ""
			decision := ""
			var current api.ServerStore
			if json.Unmarshal(apiErr.Body, &current) == nil {
				decision = s.handleRemote(&current, true)
			}
			if decision == "ignore" && s.saveAgain {
				// The store already caught up with that state meanwhile
				// (an editor's merge): write what changed since.
				s.saveAgain = false
				s.save()
				return
			}
			s.saveAgain = false
			s.env.Toast("配置已在其他位置更新，已载入最新版本")
			return
		default:
			s.lockRetries, s.LockWait = 0, ""
			s.SaveError = err.Error()
			s.env.Toast("保存失败：" + err.Error())
		}
		if s.saveAgain {
			s.saveAgain = false
			s.save()
		}
	})
}

// Saves refused by the write gate are retried maxLockRetries times,
// waiting lockBackoff, then twice as long each time.
const maxLockRetries = 5

var lockBackoff = time.Second

// lockWaitText names who the save waits for.
func lockWaitText(err *api.Error) string {
	if err.Lock != nil && err.Lock.Holder.Name != "" {
		return "等待「" + err.Lock.Holder.Name + "」完成操作"
	}
	return "等待其他设备完成操作"
}

func (s *Store) serverStore() (api.ServerStore, error) {
	doc, err := json.Marshal(s.doc)
	if err != nil {
		return api.ServerStore{}, err
	}
	profiles := s.profiles
	if profiles == nil {
		profiles = []*model.Profile{}
	}
	rawProfiles, err := json.Marshal(profiles)
	if err != nil {
		return api.ServerStore{}, err
	}
	library := s.networkConfigs
	if library == nil {
		library = []*model.NetworkConfig{}
	}
	rawLibrary, err := json.Marshal(library)
	if err != nil {
		return api.ServerStore{}, err
	}
	value := api.ServerStore{Revision: s.revision, Doc: doc, Profiles: rawProfiles, NetworkConfigs: rawLibrary}
	if s.activeProfile != "" {
		active := s.activeProfile
		value.ActiveProfileID = &active
	}
	return value, nil
}

// Emit builds the configuration of the current document for the host.
func (s *Store) Emit() emit.Result {
	return emit.Emit(s.Doc(), emit.Options{NetworkConfigs: s.networkConfigs, LocalAddresses: s.LocalAddresses})
}

// Apply emits the document with fresh local addresses and sends it to the
// core, restarting its services (SIGHUP); done receives the outcome on the
// UI thread.
func (s *Store) Apply(done func(error)) { s.apply(false, done) }

// ApplyLive applies the document to the running core without restarting
// it. Changes that need a restart fail with *api.RestartRequiredError.
func (s *Store) ApplyLive(done func(error)) { s.apply(true, done) }

// Applying reports an apply in flight.
func (s *Store) Applying() bool { return s.applying }

func (s *Store) apply(live bool, done func(error)) {
	if s.applying {
		return
	}
	doc := s.Doc()
	library := s.networkConfigs
	disableUDPDirect := tailscaleUDPDirectDisabled(doc)
	s.applying, s.ApplyError = true, ""
	// The rules whose connections this apply closes, once the core runs
	// the new configuration.
	var interrupt, stale []pendingInterrupt
	var devices, staleDevices []pendingProfile
	if live {
		interrupt, stale = validInterrupts(doc, s.loadInterrupts(), s.appliedExits())
		devices, staleDevices = validProfileInterrupts(doc, s.loadProfileMarks(), s.appliedScopes())
	}
	// The profile switches this apply carries, named if it fails.
	switches := s.SwitchedProfileChanges(doc)
	var outcome InterruptResult
	var listFailed bool
	// The addresses the configuration is emitted with are kept, so the
	// unapplied changes compare against the same ones.
	var addresses []string
	env.Go(s.env, func(ctx context.Context) (api.ApplyResult, error) {
		var err error
		addresses, err = s.env.API.LocalAddresses(ctx)
		if err != nil {
			addresses = s.LocalAddresses
		}
		if problem := model.TailscaleLimitError(doc); problem != "" {
			return api.ApplyResult{}, errors.New(problem)
		}
		result := emit.Emit(doc, emit.Options{NetworkConfigs: library, LocalAddresses: addresses})
		if len(result.Errors) > 0 {
			return api.ApplyResult{}, errors.New(strings.Join(result.Errors, "；"))
		}
		options := api.ApplyOptions{DisableTailscaleUDPDirect: disableUDPDirect, RuleMap: result.RuleMap, DNSRuleMap: result.DNSRuleMap}
		if !live {
			return s.env.API.Apply(ctx, result.JSON, options)
		}
		// The connections are listed before the apply, while they still go
		// where the old rule sent them; those that survive it close after.
		ids, listErr := s.ruleConnections(ctx, interrupt)
		var scopes []string
		for _, p := range devices {
			scopes = append(scopes, p.from)
		}
		if listErr == nil {
			var scopeIDs []string
			scopeIDs, listErr = s.scopeConnections(ctx, scopes)
			ids = append(ids, scopeIDs...)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		applied, err := s.env.API.ApplyLive(ctx, result.JSON, options)
		if err != nil || len(interrupt)+len(devices) == 0 {
			return applied, err
		}
		if listErr != nil {
			outcome.Error = listErr.Error()
			listFailed = true
			return applied, nil
		}
		closed, closeErr := s.env.API.CloseConnectionIDs(ctx, ids)
		outcome.Closed = closed.Closed
		if closeErr != nil {
			outcome.Error = closeErr.Error()
		}
		return applied, nil
	}, func(result api.ApplyResult, err error) {
		s.applying = false
		if err == nil {
			s.NoteApplied(doc)
		}
		if err == nil && live {
			// Applied: the marks are spent, whether or not the close worked.
			s.interrupted = outcome
			s.spendInterrupts(stale)
			s.spendProfileInterrupts(staleDevices)
			if !listFailed {
				s.spendInterrupts(interrupt)
				s.spendProfileInterrupts(devices)
			}
		}
		if !slices.Equal(addresses, s.LocalAddresses) && addresses != nil {
			s.LocalAddresses = addresses
			s.changed()
		}
		if err == nil && result.Runtime != nil {
			s.env.Server.SetRuntime(*result.Runtime)
		}
		var restart *api.RestartRequiredError
		if err != nil && !errors.As(err, &restart) {
			if len(switches) > 0 {
				err = fmt.Errorf("%s未能应用，核心仍使用原配置：%w", strings.Join(switches, "、"), err)
			}
			s.ApplyError = err.Error()
		}
		s.RefreshApplied()
		if done != nil {
			done(err)
		}
	})
}

func tailscaleUDPDirectDisabled(doc *model.Doc) bool {
	for _, e := range doc.Endpoints {
		if e.Enabled && e.Type == "tailscale" && e.Fields["disable_udp_direct"] == true {
			return true
		}
	}
	return false
}

// UseLocalAddresses keeps the addresses a configuration was emitted with
// for an apply, so the unapplied changes compare against the same ones.
func (s *Store) UseLocalAddresses(addresses []string) {
	if addresses != nil && !slices.Equal(addresses, s.LocalAddresses) {
		s.LocalAddresses = addresses
		s.changed()
	}
}

// RefreshLocalAddresses reloads the host's interface addresses.
func (s *Store) RefreshLocalAddresses() {
	env.Go(s.env, func(ctx context.Context) ([]string, error) {
		return s.env.API.LocalAddresses(ctx)
	}, func(addresses []string, err error) {
		if err == nil {
			s.LocalAddresses = addresses
			s.changed()
		}
	})
}
