package main

import (
	_ "embed"
)

// trayIcon is the menu bar icon: the logo's K (tools/trayicon), black on
// transparent so that macOS tints it as a template image. It is 36 pixels
// square, 18 points on a Retina display.
//
//go:embed resources/tray-icon.png
var trayIcon []byte

// trayIconPNG returns the menu bar icon.
func trayIconPNG() []byte { return trayIcon }
