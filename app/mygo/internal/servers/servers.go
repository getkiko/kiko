// Package servers is the desktop app's directory of kiko-servers: the
// app's own server (the local server, always present, never stored) and the remote
// servers paired with it. Each remote entry keeps the server's address and
// pinned certificate fingerprint in servers.json; its device token stays
// in the system's credential store, or, where there is none, in a file
// only the user can read.
//
// Pairing trades a one-time code for the token: from a pairing string
// (kiko-pair://host:port?fp=&code=&name=), which carries the fingerprint,
// or by hand from an address and code, trusting the certificate the user
// confirms on first use.
//
// A Directory is safe from any goroutine. Pairing and removal talk to the
// server and may wait on the credential store: run them off the UI
// thread.
package servers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// LocalID is the id of the app's own server.
const LocalID = "local"

// DefaultPort is the port of a server's remote access when an address
// names none.
const DefaultPort = 9443

// Server is an entry of the directory.
type Server struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the server's base URL: https://host:port for a remote one.
	URL string `json:"url"`
	// Fingerprint is the pinned certificate's SHA-256, lowercase hex.
	Fingerprint string `json:"fingerprint,omitempty"`
	// DeviceID is this device's id at the server, to revoke it.
	DeviceID string `json:"deviceId,omitempty"`
	// Color tells the server's windows apart, as "#5856D6".
	Color         string    `json:"color,omitempty"`
	AddedAt       time.Time `json:"addedAt"`
	LastConnected time.Time `json:"lastConnected,omitzero"`
}

// Remote reports whether s is a paired remote server.
func (s Server) Remote() bool { return s.ID != LocalID }

// Pin is the fingerprint's bytes.
func (s Server) Pin() []byte {
	fp, _ := api.ParseFingerprint(s.Fingerprint)
	return fp
}

// Address is the server's host:port.
func (s Server) Address() string {
	if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return s.URL
}

// Colors are the accents given to remote servers in turn.
var Colors = []string{"#5856D6", "#AF52DE", "#30B0C7", "#FF2D55", "#A2845E", "#00C7BE"}

// SecretStore keeps the device tokens: the system's credential store.
// Get fails with an error matching ErrSecretNotFound for a missing key;
// a missing store fails with errors.ErrUnsupported, and the directory
// keeps tokens in a file instead.
type SecretStore interface {
	Set(key string, value []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// ErrSecretNotFound is what a SecretStore's Get matches for a missing
// key.
var ErrSecretNotFound = errors.New("secret not found")

// ErrNoToken is a remote server without a stored token: pair it again.
var ErrNoToken = errors.New("没有找到此服务器的令牌，请重新配对")

// file is servers.json.
type file struct {
	Servers []Server `json:"servers"`
	// Restore reopens the windows open at quit.
	Restore bool     `json:"restoreWindows,omitempty"`
	Open    []string `json:"openWindows,omitempty"`
}

// Directory is the list of servers.
type Directory struct {
	path      string
	tokenPath string
	secrets   SecretStore
	device    string

	// tokens serializes edits of the token file.
	tokens sync.Mutex

	mu       sync.Mutex
	local    Server
	file     file
	fallback bool
	onChange []func()
}

// Open reads the directory kept in dir (servers.json). local is the app's
// own server; secrets may be nil, the tokens then kept in a file.
// deviceName is how this device introduces itself when pairing.
func Open(dir string, local Server, secrets SecretStore, deviceName string) (*Directory, error) {
	local.ID = LocalID
	if local.Name == "" {
		local.Name = "本机"
	}
	d := &Directory{
		path:      filepath.Join(dir, "servers.json"),
		tokenPath: filepath.Join(dir, "server-tokens.json"),
		secrets:   secrets,
		device:    deviceName,
		local:     local,
		fallback:  secrets == nil,
	}
	data, err := os.ReadFile(d.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &d.file); err != nil {
			return nil, fmt.Errorf("读取服务器列表失败: %w", err)
		}
	}
	d.file.Servers = slices.DeleteFunc(d.file.Servers, func(s Server) bool { return s.ID == "" || s.ID == LocalID })
	return d, nil
}

// OnChange calls fn after each change of the list, on the goroutine that
// made it.
func (d *Directory) OnChange(fn func()) {
	d.mu.Lock()
	d.onChange = append(d.onChange, fn)
	d.mu.Unlock()
}

func (d *Directory) changed() {
	d.mu.Lock()
	fns := slices.Clone(d.onChange)
	d.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// List returns the local server and then the remote servers in the order added.
func (d *Directory) List() []Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Server{d.local}, d.file.Servers...)
}

// Get returns the server with id.
func (d *Directory) Get(id string) (Server, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == LocalID {
		return d.local, true
	}
	i := d.index(id)
	if i < 0 {
		return Server{}, false
	}
	return d.file.Servers[i], true
}

func (d *Directory) index(id string) int {
	return slices.IndexFunc(d.file.Servers, func(s Server) bool { return s.ID == id })
}

// InsecureTokens reports whether tokens are kept in a file because the
// system has no credential store.
func (d *Directory) InsecureTokens() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fallback
}

// Token returns a remote server's device token.
func (d *Directory) Token(id string) (string, error) {
	value, err := d.getSecret(secretKey(id))
	if errors.Is(err, ErrSecretNotFound) {
		return "", ErrNoToken
	}
	return string(value), err
}

// Client returns an API client of the server: the loopback client for
// the local server, a pinned client with the device token for a remote one.
func (d *Directory) Client(id string) (*api.Client, error) {
	s, ok := d.Get(id)
	if !ok {
		return nil, fmt.Errorf("服务器 %s 不存在", id)
	}
	if !s.Remote() {
		return api.New(s.URL), nil
	}
	token, err := d.Token(id)
	if err != nil {
		return nil, err
	}
	return api.NewWith(s.URL, api.Options{Token: token, Fingerprint: s.Pin()}), nil
}

// Options are a remote server's client options, for a client re-created
// after pairing again.
func (d *Directory) Options(id string) (api.Options, error) {
	s, ok := d.Get(id)
	if !ok || !s.Remote() {
		return api.Options{}, nil
	}
	token, err := d.Token(id)
	return api.Options{Token: token, Fingerprint: s.Pin()}, err
}

// PairURI is a parsed pairing string.
type PairURI struct {
	// Host is host:port.
	Host        string
	Fingerprint []byte
	Code        string
	Name        string
}

// URL is the server's base URL.
func (p PairURI) URL() string { return "https://" + p.Host }

// PairScheme is the scheme of pairing strings.
const PairScheme = "kiko-pair"

// legacyPairScheme is the scheme printed by servers from before the
// rename to Kiko (sbcfg-server pair); their pairing strings still work.
const legacyPairScheme = "sbcfg-pair"

// IsPairURI reports whether input looks like a pairing string rather than
// a typed address and code.
func IsPairURI(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	return strings.HasPrefix(lower, PairScheme+":") || strings.HasPrefix(lower, legacyPairScheme+":")
}

// ParsePairURI reads kiko-pair://host:port?fp=<hex>&code=<code>&name=<name>.
func ParsePairURI(text string) (PairURI, error) {
	u, err := url.Parse(strings.TrimSpace(text))
	if err != nil || (u.Scheme != PairScheme && u.Scheme != legacyPairScheme) || u.Host == "" {
		return PairURI{}, errors.New("配对字符串无效：应以 kiko-pair:// 开头")
	}
	q := u.Query()
	fp, err := api.ParseFingerprint(q.Get("fp"))
	if err != nil {
		return PairURI{}, errors.New("配对字符串缺少有效的证书指纹")
	}
	code := q.Get("code")
	if code == "" {
		return PairURI{}, errors.New("配对字符串缺少配对码")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), strconv.Itoa(DefaultPort))
	}
	return PairURI{Host: host, Fingerprint: fp, Code: code, Name: q.Get("name")}, nil
}

// HostPort reads a server address typed by hand: host, host:port or an
// https:// URL, the port defaulting to DefaultPort.
func HostPort(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return "", errors.New("服务器地址无效")
		}
		if u.Scheme != "https" {
			return "", errors.New("远程服务器只支持 https")
		}
		addr = u.Host
	}
	addr = strings.TrimRight(addr, "/")
	if addr == "" {
		return "", errors.New("请填写服务器地址")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port: a host name, an IPv4 address or a bare IPv6 address.
		host, port = strings.Trim(addr, "[]"), strconv.Itoa(DefaultPort)
	}
	if host == "" || strings.ContainsAny(host, " /?#") {
		return "", errors.New("服务器地址无效")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", errors.New("端口无效")
	}
	return net.JoinHostPort(host, port), nil
}

// Add pairs with the server of a pairing string, whose fingerprint it
// trusts. A server already listed at the same address is paired again
// instead of added twice.
func (d *Directory) Add(ctx context.Context, uri string) (Server, error) {
	p, err := ParsePairURI(uri)
	if err != nil {
		return Server{}, err
	}
	return d.pair(ctx, "", p)
}

// AddManual pairs with the server at addr using code. It reads the
// server's certificate first and pairs only if confirm, shown its
// fingerprint, accepts it: trust on first use.
func (d *Directory) AddManual(ctx context.Context, addr, code string, confirm func(fp []byte) bool) (Server, error) {
	p, err := d.manual(ctx, addr, code, confirm)
	if err != nil {
		return Server{}, err
	}
	return d.pair(ctx, "", p)
}

func (d *Directory) manual(ctx context.Context, addr, code string, confirm func(fp []byte) bool) (PairURI, error) {
	host, err := HostPort(addr)
	if err != nil {
		return PairURI{}, err
	}
	if strings.TrimSpace(code) == "" {
		return PairURI{}, errors.New("请填写配对码")
	}
	fp, err := api.FetchFingerprint(ctx, host)
	if err != nil {
		return PairURI{}, fmt.Errorf("无法连接到 %s：%w", host, err)
	}
	if confirm == nil || !confirm(fp) {
		return PairURI{}, errors.New("证书指纹未确认")
	}
	return PairURI{Host: host, Fingerprint: fp, Code: strings.TrimSpace(code)}, nil
}

// Repair pairs a listed server again after its token was revoked or its
// certificate changed. input is a pairing string, or just a code: the
// server's certificate is then read, and confirm must accept it when it
// differs from the pinned one.
func (d *Directory) Repair(ctx context.Context, id, input string, confirm func(fp []byte) bool) (Server, error) {
	s, ok := d.Get(id)
	if !ok || !s.Remote() {
		return Server{}, errors.New("只有远程服务器需要配对")
	}
	input = strings.TrimSpace(input)
	var p PairURI
	if IsPairURI(input) {
		parsed, err := ParsePairURI(input)
		if err != nil {
			return Server{}, err
		}
		p = parsed
	} else {
		pinned := s.Pin()
		parsed, err := d.manual(ctx, s.Address(), input, func(fp []byte) bool {
			return bytes.Equal(fp, pinned) || (confirm != nil && confirm(fp))
		})
		if err != nil {
			return Server{}, err
		}
		p = parsed
	}
	return d.pair(ctx, id, p)
}

// pair trades p's code for a token and records the server: id, or the
// server listed at p's address, or a new entry.
func (d *Directory) pair(ctx context.Context, id string, p PairURI) (Server, error) {
	result, err := api.Pair(ctx, p.URL(), p.Fingerprint, p.Code, d.device)
	if err != nil {
		return Server{}, err
	}
	d.mu.Lock()
	i := -1
	if id != "" {
		i = d.index(id)
	}
	if i < 0 {
		i = slices.IndexFunc(d.file.Servers, func(s Server) bool { return s.URL == p.URL() })
	}
	var s Server
	if i >= 0 {
		s = d.file.Servers[i]
	} else {
		s = Server{ID: newID(), AddedAt: time.Now().UTC(), Color: Colors[len(d.file.Servers)%len(Colors)]}
	}
	s.URL = p.URL()
	s.Fingerprint = api.FormatFingerprint(p.Fingerprint)
	s.DeviceID = result.DeviceID
	if s.Name == "" {
		s.Name = firstNonEmpty(result.ServerName, p.Name, strings.Split(p.Host, ":")[0])
	}
	d.mu.Unlock()
	if err := d.setSecret(secretKey(s.ID), []byte(result.Token)); err != nil {
		return Server{}, fmt.Errorf("保存令牌失败: %w", err)
	}
	d.mu.Lock()
	if i >= 0 && i < len(d.file.Servers) && d.file.Servers[i].ID == s.ID {
		d.file.Servers[i] = s
	} else {
		d.file.Servers = append(d.file.Servers, s)
	}
	err = d.saveLocked()
	d.mu.Unlock()
	d.changed()
	return s, err
}

// Rename names a remote server.
func (d *Directory) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("请填写名称")
	}
	err := d.edit(id, func(s *Server) { s.Name = name })
	if err == nil {
		d.changed()
	}
	return err
}

// Touch records that the server's window connected at t. It is written
// at most once a minute per server.
func (d *Directory) Touch(id string, t time.Time) {
	d.mu.Lock()
	i := d.index(id)
	if i < 0 || t.Sub(d.file.Servers[i].LastConnected) < time.Minute {
		d.mu.Unlock()
		return
	}
	d.file.Servers[i].LastConnected = t.UTC()
	_ = d.saveLocked()
	d.mu.Unlock()
	d.changed()
}

func (d *Directory) edit(id string, fn func(s *Server)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.index(id)
	if i < 0 {
		return fmt.Errorf("服务器 %s 不存在", id)
	}
	fn(&d.file.Servers[i])
	return d.saveLocked()
}

// Remove forgets a remote server and its token. It revokes this device at
// the server first, which may fail (the server unreachable) without
// keeping the entry.
func (d *Directory) Remove(ctx context.Context, id string) error {
	s, ok := d.Get(id)
	if !ok || !s.Remote() {
		return errors.New("本机服务器不能移除")
	}
	if s.DeviceID != "" {
		if client, err := d.Client(id); err == nil {
			revoke, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = client.RevokeDevice(revoke, s.DeviceID)
			cancel()
		}
	}
	_ = d.deleteSecret(secretKey(id))
	d.mu.Lock()
	if i := d.index(id); i >= 0 {
		d.file.Servers = slices.Delete(d.file.Servers, i, i+1)
	}
	d.file.Open = slices.DeleteFunc(d.file.Open, func(open string) bool { return open == id })
	err := d.saveLocked()
	d.mu.Unlock()
	d.changed()
	return err
}

// Restore reports whether the windows open at quit reopen at launch, and
// which they were.
func (d *Directory) Restore() (bool, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Restore, slices.Clone(d.file.Open)
}

// SetRestore turns restoring the windows on or off.
func (d *Directory) SetRestore(on bool) error {
	d.mu.Lock()
	d.file.Restore = on
	err := d.saveLocked()
	d.mu.Unlock()
	d.changed()
	return err
}

// SetOpen records the remote servers whose windows are open.
func (d *Directory) SetOpen(ids []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == LocalID || d.index(id) < 0 })
	if slices.Equal(ids, d.file.Open) {
		return
	}
	d.file.Open = ids
	_ = d.saveLocked()
}

func (d *Directory) saveLocked() error {
	data, err := json.MarshalIndent(d.file, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(d.path, data)
}

// writeFile replaces path atomically, readable by the user only.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func secretKey(id string) string { return "server:" + id }

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
