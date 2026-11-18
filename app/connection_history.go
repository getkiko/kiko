package main

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/proto"
)

// connectionHistory belongs to kiko-server. It survives core restarts and UI queries.
type connectionHistory struct {
	mu   sync.Mutex
	run  string
	rows map[string]*connectionRecord
	// cleared holds active connections whose records were cleared: their
	// updates stay hidden until their close event or a new run.
	cleared map[string]struct{}
	// closedLimit is how many closed connections the history keeps, the
	// most recently closed; 0 keeps them all.
	closedLimit int
	// revision counts the changes of the history, for the ETag of queries.
	revision uint64
	// resolveRule names a matched rule by the applied configuration's rule
	// map; nil keeps the core's rule without its index.
	resolveRule func(string) connRule
	// hits counts the connections each route table rule took since the
	// last takeHits, by ruleHitKey: new connections and refusals.
	hits map[string]int
	// rejectCounts are the refusals counted of each refused connection,
	// kept past clearing and trimming, so that a refusal sent again with
	// a larger count adds only the new ones.
	rejectCounts map[string]int64
}

// ruleHitKey keys a rule's hits: the rule's ID, or "final:<owner>" for a
// device's default outbound; "" for a rule outside the route table.
func ruleHitKey(rule connRule) string {
	switch {
	case rule.ID != "":
		return rule.ID
	case rule.Final:
		return "final:" + rule.Owner
	}
	return ""
}

// takeHits returns the hits counted since the last call and starts over.
func (h *connectionHistory) takeHits() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	hits := h.hits
	h.hits = nil
	return hits
}

// ruleRates sums the rates of the open connections updated within
// rateTimeout by rule (ruleHitKey): bytes per second, up and down
// together, nil when none moves.
func (h *connectionHistory) ruleRates(now time.Time) map[string]float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var rates map[string]float64
	for _, r := range h.rows {
		if r.conn.GetClosedAt() > 0 || now.Sub(r.updatedAt) > rateTimeout || r.upRate+r.downRate <= 0 {
			continue
		}
		key := ruleHitKey(r.matchedRule())
		if key == "" {
			continue
		}
		if rates == nil {
			rates = map[string]float64{}
		}
		rates[key] += r.upRate + r.downRate
	}
	return rates
}

// hitLocked counts n hits of rule.
func (h *connectionHistory) hitLocked(rule connRule, n int) {
	key := ruleHitKey(rule)
	if key == "" || n <= 0 {
		return
	}
	if h.hits == nil {
		h.hits = map[string]int{}
	}
	h.hits[key] += n
}

// connectionRecord is a connection of the history with its last rates.
type connectionRecord struct {
	conn *daemon.Connection
	// run is the core run that reported the connection.
	run string
	// upRate and downRate are the bytes of the last update, sent every
	// second, so bytes per second.
	upRate, downRate float64
	// peakUp and peakDown are the highest rates the connection had.
	peakUp, peakDown float64
	updatedAt        time.Time
	// closeReason tells why the history closed the record itself.
	closeReason string
	// rule is the matched rule, named when the connection arrived.
	rule connRule
}

// ruleOf names a rule the core reported.
func (h *connectionHistory) ruleOf(rule string) connRule {
	if h.resolveRule == nil {
		return connRule{Label: stripConnRuleIndex(rule), set: true}
	}
	return h.resolveRule(rule)
}

// matchedRule is the record's rule, named from the raw rule when unset.
func (r *connectionRecord) matchedRule() connRule {
	if r.rule.set {
		return r.rule
	}
	return connRule{Label: stripConnRuleIndex(r.conn.GetRule()), set: true}
}

// closeReasonCoreRestart marks the connections the history closed because
// the core run that reported them ended.
const closeReasonCoreRestart = "core-restart"

// rateTimeout is how long a rate holds without a new update.
const rateTimeout = 2 * time.Second

// defaultClosedConnectionLimit is closedLimit until the user sets one.
const defaultClosedConnectionLimit = 1000

// closedSlack is how far closed records may pass limit before the oldest
// go, so that a busy host trims in batches rather than on every close.
func closedSlack(limit int) int { return max(limit/20, 10) }

func cloneConnection(conn *daemon.Connection) *daemon.Connection {
	return proto.Clone(conn).(*daemon.Connection)
}

func (h *connectionHistory) ingest(run string, frame *daemon.ConnectionEvents, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rows == nil {
		h.rows = make(map[string]*connectionRecord)
	}
	changed := false
	if frame.GetReset_() || h.run != "" && h.run != run {
		restarted := h.run != "" && h.run != run
		present := make(map[string]bool, len(frame.GetEvents()))
		if !restarted {
			for _, event := range frame.GetEvents() {
				present[event.GetId()] = true
			}
		}
		// Cleared connections missing from the new snapshot have ended.
		for id := range h.cleared {
			if !present[id] {
				delete(h.cleared, id)
			}
		}
		for id, record := range h.rows {
			if record.conn.GetClosedAt() > 0 || present[id] {
				continue
			}
			closed := cloneConnection(record.conn)
			closed.ClosedAt = now.UnixMilli()
			next := &connectionRecord{conn: closed, run: record.run, updatedAt: now, rule: record.rule,
				peakUp: record.peakUp, peakDown: record.peakDown}
			if restarted {
				next.closeReason = closeReasonCoreRestart
			}
			h.rows[id] = next
			changed = true
		}
	}
	h.run = run
	for _, event := range frame.GetEvents() {
		id := event.GetId()
		if id == "" {
			continue
		}
		if _, hidden := h.cleared[id]; hidden {
			if event.GetType() == daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED {
				delete(h.cleared, id)
			}
			continue
		}
		previous := h.rows[id]
		var current *daemon.Connection
		update := event.GetType() == daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE
		if event.GetConnection() != nil && (previous == nil || !update) {
			current = cloneConnection(event.GetConnection())
		} else if previous != nil {
			current = cloneConnection(previous.conn)
			current.UplinkTotal += event.GetUplinkDelta()
			current.DownlinkTotal += event.GetDownlinkDelta()
			// An update carries the connection when its decryption state
			// changed: only those fields follow it, the totals keep the deltas.
			if conn := event.GetConnection(); conn != nil {
				current.Mitm, current.HttpRequests, current.MitmError = conn.GetMitm(), conn.GetHttpRequests(), conn.GetMitmError()
			}
		}
		if current == nil {
			continue
		}
		if current.Id == "" {
			current.Id = id
		}
		record := &connectionRecord{conn: current, run: run, updatedAt: now}
		// The rule keeps the name it had when the connection arrived: a
		// later apply renumbers the rules.
		if previous != nil && previous.rule.set {
			record.rule = previous.rule
		} else {
			record.rule = h.ruleOf(current.GetRule())
		}
		if previous != nil {
			record.peakUp, record.peakDown = previous.peakUp, previous.peakDown
		}
		// A snapshot's connections are not new: hits count what arrives
		// after it, a refusal by each time it repeats.
		switch rejects := current.GetRejectCount(); {
		case rejects > 0:
			if h.rejectCounts == nil || len(h.rejectCounts) >= 4*rejectedConnectionLimit {
				h.rejectCounts = map[string]int64{}
			}
			counted, known := h.rejectCounts[id]
			h.rejectCounts[id] = rejects
			if !frame.GetReset_() && (known || previous == nil) {
				h.hitLocked(record.rule, int(rejects-counted))
			}
		case previous == nil && !frame.GetReset_():
			h.hitLocked(record.rule, 1)
		}
		switch event.GetType() {
		case daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE:
			record.upRate, record.downRate = float64(event.GetUplinkDelta()), float64(event.GetDownlinkDelta())
			record.peakUp, record.peakDown = max(record.peakUp, record.upRate), max(record.peakDown, record.downRate)
		case daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED:
			current.ClosedAt = event.GetClosedAt()
			if current.ClosedAt == 0 {
				current.ClosedAt = now.UnixMilli()
			}
		}
		h.rows[id] = record
		changed = true
	}
	if !changed {
		return
	}
	h.revision++
	closed, rejected := h.closedCountLocked()
	if h.closedLimit > 0 && closed > h.closedLimit+closedSlack(h.closedLimit) ||
		rejected > rejectedConnectionLimit+closedSlack(rejectedConnectionLimit) {
		h.trimLocked()
	}
}

// rejectedConnectionLimit is how many refused connections the history
// keeps, apart from closedLimit, so that refusals do not push the closed
// connections out.
const rejectedConnectionLimit = 500

// closedCountLocked counts the closed records an outbound carried and the
// refused ones.
func (h *connectionHistory) closedCountLocked() (closed, rejected int) {
	for _, record := range h.rows {
		switch {
		case record.conn.GetRejectCount() > 0:
			rejected++
		case record.conn.GetClosedAt() > 0:
			closed++
		}
	}
	return closed, rejected
}

// trimLocked drops the closed records past closedLimit and the refused
// ones past rejectedConnectionLimit, the earliest closed first.
func (h *connectionHistory) trimLocked() {
	var closed, rejected []*daemon.Connection
	for _, record := range h.rows {
		switch {
		case record.conn.GetRejectCount() > 0:
			rejected = append(rejected, record.conn)
		case record.conn.GetClosedAt() > 0:
			closed = append(closed, record.conn)
		}
	}
	trimmed := false
	drop := func(list []*daemon.Connection, limit int) {
		if limit <= 0 || len(list) <= limit {
			return
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].GetClosedAt() == list[j].GetClosedAt() {
				return list[i].GetId() < list[j].GetId()
			}
			return list[i].GetClosedAt() < list[j].GetClosedAt()
		})
		for _, conn := range list[:len(list)-limit] {
			delete(h.rows, conn.GetId())
		}
		trimmed = true
	}
	drop(closed, h.closedLimit)
	drop(rejected, rejectedConnectionLimit)
	if trimmed {
		h.revision++
	}
}

// setClosedLimit sets how many closed connections to keep (0: all) and
// trims the history to it at once.
func (h *connectionHistory) setClosedLimit(limit int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closedLimit = limit
	h.trimLocked()
}

func (h *connectionHistory) getClosedLimit() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closedLimit
}

// Scopes of connectionHistory.clear.
const (
	clearAllConnections    = "all"
	clearActiveConnections = "active"
	clearClosedConnections = "closed"
)

// clear removes the records of scope from the history; the connections
// themselves keep running. Cleared active connections stay hidden until
// they close.
func (h *connectionHistory) clear(scope string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, record := range h.rows {
		closed := record.conn.GetClosedAt() > 0
		if scope == clearAllConnections || scope == clearClosedConnections && closed || scope == clearActiveConnections && !closed {
			delete(h.rows, id)
			if !closed {
				if h.cleared == nil {
					h.cleared = make(map[string]struct{})
				}
				h.cleared[id] = struct{}{}
			}
		}
	}
	h.revision++
}

// sortedLocked lists the records by creation, with h.mu held.
func (h *connectionHistory) sortedLocked() []*connectionRecord {
	rows := make([]*connectionRecord, 0, len(h.rows))
	for _, record := range h.rows {
		rows = append(rows, record)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].conn, rows[j].conn
		if a.GetCreatedAt() == b.GetCreatedAt() {
			return a.GetId() < b.GetId()
		}
		return a.GetCreatedAt() < b.GetCreatedAt()
	})
	return rows
}

func (h *connectionHistory) snapshot() []*daemon.Connection {
	h.mu.Lock()
	defer h.mu.Unlock()
	records := h.sortedLocked()
	rows := make([]*daemon.Connection, len(records))
	for i, record := range records {
		conn := record.conn
		if label := record.matchedRule().Label; label != conn.GetRule() {
			conn = cloneConnection(conn)
			conn.Rule = label
		}
		rows[i] = conn
	}
	return rows
}

// queryRows lists the history by creation as rows of a query, with the
// rates of now, and its revision.
func (h *connectionHistory) queryRows(now time.Time) ([]*connRow, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	records := h.sortedLocked()
	rows := make([]*connRow, len(records))
	for i, record := range records {
		rows[i] = record.row(now)
	}
	return rows, h.revision
}

// queryRow is the row of the connection id, if the history has it.
func (h *connectionHistory) queryRow(id string, now time.Time) *connRow {
	h.mu.Lock()
	defer h.mu.Unlock()
	if record := h.rows[id]; record != nil {
		return record.row(now)
	}
	return nil
}

// row is the record as a row of a query: rates hold for rateTimeout after
// an update and are 0 once closed.
func (r *connectionRecord) row(now time.Time) *connRow {
	row := &connRow{Conn: connInfoOf(r.conn), UpTotal: r.conn.GetUplinkTotal(), DownTotal: r.conn.GetDownlinkTotal(),
		ClosedAt: max(r.conn.GetClosedAt(), 0), Run: r.run, CloseReason: r.closeReason, PeakUp: r.peakUp, PeakDown: r.peakDown}
	rule := r.matchedRule()
	row.Conn.Rule, row.Conn.RuleID, row.Conn.RuleOwner, row.Conn.RuleSection = rule.Label, rule.ID, rule.Owner, rule.Section
	row.RuleKey = ruleHitKey(rule)
	if row.ClosedAt == 0 && now.Sub(r.updatedAt) <= rateTimeout {
		row.UpRate, row.DownRate = r.upRate, r.downRate
	}
	return row
}

// ruleHitInterval is how often the route table's rule hits are sent.
const ruleHitInterval = time.Second

// publishRuleHits sends the "ruleHits" event each ruleHitInterval with the
// hits of each rule since the last one and the rates of its open
// connections ({"hits": {key: n}, "rates": {key: bytes/s}}, ruleHitKey),
// nothing in an interval without either, until ctx ends.
func (s *server) publishRuleHits(ctx context.Context) {
	// Hits of an earlier core run are not this one's.
	s.connections.takeHits()
	ticker := time.NewTicker(ruleHitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hits, rates := s.connections.takeHits(), s.connections.ruleRates(time.Now())
			if len(hits) > 0 || len(rates) > 0 {
				s.emit("ruleHits", map[string]any{"hits": hits, "rates": rates})
			}
		}
	}
}

func (s *server) collectConnectionHistory(ctx context.Context, client daemon.StartedServiceClient, started time.Time) {
	run := usageRunID(started)
	go s.publishRuleHits(ctx)
	for ctx.Err() == nil {
		attempt, cancel := context.WithCancel(ctx)
		stream, err := client.SubscribeConnections(attempt, &daemon.SubscribeConnectionsRequest{Interval: int64(time.Second)})
		if err == nil {
			for ctx.Err() == nil {
				frame, recvErr := stream.Recv()
				if recvErr != nil {
					break
				}
				s.connections.ingest(run, frame, time.Now())
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
