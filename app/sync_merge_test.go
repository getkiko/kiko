package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mp(id, name string, updatedAt int64) mergeProfile {
	doc := json.RawMessage(`{"version":1,"n":"` + name + `"}`)
	return mergeProfile{ID: id, Name: name, Doc: doc, UpdatedAt: updatedAt, Hash: syncProfileHash(name, doc)}
}

func mpDoc(id, name, docText string, updatedAt int64) mergeProfile {
	doc := json.RawMessage(docText)
	return mergeProfile{ID: id, Name: name, Doc: doc, UpdatedAt: updatedAt, Hash: syncProfileHash(name, doc)}
}

func mergeIDs(out mergeOutput) map[string]string {
	m := map[string]string{}
	for _, p := range out.Profiles {
		m[p.ID] = p.Name
	}
	return m
}

func TestMergeProfiles(t *testing.T) {
	now := time.UnixMilli(2_000_000_000_000)
	nextID := func() string { return "prof_copy" }
	base := mp("a", "A", 100)
	changedLocal := mpDoc("a", "A", `{"version":1,"edited":"local"}`, 300)
	changedRemote := mpDoc("a", "A", `{"version":1,"edited":"remote"}`, 200)
	tests := []struct {
		name       string
		in         mergeInput
		wantNames  map[string]string
		wantResult syncResult
		wantLocal  bool
		wantRemote bool
	}{
		{
			name:       "identical sets change nothing",
			in:         mergeInput{Local: []mergeProfile{base}, Remote: []mergeProfile{base}, LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{},
		},
		{
			name:       "profile only local is pushed",
			in:         mergeInput{Local: []mergeProfile{base}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pushed: 1}, wantRemote: true,
		},
		{
			name:       "profile only remote is pulled",
			in:         mergeInput{Remote: []mergeProfile{base}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pulled: 1}, wantLocal: true,
		},
		{
			name: "only local changed since last sync",
			in: mergeInput{Local: []mergeProfile{changedLocal}, Remote: []mergeProfile{base},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pushed: 1}, wantRemote: true,
		},
		{
			name: "only remote changed since last sync",
			in: mergeInput{Local: []mergeProfile{base}, Remote: []mergeProfile{changedRemote},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pulled: 1}, wantLocal: true,
		},
		{
			name: "both changed keeps newer and saves a conflict copy",
			in: mergeInput{Local: []mergeProfile{changedLocal}, Remote: []mergeProfile{changedRemote},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A", "prof_copy": "A (冲突副本 " + now.Format("2006-01-02 15:04") + ")"},
			wantResult: syncResult{Conflicts: 1}, wantLocal: true, wantRemote: true,
		},
		{
			name:       "never synced but different content is a conflict",
			in:         mergeInput{Local: []mergeProfile{changedLocal}, Remote: []mergeProfile{changedRemote}},
			wantNames:  map[string]string{"a": "A", "prof_copy": "A (冲突副本 " + now.Format("2006-01-02 15:04") + ")"},
			wantResult: syncResult{Conflicts: 1}, wantLocal: true, wantRemote: true,
		},
		{
			name: "remote tombstone newer than local edit deletes locally",
			in: mergeInput{Local: []mergeProfile{base}, RemoteTombstones: []syncTombstone{{ID: "a", DeletedAt: now.UnixMilli() - 1000}},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{},
			wantResult: syncResult{Deleted: 1}, wantLocal: true,
		},
		{
			name: "local edit newer than remote tombstone resurrects",
			in: mergeInput{Local: []mergeProfile{changedLocal}, RemoteTombstones: []syncTombstone{{ID: "a", DeletedAt: 200}},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pushed: 1}, wantRemote: true,
		},
		{
			name: "local tombstone newer than remote copy deletes remotely",
			in: mergeInput{Remote: []mergeProfile{base}, LocalTombstones: []syncTombstone{{ID: "a", DeletedAt: now.UnixMilli() - 1000}},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{},
			wantResult: syncResult{Deleted: 1}, wantRemote: true,
		},
		{
			name: "remote edit newer than local tombstone is pulled back",
			in: mergeInput{Remote: []mergeProfile{changedRemote}, LocalTombstones: []syncTombstone{{ID: "a", DeletedAt: 150}},
				LastSynced: map[string]string{"a": base.Hash}},
			wantNames:  map[string]string{"a": "A"},
			wantResult: syncResult{Pulled: 1}, wantLocal: true, wantRemote: false,
		},
		{
			name:       "same name different ids are both kept",
			in:         mergeInput{Local: []mergeProfile{mp("a", "家里", 100)}, Remote: []mergeProfile{mp("b", "家里", 100)}},
			wantNames:  map[string]string{"a": "家里", "b": "家里"},
			wantResult: syncResult{Pushed: 1, Pulled: 1}, wantLocal: true, wantRemote: true,
		},
		{
			name: "renaming propagates because the name is part of the hash",
			in: mergeInput{Local: []mergeProfile{mp("a", "新名字", 300)}, Remote: []mergeProfile{mp("a", "旧名字", 100)},
				LastSynced: map[string]string{"a": mp("a", "旧名字", 100).Hash}},
			wantNames:  map[string]string{"a": "新名字"},
			wantResult: syncResult{Pushed: 1}, wantRemote: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Now, tt.in.NewID = now, nextID
			if tt.in.LastSynced == nil {
				tt.in.LastSynced = map[string]string{}
			}
			out := mergeProfiles(tt.in)
			got := mergeIDs(out)
			if len(got) != len(tt.wantNames) {
				t.Fatalf("profiles=%v want %v", got, tt.wantNames)
			}
			for id, name := range tt.wantNames {
				if got[id] != name {
					t.Fatalf("profiles=%v want %v", got, tt.wantNames)
				}
			}
			if out.Result != tt.wantResult {
				t.Fatalf("result=%+v want %+v", out.Result, tt.wantResult)
			}
			if out.LocalChanged != tt.wantLocal || out.RemoteChanged != tt.wantRemote {
				t.Fatalf("local=%v remote=%v want local=%v remote=%v", out.LocalChanged, out.RemoteChanged, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}

func TestMergeConflictWinnerIsTheNewerSide(t *testing.T) {
	now := time.UnixMilli(2_000_000_000_000)
	local := mpDoc("a", "A", `{"who":"local"}`, 100)
	remote := mpDoc("a", "A", `{"who":"remote"}`, 900)
	out := mergeProfiles(mergeInput{Local: []mergeProfile{local}, Remote: []mergeProfile{remote}, Now: now, NewID: func() string { return "c" },
		LastSynced: map[string]string{"a": "old"}})
	if len(out.Profiles) != 2 || !strings.Contains(string(out.Profiles[0].Doc), "remote") || !strings.Contains(string(out.Profiles[1].Doc), "local") {
		t.Fatalf("winner/loser wrong: %+v", out.Profiles)
	}
	if out.Profiles[1].Hash != syncProfileHash(out.Profiles[1].Name, out.Profiles[1].Doc) {
		t.Fatal("conflict copy hash must match its own name and doc")
	}
}

func TestMergeTombstonesPrunedAfterTTLAndDroppedWhenProfileExists(t *testing.T) {
	now := time.UnixMilli(2_000_000_000_000)
	old := now.Add(-syncTombstoneTTL - time.Hour).UnixMilli()
	recent := now.Add(-time.Hour).UnixMilli()
	out := mergeProfiles(mergeInput{
		Local:           []mergeProfile{mp("alive", "A", 100)},
		LocalTombstones: []syncTombstone{{ID: "stale", DeletedAt: old}, {ID: "gone", DeletedAt: recent}, {ID: "alive", DeletedAt: 50}},
		Now:             now, NewID: func() string { return "x" }, LastSynced: map[string]string{},
	})
	if len(out.Tombstones) != 1 || out.Tombstones[0].ID != "gone" {
		t.Fatalf("tombstones=%+v", out.Tombstones)
	}
}

func TestMergeProfilesPreservesDeviceLabels(t *testing.T) {
	doc := json.RawMessage(`{"route":{"deviceLabels":{"mac:02:11:22:33:44:55":{"alias":"客厅电视","hostname":"TV"}},"rules":[{"match":{"source_mac_address":["02:11:22:33:44:55"]}}],"dhcpLeaseFiles":["/tmp/dhcp.leases"]}}`)
	profile := mergeProfile{ID: "devices", Name: "路由器", Doc: doc, UpdatedAt: 100, Hash: syncProfileHash("路由器", doc)}
	output := mergeProfiles(mergeInput{Remote: []mergeProfile{profile}, Now: time.Now(), NewID: func() string { return "copy" }})
	if len(output.Profiles) != 1 || string(output.Profiles[0].Doc) != string(doc) {
		t.Fatalf("device metadata must survive profile sync: %+v", output.Profiles)
	}
}
