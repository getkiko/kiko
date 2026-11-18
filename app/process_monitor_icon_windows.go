//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows an App is an executable installed outside the system
// directory, named by its version resource, its icon drawn from the
// executable's resources.

var (
	iconUser32              = windows.NewLazySystemDLL("user32.dll")
	iconGDI32               = windows.NewLazySystemDLL("gdi32.dll")
	procPrivateExtractIcons = iconUser32.NewProc("PrivateExtractIconsW")
	procGetIconInfo         = iconUser32.NewProc("GetIconInfo")
	procDestroyIcon         = iconUser32.NewProc("DestroyIcon")
	procGetDC               = iconUser32.NewProc("GetDC")
	procReleaseDC           = iconUser32.NewProc("ReleaseDC")
	procGetObject           = iconGDI32.NewProc("GetObjectW")
	procGetDIBits           = iconGDI32.NewProc("GetDIBits")
	procDeleteObject        = iconGDI32.NewProc("DeleteObject")
)

// windowsAppNames caches each executable's App name.
var windowsAppNames sync.Map

// platformAppIdentity is the App an executable at path is: the executable
// itself, named as its version resource describes it.
func platformAppIdentity(path string) (appPath, appName string) {
	if !strings.EqualFold(filepath.Ext(path), ".exe") || !filepath.IsAbs(path) || windowsSystemPath(path) {
		return "", ""
	}
	if name, ok := windowsAppNames.Load(path); ok {
		return path, name.(string)
	}
	name := windowsAppName(path, executableVersionStrings(path))
	windowsAppNames.Store(path, name)
	return path, name
}

func windowsSystemPath(path string) bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return windowsPathWithin(path, root)
}

// executableVersionStrings reads the FileDescription and ProductName of an
// executable's version resource.
func executableVersionStrings(path string) map[string]string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return nil
	}
	data := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&data[0])) != nil {
		return nil
	}
	var languages []string
	var block unsafe.Pointer
	var length uint32
	if windows.VerQueryValue(unsafe.Pointer(&data[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&block), &length) == nil && length >= 4 {
		pairs := unsafe.Slice((*uint16)(block), length/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			languages = append(languages, fmt.Sprintf("%04x%04x", pairs[i], pairs[i+1]))
		}
	}
	languages = append(languages, "040904b0", "040904e4", "080404b0")
	out := map[string]string{}
	for _, field := range []string{"FileDescription", "ProductName"} {
		for _, language := range languages {
			var value unsafe.Pointer
			var n uint32
			if windows.VerQueryValue(unsafe.Pointer(&data[0]), `\StringFileInfo\`+language+`\`+field, unsafe.Pointer(&value), &n) == nil && n > 0 {
				if s := strings.TrimSpace(windows.UTF16PtrToString((*uint16)(value))); s != "" {
					out[field] = s
					break
				}
			}
		}
	}
	return out
}

type iconInfo struct {
	icon     int32
	xHotspot uint32
	yHotspot uint32
	mask     windows.Handle
	color    windows.Handle
}

type bitmapObject struct {
	kind       int32
	width      int32
	height     int32
	widthBytes int32
	planes     uint16
	bitsPixel  uint16
	bits       uintptr
}

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

// processBundleIcon draws an executable's first icon as a PNG.
func processBundleIcon(_ context.Context, path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var icon windows.Handle
	var id uint32
	n, _, _ := procPrivateExtractIcons.Call(uintptr(unsafe.Pointer(name)), 0, 128, 128,
		uintptr(unsafe.Pointer(&icon)), uintptr(unsafe.Pointer(&id)), 1, 0)
	if n == 0 || n == 0xFFFFFFFF || icon == 0 {
		return nil, errors.New("executable has no icon")
	}
	defer procDestroyIcon.Call(uintptr(icon))
	var info iconInfo
	if ok, _, err := procGetIconInfo.Call(uintptr(icon), uintptr(unsafe.Pointer(&info))); ok == 0 {
		return nil, err
	}
	if info.mask != 0 {
		defer procDeleteObject.Call(uintptr(info.mask))
	}
	if info.color == 0 {
		return nil, errors.New("monochrome icon")
	}
	defer procDeleteObject.Call(uintptr(info.color))
	var bm bitmapObject
	if ok, _, _ := procGetObject.Call(uintptr(info.color), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ok == 0 || bm.width <= 0 || bm.height <= 0 {
		return nil, errors.New("unreadable icon bitmap")
	}
	w, h := int(bm.width), int(bm.height)
	color, err := iconPixels(info.color, w, h)
	if err != nil {
		return nil, err
	}
	var mask []byte
	if info.mask != 0 {
		mask, _ = iconPixels(info.mask, w, h)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, iconImage(color, mask, w, h)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// iconPixels reads a bitmap as top-down 32-bit BGRA rows.
func iconPixels(bitmap windows.Handle, w, h int) ([]byte, error) {
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return nil, errors.New("no device context")
	}
	defer procReleaseDC.Call(0, dc)
	header := bitmapInfoHeader{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	header.size = uint32(unsafe.Sizeof(header))
	pixels := make([]byte, w*h*4)
	// The BITMAPINFO's colour table follows the header; 32-bit BI_RGB has none.
	if lines, _, _ := procGetDIBits.Call(dc, uintptr(bitmap), 0, uintptr(h), uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&header)), 0); lines == 0 {
		return nil, errors.New("unreadable icon pixels")
	}
	return pixels, nil
}

func identifyProcessIcon(p *monitoredProcess) {
	identifyProcess(p)
}
