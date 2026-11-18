package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	bolt "github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/daemon"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

var usageDimensions = []string{"source", "process", "host", "rootDomain", "outbound", "chain", "rule", "inbound", "user", "network", "protocol", "destination", "port"}
var usageRecordsBucket = []byte("minutes-v1")
var usageStateBucket = []byte("state-v1")

const usageMaxRecords = 250000
const usageMaxClosedCheckpoints = 8192

type usageCounters struct {
	Upload      uint64 `json:"upload,string"`
	Download    uint64 `json:"download,string"`
	Connections uint64 `json:"connections,string"`
}

func (c *usageCounters) add(other usageCounters) {
	c.Upload += other.Upload
	c.Download += other.Download
	c.Connections += other.Connections
}

type usageRecord struct {
	Time       int64             `json:"time"`
	Dimensions map[string]string `json:"dimensions"`
	usageCounters
}

type usageCheckpoint struct {
	Upload, Download uint64
	Dimensions       map[string]string
	SeenAt           int64
	Closed           bool
}

type usageMetadata struct {
	Since          int64 `json:"since"`
	RetentionDays  int   `json:"retentionDays"`
	EvictedBuckets int64 `json:"evictedBuckets"`
}

type usageObservation struct {
	Run       string
	Since     int64
	LastEvent int64
}

type usageStore struct {
	mu          sync.Mutex
	collection  sync.Mutex
	file        string
	loaded      bool
	meta        usageMetadata
	checkpoints map[string]usageCheckpoint
	pending     map[string]usageRecord
	observation usageObservation
	dirty       bool
	lastPrune   time.Time
	connected   bool
	lastEvent   int64
	lastError   string
	metrics     []metricPoint
	// ruleLabel names a matched rule by the applied configuration's rule
	// map; nil keeps the core's rule without its index.
	ruleLabel func(string) string
}

func newUsageStore(dataDir string) *usageStore {
	return &usageStore{file: filepath.Join(dataDir, "usage.db"), checkpoints: make(map[string]usageCheckpoint), pending: make(map[string]usageRecord)}
}

// Opening under the store mutex keeps readers and flushes consistent, and avoids
// a database handle outliving the server or preventing a second local instance.
func (u *usageStore) database() (*bolt.DB, error) {
	db, err := bolt.Open(u.file, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if !u.loaded {
		u.meta = usageMetadata{Since: time.Now().UnixMilli()}
		err = db.Update(func(tx *bolt.Tx) error {
			if _, err := tx.CreateBucketIfNotExists(usageRecordsBucket); err != nil {
				return err
			}
			state, err := tx.CreateBucketIfNotExists(usageStateBucket)
			if err != nil {
				return err
			}
			if data := state.Get([]byte("metadata")); data != nil {
				if err := json.Unmarshal(data, &u.meta); err != nil {
					return fmt.Errorf("read usage metadata: %w", err)
				}
			}
			if data := state.Get([]byte("checkpoints")); data != nil {
				if err := json.Unmarshal(data, &u.checkpoints); err != nil {
					return fmt.Errorf("read usage checkpoints: %w", err)
				}
			}
			if data := state.Get([]byte("observation")); data != nil {
				if err := json.Unmarshal(data, &u.observation); err != nil {
					return fmt.Errorf("read usage observation: %w", err)
				}
				u.lastEvent = u.observation.LastEvent
			}
			return nil
		})
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if u.meta.RetentionDays != 0 {
			u.meta.RetentionDays = 0
			u.dirty = true
		}
		u.loaded = true
	}
	return db, nil
}

func usageHost(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return strings.Trim(address, "[]")
}

// Normalize at collection and read time so historical records share the same
// grouping semantics without changing their persisted keys or counters.
func normalizeUsageDomain(host string) (string, string) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if net.ParseIP(host) != nil {
		return host, host
	}
	if ascii, err := idna.Lookup.ToASCII(host); err == nil {
		host = strings.TrimSuffix(ascii, ".")
	}
	root := host
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		root = domain
	}
	return host, root
}

func normalizeUsageDimensions(values map[string]string) {
	if host := values["host"]; host != "" {
		values["host"], values["rootDomain"] = normalizeUsageDomain(host)
	}
}

func connectionUsageDimensions(c *daemon.Connection) map[string]string {
	host := c.GetDomain()
	if host == "" {
		host = usageHost(c.GetDestination())
	}
	host, root := normalizeUsageDomain(host)
	process := "未知进程"
	if info := c.GetProcessInfo(); info != nil {
		if info.GetProcessPath() != "" {
			process = info.GetProcessPath()
		} else if len(info.GetPackageNames()) > 0 {
			process = strings.Join(info.GetPackageNames(), ", ")
		}
	}
	_, port, _ := net.SplitHostPort(c.GetDestination())
	values := map[string]string{
		"source": usageHost(c.GetSource()), "process": process, "host": host, "rootDomain": root,
		"outbound": c.GetOutbound(), "chain": strings.Join(chainOrder(c.GetChainList()), " → "), "rule": stripConnRuleIndex(c.GetRule()),
		"inbound": c.GetInbound(), "user": c.GetUser(), "network": strings.ToUpper(c.GetNetwork()),
		"protocol": strings.ToUpper(c.GetProtocol()), "destination": usageHost(c.GetDestination()), "port": port,
	}
	for key, value := range values {
		if value == "" {
			values[key] = "—"
		}
	}
	return values
}

func usageRecordKey(record usageRecord) string {
	b, _ := json.Marshal(record.Dimensions)
	return fmt.Sprintf("%013d:%x", record.Time, sha256.Sum256(b))
}

func (u *usageStore) addLocked(now time.Time, dimensions map[string]string, counters usageCounters) {
	if counters == (usageCounters{}) {
		return
	}
	record := usageRecord{Time: now.Truncate(time.Minute).UnixMilli(), Dimensions: dimensions, usageCounters: counters}
	key := usageRecordKey(record)
	if old, ok := u.pending[key]; ok {
		old.add(counters)
		u.pending[key] = old
	} else {
		u.pending[key] = record
	}
}

// Cumulative checkpoints reconcile reset/reconnect frames and final close totals.
// Existing connections in the first snapshot establish the observation baseline.
func (u *usageStore) ingest(run string, frame *daemon.ConnectionEvents, now time.Time) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		db, err := u.database()
		if err != nil {
			return err
		}
		_ = db.Close()
	}
	if u.observation.Run != run {
		u.observation = usageObservation{Run: run, Since: now.UnixMilli()}
		// Databases from before observation persistence can recover the boundary
		// from checkpoints belonging to this core run.
		for key, checkpoint := range u.checkpoints {
			if strings.HasPrefix(key, run+":") {
				u.observation.Since = min(u.observation.Since, checkpoint.SeenAt)
				u.observation.LastEvent = max(u.observation.LastEvent, checkpoint.SeenAt)
			}
		}
		if u.observation.LastEvent > 0 {
			// A history clear also bounds recovery from legacy checkpoints.
			u.observation.Since = max(u.observation.Since, u.meta.Since)
			u.observation.LastEvent = max(u.observation.LastEvent, u.meta.Since)
		}
	}
	u.lastEvent = now.UnixMilli()
	u.connected, u.lastError = true, ""
	if frame.GetReset_() {
		present := make(map[string]bool, len(frame.GetEvents()))
		for _, event := range frame.GetEvents() {
			present[run+":"+event.GetId()] = true
		}
		// Connections absent from a fresh snapshot have left the active set.
		for key, checkpoint := range u.checkpoints {
			if !checkpoint.Closed && !present[key] {
				checkpoint.Closed = true
				u.checkpoints[key] = checkpoint
			}
		}
	}
	for _, event := range frame.GetEvents() {
		// A refused connection carried nothing: usage counts what outbounds
		// carried.
		if event.GetConnection().GetRejectCount() > 0 {
			continue
		}
		key := run + ":" + event.GetId()
		previous, exists := u.checkpoints[key]
		current := previous
		var count uint64
		if c := event.GetConnection(); c != nil {
			current.Dimensions = connectionUsageDimensions(c)
			// A connection keeps the rule name it arrived with: a later
			// apply renumbers the rules.
			if rule := previous.Dimensions["rule"]; exists && rule != "" {
				current.Dimensions["rule"] = rule
			} else if u.ruleLabel != nil {
				if label := u.ruleLabel(c.GetRule()); label != "" {
					current.Dimensions["rule"] = label
				}
			}
			current.Upload, current.Download = uint64(max(c.GetUplinkTotal(), 0)), uint64(max(c.GetDownlinkTotal(), 0))
			current.Closed = c.GetClosedAt() > 0 || event.GetType() == daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED
			if !exists {
				baseline := frame.GetReset_() && (u.observation.LastEvent == 0 ||
					c.GetCreatedAt() < u.observation.Since ||
					current.Closed && c.GetClosedAt() <= u.observation.LastEvent)
				if baseline {
					previous.Upload, previous.Download = current.Upload, current.Download
				}
				// Fresh connections retained during a gap contribute their full totals.
				// Historical closed replays stay outside the observed usage.
				if !baseline || !current.Closed {
					count = 1
				}
			}
		} else if exists {
			current.Upload += uint64(max(event.GetUplinkDelta(), 0))
			current.Download += uint64(max(event.GetDownlinkDelta(), 0))
			current.Closed = current.Closed || event.GetType() == daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED
		} else {
			continue
		}
		var upload, download uint64
		if current.Upload >= previous.Upload {
			upload = current.Upload - previous.Upload
		}
		if current.Download >= previous.Download {
			download = current.Download - previous.Download
		}
		if !previous.Closed {
			u.addLocked(now, current.Dimensions, usageCounters{Upload: upload, Download: download, Connections: count})
		}
		current.SeenAt = now.UnixMilli()
		u.checkpoints[key] = current
	}
	u.observation.LastEvent = now.UnixMilli()
	u.dirty = true
	return nil
}

func (u *usageStore) flushLocked(db *bolt.DB, now time.Time) error {
	if !u.dirty && now.Sub(u.lastPrune) < time.Hour {
		return nil
	}
	meta := u.meta
	err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usageRecordsBucket)
		for key, record := range u.pending {
			if old := bucket.Get([]byte(key)); old != nil {
				var value usageRecord
				if err := json.Unmarshal(old, &value); err != nil {
					return err
				}
				record.add(value.usageCounters)
			}
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err = bucket.Put([]byte(key), data); err != nil {
				return err
			}
		}
		// Minute records remain available for full export until explicitly cleared.
		state := tx.Bucket(usageStateBucket)
		var closedKeys []string
		for key, checkpoint := range u.checkpoints {
			if checkpoint.SeenAt < now.Add(-24*time.Hour).UnixMilli() && checkpoint.Closed {
				delete(u.checkpoints, key)
			} else if checkpoint.Closed {
				closedKeys = append(closedKeys, key)
			}
		}
		if len(closedKeys) > usageMaxClosedCheckpoints {
			sort.Slice(closedKeys, func(i, j int) bool {
				a, b := u.checkpoints[closedKeys[i]].SeenAt, u.checkpoints[closedKeys[j]].SeenAt
				if a == b {
					return closedKeys[i] < closedKeys[j]
				}
				return a < b
			})
			for _, key := range closedKeys[:len(closedKeys)-usageMaxClosedCheckpoints] {
				delete(u.checkpoints, key)
			}
		}
		for key, value := range map[string]any{"metadata": meta, "checkpoints": u.checkpoints, "observation": u.observation} {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err = state.Put([]byte(key), data); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		u.meta = meta
		u.pending = make(map[string]usageRecord)
		u.dirty = false
		if now.Sub(u.lastPrune) >= time.Hour {
			u.lastPrune = now
		}
	}
	return err
}

func (u *usageStore) flush() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	db, err := u.database()
	if err != nil {
		return err
	}
	defer db.Close()
	return u.flushLocked(db, time.Now())
}

type usageQuery struct {
	From, To  int64
	Group     string
	Secondary string
	// Filters is the chosen values of each dimension: a record passes a
	// dimension with any of its values (or), and every dimension (and).
	Filters   map[string][]string
	Search    string
	Sort      string
	Ascending bool
	Page      int
	Limit     int
}

type usageGroup struct {
	Key    string   `json:"key"`
	Labels []string `json:"labels"`
	usageCounters
}
type usageTrend struct {
	Time int64 `json:"time"`
	usageCounters
}
type usageFacet struct {
	Value string `json:"value"`
	usageCounters
}
type usageResponse struct {
	From           int64                   `json:"from"`
	To             int64                   `json:"to"`
	Rows           []usageGroup            `json:"rows"`
	Groups         int                     `json:"groups"`
	Page           int                     `json:"page"`
	Limit          int                     `json:"limit"`
	Totals         usageCounters           `json:"totals"`
	Trend          []usageTrend            `json:"trend"`
	Facets         map[string][]usageFacet `json:"facets"`
	Since          int64                   `json:"since"`
	RetentionDays  int                     `json:"retentionDays"`
	EvictedBuckets int64                   `json:"evictedBuckets"`
	Connected      bool                    `json:"connected"`
	LastEvent      int64                   `json:"lastEvent"`
	Error          string                  `json:"error,omitempty"`
	// RuleLinks are the route table rules of rule labels.
	RuleLinks map[string]ruleLink `json:"ruleLinks,omitempty"`
}

func (u *usageStore) query(q usageQuery) (usageResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	db, err := u.database()
	if err != nil {
		return usageResponse{}, err
	}
	defer db.Close()
	if err = u.flushLocked(db, time.Now()); err != nil {
		return usageResponse{}, err
	}
	response := usageResponse{From: q.From, To: q.To, Rows: []usageGroup{}, Trend: []usageTrend{}, Facets: map[string][]usageFacet{}, Page: q.Page, Limit: q.Limit, Since: u.meta.Since, RetentionDays: u.meta.RetentionDays, EvictedBuckets: u.meta.EvictedBuckets, Connected: u.connected, LastEvent: u.lastEvent, Error: u.lastError}
	groups := make(map[string]*usageGroup)
	facets := make(map[string]map[string]*usageFacet)
	for _, dimension := range usageDimensions {
		facets[dimension] = make(map[string]*usageFacet)
	}
	// Minute storage is exposed explicitly; bucket boundaries align to Unix time.
	minute := int64(time.Minute / time.Millisecond)
	step := max(minute, ((q.To-q.From+60*minute-1)/(60*minute))*minute)
	trend := make(map[int64]*usageTrend)
	for t := q.From / step * step; t < q.To; t += step {
		trend[t] = &usageTrend{Time: t}
	}
	// Each filter and the search is a condition; a dimension's facet counts
	// the records passing every condition but its own filter.
	var conditions []func(usageRecord) bool
	own := make(map[string]int, len(q.Filters))
	for _, dimension := range slices.Sorted(maps.Keys(q.Filters)) {
		chosen := q.Filters[dimension]
		if len(chosen) == 0 {
			continue
		}
		set := make(map[string]bool, len(chosen))
		for _, value := range chosen {
			set[value] = true
		}
		own[dimension] = len(conditions)
		conditions = append(conditions, func(record usageRecord) bool { return set[record.Dimensions[dimension]] })
	}
	if search := strings.ToLower(q.Search); search != "" {
		conditions = append(conditions, func(record usageRecord) bool {
			for _, value := range record.Dimensions {
				if strings.Contains(strings.ToLower(value), search) {
					return true
				}
			}
			return false
		})
	}
	err = db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(usageRecordsBucket).Cursor()
		start := []byte(fmt.Sprintf("%013d:", q.From))
		end := fmt.Sprintf("%013d:", q.To)
		for key, data := cursor.Seek(start); key != nil && string(key) < end; key, data = cursor.Next() {
			var record usageRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			normalizeUsageDimensions(record.Dimensions)
			miss := facetMiss(len(conditions), func(i int) bool { return conditions[i](record) })
			if miss == facetNone {
				continue
			}
			for _, dimension := range usageDimensions {
				i, ok := own[dimension]
				if !ok {
					i = -1
				}
				if !facetCounts(miss, i) {
					continue
				}
				value := record.Dimensions[dimension]
				if facets[dimension][value] == nil {
					facets[dimension][value] = &usageFacet{Value: value}
				}
				facets[dimension][value].add(record.usageCounters)
			}
			if miss != facetAll {
				continue
			}
			response.Totals.add(record.usageCounters)
			labels := []string{record.Dimensions[q.Group]}
			if q.Secondary != "" {
				labels = append(labels, record.Dimensions[q.Secondary])
			}
			labelBytes, _ := json.Marshal(labels)
			groupKey := string(labelBytes)
			if groups[groupKey] == nil {
				groups[groupKey] = &usageGroup{Key: groupKey, Labels: labels}
			}
			groups[groupKey].add(record.usageCounters)
			bucket := record.Time / step * step
			if trend[bucket] != nil {
				trend[bucket].add(record.usageCounters)
			}
		}
		return nil
	})
	if err != nil {
		return usageResponse{}, err
	}
	for _, group := range groups {
		response.Rows = append(response.Rows, *group)
	}
	amount := func(c usageCounters) uint64 {
		switch q.Sort {
		case "upload":
			return c.Upload
		case "download":
			return c.Download
		case "connections":
			return c.Connections
		default:
			return c.Upload + c.Download
		}
	}
	sort.Slice(response.Rows, func(i, j int) bool {
		a, b := response.Rows[i], response.Rows[j]
		if q.Sort == "label" || amount(a.usageCounters) == amount(b.usageCounters) {
			if q.Ascending && q.Sort == "label" {
				return a.Key < b.Key
			}
			if q.Sort == "label" {
				return a.Key > b.Key
			}
			return a.Key < b.Key
		}
		if q.Ascending {
			return amount(a.usageCounters) < amount(b.usageCounters)
		}
		return amount(a.usageCounters) > amount(b.usageCounters)
	})
	response.Groups = len(response.Rows)
	if q.Limit > 0 {
		start := min(q.Page*q.Limit, len(response.Rows))
		response.Rows = response.Rows[start:min(start+q.Limit, len(response.Rows))]
	}
	for _, value := range trend {
		response.Trend = append(response.Trend, *value)
	}
	sort.Slice(response.Trend, func(i, j int) bool { return response.Trend[i].Time < response.Trend[j].Time })
	for dimension, values := range facets {
		items := make([]usageFacet, 0, len(values))
		for _, item := range values {
			items = append(items, *item)
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i].Upload+items[i].Download, items[j].Upload+items[j].Download
			if a == b {
				return items[i].Value < items[j].Value
			}
			return a > b
		})
		limited := items[:min(100, len(items))]
		// A chosen value stays listed, with what it has, to be unchosen.
		for _, value := range q.Filters[dimension] {
			if slices.ContainsFunc(limited, func(item usageFacet) bool { return item.Value == value }) {
				continue
			}
			chosen := usageFacet{Value: value}
			if item := values[value]; item != nil {
				chosen = *item
			}
			limited = append(limited, chosen)
		}
		response.Facets[dimension] = limited
	}
	return response, nil
}

func (u *usageStore) configure(retention int, clear bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	db, err := u.database()
	if err != nil {
		return err
	}
	defer db.Close()
	if clear {
		err = db.Update(func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket(usageRecordsBucket); err != nil {
				return err
			}
			_, err := tx.CreateBucket(usageRecordsBucket)
			return err
		})
		if err != nil {
			return err
		}
		u.pending = make(map[string]usageRecord)
		u.meta.Since, u.meta.EvictedBuckets = time.Now().UnixMilli(), 0
		u.observation.Since, u.observation.LastEvent = u.meta.Since, u.meta.Since
	} else {
		u.meta.RetentionDays = 0
	}
	u.dirty = true
	u.lastPrune = time.Time{}
	return u.flushLocked(db, time.Now())
}

func usageRunID(start time.Time) string { return strconv.FormatInt(start.UnixNano(), 10) }
