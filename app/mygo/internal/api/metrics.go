package api

import "context"

// MetricPoint is a sample of the core's metrics, one a second: rates in
// bytes per second, memory in bytes. A nil field is a sample the core did
// not report, a gap that keeps its place in time.
type MetricPoint struct {
	Time        int64    `json:"time"`
	Upload      *float64 `json:"upload"`
	Download    *float64 `json:"download"`
	Memory      *float64 `json:"memory"`
	Connections *float64 `json:"connections"`
	// UploadTotal and DownloadTotal are the core run's byte counts, sent
	// as decimal strings.
	UploadTotal   string `json:"uploadTotal"`
	DownloadTotal string `json:"downloadTotal"`
}

// MetricsHistory is GET /metrics/history: the latest samples, oldest
// first.
func (c *Client) MetricsHistory(ctx context.Context) ([]MetricPoint, error) {
	var v struct {
		Points []MetricPoint `json:"points"`
	}
	return v.Points, c.Get(ctx, "/metrics/history", &v)
}
