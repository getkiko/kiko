package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const deviceSeenFlushInterval = time.Minute

// remoteDevice is a paired client. Only the SHA-256 of its token is kept.
type remoteDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	TokenHash  string    `json:"tokenHash"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	LastAddr   string    `json:"lastAddr"`
}

type remoteDeviceView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	LastAddr   string    `json:"lastAddr"`
}

type deviceSession struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type deviceStore struct {
	path      string
	mu        sync.Mutex
	devices   []remoteDevice
	sessions  map[string]deviceSession
	lastFlush time.Time
	dirty     bool
	now       func() time.Time
}

func loadDeviceStore(path string) *deviceStore {
	d := &deviceStore{path: path, sessions: map[string]deviceSession{}, now: time.Now}
	var file struct {
		Devices []remoteDevice `json:"devices"`
	}
	if readJSON(path, &file) == nil {
		d.devices = file.Devices
	}
	return d
}

func (d *deviceStore) saveLocked() error {
	devices := d.devices
	if devices == nil {
		devices = []remoteDevice{}
	}
	err := atomicJSON(d.path, map[string]any{"devices": devices})
	if err == nil {
		d.lastFlush = d.now()
		d.dirty = false
	}
	return err
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cleanDeviceName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "未命名设备"
	}
	for utf8.RuneCountInString(name) > 64 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// add registers a device and returns its bearer token, shown only once.
func (d *deviceStore) add(name, addr string) (string, remoteDevice, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", remoteDevice{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := d.now().UTC()
	device := remoteDevice{ID: randomID(8), Name: cleanDeviceName(name), TokenHash: hex.EncodeToString(hashToken(token)), CreatedAt: now, LastSeenAt: now, LastAddr: addr}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.devices = append(d.devices, device)
	if err := d.saveLocked(); err != nil {
		d.devices = d.devices[:len(d.devices)-1]
		return "", remoteDevice{}, err
	}
	return token, device, nil
}

// authenticate resolves a bearer token. The returned context ends when the
// device is revoked.
func (d *deviceStore) authenticate(token, addr string) (remoteDevice, context.Context, bool) {
	if token == "" {
		return remoteDevice{}, nil, false
	}
	sum := hashToken(token)
	d.mu.Lock()
	defer d.mu.Unlock()
	index := -1
	for i := range d.devices {
		stored, err := hex.DecodeString(d.devices[i].TokenHash)
		if err == nil && subtle.ConstantTimeCompare(stored, sum) == 1 {
			index = i
		}
	}
	if index < 0 {
		return remoteDevice{}, nil, false
	}
	device := &d.devices[index]
	now := d.now().UTC()
	if device.LastAddr != addr || now.Sub(device.LastSeenAt) >= time.Second {
		device.LastSeenAt, device.LastAddr = now, addr
		d.dirty = true
	}
	if d.dirty && now.Sub(d.lastFlush) >= deviceSeenFlushInterval {
		_ = d.saveLocked()
	}
	session, ok := d.sessions[device.ID]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		session = deviceSession{ctx: ctx, cancel: cancel}
		d.sessions[device.ID] = session
	}
	return *device, session.ctx, true
}

func (d *deviceStore) revoke(id string) (remoteDevice, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, device := range d.devices {
		if device.ID != id {
			continue
		}
		d.devices = append(d.devices[:i:i], d.devices[i+1:]...)
		_ = d.saveLocked()
		if session, ok := d.sessions[id]; ok {
			session.cancel()
			delete(d.sessions, id)
		}
		return device, true
	}
	return remoteDevice{}, false
}

func (d *deviceStore) list() []remoteDeviceView {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]remoteDeviceView, 0, len(d.devices))
	for _, device := range d.devices {
		out = append(out, remoteDeviceView{ID: device.ID, Name: device.Name, CreatedAt: device.CreatedAt, LastSeenAt: device.LastSeenAt, LastAddr: device.LastAddr})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (d *deviceStore) flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dirty {
		_ = d.saveLocked()
	}
}
