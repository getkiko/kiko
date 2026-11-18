package main

import (
	"encoding/json"
	"time"
)

type mergeProfile struct {
	Kind      string
	ID        string
	Name      string
	Doc       json.RawMessage
	UpdatedAt int64
	Hash      string
}

type mergeInput struct {
	Local, Remote                     []mergeProfile
	LocalTombstones, RemoteTombstones []syncTombstone
	LastSynced                        map[string]string
	Now                               time.Time
	NewID                             func() string
	// ConflictID, when set, names a conflict copy of loser instead of
	// NewID, and ConflictAt dates it instead of Now: merging the same input
	// again then yields the same copy, which is not added twice.
	ConflictID func(loser mergeProfile) string
	ConflictAt time.Time
}

type mergeOutput struct {
	Profiles                    []mergeProfile
	Tombstones                  []syncTombstone
	Result                      syncResult
	LocalChanged, RemoteChanged bool
}

func indexProfiles(list []mergeProfile) map[string]mergeProfile {
	out := make(map[string]mergeProfile, len(list))
	for _, p := range list {
		out[p.ID] = p
	}
	return out
}

func indexTombstones(list []syncTombstone) map[string]int64 {
	out := make(map[string]int64, len(list))
	for _, t := range list {
		if t.DeletedAt > out[t.ID] {
			out[t.ID] = t.DeletedAt
		}
	}
	return out
}

// mergeProfiles does a three-way merge by profile id. If only one side changed
// since the last sync, that side wins; if both changed, the newer one wins and
// the other is kept as a conflict copy.
func mergeProfiles(in mergeInput) mergeOutput {
	local, remote := indexProfiles(in.Local), indexProfiles(in.Remote)
	localTombs, remoteTombs := indexTombstones(in.LocalTombstones), indexTombstones(in.RemoteTombstones)
	order := make([]string, 0, len(in.Local)+len(in.Remote))
	for _, p := range in.Local {
		order = append(order, p.ID)
	}
	for _, p := range in.Remote {
		if _, ok := local[p.ID]; !ok {
			order = append(order, p.ID)
		}
	}

	var out mergeOutput
	kept := map[string]bool{}
	keep := func(p mergeProfile) {
		out.Profiles = append(out.Profiles, p)
		kept[p.ID] = true
	}
	for _, id := range order {
		l, hasLocal := local[id]
		r, hasRemote := remote[id]
		switch {
		case hasLocal && hasRemote:
			if l.Hash == r.Hash {
				keep(l)
				continue
			}
			last := in.LastSynced[id]
			localChanged, remoteChanged := l.Hash != last, r.Hash != last
			switch {
			case localChanged && !remoteChanged:
				keep(l)
				out.Result.Pushed++
			case !localChanged && remoteChanged:
				keep(r)
				out.Result.Pulled++
			default:
				winner, loser := l, r
				if r.UpdatedAt > l.UpdatedAt {
					winner, loser = r, l
				}
				keep(winner)
				at := in.Now
				var id string
				if in.ConflictID != nil {
					at, id = in.ConflictAt, in.ConflictID(loser)
				} else {
					id = in.NewID()
				}
				id = conflictEntryID(loser, id)
				_, inLocal := local[id]
				_, inRemote := remote[id]
				if !inLocal && !inRemote && !kept[id] {
					name := loser.Name + " (冲突副本 " + at.Format("2006-01-02 15:04") + ")"
					keep(mergeProfile{
						ID: id, Kind: loser.Kind, Name: name, Doc: loser.Doc, UpdatedAt: at.UnixMilli(),
						Hash: syncProfileHash(name, loser.Doc),
					})
				}
				out.Result.Conflicts++
			}
		case hasLocal:
			if t, ok := remoteTombs[id]; ok && t >= l.UpdatedAt {
				out.Result.Deleted++
				continue
			}
			keep(l)
			out.Result.Pushed++
		default:
			if t, ok := localTombs[id]; ok && t > r.UpdatedAt {
				out.Result.Deleted++
				continue
			}
			keep(r)
			out.Result.Pulled++
		}
	}

	tombs := map[string]int64{}
	for id, at := range localTombs {
		tombs[id] = at
	}
	for id, at := range remoteTombs {
		if at > tombs[id] {
			tombs[id] = at
		}
	}
	for id, at := range tombs {
		if kept[id] || in.Now.Sub(time.UnixMilli(at)) > syncTombstoneTTL {
			delete(tombs, id)
		}
	}
	out.Tombstones = sortedTombstones(tombs)
	out.LocalChanged = !sameProfileSet(out.Profiles, in.Local) || !sameTombstones(out.Tombstones, in.LocalTombstones)
	out.RemoteChanged = !sameProfileSet(out.Profiles, in.Remote) || !sameTombstones(out.Tombstones, in.RemoteTombstones)
	return out
}

func sameProfileSet(a, b []mergeProfile) bool {
	if len(a) != len(b) {
		return false
	}
	other := indexProfiles(b)
	for _, p := range a {
		if q, ok := other[p.ID]; !ok || q.Hash != p.Hash {
			return false
		}
	}
	return true
}

func sameTombstones(a, b []syncTombstone) bool {
	left, right := indexTombstones(a), indexTombstones(b)
	if len(left) != len(right) {
		return false
	}
	for id, at := range left {
		if right[id] != at {
			return false
		}
	}
	return true
}

func conflictEntryID(source mergeProfile, id string) string {
	if source.Kind == "network" {
		return networkSyncPrefix + id
	}
	return id
}
