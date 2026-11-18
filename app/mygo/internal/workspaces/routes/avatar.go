package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/deviceimg"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// avatarSize is the side of a device's picture in the sidebar.
const avatarSize = 20

// avatarCacheMax bounds the decoded pictures kept; at the limit the least
// recently used one is dropped.
const avatarCacheMax = 256

// avatarGet returns a cached picture and marks it as used.
func (w *Workspace) avatarGet(key string) (*ui.Bitmap, bool) {
	b, ok := w.avatars[key]
	if ok {
		w.avatarTick++
		w.avatarUsed[key] = w.avatarTick
	}
	return b, ok
}

// avatarPut caches a picture, evicting the least recently used one when
// the cache is full.
func (w *Workspace) avatarPut(key string, b *ui.Bitmap) {
	if w.avatars == nil {
		w.avatars, w.avatarUsed = map[string]*ui.Bitmap{}, map[string]uint64{}
	}
	if _, ok := w.avatars[key]; !ok && len(w.avatars) >= avatarCacheMax {
		oldest, first := "", true
		var least uint64
		for k, used := range w.avatarUsed {
			if first || used < least {
				oldest, least, first = k, used, false
			}
		}
		delete(w.avatars, oldest)
		delete(w.avatarUsed, oldest)
	}
	w.avatars[key] = b
	w.avatarTick++
	w.avatarUsed[key] = w.avatarTick
}

// builtinKey is the built-in picture a device shows when it has no
// custom one: the key a scan saved with it.
func builtinKey(d *model.RouteDevice) string {
	if d == nil {
		return ""
	}
	return d.ImageKey
}

// avatarKey is the cache key of a device picture: a digest of the data
// URL, computed once per distinct picture. The comparison with the
// remembered picture is instant for the same string.
type avatarKey struct {
	image string
	key   string
}

// customAvatarKey is the cache key of d's own picture.
func (w *Workspace) customAvatarKey(d *model.RouteDevice) string {
	if k, ok := w.avatarKeys[d.ID]; ok && k.image == d.Image {
		return k.key
	}
	if w.avatarKeys == nil {
		w.avatarKeys = map[string]avatarKey{}
	}
	sum := sha256.Sum256([]byte(d.Image))
	key := "c:" + hex.EncodeToString(sum[:16])
	w.avatarKeys[d.ID] = avatarKey{image: d.Image, key: key}
	return key
}

// avatarBitmap is the decoded picture of a device: its own image first,
// then the built-in one; nil for the type icon. Decoded pictures are
// cached, so frames never decode.
func (w *Workspace) avatarBitmap(dark bool, d *model.RouteDevice) *ui.Bitmap {
	if d == nil {
		return nil
	}
	if w.avatars == nil {
		w.avatars, w.avatarUsed = map[string]*ui.Bitmap{}, map[string]uint64{}
	}
	if d.Image != "" {
		key := w.customAvatarKey(d)
		if b, ok := w.avatarGet(key); ok {
			if b != nil {
				return b
			}
		} else {
			var bitmap *ui.Bitmap
			if data, err := deviceimg.Decode(d.Image); err == nil {
				bitmap, _ = ui.DecodeBitmap(data)
			}
			w.avatarPut(key, bitmap)
			if bitmap != nil {
				return bitmap
			}
		}
	}
	return w.builtinBitmap(dark, builtinKey(d))
}

// builtinBitmap is the decoded built-in picture of key, nil for none.
func (w *Workspace) builtinBitmap(dark bool, name string) *ui.Bitmap {
	if name == "" {
		return nil
	}
	if w.avatars == nil {
		w.avatars, w.avatarUsed = map[string]*ui.Bitmap{}, map[string]uint64{}
	}
	key := "b:" + name
	load := deviceimg.Builtin
	if dark {
		key += ":dark"
		load = deviceimg.BuiltinDark
	}
	if b, ok := w.avatarGet(key); ok {
		return b
	}
	var bitmap *ui.Bitmap
	if data, ok := load(name); ok {
		bitmap, _ = ui.DecodeBitmap(data)
	}
	w.avatarPut(key, bitmap)
	return bitmap
}

// deviceAvatar shows a device's picture as a rounded square, or the type
// icon when it has none, centred in a square of avatarSize so that the
// names of all sidebar rows start at the same x.
func (w *Workspace) deviceAvatar(c *ui.Context, d *model.RouteDevice, icon kit.Symbol) {
	if b := w.avatarBitmap(c.Theme().Dark, d); b != nil {
		ui.Image(c, b).Size(avatarSize, avatarSize).Radius(5).Shrink(0)
		return
	}
	ui.Box(c).Size(avatarSize, avatarSize).Shrink(0).Center().Children(func() { kit.Icon(c, icon, 18) })
}

// ---- custom picture ----

var imageFilter = env.FileFilter{Name: "图片", Extensions: []string{"jpg", "jpeg", "png", "webp", "gif"}}

// pickImage asks for a picture, compresses it and hands the data URL to
// done on the UI thread; done gets "" and a short message on failure. A
// cancelled dialog does nothing.
func (w *Workspace) pickImage(done func(image string, problem string)) {
	if w.picking {
		return
	}
	shell := w.env.Shell
	if shell == nil {
		done("", "无法选择文件")
		return
	}
	w.picking = true
	type result struct {
		image string
		ok    bool
	}
	env.Go(w.env, func(context.Context) (result, error) {
		data, _, ok, err := shell.OpenFile(imageFilter)
		if err != nil || !ok {
			return result{}, err
		}
		image, err := deviceimg.Compress(data)
		return result{image, true}, err
	}, func(r result, err error) {
		w.picking = false
		switch {
		case err != nil:
			done("", "无法使用该图片")
		case r.ok:
			done(r.image, "")
		}
	})
}

// chooseImage is "Choose image" / "Change" from the sidebar: the chosen picture is
// saved to the device at once.
func (w *Workspace) chooseImage(deviceID string) {
	w.leaveEditor(func() {
		w.pickImage(func(image, problem string) {
			if problem != "" {
				w.err = problem
				return
			}
			w.setDeviceImage(deviceID, image)
		})
	})
}

// imageRemove is the confirmation of removing a device's picture, beside
// the sidebar button it came from (from).
type imageRemove struct {
	state    kit.ConfirmState
	deviceID string
	from     string
	anchor   ui.Element
	anchored bool
}

// requestRemoveImage is "Remove image" from the sidebar menu: it asks beside the
// row's More button.
func (w *Workspace) requestRemoveImage(from, deviceID string) {
	w.leaveEditor(func() {
		w.imageRemove = imageRemove{deviceID: deviceID, from: from}
		w.imageRemove.state.Open = true
	})
}

// removeImage drops a device's picture.
func (w *Workspace) removeImage(deviceID string) bool {
	return w.setDeviceImage(deviceID, "")
}

// imageRemoveView is the confirmation of removing a picture, at the button
// it came from when the sidebar built it this frame.
func (w *Workspace) imageRemoveView(c *ui.Context, doc *model.Doc) {
	rm := &w.imageRemove
	if !rm.state.Open {
		rm.anchored = false
		return
	}
	d := findDevice(doc, rm.deviceID)
	if d == nil || !rm.anchored {
		rm.state.Open = false
		return
	}
	id := rm.deviceID
	kit.ConfirmPopover(c, rm.anchor, &rm.state, kit.ConfirmOptions{
		Title: "移除「" + d.Name + "」的图片？", Confirm: "移除", Danger: true,
		Run: func(done func(error)) {
			if w.removeImage(id) {
				done(nil)
				return
			}
			msg := w.err
			w.err = ""
			done(errors.New(msg))
		},
	})
	rm.anchored = false
}

// setDeviceImage saves a device's picture ("" removes it) when the
// document's picture budget allows.
func (w *Workspace) setDeviceImage(deviceID, image string) bool {
	if err := model.CheckDeviceImage(w.doc(), deviceID, image); err != nil {
		w.err = err.Error()
		return false
	}
	return w.save(func(doc *model.Doc) error {
		d := findDevice(doc, deviceID)
		if d == nil {
			return errors.New("设备不存在")
		}
		d.Image = image
		return nil
	})
}

// imageRow is the dialog's Device image control: it edits the draft, which
// "Save device" saves.
func (w *Workspace) imageRow(c *ui.Context, d *deviceDialog) {
	dev := d.draft
	dialogRow(c, "设备图片", func() {
		ui.Row(c).Gap(10).Children(func() {
			if b := w.avatarBitmap(c.Theme().Dark, &dev); b != nil {
				ui.Image(c, b).Size(40, 40).Radius(9).Shrink(0)
			} else {
				ui.Box(c).Size(40, 40).Radius(9).Center().Shrink(0).Background(kit.T(c).Fill).TextColor(kit.T(c).Label3).Children(func() {
					kit.Icon(c, deviceIcon(dev.Icon), 20)
				})
			}
			label := "选择图片"
			if dev.Image != "" {
				label = "更换"
			}
			kit.Button(c, label, kit.ButtonOptions{Small: true, Disabled: w.picking}).OnClick(func() {
				w.pickImage(func(image, problem string) {
					if problem != "" {
						d.validation = problem
						return
					}
					if err := model.CheckDeviceImage(w.doc(), dev.ID, image); err != nil {
						d.validation = err.Error()
						return
					}
					d.validation = ""
					d.draft.Image = image
				})
			})
			if dev.Image != "" {
				kit.Button(c, "移除", kit.ButtonOptions{Variant: kit.TextButton, Small: true}).OnClick(func() { d.draft.Image = "" })
			}
		})
	})
}

// ---- IP changed ----

// hostText is an address without its single-host prefix length.
func hostText(a string) string {
	return strings.TrimSuffix(strings.TrimSuffix(a, "/32"), "/128")
}

// pendingText is the hint "old IP → new IP" of a device with a pending change.
// A pending address without a paired old one is shown as an addition.
func pendingText(d *model.RouteDevice) string {
	var parts []string
	for _, p := range d.PendingAddresses {
		if from := d.PendingFrom[p]; from != "" {
			parts = append(parts, hostText(from)+" → "+hostText(p))
		} else {
			parts = append(parts, "新增 "+hostText(p))
		}
	}
	return "IP 已变更：" + strings.Join(parts, "、")
}

// pendingMenu offers "Update address" and "Ignore" for a device's detected new address.
func (w *Workspace) pendingMenu(m *ui.Menu, d *model.RouteDevice) {
	if m.Item("更新地址").Chosen() {
		w.confirmPending(d.ID)
	}
	if m.Item("忽略").Chosen() {
		w.dismissPending(d.ID)
	}
}

// confirmPending makes the detected addresses the device's own.
func (w *Workspace) confirmPending(id string) {
	w.leaveEditor(func() {
		w.save(func(doc *model.Doc) error {
			next, err := model.ConfirmPendingAddresses(doc, id)
			return replace(doc, next, err)
		})
	})
}

// dismissPending drops the detected addresses and keeps the device's.
func (w *Workspace) dismissPending(id string) {
	w.leaveEditor(func() {
		w.save(func(doc *model.Doc) error {
			next, err := model.DismissPending(doc, id)
			return replace(doc, next, err)
		})
	})
}
