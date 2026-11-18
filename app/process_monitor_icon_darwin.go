//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"howett.net/plist"
)

// Covers a 40pt inspector icon on displays up to 3x pixel density.
const processIconSize = 128

func identifyProcessIcon(p *monitoredProcess) {
	if strings.Contains(p.Path, ".app.bundle/") {
		if args := darwinProcessArgs(p.PID); len(args) > 0 {
			p.originalPath = args[0]
		}
	}
	identifyProcess(p)
}

func processBundleIcon(ctx context.Context, bundle string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return nil, err
	}
	var info struct {
		Icon     string `plist:"CFBundleIconFile"`
		IconName string `plist:"CFBundleIconName"`
	}
	if _, err = plist.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	if info.Icon == "" && info.IconName == "" {
		return nil, errors.New("bundle icon unavailable")
	}
	temp, err := os.MkdirTemp("", "kiko-process-icon-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	target := filepath.Join(temp, "icon.png")
	convert := func(source string) error {
		return exec.CommandContext(ctx, "/usr/bin/sips", "-s", "format", "png", "-Z", strconv.Itoa(processIconSize), source, "--out", target).Run()
	}
	if info.Icon != "" && filepath.Base(info.Icon) == info.Icon {
		if filepath.Ext(info.Icon) == "" {
			info.Icon += ".icns"
		}
		err = convert(filepath.Join(bundle, "Contents", "Resources", info.Icon))
		if err == nil {
			return os.ReadFile(target)
		}
	}
	if info.IconName == "" {
		if err == nil {
			err = errors.New("bundle icon unavailable")
		}
		return nil, err
	}
	// Asset-catalog icons (CFBundleIconName / Assets.car) are resolved by
	// Icon Services through NSWorkspace, including the App's chosen appearance.
	if err := writeDarwinWorkspaceIcon(ctx, bundle, target); err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

var darwinIconWorkspace struct {
	sync.Once
	err error
}

func writeDarwinWorkspaceIcon(ctx context.Context, bundle, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	darwinIconWorkspace.Do(func() {
		_, darwinIconWorkspace.err = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	})
	if darwinIconWorkspace.err != nil {
		return darwinIconWorkspace.err
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("new"))
	defer pool.Send(objc.RegisterName("drain"))
	stringClass := objc.ID(objc.GetClass("NSString"))
	nsBundle := stringClass.Send(objc.RegisterName("stringWithUTF8String:"), bundle)
	nsTarget := stringClass.Send(objc.RegisterName("stringWithUTF8String:"), target)
	workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(objc.RegisterName("sharedWorkspace"))
	icon := workspace.Send(objc.RegisterName("iconForFile:"), nsBundle)
	// Draw into an 8-bit RGBA context. TIFFRepresentation may expose an HDR
	// material layer with a nearly transparent alpha channel for modern icons.
	bitmap := objc.ID(objc.GetClass("NSBitmapImageRep")).Send(objc.RegisterName("alloc"))
	space := stringClass.Send(objc.RegisterName("stringWithUTF8String:"), "NSCalibratedRGBColorSpace")
	bitmap = bitmap.Send(objc.RegisterName("initWithBitmapDataPlanes:pixelsWide:pixelsHigh:bitsPerSample:samplesPerPixel:hasAlpha:isPlanar:colorSpaceName:bitmapFormat:bytesPerRow:bitsPerPixel:"),
		uintptr(0), int64(processIconSize), int64(processIconSize), int64(8), int64(4), true, false, space, uint64(0), int64(processIconSize*4), int64(32))
	if bitmap == 0 {
		return errors.New("system icon bitmap unavailable")
	}
	defer bitmap.Send(objc.RegisterName("release"))
	graphicsClass := objc.ID(objc.GetClass("NSGraphicsContext"))
	graphics := graphicsClass.Send(objc.RegisterName("graphicsContextWithBitmapImageRep:"), bitmap)
	if graphics == 0 {
		return errors.New("system icon graphics context unavailable")
	}
	graphicsClass.Send(objc.RegisterName("saveGraphicsState"))
	defer graphicsClass.Send(objc.RegisterName("restoreGraphicsState"))
	graphicsClass.Send(objc.RegisterName("setCurrentContext:"), graphics)
	graphics.Send(objc.RegisterName("setImageInterpolation:"), uint64(3)) // NSImageInterpolationHigh
	type rect struct{ X, Y, Width, Height float64 }
	icon.Send(objc.RegisterName("drawInRect:fromRect:operation:fraction:"), rect{Width: processIconSize, Height: processIconSize}, rect{}, uint64(2), float64(1))
	properties := objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionary"))
	data := bitmap.Send(objc.RegisterName("representationUsingType:properties:"), uint64(4), properties)
	if data == 0 || !objc.Send[bool](data, objc.RegisterName("writeToFile:atomically:"), nsTarget, true) {
		return errors.New("system application icon unavailable")
	}
	return ctx.Err()
}
