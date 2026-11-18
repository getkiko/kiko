// Command kiko-desktop is the MyGo desktop app for Kiko.
//
// It starts kiko-server, shows the Kiko console — drawn natively by
// MyGo's ui package — and shuts the server down when the app quits.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

func main() {
	// A single instance owns the console server and the app windows.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// Updates come from the signed releases named in mygo.json's updates;
	// builds without them, such as development builds, never check.
	mygo.Use(native.New(updater.Options{Language: "zh-Hans"}))
	shell := newShell()
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		shell.openServer(servers.LocalID)
	})
	mygo.App.WhenReady(shell.start)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
