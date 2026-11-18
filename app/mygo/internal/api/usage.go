package api

import (
	"context"
	"net/http"
)

// UsageCounters are traffic totals; the server sends them as decimal
// strings so that they keep full 64-bit precision.
type UsageCounters struct {
	Upload      uint64 `json:"upload,string"`
	Download    uint64 `json:"download,string"`
	Connections uint64 `json:"connections,string"`
}

// Total is upload plus download.
func (c UsageCounters) Total() uint64 { return c.Upload + c.Download }

// UsageGroup is a row of GET /usage: one value (two with a secondary
// dimension) and its counters.
type UsageGroup struct {
	Key    string   `json:"key"`
	Labels []string `json:"labels"`
	UsageCounters
}

// Label returns the i-th label, "" when absent.
func (g UsageGroup) Label(i int) string {
	if i < len(g.Labels) {
		return g.Labels[i]
	}
	return ""
}

// UsageFacet is a filter option of a dimension with its counters.
type UsageFacet struct {
	Value string `json:"value"`
	UsageCounters
}

// UsageTrend is one time bucket of the selected range.
type UsageTrend struct {
	Time int64 `json:"time"`
	UsageCounters
}

// UsageResponse is GET /usage.
type UsageResponse struct {
	From           int64                   `json:"from"`
	To             int64                   `json:"to"`
	Rows           []UsageGroup            `json:"rows"`
	Groups         int                     `json:"groups"`
	Page           int                     `json:"page"`
	Limit          int                     `json:"limit"`
	Totals         UsageCounters           `json:"totals"`
	Trend          []UsageTrend            `json:"trend"`
	Facets         map[string][]UsageFacet `json:"facets"`
	Since          int64                   `json:"since"`
	RetentionDays  int                     `json:"retentionDays"`
	EvictedBuckets int64                   `json:"evictedBuckets"`
	Connected      bool                    `json:"connected"`
	LastEvent      int64                   `json:"lastEvent"`
	Error          string                  `json:"error,omitempty"`
	// RuleLinks are the route table rules of rule labels.
	RuleLinks map[string]RuleLink `json:"ruleLinks,omitempty"`
}

// RuleLink is the route table rule a rule label opens.
type RuleLink struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Section string `json:"section"`
}

// Usage queries recorded traffic; params is the encoded query without
// "?" (see usage.Query.Params).
func (c *Client) Usage(ctx context.Context, params string) (UsageResponse, error) {
	var v UsageResponse
	return v, c.Get(ctx, "/usage?"+params, &v)
}

// UsageExport downloads GET /usage/export as CSV bytes.
func (c *Client) UsageExport(ctx context.Context, params string) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/usage/export?"+params)
}

// SetUsageRetention sets how many days of usage the server keeps.
func (c *Client) SetUsageRetention(ctx context.Context, days int) (int, error) {
	var v struct {
		RetentionDays int `json:"retentionDays"`
	}
	err := c.Send(ctx, http.MethodPatch, "/usage/settings", map[string]int{"retentionDays": days}, &v)
	return v.RetentionDays, err
}

// ClearUsage deletes the recorded usage history.
func (c *Client) ClearUsage(ctx context.Context) error {
	return c.Send(ctx, http.MethodDelete, "/usage/history", nil, nil)
}
