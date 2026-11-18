package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

// CoreLogLevel is a core log message's level: the name ("INFO") or the
// number the server sends.
type CoreLogLevel struct {
	Name   string
	Number int
	// IsNumber reports whether the server sent a number.
	IsNumber bool
}

// UnmarshalJSON accepts a string or a number.
func (l *CoreLogLevel) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		return json.Unmarshal(raw, &l.Name)
	}
	if string(raw) == "null" {
		return nil
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return err
	}
	l.Number, l.IsNumber = n, true
	return nil
}

// CoreLogMessage is a line of the core's log stream.
type CoreLogMessage struct {
	Level   CoreLogLevel `json:"level"`
	Message string       `json:"message"`
}

// CoreLogUpdate is an item of GET /logs/core: the default level first,
// then batches of messages; Reset replaces the history.
type CoreLogUpdate struct {
	Reset        bool             `json:"reset,omitempty"`
	DefaultLevel *int             `json:"defaultLevel,omitempty"`
	Messages     []CoreLogMessage `json:"messages,omitempty"`
}

// CoreLogs follows the running core's log stream until it or ctx ends.
func (c *Client) CoreLogs(ctx context.Context, update func(CoreLogUpdate)) error {
	return c.NDJSON(ctx, http.MethodGet, "/logs/core", nil, false, func(raw json.RawMessage) error {
		var u CoreLogUpdate
		if err := json.Unmarshal(raw, &u); err != nil {
			return err
		}
		update(u)
		return nil
	})
}
