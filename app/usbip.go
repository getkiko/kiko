package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usbipGOOS can be overridden by tests. Exporting devices on macOS requires disabling
// SIP, so Kiko disables the feature there.
var usbipGOOS = runtime.GOOS

const usbipMacOSBlocked = "导出设备需要关闭 SIP，Kiko 已禁用"

type usbipShareState struct {
	Running    bool     `json:"running"`
	ServiceTag string   `json:"serviceTag,omitempty"`
	PID        int      `json:"pid,omitempty"`
	Devices    []string `json:"devices,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type usbipShare struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	state    usbipShareState
}

func (s *server) usbipShareState() usbipShareState {
	s.usb.mu.Lock()
	defer s.usb.mu.Unlock()
	return s.usb.state
}

func (s *server) getUSBIPShare(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.usbipShareState())
}

type usbDevice struct {
	BusID     string `json:"busId"`
	VendorID  uint16 `json:"vendorId"`
	ProductID uint16 `json:"productId"`
	Serial    string `json:"serial,omitempty"`
	Product   string `json:"product,omitempty"`
}

// parseUSBDeviceList parses the TSV printed by `kiko-core api usbip device list` when
// stdout is not a terminal: busid, vid:pid, product, serial, with "-" for empty values.
func parseUSBDeviceList(output string) []usbDevice {
	devices := []usbDevice{}
	for line := range strings.SplitSeq(output, "\n") {
		cells := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(cells) < 4 {
			continue
		}
		vendor, product, ok := strings.Cut(cells[1], ":")
		vendorID, vendorErr := strconv.ParseUint(vendor, 16, 16)
		productID, productErr := strconv.ParseUint(product, 16, 16)
		if !ok || vendorErr != nil || productErr != nil {
			continue
		}
		clean := func(value string) string {
			if value == "-" {
				return ""
			}
			return value
		}
		devices = append(devices, usbDevice{
			BusID: cells[0], VendorID: uint16(vendorID), ProductID: uint16(productID),
			Product: clean(cells[2]), Serial: clean(cells[3]),
		})
	}
	return devices
}

func (s *server) usbDevices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, s.singBox, "api", "usbip", "device", "list").Output()
	if err != nil {
		message := err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.TrimSpace(string(exitErr.Stderr)) != "" {
			message = strings.TrimSpace(string(exitErr.Stderr))
		}
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": parseUSBDeviceList(string(out))})
}

type prefixLogWriter struct {
	s       *server
	pending []byte
	last    string
}

func (p *prefixLogWriter) Write(b []byte) (int, error) {
	p.pending = append(p.pending, b...)
	for {
		i := bytes.IndexByte(p.pending, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(p.pending[:i]), "\r"); line != "" {
			p.last = line
			p.s.logs.add("[usbip] " + line)
		}
		p.pending = p.pending[i+1:]
	}
	return len(b), nil
}

func (s *server) startUSBIPShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServiceTag string   `json:"serviceTag"`
		Devices    []string `json:"devices"`
		Capture    bool     `json:"capture"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if usbipGOOS == "darwin" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": usbipMacOSBlocked})
		return
	}
	for _, value := range append([]string{in.ServiceTag}, in.Devices...) {
		if strings.HasPrefix(value, "-") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "参数不能以 - 开头: " + value})
			return
		}
	}
	for _, device := range in.Devices {
		if device == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "设备选择器不能为空"})
			return
		}
	}
	if s.grpcClient() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "gRPC API 未连接"})
		return
	}
	s.rpc.mu.Lock()
	port, secret := s.rpc.port, s.rpc.secret
	s.rpc.mu.Unlock()

	args := []string{"api", "usbip", "share"}
	if in.ServiceTag != "" {
		args = append(args, "--service", in.ServiceTag)
	}
	if in.Capture {
		args = append(args, "--capture")
	}
	if len(in.Devices) == 0 {
		args = append(args, "--all")
	} else {
		args = append(args, "--")
		args = append(args, in.Devices...)
	}
	cmd := exec.Command(s.singBox, args...)
	cmd.Env = append(os.Environ(), "BOX_API_URL=http://127.0.0.1:"+strconv.Itoa(port), "BOX_API_SECRET="+secret)
	prepareProcess(cmd)
	logs := &prefixLogWriter{s: s}
	cmd.Stdout, cmd.Stderr = logs, logs

	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	s.usb.mu.Lock()
	if s.usb.cmd != nil {
		s.usb.mu.Unlock()
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "已在导出设备，请先停止"})
		return
	}
	if err := cmd.Start(); err != nil {
		s.usb.mu.Unlock()
		s.mu.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	s.usb.cmd, s.usb.stopping = cmd, false
	s.usb.state = usbipShareState{Running: true, ServiceTag: in.ServiceTag, PID: cmd.Process.Pid, Devices: in.Devices}
	state := s.usb.state
	s.usb.mu.Unlock()
	s.mu.Unlock()
	s.logs.add("[kiko] 开始导出 USB 设备")
	s.emit("usbipShare", state)
	go s.waitUSBIPShare(cmd, logs)
	writeJSON(w, http.StatusOK, state)
}

func (s *server) waitUSBIPShare(cmd *exec.Cmd, logs *prefixLogWriter) {
	err := cmd.Wait()
	s.usb.mu.Lock()
	if s.usb.cmd != cmd {
		s.usb.mu.Unlock()
		return
	}
	s.usb.cmd = nil
	s.usb.state.Running, s.usb.state.PID = false, 0
	if err != nil && !s.usb.stopping {
		s.usb.state.Error = logs.last
		if s.usb.state.Error == "" {
			s.usb.state.Error = err.Error()
		}
	}
	state := s.usb.state
	s.usb.mu.Unlock()
	s.logs.add("[kiko] USB 设备导出已结束")
	s.emit("usbipShare", state)
}

// stopUSBIPShare asks the export process to terminate without waiting; waitUSBIPShare
// handles the process exit.
func (s *server) stopUSBIPShare() {
	s.usb.mu.Lock()
	cmd := s.usb.cmd
	s.usb.stopping = cmd != nil
	s.usb.mu.Unlock()
	if cmd == nil {
		return
	}
	if gracefulProcess(cmd) != nil {
		_ = forceProcess(cmd)
		return
	}
	time.AfterFunc(5*time.Second, func() {
		s.usb.mu.Lock()
		still := s.usb.cmd == cmd
		s.usb.mu.Unlock()
		if still {
			_ = forceProcess(cmd)
		}
	})
}

func (s *server) stopUSBIPShareHandler(w http.ResponseWriter, r *http.Request) {
	s.stopUSBIPShare()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && s.usbipShareState().Running {
		time.Sleep(50 * time.Millisecond)
	}
	writeJSON(w, http.StatusOK, s.usbipShareState())
}
