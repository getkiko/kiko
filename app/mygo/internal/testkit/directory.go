package testkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// Directory is a fake env.ServerDirectory: the local server and the servers added,
// recording the calls. Pairing strings starting "kiko-pair://" add a
// server named by their name parameter; a manual add first asks to
// confirm the fingerprint Pin.
type Directory struct {
	mu      sync.Mutex
	list    []servers.Server
	restore bool
	// Pin is the certificate a manual add or a code repair reads.
	Pin []byte
	// Calls are "open <id>", "add <uri>", "manual <addr> <code>",
	// "repair <id> <input>", "rename <id> <name>", "remove <id>".
	calls []string
	// Insecure makes InsecureTokens report the file fallback.
	Insecure bool
}

// NewDirectory returns a directory holding only the local server.
func NewDirectory() *Directory {
	return &Directory{list: []servers.Server{{ID: servers.LocalID, Name: "本机", URL: "http://127.0.0.1:9099"}},
		Pin: make([]byte, 32)}
}

func serverEntry(id, name, url, fp string) servers.Server {
	return servers.Server{ID: id, Name: name, URL: url, Fingerprint: fp, Color: servers.Colors[0],
		AddedAt: time.Now().Add(-time.Hour), LastConnected: time.Now().Add(-5 * time.Minute)}
}

func (d *Directory) add(s servers.Server) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.list = append(d.list, s)
}

// Add lists a server, as pairing would.
func (d *Directory) Add(id, name, url, fp string) { d.add(serverEntry(id, name, url, fp)) }

func (d *Directory) record(call string) {
	d.mu.Lock()
	d.calls = append(d.calls, call)
	d.mu.Unlock()
}

// Calls returns the calls so far.
func (d *Directory) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *Directory) Servers() []servers.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.list)
}

func (d *Directory) InsecureTokens() bool { return d.Insecure }

func (d *Directory) OpenServer(id string) { d.record("open " + id) }

func (d *Directory) AddServer(_ context.Context, uri string) (servers.Server, error) {
	d.record("add " + uri)
	p, err := servers.ParsePairURI(uri)
	if err != nil {
		return servers.Server{}, err
	}
	s := serverEntry(fmt.Sprintf("srv%d", len(d.Servers())), p.Name, p.URL(), "")
	d.add(s)
	return s, nil
}

func (d *Directory) AddServerManual(_ context.Context, addr, code string, confirm func([]byte) bool) (servers.Server, error) {
	if !confirm(d.Pin) {
		return servers.Server{}, errors.New("证书指纹未确认")
	}
	d.record("manual " + addr + " " + code)
	s := serverEntry(fmt.Sprintf("srv%d", len(d.Servers())), addr, "https://"+addr, "")
	d.add(s)
	return s, nil
}

func (d *Directory) RepairServer(_ context.Context, id, input string, confirm func([]byte) bool) error {
	if !servers.IsPairURI(input) && !confirm(d.Pin) {
		return errors.New("证书指纹未确认")
	}
	d.record("repair " + id + " " + input)
	return nil
}

func (d *Directory) RenameServer(id, name string) error {
	d.record("rename " + id + " " + name)
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.list {
		if d.list[i].ID == id {
			d.list[i].Name = name
		}
	}
	return nil
}

func (d *Directory) RemoveServer(_ context.Context, id string) error {
	d.record("remove " + id)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.list = slices.DeleteFunc(d.list, func(s servers.Server) bool { return s.ID == id })
	return nil
}

func (d *Directory) RestoreWindows() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.restore
}

func (d *Directory) SetRestoreWindows(on bool) error {
	d.mu.Lock()
	d.restore = on
	d.mu.Unlock()
	return nil
}

// DirectoryShell is the fake shell with a server directory, as the
// desktop shell gives every console.
type DirectoryShell struct {
	*Shell
	*Directory
}

var _ env.ServerDirectory = (*DirectoryShell)(nil)

// NewDirectoryEnv returns the local console's environment for srv with a
// server directory.
func NewDirectoryEnv(srv *Server) (*env.Env, *DirectoryShell) {
	shell := &DirectoryShell{Shell: &Shell{}, Directory: NewDirectory()}
	e := env.New(api.New(srv.URL), shell)
	e.Target = env.Target{ID: servers.LocalID, Name: "本机", URL: srv.URL}
	return e, shell
}
