//go:build darwin

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"
)

func TestProcessMonitorDarwinNetworkCSV(t *testing.T) {
	parsed := parseDarwinProcessNetwork(",bytes_in,bytes_out,\nGoogle Chrome Helper.123,1234,5678,\ntcp4 127.0.0.1:123<->127.0.0.1:456,1000,5000,\nudp4 *:5353<->*:*,234,678,\ncom.example.app.456,0,3,\ntcp4 *:22<->*:*,,,\n\"a,b.789\",42,99,\nudp6 *.1<->*.2,42,99,\ninvalid,no,no,\ntcp4 *:1<->*:2,999,999,\n")
	if len(parsed) != 3 || parsed[123]["tcp4 127.0.0.1:123<->127.0.0.1:456"] != [2]uint64{1000, 5000} || parsed[123]["udp4 *:5353<->*:*"] != [2]uint64{234, 678} || len(parsed[456]) != 0 || parsed[789]["udp6 *.1<->*.2"] != [2]uint64{42, 99} {
		t.Fatalf("CSV parsing: %v", parsed)
	}
}

func TestProcessMonitorDarwinCPUTimeMatchesKernelTask(t *testing.T) {
	if err := loadDarwinMonitor(); err != nil {
		t.Fatal(err)
	}
	var task darwinTaskInfo
	pid := int32(os.Getpid())
	if darwinMonitor.pidInfo(pid, 4, 0, unsafe.Pointer(&task), int32(unsafe.Sizeof(task))) != int32(unsafe.Sizeof(task)) {
		t.Fatal("task info unavailable")
	}
	var usage darwinProcessUsage
	if darwinMonitor.rusage(pid, 2, unsafe.Pointer(&usage)) != 0 {
		t.Fatal("rusage unavailable")
	}
	taskSeconds := (float64(task.TotalUser) + float64(task.TotalSystem)) * darwinMonitor.scale
	usageSeconds := (float64(usage.Values[0]) + float64(usage.Values[1])) * darwinMonitor.scale
	if usageSeconds < taskSeconds || usageSeconds-taskSeconds > 1 {
		t.Fatalf("CPU units differ: task %f rusage %f", taskSeconds, usageSeconds)
	}
}
func TestProcessMonitorDarwinPSCPUTime(t *testing.T) {
	for raw, want := range map[string]float64{"5:43.41": 343.41, "13367:13.42": 802033.42, "223:37:15": 805035} {
		got, err := parsePSCPUTime(raw)
		if err != nil || math.Abs(got-want) > .001 {
			t.Fatalf("%s: %f %v", raw, got, err)
		}
	}
}
func TestProcessMonitorDarwinPSIncludesPrivilegedCommand(t *testing.T) {
	command := "/Library/PrivilegedHelperTools/io.github.getkiko.kiko.core run -c /Users/ubuntu/Library/Application Support/Kiko/config.json  --flag"
	items := parseDarwinProcessPS(" 5430  2.1 1024 2048 29:49.79 S " + command + "\n1 0.1 42 99 1:00.00 Ss /sbin/launchd\n9 0 0 0 0:00.00 Z\nbad row\n")
	if len(items) != 3 || items[5430].Command != command || items[1].Command != "/sbin/launchd" || items[9].Command != "" {
		t.Fatalf("ps command parsing: %+v", items)
	}
	if items[5430].Memory == nil || *items[5430].Memory != 1024*1024 || items[5430].CPUTime == nil || *items[5430].CPUTime != 1789.79 {
		t.Fatalf("ps metrics: %+v", items[5430])
	}
}

func TestProcessMonitorDarwinArgsExcludeEnvironment(t *testing.T) {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, 3)
	raw = append(raw, []byte("/bin/tool\x00\x00/bin/tool\x00\x00argument with spaces\x00SECRET=value\x00")...)
	args := parseDarwinProcessArgs(raw)
	if len(args) != 3 || args[0] != "/bin/tool" || args[1] != "" || args[2] != "argument with spaces" {
		t.Fatalf("arguments: %q", args)
	}
	if parseDarwinProcessArgs(raw[:8]) != nil {
		t.Fatal("accepted truncated arguments")
	}
}
func TestProcessMonitorDarwinNativeMetadataAndHost(t *testing.T) {
	if err := loadDarwinMonitor(); err != nil {
		t.Fatal(err)
	}
	if darwinProcessUser(0) != "root" {
		t.Fatalf("root username: %s", darwinProcessUser(0))
	}
	pid := int32(os.Getpid())
	if started, err := monitoredProcessStartedAt(context.Background(), pid); err != nil || started <= 0 {
		t.Fatalf("start time: %d %v", started, err)
	}
	if path, err := monitoredProcessPath(context.Background(), pid); err != nil || path == "" {
		t.Fatalf("path: %s %v", path, err)
	}
	if command := darwinProcessCommand(pid); command == "" {
		t.Fatal("empty command")
	}
	counters, used, total := collectMonitorHost(context.Background())
	if counters == nil || counters.Total <= 0 || used == nil || total == nil || *used == 0 || *used > *total {
		t.Fatalf("host counters: %+v used=%v total=%v", counters, used, total)
	}
}

func TestProcessMonitorDarwinNetworkChild(t *testing.T) {
	address := os.Getenv("KIKO_MONITOR_NETWORK_TEST_ADDR")
	if address == "" {
		return
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(make([]byte, 1000000)); err != nil {
		t.Fatal(err)
	}
	fmt.Println("sent")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("closed")
	scanner.Scan() // Keep the process alive after closing its socket.
}

func TestProcessMonitorDarwinNetworkTotalsSurviveRealSocketClosure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessMonitorDarwinNetworkChild$")
	cmd.Env = append(os.Environ(), "KIKO_MONITOR_NETWORK_TEST_ADDR="+listener.Addr().String())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(5 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.ReadFull(conn, make([]byte, 1000000)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "sent" {
		t.Fatal("child failed to send traffic")
	}
	pid := int32(cmd.Process.Pid)
	started, err := monitoredProcessStartedAt(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	sample := func(old monitoredProcess) monitoredProcess {
		t.Helper()
		raw, err := darwinProcessNetwork(ctx)
		if err != nil {
			t.Fatal(err)
		}
		p := monitoredProcess{PID: pid, StartedAt: started, networkSample: &processNetworkSample{sockets: raw[pid]}}
		retainProcessNetwork(&p, old)
		return p
	}
	first := sample(monitoredProcess{})
	if first.SentBytes == nil || *first.SentBytes < 1000000 {
		t.Fatalf("sent bytes: %v", first.SentBytes)
	}
	if _, err = io.WriteString(stdin, "close\n"); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "closed" {
		t.Fatal("child failed to close socket")
	}
	_ = conn.Close()
	second := sample(first)
	if second.SentBytes == nil || *second.SentBytes != *first.SentBytes {
		t.Fatalf("socket closure changed total: first %d second %v", *first.SentBytes, second.SentBytes)
	}
	if actual, err := monitoredProcessStartedAt(ctx, pid); err != nil || actual != started {
		t.Fatalf("child should remain alive: %d %v", actual, err)
	}
}
