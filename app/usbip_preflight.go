package main

import (
	"net/http"
	"os/user"
	"runtime"
	"strings"
)

type usbipPreflight struct {
	OS              string            `json:"os"`
	Arch            string            `json:"arch"`
	User            string            `json:"user"`
	Elevated        bool              `json:"elevated"`
	ServerSupported bool              `json:"serverSupported"`
	SingBoxUSBIP    bool              `json:"singBoxUSBIP"`
	Kernel          *usbipKernelState `json:"kernel,omitempty"`
}

// The variables below allow injection in tests.
var (
	currentUsername = func() string {
		if current, err := user.Current(); err == nil {
			return current.Username
		}
		return ""
	}
	usbipKernelDetector = detectUSBIPKernel
)

// usbipPreflight reports whether the machine running kiko-server can run
// USB/IP: whether kiko-core was built with it, permissions, and the Linux
// kernel modules.
func (s *server) usbipPreflight(w http.ResponseWriter, r *http.Request) {
	result := usbipPreflight{
		OS: usbipGOOS, Arch: runtime.GOARCH, User: currentUsername(), Elevated: isElevated(),
		ServerSupported: usbipGOOS != "darwin",
		SingBoxUSBIP:    strings.Contains(s.sbVersion, "with_usbip"),
	}
	if usbipGOOS == "linux" {
		result.Kernel = usbipKernelDetector()
	}
	writeJSON(w, http.StatusOK, result)
}
