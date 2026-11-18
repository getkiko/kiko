package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func beginTaildropTask(t *testing.T, s *server, ctx context.Context, tag, peer, manifest string, files [][2]string) taildropTask {
	t.Helper()
	body, contentType := multipartBody(t, manifest, files)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/taildrop/send?async=true&endpointTag="+tag+"&peer="+peer, body).WithContext(ctx)
	request.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, request)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var task taildropTask
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	return task
}
func readTaildropTasks(t *testing.T, s *server, tag string) []taildropTask {
	t.Helper()
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/taildrop/sends?endpointTag="+tag, nil))
	var tasks []taildropTask
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatal(err)
	}
	return tasks
}
func awaitTaildropState(t *testing.T, s *server, id, state string) {
	t.Helper()
	eventually(t, "task "+id+" "+state, func() bool {
		s.td.mu.Lock()
		defer s.td.mu.Unlock()
		return s.td.tasks[id].State == state
	})
}

func TestTaildropTaskSurvivesRequestCloseAndReopens(t *testing.T) {
	s, fake, events := connectedEndpointServer(t)
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	fake.rec.Lock()
	fake.sendGate = gate
	fake.sendStarted = started
	fake.rec.Unlock()
	ctx, closeSheet := context.WithCancel(context.Background())
	defer closeSheet()
	// Exceed multipart's prefetch buffer to exercise staging the entire upload.
	data := strings.Repeat("abcd", 32<<10)
	task := beginTaildropTask(t, s, ctx, "ts", "node1", fmt.Sprintf(`[{"name":"a.txt","size":%d},{"name":"b.txt","size":2}]`, len(data)), [][2]string{{"a.txt", data}, {"b.txt", "de"}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer did not start")
	}
	closeSheet()
	// Reopening reads a fresh server snapshot after the original request ends.
	tasks := readTaildropTasks(t, s, "ts")
	if len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].State != "sending" || len(tasks[0].Files) != 2 {
		t.Fatalf("reopened: %+v", tasks)
	}
	close(gate)
	awaitTaildropState(t, s, task.ID, "completed")
	tasks = readTaildropTasks(t, s, "ts")
	if tasks[0].SentBytes != int64(len(data)+2) {
		t.Fatalf("progress: %+v", tasks[0])
	}
	fake.rec.Lock()
	sent := string(fake.sendData)
	fake.rec.Unlock()
	if sent != data+"de" {
		t.Fatal("staging corrupted file contents")
	}
	eventually(t, "global completion event", func() bool {
		for {
			select {
			case raw := <-events:
				var event struct {
					Type string       `json:"type"`
					Data taildropTask `json:"data"`
				}
				if json.Unmarshal([]byte(raw), &event) == nil && event.Type == "taildropSend" && event.Data.ID == task.ID && event.Data.State == "completed" {
					return true
				}
			default:
				return false
			}
		}
	})
}

func TestTaildropTaskCancellationAndIsolation(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	gate := make(chan struct{})
	started := make(chan struct{}, 2)
	fake.rec.Lock()
	fake.sendGate = gate
	fake.sendStarted = started
	fake.rec.Unlock()
	first := beginTaildropTask(t, s, context.Background(), "ts1", "node1", `[{"name":"a","size":1}]`, [][2]string{{"a", "x"}})
	second := beginTaildropTask(t, s, context.Background(), "ts2", "node2", `[{"name":"b","size":1}]`, [][2]string{{"b", "y"}})
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("send did not start")
		}
	}
	rec := postJSON(s.taildropSendCancel, "POST", fmt.Sprintf(`{"id":%q}`, first.ID))
	if rec.Code != 200 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	tasks := readTaildropTasks(t, s, "ts1")
	if len(tasks) != 1 || tasks[0].State != "canceled" {
		t.Fatalf("canceled: %+v", tasks)
	}
	tasks = readTaildropTasks(t, s, "ts2")
	if len(tasks) != 1 || tasks[0].State != "sending" {
		t.Fatalf("other endpoint: %+v", tasks)
	}
	close(gate)
	awaitTaildropState(t, s, second.ID, "completed")
	awaitTaildropState(t, s, first.ID, "canceled")
	if rec = postJSON(s.taildropSendCancel, "POST", `{"id":"missing"}`); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestTaildropTaskFailureIsRetained(t *testing.T) {
	s, _, _ := connectedEndpointServer(t)
	task := beginTaildropTask(t, s, context.Background(), "ts", "node", `[{"name":"a","size":4}]`, [][2]string{{"a", "abc"}})
	awaitTaildropState(t, s, task.ID, "failed")
	tasks := readTaildropTasks(t, s, "ts")
	if len(tasks) != 1 || !strings.Contains(tasks[0].Error, "大小") {
		t.Fatalf("failure: %+v", tasks)
	}
}

func TestTaildropTaskProgressAcrossFiles(t *testing.T) {
	s := newGRPCTestServer(t)
	task := &taildropTask{State: "sending", TotalBytes: 15, Files: []taildropManifestEntry{{Name: "a", Size: 10}, {Name: "b", Size: 5}}}
	output := &taildropTaskWriter{s: s, task: task, header: make(http.Header)}
	for _, step := range []struct {
		line string
		want int64
	}{
		{`{"fileIndex":0,"sentBytes":"6"}`, 6},
		{`{"fileIndex":0,"sentBytes":"10","fileCompleted":true}`, 10},
		{`{"fileIndex":0,"sentBytes":"10","fileCompleted":true}`, 10},
		{`{"fileIndex":1,"sentBytes":"2"}`, 12},
		{`{"fileIndex":1,"sentBytes":"5","fileCompleted":true}`, 15},
	} {
		output.Write([]byte(step.line + "\n"))
		if task.SentBytes != step.want {
			t.Fatalf("%s: got %d want %d", step.line, task.SentBytes, step.want)
		}
	}
}
