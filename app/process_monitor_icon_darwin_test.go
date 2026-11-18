//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessMonitorDarwinIconChild(t *testing.T) {
	if os.Getenv("KIKO_MONITOR_ICON_TEST_CHILD") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestProcessMonitorDarwinAssetCatalogIcon(t *testing.T) {
	// TickTick ships CFBundleIconName + Assets.car and exercises the system
	// Icon Services path that the ICNS fixture below cannot cover.
	const bundle = "/Applications/TickTick.app"
	if _, err := os.Stat(bundle); err != nil {
		t.Skip("TickTick fixture is available on the development Mac")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := processBundleIcon(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	icon, err := png.Decode(bytes.NewReader(data))
	if err != nil || icon.Bounds().Dx() != 128 || icon.Bounds().Dy() != 128 {
		t.Fatalf("asset-catalog PNG: %v", err)
	}
	opaque := 0
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			_, _, _, alpha := icon.At(x, y).RGBA()
			if alpha > 0xc000 {
				opaque++
			}
		}
	}
	if opaque < 4096 {
		t.Fatalf("asset-catalog image is mostly empty: %d opaque pixels", opaque)
	}
}

func TestProcessMonitorDarwinIconServesConvertedPNG(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Test App.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	bin := filepath.Join(bundle, "Contents", "MacOS", "icon-test")
	for _, dir := range []string{resources, filepath.Dir(bin)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryData, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	write(bin, binaryData, 0700)
	write(filepath.Join(bundle, "Contents", "Info.plist"), []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIconFile</key><string>TestIcon</string></dict></plist>`), 0600)
	// A real ICNS container with a known image verifies conversion and HTTP bytes.
	img := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 220, G: 40, B: 80, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	icns := make([]byte, 16)
	copy(icns, "icns")
	binary.BigEndian.PutUint32(icns[4:8], uint32(16+encoded.Len()))
	copy(icns[8:], "ic07")
	binary.BigEndian.PutUint32(icns[12:16], uint32(8+encoded.Len()))
	icon := filepath.Join(resources, "TestIcon.icns")
	write(icon, append(icns, encoded.Bytes()...), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-test.run=^TestProcessMonitorDarwinIconChild$")
	cmd.Env = append(os.Environ(), "KIKO_MONITOR_ICON_TEST_CHILD=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("child readiness: %q, %v", ready, err)
	}
	started, err := monitoredProcessStartedAt(context.Background(), int32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	mux := newRouteMux(nil)
	registerProcessMonitorActions(mux)
	url := fmt.Sprintf("/api/v1/process-monitor/%d/icon?startedAt=%d", cmd.Process.Pid, started)
	// A missing file can recover on the same handler after installation finishes.
	if err := os.Rename(icon, icon+".pending"); err != nil {
		t.Fatal(err)
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest("GET", url, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing icon: %d", missing.Code)
	}
	if err := os.Rename(icon+".pending", icon); err != nil {
		t.Fatal(err)
	}
	request := func() []byte {
		t.Helper()
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest("GET", url, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("icon response: %d %s", recorder.Code, recorder.Header().Get("Content-Type"))
		}
		decoded, err := png.Decode(bytes.NewReader(recorder.Body.Bytes()))
		if err != nil || decoded.Bounds() != img.Bounds() {
			t.Fatalf("PNG conversion: %v", err)
		}
		r, g, b, _ := decoded.At(32, 32).RGBA()
		if r <= g || r <= b {
			t.Fatal("converted icon lost its source image")
		}
		return recorder.Body.Bytes()
	}
	first := request()
	if err := os.Remove(icon); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, request()) {
		t.Fatal("cached PNG changed")
	}
}
