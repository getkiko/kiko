package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Reuse the native test executable as both server and kiko-core stub. This runs
// the real desktop startup and parent pipe lifecycle on Windows without a shell.
func TestMain(m *testing.M) {
	if os.Getenv("KIKO_LIFETIME_TEST") == "1" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			if os.Getenv("KIKO_DESKTOP_SHUTDOWN_TOKEN") != "" {
				os.Exit(99)
			}
			fmt.Println("sing-box test with_clash_api")
			os.Exit(0)
		case "check":
			os.Exit(0)
		case "--core", "--sing-box":
			main()
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestDesktopParentEOFStopsServerAndEventStream(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(binary, "--core", binary, "--listen", address, "--data-dir", t.TempDir())
	cmd.Env = append(os.Environ(), "KIKO_LIFETIME_TEST=1", "KIKO_DESKTOP_SHUTDOWN_TOKEN=test-token")
	hideProcessWindow(cmd)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case err := <-done:
			t.Fatalf("startup exited: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("server startup timeout")
		default:
		}
		response, fetchErr := client.Get("http://" + address + "/api/v1/info")
		if fetchErr == nil {
			var info struct {
				PID int `json:"pid"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&info)
			response.Body.Close()
			if decodeErr == nil && info.PID == cmd.Process.Pid {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/api/v1/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err = stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(response.Body); err != nil {
		t.Fatal("event stream failed to close:", err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("server exit: %v\n%s", err, output.String())
		}
	case <-ctx.Done():
		t.Fatal("server remains alive after parent EOF")
	}
}
