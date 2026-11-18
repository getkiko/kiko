package main

import (
	"context"
	"io"
	"testing"
	"time"
)

type endedEndpointStream struct{}

func (endedEndpointStream) Recv() (int, error) { return 0, io.EOF }

func TestEndpointStreamInvalidatesAndReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	invalidated := make(chan struct{}, 2)
	opened := 0
	go func() {
		defer close(done)
		followStream(&server{}, ctx, "fixture", func(context.Context) (recvStream[int], error) {
			opened++
			if opened == 2 {
				cancel()
			}
			return endedEndpointStream{}, nil
		}, func(int) {}, func() { invalidated <- struct{}{} })
	}()
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("stale status retained")
	}
	<-done
	if opened != 2 {
		t.Fatalf("subscription opened %d times", opened)
	}
}
