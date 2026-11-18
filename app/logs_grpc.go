package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"
)

// The official dashboard reads SubscribeLog and filters its structured levels locally.
func (s *server) streamCoreLogs(w http.ResponseWriter, r *http.Request) {
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	levelCtx, levelCancel := context.WithTimeout(ctx, 5*time.Second)
	level, err := client.GetDefaultLogLevel(levelCtx, &emptypb.Empty{})
	levelCancel()
	if err != nil {
		writeRPCError(w, err)
		return
	}
	stream, err := client.SubscribeLog(ctx, &emptypb.Empty{})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := &ndjsonWriter{w: w}
	b, _ := json.Marshal(map[string]any{"defaultLevel": int32(level.GetLevel())})
	out.line(b)
	for {
		message, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil && err != io.EOF {
				out.errorLine(err.Error())
			}
			return
		}
		b, _ = protoJSON.Marshal(message)
		out.line(b)
	}
}

func (s *server) clearCoreLogs(w http.ResponseWriter, r *http.Request) {
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := client.ClearLogs(ctx, &emptypb.Empty{}); err != nil {
		writeRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
