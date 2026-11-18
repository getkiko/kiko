package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Uint64 is a 64-bit cursor or counter, which the DNS monitor writes as a
// string; it reads a string or a number and writes a string.
type Uint64 uint64

// UnmarshalJSON reads "123", 123 or null.
func (v *Uint64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(text, 64)
		if ferr != nil {
			return err
		}
		n = uint64(f)
	}
	*v = Uint64(n)
	return nil
}

// MarshalJSON writes the value as a string.
func (v Uint64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(v.String())), nil
}

func (v Uint64) String() string { return strconv.FormatUint(uint64(v), 10) }

// DNSQuestion is a question of a DNS message.
type DNSQuestion struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Class string `json:"class"`
}

// DNSRecord is a resource record.
type DNSRecord struct {
	Name string `json:"name"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`
	Data string `json:"data"`
}

// DNSMessage is a captured DNS message.
type DNSMessage struct {
	ID               int           `json:"id"`
	Rcode            string        `json:"rcode"`
	Opcode           int           `json:"opcode"`
	Size             int           `json:"size"`
	Flags            []string      `json:"flags,omitempty"`
	Questions        []DNSQuestion `json:"questions,omitempty"`
	Answer           []DNSRecord   `json:"answer,omitempty"`
	Authority        []DNSRecord   `json:"authority,omitempty"`
	Additional       []DNSRecord   `json:"additional,omitempty"`
	TruncatedCapture bool          `json:"truncated_capture,omitempty"`
	Wire             string        `json:"wire,omitempty"`
}

// DNSFailure is why a query or an attempt failed.
type DNSFailure struct {
	Kind    string `json:"kind"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// DNSConnection is the upstream connection of an attempt.
type DNSConnection struct {
	ID               string   `json:"id"`
	CreatedAt        string   `json:"created_at"`
	Protocol         string   `json:"protocol"`
	Configured       string   `json:"configured,omitempty"`
	Local            string   `json:"local,omitempty"`
	Peer             string   `json:"peer,omitempty"`
	PeerEvidence     string   `json:"peer_evidence"`
	Outbound         []string `json:"outbound,omitempty"`
	OutboundEvidence string   `json:"outbound_evidence"`
	TLSVersion       string   `json:"tls_version,omitempty"`
	ServerName       string   `json:"server_name,omitempty"`
	ALPN             string   `json:"alpn,omitempty"`
}

// DNSAttempt is one upstream attempt of a query.
type DNSAttempt struct {
	ID          string         `json:"id"`
	StartedAt   string         `json:"started_at"`
	FinishedAt  string         `json:"finished_at,omitempty"`
	Resolver    string         `json:"resolver"`
	Transport   string         `json:"transport"`
	Protocol    string         `json:"protocol"`
	Endpoint    string         `json:"endpoint,omitempty"`
	NetworkUsed bool           `json:"network_used"`
	Visibility  string         `json:"visibility"`
	Connection  *DNSConnection `json:"connection,omitempty"`
	Reused      bool           `json:"reused"`
	WireID      int            `json:"wire_id"`
	SentAt      string         `json:"sent_at,omitempty"`
	FirstByteAt string         `json:"first_byte_at,omitempty"`
	HTTPVersion string         `json:"http_version,omitempty"`
	HTTPStatus  int            `json:"http_status,omitempty"`
	StreamID    string         `json:"stream_id,omitempty"`
	Response    *DNSMessage    `json:"response,omitempty"`
	Error       *DNSFailure    `json:"error,omitempty"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
}

// DNSSource is where a query came from.
type DNSSource struct {
	Inbound     string `json:"inbound,omitempty"`
	Address     string `json:"address,omitempty"`
	Destination string `json:"destination,omitempty"`
	Network     string `json:"network,omitempty"`
	Process     string `json:"process,omitempty"`
	ProcessPath string `json:"process_path,omitempty"`
	PID         int    `json:"pid,omitempty"`
	User        string `json:"user,omitempty"`
}

// DNSScriptRun is one dns script action of a query. Outcome is answer,
// server, pass, reject, error or maybe.
type DNSScriptRun struct {
	Tag        string  `json:"tag"`
	Outcome    string  `json:"outcome"`
	Detail     string  `json:"detail,omitempty"`
	Cached     bool    `json:"cached,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`
}

// DNSQuery is a DNS query the core observed. Raw keeps the record as the
// server sent it, every captured field included.
type DNSQuery struct {
	ID                string        `json:"id"`
	ParentID          string        `json:"parent_id,omitempty"`
	OperationID       string        `json:"operation_id,omitempty"`
	Revision          Uint64        `json:"revision"`
	Seq               Uint64        `json:"seq"`
	StartedAt         string        `json:"started_at"`
	FinishedAt        string        `json:"finished_at,omitempty"`
	DurationMS        float64       `json:"duration_ms"`
	Role              string        `json:"role"`
	Status            string        `json:"status"`
	Source            DNSSource     `json:"source"`
	Questions         []DNSQuestion `json:"questions"`
	ResolvedAddresses []string      `json:"resolved_addresses,omitempty"`
	Request           *DNSMessage   `json:"request,omitempty"`
	Response          *DNSMessage   `json:"response,omitempty"`
	Resolver          string        `json:"resolver,omitempty"`
	Transport         string        `json:"transport,omitempty"`
	Candidates        []string      `json:"candidates,omitempty"`
	Cache             string        `json:"cache,omitempty"`
	SharedWith        string        `json:"shared_with,omitempty"`
	Policy            []string      `json:"policy,omitempty"`
	Attempts          []DNSAttempt  `json:"attempts,omitempty"`
	Error             *DNSFailure   `json:"error,omitempty"`
	Delivery          string        `json:"delivery,omitempty"`
	DeliveryError     *DNSFailure   `json:"delivery_error,omitempty"`
	Incomplete        bool          `json:"incomplete,omitempty"`
	// Rewritten names the feature that modified the answer.
	Rewritten string `json:"rewritten,omitempty"`
	// Scripts are the dns script actions that ran for the query, in order.
	Scripts []DNSScriptRun `json:"scripts,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the query and keeps its raw record.
func (q *DNSQuery) UnmarshalJSON(data []byte) error {
	type plain DNSQuery
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*q = DNSQuery(v)
	q.Raw = bytes.Clone(data)
	return nil
}

// MarshalJSON writes the raw record when there is one.
func (q DNSQuery) MarshalJSON() ([]byte, error) {
	if len(q.Raw) > 0 {
		return q.Raw, nil
	}
	type plain DNSQuery
	return json.Marshal(plain(q))
}

// DNSStats are the monitor's counters.
type DNSStats struct {
	Total     Uint64  `json:"total"`
	Pending   int     `json:"pending"`
	Retained  int     `json:"retained"`
	Bytes     float64 `json:"bytes"`
	Dropped   Uint64  `json:"dropped"`
	Evicted   Uint64  `json:"evicted"`
	Errors    Uint64  `json:"errors"`
	Timeouts  Uint64  `json:"timeouts"`
	CacheHits Uint64  `json:"cache_hits"`
}

// DNSView is a page of queries, of the history or of the live stream.
type DNSView struct {
	Instance   string     `json:"instance"`
	Seq        Uint64     `json:"seq"`
	Reset      bool       `json:"reset,omitempty"`
	Gap        bool       `json:"gap,omitempty"`
	Queries    []DNSQuery `json:"queries"`
	Stats      DNSStats   `json:"stats"`
	NextBefore string     `json:"next_before,omitempty"`
}

// DNSCapabilities is GET /dns/capabilities.
type DNSCapabilities struct {
	Supported     bool     `json:"supported"`
	Enabled       bool     `json:"enabled"`
	SchemaVersion int      `json:"schema_version,omitempty"`
	Features      []string `json:"features,omitempty"`
	Limitations   []string `json:"limitations,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

// DNSCapabilities reports whether the core monitors DNS.
func (c *Client) DNSCapabilities(ctx context.Context) (DNSCapabilities, error) {
	var v DNSCapabilities
	return v, c.Get(ctx, "/dns/capabilities", &v)
}

// DNSQueries reads up to 500 queries older than the cursor before.
func (c *Client) DNSQueries(ctx context.Context, before string) (DNSView, error) {
	var v DNSView
	return v, c.Get(ctx, "/dns/queries?limit=500&before="+esc(before), &v)
}

// DNSQuery reads one query with its full detail.
func (c *Client) DNSQuery(ctx context.Context, id, instance string) (DNSQuery, error) {
	var v DNSQuery
	return v, c.Get(ctx, "/dns/queries/"+pathEscape(id)+"?instance="+esc(instance), &v)
}

// DNSRuleMap is the DNS rule map of the configuration applied at
// AppliedAt: its DNS rules by their index in dns.rules, dns.final at -1.
// Queries that began before AppliedAt went by an earlier configuration.
type DNSRuleMap struct {
	AppliedAt time.Time `json:"appliedAt"`
	Rules     []RuleRef `json:"rules"`
}

// DNSRuleMap reads the DNS rule map of the applied configuration.
func (c *Client) DNSRuleMap(ctx context.Context) (DNSRuleMap, error) {
	var v DNSRuleMap
	return v, c.Get(ctx, "/dns/rule-map", &v)
}

// ClearDNSHistory removes finished queries from the history.
func (c *Client) ClearDNSHistory(ctx context.Context) (DNSView, error) {
	var v DNSView
	return v, c.Send(ctx, http.MethodDelete, "/dns/history", struct{}{}, &v)
}

// DNSStream follows GET /dns/stream from the cursor after of instance
// until it or ctx ends, calling view with each update.
func (c *Client) DNSStream(ctx context.Context, instance, after string, view func(DNSView)) error {
	path := "/dns/stream?instance=" + esc(instance) + "&after=" + esc(after)
	return c.NDJSON(ctx, http.MethodGet, path, nil, false, func(line json.RawMessage) error {
		var v DNSView
		if err := json.Unmarshal(line, &v); err != nil {
			return err
		}
		view(v)
		return nil
	})
}
