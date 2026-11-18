package lanscan

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"howett.net/plist"
)

// lockdownLabel names this client in lockdownd requests.
const lockdownLabel = "Kiko"

// LockdownInfo is what an unpaired, read-only lockdownd query returns.
// Prohibited keys (ModelNumber, BuildVersion, UniqueDeviceID) are never asked.
type LockdownInfo struct {
	ProductType    string // iPhone18,2
	HardwareModel  string // V54AP
	DeviceName     string
	ProductVersion string // 27.0.1
}

// OS is the system name and version, e.g. "iOS 27.0.1".
func (l LockdownInfo) OS() string {
	if l.ProductVersion == "" {
		return ""
	}
	return osFamily(l.ProductType) + " " + l.ProductVersion
}

func osFamily(model string) string {
	switch {
	case strings.HasPrefix(model, "iPad"):
		return "iPadOS"
	case strings.HasPrefix(model, "Watch"):
		return "watchOS"
	case strings.HasPrefix(model, "AppleTV"):
		return "tvOS"
	case strings.HasPrefix(model, "AudioAccessory"):
		return "HomePod"
	case strings.HasPrefix(model, "RealityDevice"):
		return "visionOS"
	}
	return "iOS"
}

const lockdownMaxFrame = 1 << 20

var errNotLockdown = errors.New("not lockdownd")

// QueryLockdown asks lockdownd on conn for QueryType, then reads a few
// public values. It neither pairs nor writes anything to the device.
func QueryLockdown(ctx context.Context, conn net.Conn, timeout time.Duration) (LockdownInfo, bool) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	reply, err := lockdownCall(conn, map[string]any{"Label": lockdownLabel, "Request": "QueryType"})
	if err != nil || reply["Type"] != "com.apple.mobile.lockdown" {
		return LockdownInfo{}, false
	}
	var info LockdownInfo
	for _, k := range []struct {
		key string
		dst *string
	}{{"ProductType", &info.ProductType}, {"DeviceName", &info.DeviceName}, {"ProductVersion", &info.ProductVersion}, {"HardwareModel", &info.HardwareModel}} {
		reply, err := lockdownCall(conn, map[string]any{"Label": lockdownLabel, "Request": "GetValue", "Key": k.key})
		if err != nil {
			break
		}
		if v, ok := reply["Value"].(string); ok {
			*k.dst = strings.TrimSpace(v)
		}
	}
	return info, info.ProductType != "" || info.DeviceName != ""
}

func lockdownCall(rw io.ReadWriter, req map[string]any) (map[string]any, error) {
	body, err := plist.Marshal(req, plist.XMLFormat)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	if _, err := rw.Write(frame); err != nil {
		return nil, err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(rw, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > lockdownMaxFrame {
		return nil, errNotLockdown
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(rw, data); err != nil {
		return nil, err
	}
	return decodeLockdownReply(data)
}

// decodeLockdownReply accepts only an XML plist dictionary (what was asked
// for) and never lets a malformed reply panic the scan.
func decodeLockdownReply(data []byte) (reply map[string]any, err error) {
	body := bytes.TrimLeft(bytes.TrimPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("\xef\xbb\xbf")), " \t\r\n")
	if !bytes.HasPrefix(body, []byte("<?xml")) {
		return nil, errNotLockdown
	}
	defer func() {
		if recover() != nil {
			reply, err = nil, errNotLockdown
		}
	}()
	if _, err := plist.Unmarshal(body, &reply); err != nil {
		return nil, err
	}
	return reply, nil
}
