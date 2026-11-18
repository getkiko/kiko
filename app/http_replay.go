package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	httpReplayMaxBody    = 8 << 20
	httpReplayMaxTimeout = 120 * time.Second
	// httpReplayRPCSlack covers the core's own bookkeeping after the request
	// timeout.
	httpReplayRPCSlack = 10 * time.Second
)

// httpReplaySupported reports whether the core serves Replay. An empty
// request is rejected by a core that has it (invalid argument, or a failed
// precondition while capture is off) and answered "unimplemented" by one
// that does not.
func httpReplaySupported(ctx context.Context, client mitmapi.HTTPCaptureClient) bool {
	_, err := client.Replay(ctx, &mitmapi.ReplayRequest{})
	return status.Code(err) != codes.Unimplemented
}

type httpReplayRequestJSON struct {
	SourceID  string           `json:"source_id"`
	Method    string           `json:"method"`
	URL       string           `json:"url"`
	Headers   []httpHeaderJSON `json:"headers"`
	Body      string           `json:"body"`
	Outbound  string           `json:"outbound"`
	TimeoutMS uint32           `json:"timeout_ms"`
	VerifyTLS *bool            `json:"verify_tls"`
}

type httpReplayResponseJSON struct {
	Record *httpRecordJSON `json:"record"`
	Error  string          `json:"error"`
}

func httpBadRequest(w http.ResponseWriter, message string) {
	writeJSON(w, 400, map[string]string{"error": message})
}

// replayHTTPCapture sends the request through the core's router and answers
// with the record of its exchange. Upstream failures are part of the answer
// (state failed); only invalid input and an unavailable core fail the call.
func (s *server) replayHTTPCapture(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	var in httpReplayRequestJSON
	if !decode(w, r, &in) {
		return
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	target, err := url.Parse(in.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		httpBadRequest(w, "URL 须为 http 或 https 地址")
		return
	}
	var body []byte
	if in.Body != "" {
		if base64.StdEncoding.DecodedLen(len(in.Body)) > httpReplayMaxBody+3 {
			httpBadRequest(w, "请求正文超过 8 MB")
			return
		}
		body, err = base64.StdEncoding.DecodeString(in.Body)
		if err != nil {
			httpBadRequest(w, "正文不是有效的 base64")
			return
		}
		if len(body) > httpReplayMaxBody {
			httpBadRequest(w, "请求正文超过 8 MB")
			return
		}
	}
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout > httpReplayMaxTimeout {
		in.TimeoutMS = uint32(httpReplayMaxTimeout / time.Millisecond)
		timeout = httpReplayMaxTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	request := &mitmapi.ReplayRequest{
		SourceId:  in.SourceID,
		Method:    method,
		Url:       in.URL,
		Body:      body,
		Outbound:  in.Outbound,
		TimeoutMs: in.TimeoutMS,
		VerifyTls: in.VerifyTLS == nil || *in.VerifyTLS,
	}
	for _, header := range in.Headers {
		request.Headers = append(request.Headers, &mitmapi.HTTPHeader{Name: header.Name, Value: header.Value})
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+httpReplayRPCSlack)
	defer cancel()
	response, err := client.Replay(ctx, request, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage), grpc.MaxCallSendMsgSize(httpReplayMaxBody+1<<20))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, httpReplayResponseJSON{Record: httpRecord(response.Record), Error: response.Error})
}
