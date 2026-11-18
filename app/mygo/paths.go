package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
)

// Paths resolves the bundled binaries, helper resources and data directory
// for packaged and development layouts.
type Paths struct {
	// resources is PathResources: Contents/Resources inside a macOS app
	// bundle, the executable's directory elsewhere.
	resources string
	// sourceRoot is the project root when running unpackaged, used to find
	// the build/ tree during development.
	sourceRoot string
	// userData is the app's data directory.
	userData string
	// serverData is kiko-server's data directory, set when its migration
	// ran; empty means <userData>/kiko-server.
	serverData string
}

func newPaths() (*Paths, error) {
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil, err
	}
	appData, err := mygo.App.Path(mygo.PathAppData)
	if err != nil {
		return nil, err
	}
	userData, err := userDataDir(appData, mygo.App.Name())
	if err != nil {
		return nil, err
	}
	serverData, err := migrateLegacyServerData(userData)
	if err != nil {
		return nil, err
	}
	// Under `go run` the executable is temporary and PathResources falls
	// back to ./resources, so the project root is the working directory.
	sourceRoot, _ := os.Getwd()
	return &Paths{resources: resources, sourceRoot: sourceRoot, userData: userData, serverData: serverData}, nil
}

// userDataDir migrates the app's data directory and returns it. When the
// legacy directory could not move, MyGo's PathUserData is pointed at it
// before anything asks for that path: PathUserData creates
// <appData>/<appName> on first use, and the framework's own files
// (window state, updater state) would otherwise land there, which would
// stop the move at the next start.
func userDataDir(appData, appName string) (string, error) {
	appDir, err := migrateLegacyData(appData, appName)
	if err != nil {
		return "", err
	}
	if appDir != filepath.Join(appData, appName) {
		mygo.App.SetPath(mygo.PathUserData, appDir)
	}
	return mygo.App.Path(mygo.PathUserData)
}

// frameworkFiles are the files MyGo writes into PathUserData on its own,
// without any data of the app.
var frameworkFiles = map[string]bool{
	"window-state.json": true, "recent-documents.json": true, "updater.json": true, "locale": true,
}

// removeIfUnused removes dir when it holds nothing but the given files
// MyGo writes on its own and empty directories, as a run from before the
// fallback to the legacy directory may leave behind, and reports whether
// dir is gone. Anything else keeps dir as it is.
func removeIfUnused(dir string, allowed map[string]bool) bool {
	if !onlyFiles(dir, allowed) {
		return false
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[kiko] removing %s failed: %v", dir, err)
		return false
	}
	return true
}

// onlyFiles reports whether dir holds only regular files named in allowed
// and directories holding nothing at all.
func onlyFiles(dir string, allowed map[string]bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			if !onlyFiles(filepath.Join(dir, entry.Name()), nil) {
				return false
			}
		case entry.Type().IsRegular() && allowed[entry.Name()]:
		default:
			return false
		}
	}
	return true
}

// legacyDataNames are the app's earlier names (Poppy, then sbcfg-desktop),
// newest first, whose data directories move to the current name.
var legacyDataNames = []string{"Poppy", "sbcfg-desktop"}

// renameDir moves a data directory; tests replace it to fail.
var renameDir = os.Rename

// moveLegacyDir renames legacy to current and returns the directory to use
// this run: current, or legacy when the rename failed (Windows refuses to
// move a directory holding an open file). An empty directory is never used
// in its place; the move is retried at the next start.
func moveLegacyDir(legacy, current string) string {
	if err := renameDir(legacy, current); err != nil {
		log.Printf("[kiko] moving %s to %s failed, using it where it is: %v", legacy, current, err)
		return legacy
	}
	return current
}

// migrateLegacyData moves the app's data directory from an earlier name to
// <appData>/<appName> once and returns the app's data directory for this
// run.
func migrateLegacyData(appData, appName string) (string, error) {
	current := filepath.Join(appData, appName)
	if _, err := os.Stat(current); err == nil {
		// A current directory with only MyGo's own files is left over from
		// a run that kept using the legacy directory; it gives way to it.
		if !legacyDataExists(appData, appName) || !removeIfUnused(current, frameworkFiles) {
			return current, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	for _, name := range legacyDataNames {
		if name == appName {
			continue
		}
		legacy := filepath.Join(appData, name)
		if _, err := os.Stat(legacy); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		return moveLegacyDir(legacy, current), nil
	}
	return current, nil
}

// legacyDataExists reports whether a data directory of an earlier name is
// still there.
func legacyDataExists(appData, appName string) bool {
	for _, name := range legacyDataNames {
		if name == appName {
			continue
		}
		if info, err := os.Stat(filepath.Join(appData, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// packaged reports whether the app runs from a built bundle, matching
// the MyGO packaging state.
func (p *Paths) packaged() bool {
	return mygo.App.IsPackaged()
}

// asset returns a path under the app's own files: the bundled resources
// when packaged, the project directory when developing.
func (p *Paths) asset(parts ...string) string {
	if p.packaged() {
		return filepath.Join(append([]string{p.resources}, parts...)...)
	}
	return filepath.Join(append([]string{p.sourceRoot, "resources", runtime.GOOS + "-" + runtime.GOARCH}, parts...)...)
}

// serverDataName is kiko-server's directory inside the app's data
// directory.
const serverDataName = "kiko-server"

// legacyServerDataName is the server directory's name before the app was
// renamed Kiko; migrateLegacyServerData moves it to serverDataName.
const legacyServerDataName = "sbcfg-server"

// migrateLegacyServerData renames <userData>/sbcfg-server to
// <userData>/kiko-server once, leaving an existing kiko-server alone, and
// returns kiko-server's data directory for this run.
func migrateLegacyServerData(userData string) (string, error) {
	current := filepath.Join(userData, serverDataName)
	legacy := filepath.Join(userData, legacyServerDataName)
	if _, err := os.Lstat(current); err == nil {
		// An empty kiko-server gives way to a legacy directory.
		if info, err := os.Lstat(legacy); err != nil || !info.IsDir() || !removeIfUnused(current, nil) {
			return current, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Lstat(legacy); err != nil {
		if os.IsNotExist(err) {
			return current, nil
		}
		return "", err
	}
	return moveLegacyDir(legacy, current), nil
}

// dataDir is where kiko-server keeps its configuration, inside the
// desktop shell's data directory.
func (p *Paths) dataDir() string {
	if p.serverData != "" {
		return p.serverData
	}
	return filepath.Join(p.userData, serverDataName)
}

// binaryPath resolves kiko-server or kiko-core. An environment override
// wins for KIKO_SERVER_PATH and KIKO_CORE_PATH.
func (p *Paths) binaryPath(name string) string {
	envName := "KIKO_CORE_PATH"
	if name == "kiko-server" {
		envName = "KIKO_SERVER_PATH"
	}
	if override := os.Getenv(envName); override != "" {
		return override
	}
	filename := name
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	if p.packaged() {
		return filepath.Join(p.resources, "bin", filename)
	}
	if staged := filepath.Join(p.platformResources(), "bin", filename); fileExists(staged) {
		return staged
	}
	return filepath.Join(p.distDir(), filename)
}

// go run sees platform directories before MyGO merges them into a bundle.
func (p *Paths) platformResources() string {
	return p.asset()
}

// distDir is the per-platform package directory produced by the Makefile's
// package-macos/package-windows targets, matching bundledBinaryPath.
func (p *Paths) distDir() string {
	system := runtime.GOOS
	if runtimeIsDarwin() {
		system = "macos"
	}
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "amd64"
	}
	return filepath.Join(p.sourceRoot, "..", "build", fmt.Sprintf("package-%s-%s", system, arch))
}

// helperConfigPath points at the helperpaths/paths.json the helper package
// also embeds, so the app and the helper agree on socket and install paths.
func (p *Paths) helperConfigPath() string {
	return p.asset("helper", "paths.json")
}

// helperVersionsPath is the explicitly maintained component revision manifest.
func (p *Paths) helperVersionsPath() string {
	return p.asset("helper", "versions.json")
}

// helperInstaller is the privileged install script.
func (p *Paths) helperInstaller() string {
	return p.asset("helper", "install-helper.sh")
}

// helperExecutable is the bundled kiko-helper.
func (p *Paths) helperExecutable() string {
	if p.packaged() {
		return filepath.Join(p.resources, "helper", "kiko-helper")
	}
	if staged := filepath.Join(p.platformResources(), "helper", "kiko-helper"); fileExists(staged) {
		return staged
	}
	return filepath.Join(p.distDir(), "kiko-helper")
}

// serverURL is the loopback address kiko-server listens on; the native
// console's API client talks to it directly.
func (p *Paths) serverURL() string {
	port := serverPort
	if override := os.Getenv("KIKO_PORT"); override != "" {
		fmt.Sscanf(override, "%d", &port)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// binaryEnv returns the child environment with the binary directory first
// on PATH, so kiko-core finds its siblings. Windows treats variable names
// case-insensitively, so a single PATH key is kept there.
func binaryEnv(binaryDir string) []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		return append(env, "PATH="+binaryDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.EqualFold(strings.SplitN(entry, "=", 2)[0], "PATH") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "PATH="+binaryDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fileExists checks bundled resources and development binaries.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
