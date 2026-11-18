package main

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
)

// Rule hits count new connections and each repeated refusal, not a
// snapshot's; a connection keeps its peak rates; refusals have their own
// tab.
func TestConnectionHistoryHitsPeaksAndRefusals(t *testing.T) {
	h := &connectionHistory{resolveRule: func(rule string) connRule {
		if rule == "final" {
			return connRule{Label: "默认出口", Final: true, Owner: "dev", set: true}
		}
		return connRule{Label: rule, ID: rule, set: true}
	}}
	now := time.Now()
	event := func(typ daemon.ConnectionEventType, id string, conn *daemon.Connection) *daemon.ConnectionEvent {
		return &daemon.ConnectionEvent{Type: typ, Id: id, Connection: conn}
	}
	h.ingest("run", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{
		event(daemon.ConnectionEventType_CONNECTION_EVENT_NEW, "old", &daemon.Connection{Id: "old", Rule: "r1"}),
	}}, now)
	if hits := h.takeHits(); len(hits) != 0 {
		t.Fatalf("snapshot hits %v", hits)
	}
	h.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{
		event(daemon.ConnectionEventType_CONNECTION_EVENT_NEW, "a", &daemon.Connection{Id: "a", Rule: "r1"}),
		event(daemon.ConnectionEventType_CONNECTION_EVENT_NEW, "b", &daemon.Connection{Id: "b", Rule: "final"}),
		event(daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, "x", &daemon.Connection{Id: "x", Rule: "r2", RejectCount: 1, ClosedAt: now.UnixMilli()}),
		{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "a", UplinkDelta: 300, DownlinkDelta: 900},
	}}, now)
	h.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{
		event(daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, "x", &daemon.Connection{Id: "x", Rule: "r2", RejectCount: 3, ClosedAt: now.UnixMilli()}),
		{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "a", UplinkDelta: 100, DownlinkDelta: 50},
	}}, now)
	hits := h.takeHits()
	if hits["r1"] != 1 || hits["final:dev"] != 1 || hits["r2"] != 3 || len(hits) != 3 {
		t.Fatalf("hits %v", hits)
	}
	row := h.queryRow("a", now)
	if row == nil || row.PeakUp != 300 || row.PeakDown != 900 || row.UpRate != 100 {
		t.Fatalf("peaks %+v", row)
	}
	rows, _ := h.queryRows(now)
	page := queryConnectionPage(rows, nil, nil, connQuery{State: "rejected", Group: groupNone, Limit: 50})
	if page.Counts.Rejected != 1 || page.Counts.Closed != 0 || page.Counts.Active != 3 || page.Total != 1 {
		t.Fatalf("counts %+v total %d", page.Counts, page.Total)
	}
}

// Rule rates sum the open connections' last rates by rule; closed and
// stale ones move nothing.
func TestConnectionHistoryRuleRates(t *testing.T) {
	h := &connectionHistory{resolveRule: func(rule string) connRule { return connRule{Label: rule, ID: rule, set: true} }}
	now := time.Now()
	newConn := func(id, rule string) *daemon.ConnectionEvent {
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: &daemon.Connection{Id: id, Rule: rule}}
	}
	update := func(id string, up, down int64) *daemon.ConnectionEvent {
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: id, UplinkDelta: up, DownlinkDelta: down}
	}
	h.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{
		newConn("a", "r1"), newConn("b", "r1"), newConn("c", "r2"), newConn("idle", "r3"),
		update("a", 100, 1000), update("b", 0, 500), update("c", 40, 60),
	}}, now)
	rates := h.ruleRates(now)
	if rates["r1"] != 1600 || rates["r2"] != 100 || len(rates) != 2 {
		t.Fatalf("rates %v", rates)
	}
	if late := h.ruleRates(now.Add(3 * time.Second)); len(late) != 0 {
		t.Fatalf("stale rates %v", late)
	}
}
