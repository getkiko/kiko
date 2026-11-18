package env_test

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// Events a stream posted before it was stopped are dropped: they would
// otherwise report on the stream that replaced it.
func TestStoppedStreamPostsAreDropped(t *testing.T) {
	srv := testkit.NewServer(t)
	e, _ := testkit.NewEnv(srv)
	posted := make(chan struct{}, 16)
	e.SetInvalidate(func() { posted <- struct{}{} })
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	e.Server.Start()
	testkit.Until(t, tt, func() bool { return e.Server.Connected })
	for len(posted) > 0 {
		<-posted
	}

	srv.Emit("runtime", api.Runtime{State: "running"})
	select {
	case <-posted:
	case <-time.After(10 * time.Second):
		t.Fatal("the event was never posted")
	}
	e.Server.Stop()
	tt.Frame()
	if e.Server.Runtime.State == "running" {
		t.Errorf("a stopped stream's event was dispatched: %+v", e.Server.Runtime)
	}
}
