package processes

import (
	"regexp"
	"testing"
)

func TestPathRegex(t *testing.T) {
	re := func(path string, dir bool) *regexp.Regexp { return regexp.MustCompile(PathRegex(path, dir)) }
	win := re(`C:\Program Files\应用 (x64)\client[1].exe`, false)
	if !win.MatchString("c:/program files/应用 (x64)/client[1].exe") {
		t.Error("windows path does not match across case and separators")
	}
	if win.MatchString("C:/Program Files/应用 (x64)/client1.exe") || win.MatchString("C:/Program Files/应用 (x64)/client[1].exe.bak") {
		t.Error("windows path matches too much")
	}
	if !re(`\\server\share\a.exe`, false).MatchString("//SERVER/share/a.exe") {
		t.Error("UNC path")
	}
	dir := re(`C:\Apps\demo`, true)
	if !dir.MatchString(`C:\Apps\demo\child.exe`) || dir.MatchString(`C:\Apps\demolition\child.exe`) {
		t.Error("directory boundary")
	}
	unix := re("/Applications/Demo.app/Contents/MacOS/Demo", false)
	if !unix.MatchString("/Applications/Demo.app/Contents/MacOS/Demo") ||
		unix.MatchString("/Applications/DemoXapp/Contents/MacOS/Demo") ||
		unix.MatchString("/applications/Demo.app/Contents/MacOS/Demo") {
		t.Error("unix path")
	}
}
