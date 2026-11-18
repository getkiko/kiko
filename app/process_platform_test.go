package main

import "testing"

func TestWindowsAppIdentity(t *testing.T) {
	for _, test := range []struct {
		path    string
		version map[string]string
		want    string
	}{
		{`C:\Program Files\Google\Chrome\Application\chrome.exe`, map[string]string{"FileDescription": "Google Chrome", "ProductName": "Chrome"}, "Google Chrome"},
		{`C:\Users\a\AppData\Local\Programs\Microsoft VS Code\Code.exe`, map[string]string{"FileDescription": " ", "ProductName": "Visual Studio Code"}, "Visual Studio Code"},
		{`D:\Tools\portable.EXE`, nil, "portable"},
	} {
		if got := windowsAppName(test.path, test.version); got != test.want {
			t.Errorf("%s: %q, want %q", test.path, got, test.want)
		}
	}
	for _, test := range []struct {
		path, dir string
		within    bool
	}{
		{`C:\Windows\System32\svchost.exe`, `C:\Windows`, true},
		{`c:\windows\explorer.exe`, `C:\WINDOWS\`, true},
		{`C:/Windows/notepad.exe`, `C:\Windows`, true},
		{`C:\WindowsApps\x.exe`, `C:\Windows`, false},
		{`C:\Program Files\App\app.exe`, `C:\Windows`, false},
		{`C:\Windows`, `C:\Windows`, false},
		{`C:\x.exe`, ``, false},
	} {
		if got := windowsPathWithin(test.path, test.dir); got != test.within {
			t.Errorf("%s in %s: %v", test.path, test.dir, got)
		}
	}
}

// Icons with an alpha channel keep it; those without take the mask.
func TestIconImage(t *testing.T) {
	// Two BGRA pixels: blue half-transparent, red opaque.
	img := iconImage([]byte{255, 0, 0, 128, 0, 0, 255, 255}, nil, 2, 1)
	if got := img.Pix; got[0] != 0 || got[2] != 255 || got[3] != 128 || got[4] != 255 || got[7] != 255 {
		t.Errorf("alpha icon %v", got)
	}
	// No alpha: the white mask pixel is transparent, the black one opaque.
	img = iconImage([]byte{10, 20, 30, 0, 40, 50, 60, 0}, []byte{255, 255, 255, 0, 0, 0, 0, 0}, 2, 1)
	if got := img.Pix; got[3] != 0 || got[7] != 255 || got[4] != 60 || got[6] != 40 {
		t.Errorf("masked icon %v", got)
	}
}

func TestStatusCapHas(t *testing.T) {
	status := "Name:\tkiko-server\nCapInh:\t0000000000000000\nCapEff:\t0000000000000000\nCapAmb:\t0000000000003000\n"
	if !statusCapHas(status, "CapAmb", linuxCapNetAdmin) {
		t.Error("ambient CAP_NET_ADMIN missed")
	}
	if statusCapHas(status, "CapEff", linuxCapNetAdmin) || statusCapHas(status, "CapBnd", linuxCapNetAdmin) {
		t.Error("capability found in an empty or missing set")
	}
}

func TestSystemUID(t *testing.T) {
	if got := parseLoginUIDMin("# comment\nUID_MIN\t\t\t  500\nUID_MAX 60000\n"); got != 500 {
		t.Errorf("UID_MIN %d", got)
	}
	if got := parseLoginUIDMin("UID_MAX 60000\n"); got != 1000 {
		t.Errorf("default UID_MIN %d", got)
	}
	for uid, system := range map[uint32]bool{0: true, 101: true, 999: true, 1000: false, 1001: false, 65534: true} {
		if systemUID(uid, 1000) != system {
			t.Errorf("uid %d system %v", uid, !system)
		}
	}
}

// A parent PID reused by a later process no longer parents.
func TestDetachReusedParents(t *testing.T) {
	items := []monitoredProcess{
		{PID: 4, PPID: 0, StartedAt: 1},
		{PID: 100, PPID: 4, StartedAt: 50},
		{PID: 200, PPID: 300, StartedAt: 60},
		{PID: 300, PPID: 4, StartedAt: 90},
		{PID: 400, PPID: 999, StartedAt: 70},
	}
	detachReusedParents(items)
	for _, p := range items {
		want := map[int32]int32{4: 0, 100: 4, 200: 0, 300: 4, 400: 999}[p.PID]
		if p.PPID != want {
			t.Errorf("pid %d parent %d, want %d", p.PID, p.PPID, want)
		}
	}
}
