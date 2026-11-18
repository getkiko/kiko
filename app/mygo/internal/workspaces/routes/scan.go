package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// scanEvery is the pause between two polls of a running scan.
const scanEvery = 600 * time.Millisecond

// scanDialog is the "Scan LAN" dialog: the hosts found so far, those
// ticked to add, and the scan in progress.
type scanDialog struct {
	open    bool
	running bool
	step    int
	steps   int
	devices []api.ScanDevice
	// selected are the ticked hosts by IP; seen the hosts already offered,
	// so a later poll does not tick again what the user cleared.
	selected map[string]bool
	seen     map[string]bool
	err      string
	// failed are the hosts the last "Add selected" could not add, by IP, with the
	// short reason shown in their row.
	failed map[string]string
	id     string
	cursor int
	stop   context.CancelFunc
	merged bool
	// cancelling is that "Cancel scan" is taking a final poll before it cancels.
	cancelling bool
	// rev counts the changes of devices; status caches the known and
	// conflict state of each host for one document version and rev.
	rev    int
	status scanStatus
}

// scanStatus is the state of each scanned host relative to the devices,
// valid for one store version and scan revision.
type scanStatus struct {
	valid    bool
	doc      *model.Doc
	version  int
	rev      int
	known    map[string]bool
	conflict map[string]bool
}

// hostStatus returns whether the host is already added and whether it
// conflicts with a device, recomputing only when the document or the scan
// results changed.
func (w *Workspace) hostStatus(d *scanDialog, doc *model.Doc, h api.ScanDevice) (known, conflict bool) {
	version := 0
	if w.store != nil {
		version = w.store.Version()
	}
	st := &d.status
	if !st.valid || st.version != version || st.rev != d.rev || st.doc != doc {
		*st = scanStatus{valid: true, version: version, rev: d.rev, doc: doc, known: map[string]bool{}, conflict: map[string]bool{}}
		for _, x := range d.devices {
			host := scannedHost(x)
			st.known[x.IP] = model.ScannedKnown(doc, host)
			st.conflict[x.IP] = model.ScanConflicts(doc, host)
		}
	}
	return st.known[h.IP], st.conflict[h.IP]
}

// appendDevice adds a new device with its default groups and DNS defaults.
func appendDevice(doc *model.Doc, device model.RouteDevice) {
	doc.DNS.Groups = append(doc.DNS.Groups, &model.RouteGroup{ID: model.NewID("dg"), Name: "默认分组", DeviceID: device.ID})
	finals := map[string]model.Ref{}
	for k, v := range doc.DNS.DeviceFinals {
		finals[k] = v
	}
	finals[device.ID] = doc.DNS.Final.Or(model.Null())
	doc.DNS.DeviceFinals = finals
	detours := map[string]model.Ref{}
	for k, v := range doc.DNS.DeviceFinalDetours {
		detours[k] = v
	}
	detours[device.ID] = doc.DNS.FinalDetour.Or(model.Null())
	doc.DNS.DeviceFinalDetours = detours
	saved := device
	doc.Route.Devices = append(doc.Route.Devices, &saved)
	doc.Route.Groups = append(doc.Route.Groups, &model.RouteGroup{ID: model.NewID("rg"), Name: "默认分组", DeviceID: device.ID})
}

// canScan is whether the scan entry shows: the scan reads the network of
// the machine the server runs on, which is this one only for the local
// server.
func (w *Workspace) canScan() bool { return !w.env.Target.Remote }

// startScan opens the scan dialog and starts a scan.
func (w *Workspace) startScan() {
	w.leaveEditor(func() {
		if w.scan != nil {
			w.stopScan(w.scan)
		}
		d := &scanDialog{open: true, running: true, selected: map[string]bool{}, seen: map[string]bool{}}
		w.scan = d
		w.err = ""
		w.runScan(d)
	})
}

// startRequest starts the server scan. The request is not tied to the
// dialog, so the id always comes back and a scan the dialog gave up on can
// be cancelled. A 409 means a scan is still running: the server names it
// (also one left by a window that restarted), else the one this window
// started last is taken. It is cancelled and the start retried once.
func (w *Workspace) startRequest(client *api.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := client.StartScan(ctx)
	if api.IsStatus(err, http.StatusConflict) {
		running := api.RunningScanID(err)
		if running == "" {
			if last := w.lastScan.Load(); last != nil {
				running = *last
			}
		}
		if running != "" {
			_ = client.CancelScan(ctx, running)
			id, err = client.StartScan(ctx)
		}
	}
	if err == nil {
		w.lastScan.Store(&id)
	}
	return id, err
}

// runScan starts the scan and polls it until it ends or d stops.
func (w *Workspace) runScan(d *scanDialog) {
	ctx, cancel := context.WithCancel(context.Background())
	d.stop = cancel
	client := w.env.API
	fail := func(err error) {
		w.env.Post(func() {
			if w.scan == d {
				d.running, d.err = false, err.Error()
			}
		})
	}
	go func() {
		id, err := w.startRequest(client)
		if err != nil {
			if ctx.Err() == nil {
				fail(err)
			}
			return
		}
		if ctx.Err() != nil {
			// The dialog stopped while the start was in flight.
			cancelCtx, cancelDone := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelDone()
			_ = client.CancelScan(cancelCtx, id)
			return
		}
		w.env.Post(func() { d.id = id })
		cursor := 0
		for {
			snap, err := client.PollScan(ctx, id, cursor)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fail(err)
				return
			}
			cursor = snap.Cursor
			w.env.Post(func() {
				if w.scan == d {
					w.applyScan(d, snap)
				}
			})
			if snap.State != api.ScanRunning {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(scanEvery):
			}
		}
	}()
}

// applyScan takes a poll's changes into d.
func (w *Workspace) applyScan(d *scanDialog, snap api.ScanSnapshot) {
	d.step, d.steps = snap.Step, snap.Steps
	d.cursor = snap.Cursor
	if len(snap.Devices) > 0 {
		d.rev++
	}
	for _, h := range snap.Devices {
		replaced := false
		for i := range d.devices {
			if d.devices[i].IP == h.IP {
				d.devices[i], replaced = h, true
			}
		}
		if !replaced {
			d.devices = append(d.devices, h)
		}
		if !d.seen[h.IP] {
			d.seen[h.IP] = true
			// A host holding the address of a device with another MAC is a
			// conflict candidate and starts unticked like a known one.
			d.selected[h.IP] = !h.Self && model.DeviceForScanned(w.doc(), scannedHost(h)) == nil
		}
	}
	if snap.State == api.ScanRunning {
		return
	}
	d.running = false
	if snap.State == api.ScanFailed {
		d.err = snap.Error
		if d.err == "" {
			d.err = "扫描失败"
		}
	}
	w.mergeScan(d)
}

func scannedHost(h api.ScanDevice) model.ScannedHost {
	return model.ScannedHost{IP: h.IP, MAC: h.MAC, Name: h.Name}
}

// mergeScan records what the scan learned about the devices already
// added: a known device gains its MAC; one seen at another address gets
// that address pending (IP changed).
func (w *Workspace) mergeScan(d *scanDialog) {
	if d.merged || len(d.devices) == 0 {
		return
	}
	d.merged = true
	hosts := make([]model.ScannedHost, 0, len(d.devices))
	for _, h := range d.devices {
		if !h.Self {
			hosts = append(hosts, scannedHost(h))
		}
	}
	if next, _ := model.MergeScanned(w.doc(), hosts); next != w.doc() {
		w.save(func(doc *model.Doc) error {
			merged, _ := model.MergeScanned(doc, hosts)
			*doc = *merged
			return nil
		})
	}
}

// stopScan ends d's scan: the polling stops and a running scan is
// cancelled on the server.
func (w *Workspace) stopScan(d *scanDialog) {
	if d.stop != nil {
		d.stop()
		d.stop = nil
	}
	if d.running && d.id != "" {
		id, client := d.id, w.env.API
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = client.CancelScan(ctx, id)
		}()
	}
	d.running = false
}

// cancelScan is "Cancel scan": the scan stops and the hosts found stay listed.
//
// The polling has paused up to scanEvery, so one last poll first takes the
// hosts found since; then the scan is cancelled.
func (w *Workspace) cancelScan(d *scanDialog) {
	if d.stop != nil {
		d.stop()
		d.stop = nil
	}
	if !d.running || d.id == "" || d.cancelling {
		w.stopScan(d)
		w.mergeScan(d)
		return
	}
	d.cancelling = true
	id, cursor, client := d.id, d.cursor, w.env.API
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snap, err := client.PollScan(ctx, id, cursor)
		_ = client.CancelScan(ctx, id)
		w.env.Post(func() {
			if w.scan != d {
				return
			}
			if err == nil {
				w.applyScan(d, snap)
			}
			d.running, d.cancelling = false, false
			w.mergeScan(d)
		})
	}()
}

// scanIcon is the type icon of a scanned host's device.
func scanIcon(t string) string {
	switch t {
	case "phone", "tablet", "watch":
		return "phone"
	case "computer":
		return "computer"
	case "router":
		return "router"
	}
	return "server"
}

var scanTypeNames = map[string]string{"phone": "手机", "tablet": "平板", "watch": "手表", "computer": "电脑", "tv": "电视",
	"speaker": "音箱", "router": "路由器", "printer": "打印机", "iot": "智能设备"}

// cleanScanText makes a name or model reported by a host fit for display
// and saving: valid UTF-8, no control characters, at most 64 characters.
func cleanScanText(text string) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > 64 {
		text = strings.TrimSpace(string(r[:64]))
	}
	return text
}

func scanName(h api.ScanDevice) string {
	switch {
	case cleanScanText(h.Name) != "":
		return cleanScanText(h.Name)
	case cleanScanText(h.Model) != "":
		return cleanScanText(h.Model)
	}
	return h.IP
}

// addScanned adds the ticked hosts as devices: the host's address, MAC
// (a random one changes, so it is not kept), name, type and built-in
// picture. Each host is checked on its own: one that cannot be added is
// marked in its row and the others are added.
func (w *Workspace) addScanned(d *scanDialog) {
	doc := w.doc()
	final := newDeviceFinal(doc)
	d.failed = map[string]string{}
	d.err = ""
	var added []model.RouteDevice
	var addedIPs []string
	others := append([]*model.RouteDevice(nil), doc.Route.Devices...)
	for _, h := range d.devices {
		if !d.selected[h.IP] || h.Self || model.ScannedKnown(doc, scannedHost(h)) {
			continue
		}
		address, ok := model.HostAddress(h.IP)
		if !ok {
			d.failed[h.IP] = "地址无效"
			continue
		}
		device := model.RouteDevice{ID: model.NewID("device"), Name: scanName(h), Icon: scanIcon(h.Type),
			Addresses: []string{address}, Final: final, ImageKey: h.Image}
		if mac, ok := model.NormalizeDeviceMAC(h.MAC); ok && !model.IsRandomMAC(mac) {
			device.MAC = mac
		}
		if model.ScanConflicts(doc, scannedHost(h)) {
			d.failed[h.IP] = "地址已被其他设备使用"
			continue
		}
		if problem := model.ValidateDevice(&device, others); problem != "" {
			d.failed[h.IP] = problem
			continue
		}
		others = append(others, &device)
		added = append(added, device)
		addedIPs = append(addedIPs, h.IP)
	}
	if len(added) == 0 {
		return
	}
	if !w.save(func(doc *model.Doc) error {
		for _, device := range added {
			appendDevice(doc, device)
		}
		return nil
	}) {
		d.err = w.err
		return
	}
	for _, ip := range addedIPs {
		d.selected[ip] = false
	}
	if len(d.failed) == 0 {
		d.open = false
	}
}

// scanDialogView is the "Scan LAN" dialog.
func (w *Workspace) scanDialogView(c *ui.Context, doc *model.Doc) {
	d := w.scan
	k := kit.T(c)
	ticked := 0
	for _, h := range d.devices {
		if d.selected[h.IP] {
			ticked++
		}
	}
	kit.Dialog(c, &d.open, "扫描局域网", 560, func() {
		if d.running {
			label := "扫描中"
			if d.steps > 0 {
				label = fmt.Sprintf("扫描中 %d / %d", d.step, d.steps)
			}
			ui.Row(c).Gap(6).Children(func() {
				kit.Spinner(c, 14)
				ui.Text(c, label).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum")
			})
		} else if len(d.devices) == 0 && d.err == "" {
			ui.Text(c, "没有发现设备").FontSize(kit.FontTable).TextColor(k.Label3)
		}
		ui.Scroll(c).MaxHeight(380).Gap(1).Children(func() {
			for _, h := range d.devices {
				w.scanRow(c, doc, d, h)
			}
		})
		kit.FormError(c, d.err)
	}, func() {
		if d.running {
			kit.Button(c, "取消扫描", kit.ButtonOptions{Disabled: d.cancelling}).OnClick(func() { w.cancelScan(d) })
		} else {
			kit.Button(c, "关闭", kit.ButtonOptions{}).OnClick(func() { d.open = false })
			kit.Button(c, "重新扫描", kit.ButtonOptions{Icon: kit.SymRefreshCw}).OnClick(func() { w.startScan() })
		}
		label := "添加所选"
		if ticked > 0 {
			label = fmt.Sprintf("添加所选 %d 台", ticked)
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: ticked == 0 || d.running}).OnClick(func() { w.addScanned(d) })
	})
	if !d.open {
		w.stopScan(d)
		if w.scan == d {
			w.scan = nil
		}
	}
}

// scanRow is a host of the scan: tick box, picture, name, address and MAC.
func (w *Workspace) scanRow(c *ui.Context, doc *model.Doc, d *scanDialog, h api.ScanDevice) {
	k := kit.T(c)
	known, conflict := w.hostStatus(d, doc, h)
	// A conflicting host cannot be added (its address belongs to another
	// device), so it is not offered for ticking.
	locked := known || conflict || h.Self
	ui.Row(c.Key(h.IP)).FillWidth().MinHeight(52).Gap(10).PaddingX(4).Radius(8).Children(func() {
		picked := d.selected[h.IP] && !locked
		kit.Checkbox(c.Key("tick:"+h.IP), &picked, "").Label("选择 " + scanName(h)).Disabled(locked)
		if !locked {
			d.selected[h.IP] = picked
		}
		if b := w.builtinBitmap(c.Theme().Dark, h.Image); b != nil {
			ui.Image(c, b).Size(32, 32).Radius(7).Shrink(0)
		} else {
			ui.Box(c).Size(32, 32).Radius(7).Center().Shrink(0).Background(k.Fill).TextColor(k.Label3).Children(func() {
				kit.Icon(c, deviceIcon(scanIcon(h.Type)), 18)
			})
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Row(c).Gap(6).Children(func() {
				kit.Clipped(c, scanName(h), kit.FontBody, 500).Shrink(1)
				switch {
				case h.Self:
					kit.Badge(c, "本机", kit.Neutral)
				case known:
					kit.Badge(c, "已添加", kit.Neutral)
				case conflict:
					kit.Badge(c, "MAC 不同", kit.Neutral)
				case h.Gateway:
					kit.Badge(c, "网关", kit.Neutral)
				}
			})
			parts := []string{h.IP}
			if t := scanTypeNames[h.Type]; t != "" {
				parts = append(parts, t)
			}
			if m := cleanScanText(h.Model); m != "" && m != scanName(h) {
				parts = append(parts, m)
			}
			if id := cleanScanText(h.ModelID); id != "" {
				parts = append(parts, id)
			}
			if v := cleanScanText(h.OS); v != "" {
				parts = append(parts, v)
			}
			kit.Clipped(c, strings.Join(parts, " · "), kit.FontCaption, 400).TextColor(k.Label3)
			if problem := d.failed[h.IP]; problem != "" {
				kit.Clipped(c, problem, kit.FontCaption, 400).TextColor(k.Danger)
			}
		})
		ui.Column(c).Shrink(0).AlignItems(ui.End).Gap(1).Children(func() {
			if h.MAC == "" {
				ui.Text(c, "MAC 未识别").FontSize(kit.FontCaption).TextColor(k.Label3)
				return
			}
			ui.Text(c, h.MAC).FontSize(kit.FontCaption).TextColor(k.Label2).FontFeatures("tnum").Selectable()
			if h.MACRandom {
				ui.Text(c, "随机 MAC").FontSize(10).TextColor(k.Label3)
			}
		})
	})
}
