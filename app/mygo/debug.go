package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// probeReport is what the debug probe records about the local server's console.
type probeReport struct {
	Page       string `json:"page"`
	Loaded     bool   `json:"loaded"`
	LoadError  string `json:"loadError,omitempty"`
	Connected  bool   `json:"connected"`
	ServerURL  string `json:"serverURL"`
	CoreState  string `json:"coreState,omitempty"`
	WindowID   int    `json:"windowID"`
	Screenshot string `json:"screenshot,omitempty"`
	CaptureErr string `json:"captureError,omitempty"`
	Error      string `json:"error,omitempty"`
	CapturedAt string `json:"capturedAt"`
}

// startDebugProbe records the console's state and a PNG of the window,
// so the shell can be verified without screenshots by hand. It is inert
// unless KIKO_DEBUG_PROBE names the JSON output path; the PNG is written
// beside it.
func startDebugProbe(s *shell) {
	output := os.Getenv("KIKO_DEBUG_PROBE")
	if output == "" {
		return
	}
	go func() {
		// Give the console time to load the document and connect.
		time.Sleep(6 * time.Second)
		report := probeReport{ServerURL: s.paths.serverURL(), CapturedAt: time.Now().Format(time.RFC3339)}
		local := s.local()
		window := local.current()
		if window == nil {
			report.Error = "no window"
			writeProbe(output, report)
			return
		}
		report.WindowID = window.ID()
		// The UI thread fills a copy of its own: one answering after the
		// timeout must not write the report being written.
		answer := make(chan probeReport, 1)
		window.Update(func() {
			c := local.console
			state := probeReport{Page: c.Page(), Loaded: c.Store.Loaded, LoadError: c.Store.LoadError,
				Connected: c.Env.Server.Connected}
			if c.Env.Server.HasRuntime {
				state.CoreState = c.Env.Server.Runtime.State
			}
			answer <- state
		})
		select {
		case state := <-answer:
			report.Page, report.Loaded, report.LoadError = state.Page, state.Loaded, state.LoadError
			report.Connected, report.CoreState = state.Connected, state.CoreState
		case <-time.After(5 * time.Second):
			report.Error = "UI thread did not answer"
		}
		if png, err := window.CapturePage(); err != nil {
			report.CaptureErr = err.Error()
		} else {
			path := strings.TrimSuffix(output, ".json") + ".png"
			if err := os.WriteFile(path, png, 0o600); err != nil {
				report.CaptureErr = err.Error()
			} else {
				report.Screenshot = path
			}
		}
		writeProbe(output, report)
	}()
}

func writeProbe(path string, report probeReport) {
	data, _ := json.MarshalIndent(report, "", "  ")
	_ = os.WriteFile(path, data, 0o600)
}
