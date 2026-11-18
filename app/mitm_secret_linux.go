//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// On Linux the freedesktop Secret Service (GNOME Keyring, KWallet, etc.) is
// preferred. Without a session bus, a service or a graphical session the store
// falls back to a 0600 file, and the choice is recorded in
// <dataDir>/mitm/ca.key.backend (see mitm_secret_sticky.go).

func newSecretStore(dataDir string) secretStore {
	service := newSecretServiceStore(dialSecretBus)
	sticky := newStickySecretStore(dataDir, newPEMSecretStore(dataDir), service, secretSessionAvailable)
	// Old unscoped items are only read once this data directory has recorded
	// secretservice.
	service.scoped(dataDir, func() bool {
		backend, err := sticky.backend()
		return err == nil && backend == secretBackendService
	})
	service.keyMatches = mitmRootKeyMatcher(dataDir)
	return sticky
}

// secretSessionAvailable reports whether a graphical session exists that can
// show the unlock prompt.
func secretSessionAvailable() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

const (
	secretBusName       = "org.freedesktop.secrets"
	secretBusPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	secretIfaceService  = "org.freedesktop.Secret.Service"
	secretIfaceColl     = "org.freedesktop.Secret.Collection"
	secretIfaceItem     = "org.freedesktop.Secret.Item"
	secretIfacePrompt   = "org.freedesktop.Secret.Prompt"
	secretCallTimeout   = 30 * time.Second
	secretContentType   = "application/octet-stream"
	secretUnknownPrefix = "org.freedesktop.DBus.Error."
)

// dbusSecret mirrors the Secret Service (oayays) struct.
type dbusSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type dbusSecretConn struct {
	conn *dbus.Conn
}

// dialSecretBus connects to the session bus. It never lets godbus autostart
// dbus-launch; without a session bus address the service counts as unavailable.
func dialSecretBus() (secretBusConn, error) {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if !secretBusAddressUsable(address) {
		// Empty or autolaunch: address: only use an existing bus socket under
		// XDG_RUNTIME_DIR.
		runtime := os.Getenv("XDG_RUNTIME_DIR")
		if runtime == "" {
			return nil, errSecretServiceUnavailable
		}
		path := filepath.Join(runtime, "bus")
		if _, err := os.Stat(path); err != nil {
			return nil, errSecretServiceUnavailable
		}
		address = "unix:path=" + path
	}
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, errSecretServiceUnavailable
	}
	c := &dbusSecretConn{conn: conn}
	if !c.serviceAvailable() {
		conn.Close()
		return nil, errSecretServiceUnavailable
	}
	return c, nil
}

func secretContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), secretCallTimeout)
}

// serviceAvailable reports whether the service is running or bus-activatable.
func (c *dbusSecretConn) serviceAvailable() bool {
	ctx, cancel := secretContext()
	defer cancel()
	var owned bool
	err := c.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, secretBusName).Store(&owned)
	if err == nil && owned {
		return true
	}
	var names []string
	if err := c.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&names); err != nil {
		return false
	}
	for _, name := range names {
		if name == secretBusName {
			return true
		}
	}
	return false
}

func (c *dbusSecretConn) Close() { _ = c.conn.Close() }

func (c *dbusSecretConn) object(path dbus.ObjectPath) dbus.BusObject {
	return c.conn.Object(secretBusName, path)
}

// secretWrap maps "service does not exist" errors to unavailable and returns
// other errors unchanged (they never contain key material).
func secretWrap(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if secretErrorUnavailable(text) {
		return errSecretServiceUnavailable
	}
	return err
}

func (c *dbusSecretConn) OpenSession() (string, error) {
	ctx, cancel := secretContext()
	defer cancel()
	var output dbus.Variant
	var session dbus.ObjectPath
	err := c.object(secretBusPath).CallWithContext(ctx, secretIfaceService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session)
	if err != nil {
		return "", secretWrap(err)
	}
	return string(session), nil
}

func (c *dbusSecretConn) CollectionLocked(collection string) (bool, error) {
	value, err := c.object(dbus.ObjectPath(collection)).GetProperty(secretIfaceColl + ".Locked")
	if err != nil {
		// The default collection does not exist (e.g. the service runs but no
		// login keyring was created).
		text := err.Error()
		for _, name := range []string{secretUnknownPrefix + "UnknownObject", secretUnknownPrefix + "InvalidArgs", secretUnknownPrefix + "UnknownProperty", "org.freedesktop.Secret.Error.NoSuchObject"} {
			if strings.Contains(text, name) {
				return false, errSecretNoCollection
			}
		}
		return false, secretWrap(err)
	}
	locked, ok := value.Value().(bool)
	if !ok {
		return false, errors.New("secret service: unexpected Locked type")
	}
	return locked, nil
}

func (c *dbusSecretConn) Unlock(objects []string) (string, error) {
	ctx, cancel := secretContext()
	defer cancel()
	paths := make([]dbus.ObjectPath, len(objects))
	for i, object := range objects {
		paths[i] = dbus.ObjectPath(object)
	}
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := c.object(secretBusPath).CallWithContext(ctx, secretIfaceService+".Unlock", 0, paths).Store(&unlocked, &prompt); err != nil {
		return "", secretWrap(err)
	}
	return string(prompt), nil
}

func (c *dbusSecretConn) SearchItems(collection string, attrs map[string]string) ([]string, error) {
	ctx, cancel := secretContext()
	defer cancel()
	var items []dbus.ObjectPath
	if err := c.object(dbus.ObjectPath(collection)).CallWithContext(ctx, secretIfaceColl+".SearchItems", 0, attrs).Store(&items); err != nil {
		return nil, secretWrap(err)
	}
	result := make([]string, len(items))
	for i, item := range items {
		result[i] = string(item)
	}
	return result, nil
}

func (c *dbusSecretConn) ItemAttributes(item string) (map[string]string, error) {
	value, err := c.object(dbus.ObjectPath(item)).GetProperty(secretIfaceItem + ".Attributes")
	if err != nil {
		return nil, secretWrap(err)
	}
	attrs, ok := value.Value().(map[string]string)
	if !ok {
		return nil, errors.New("secret service: unexpected Attributes type")
	}
	return attrs, nil
}

func (c *dbusSecretConn) GetSecret(item, session string) ([]byte, error) {
	ctx, cancel := secretContext()
	defer cancel()
	var secret dbusSecret
	if err := c.object(dbus.ObjectPath(item)).CallWithContext(ctx, secretIfaceItem+".GetSecret", 0, dbus.ObjectPath(session)).Store(&secret); err != nil {
		return nil, secretWrap(err)
	}
	return secret.Value, nil
}

func (c *dbusSecretConn) CreateItem(collection, label string, attrs map[string]string, value []byte, session string) (string, string, error) {
	ctx, cancel := secretContext()
	defer cancel()
	properties := map[string]dbus.Variant{
		secretIfaceItem + ".Label":      dbus.MakeVariant(label),
		secretIfaceItem + ".Attributes": dbus.MakeVariant(attrs),
	}
	secret := dbusSecret{Session: dbus.ObjectPath(session), Parameters: []byte{}, Value: value, ContentType: secretContentType}
	var item, prompt dbus.ObjectPath
	if err := c.object(dbus.ObjectPath(collection)).CallWithContext(ctx, secretIfaceColl+".CreateItem", 0, properties, secret, true).Store(&item, &prompt); err != nil {
		return "", "", secretWrap(err)
	}
	return string(item), string(prompt), nil
}

func (c *dbusSecretConn) DeleteItem(item string) (string, error) {
	ctx, cancel := secretContext()
	defer cancel()
	var prompt dbus.ObjectPath
	if err := c.object(dbus.ObjectPath(item)).CallWithContext(ctx, secretIfaceItem+".Delete", 0).Store(&prompt); err != nil {
		return "", secretWrap(err)
	}
	return string(prompt), nil
}

// WaitPrompt subscribes to the Completed signal before showing the prompt so a
// prompt that completes quickly is not missed.
func (c *dbusSecretConn) WaitPrompt(prompt string, timeout time.Duration) (bool, error) {
	path := dbus.ObjectPath(prompt)
	options := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(secretIfacePrompt),
		dbus.WithMatchMember("Completed"),
	}
	if err := c.conn.AddMatchSignal(options...); err != nil {
		return false, secretWrap(err)
	}
	defer func() { _ = c.conn.RemoveMatchSignal(options...) }()
	signals := make(chan *dbus.Signal, 8)
	c.conn.Signal(signals)
	defer c.conn.RemoveSignal(signals)

	ctx, cancel := secretContext()
	defer cancel()
	object := c.object(path)
	if err := object.CallWithContext(ctx, secretIfacePrompt+".Prompt", 0, "").Err; err != nil {
		return false, secretWrap(err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case signal := <-signals:
			if signal == nil || signal.Path != path || signal.Name != secretIfacePrompt+".Completed" || len(signal.Body) == 0 {
				continue
			}
			dismissed, _ := signal.Body[0].(bool)
			return dismissed, nil
		case <-timer.C:
			dismissCtx, dismissCancel := secretContext()
			_ = object.CallWithContext(dismissCtx, secretIfacePrompt+".Dismiss", 0).Err
			dismissCancel()
			return false, errSecretPromptTimeout
		}
	}
}
