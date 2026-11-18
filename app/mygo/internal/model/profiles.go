package model

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// Device profiles: a device keeps several rule sets and one is in effect.
// Every profile has a scope, the owner key its rules, groups and DNS
// defaults carry (RouteRule.DeviceID and the like). The scope of the
// original profile is the device id, so documents written before profiles
// need no change; later profiles use "<device id>@<profile id>".
//
// The local scope (local server / gateway) has profiles the same way: LocalDevice
// presents it as a device whose id is LocalDeviceID, its profiles stored in
// Route.LocalProfiles and Route.LocalActiveProfile, the original profile's
// default outbound in Route.LocalFinal.

// The implicit profile of a device without Profiles.
const (
	MainProfileID   = "main"
	MainProfileName = "默认"
)

// Errors of the profile operations.
var (
	ErrProfileDevice    = errors.New("设备不存在")
	ErrProfileGone      = errors.New("配置不存在")
	ErrProfileName      = errors.New("请填写配置名称")
	ErrProfileNameTaken = errors.New("配置名称已存在")
	ErrProfileLast      = errors.New("至少保留一个配置")
)

// LocalDeviceName names the local scope.
const LocalDeviceName = "本机 / 网关"

// IsLocalScope reports a scope of the local scope's profiles.
func IsLocalScope(scope string) bool {
	return scope == LocalDeviceID || strings.HasPrefix(scope, LocalDeviceID+"@")
}

// localDeviceOf presents the local scope of a route section as a device.
func localDeviceOf(route *Route) *RouteDevice {
	return &RouteDevice{ID: LocalDeviceID, Name: LocalDeviceName, Icon: "router", Addresses: route.LocalAddresses,
		Final: route.LocalFinal.Or(route.Final).ID(), Profiles: route.LocalProfiles, ActiveProfile: route.LocalActiveProfile}
}

// LocalDevice presents the local scope as a device: id LocalDeviceID, its
// profiles and the default outbound of its original profile (Route.LocalFinal,
// inheriting route.final). Writes go through the profile functions.
func LocalDevice(doc *Doc) *RouteDevice { return localDeviceOf(&doc.Route) }

// LocalScopes lists the scopes of the local profiles, none while the local
// scope is removed.
func LocalScopes(doc *Doc) []string {
	if !LocalEnabled(doc) {
		return nil
	}
	return DeviceScopes(LocalDevice(doc))
}

// LocalActiveScope is the scope of the local profile in effect.
func LocalActiveScope(doc *Doc) string { return ActiveScope(LocalDevice(doc)) }

// ProfileDevices lists the owners of profiles: the local scope while it
// exists, then the LAN devices.
func ProfileDevices(doc *Doc) []*RouteDevice {
	if !LocalEnabled(doc) {
		return doc.Route.Devices
	}
	return append([]*RouteDevice{LocalDevice(doc)}, doc.Route.Devices...)
}

// ProfileScope is the scope key of a profile that is not the device's
// original one.
func ProfileScope(deviceID, profileID string) string { return deviceID + "@" + profileID }

// DeviceProfiles lists a device's profiles with their effective default
// outbounds; a device without Profiles has the one implicit profile.
func DeviceProfiles(d *RouteDevice) []DeviceProfile {
	if len(d.Profiles) == 0 {
		return []DeviceProfile{{ID: MainProfileID, Name: MainProfileName, Scope: d.ID, Final: d.Final}}
	}
	out := slices.Clone(d.Profiles)
	for i := range out {
		if out[i].Scope == d.ID {
			out[i].Final = d.Final
		}
	}
	return out
}

// ActiveProfileOf is the profile in effect: ActiveProfile, else the first.
func ActiveProfileOf(d *RouteDevice) DeviceProfile {
	profiles := DeviceProfiles(d)
	for _, p := range profiles {
		if p.ID == d.ActiveProfile {
			return p
		}
	}
	return profiles[0]
}

// ActiveScope is the scope whose rules the device uses.
func ActiveScope(d *RouteDevice) string { return ActiveProfileOf(d).Scope }

// DeviceScopes lists the scopes of all profiles of a device.
func DeviceScopes(d *RouteDevice) []string {
	profiles := DeviceProfiles(d)
	out := make([]string, len(profiles))
	for i, p := range profiles {
		out[i] = p.Scope
	}
	return out
}

// DeviceFinalOutbounds lists every default outbound a device keeps: its own
// and those of its other profiles.
func DeviceFinalOutbounds(d *RouteDevice) []string {
	if len(d.Profiles) == 0 {
		return []string{d.Final}
	}
	var out []string
	for _, p := range d.Profiles {
		if p.Scope == d.ID {
			out = append(out, d.Final)
		} else {
			out = append(out, p.Final)
		}
	}
	return out
}

// ScopeDevice finds the device and profile that own a scope, in effect or
// not. The local scope's profiles belong to LocalDevice.
func ScopeDevice(doc *Doc, scope string) (*RouteDevice, DeviceProfile, bool) {
	if scope == "" || scope == PreDeviceID || scope == OtherDeviceID {
		return nil, DeviceProfile{}, false
	}
	if IsLocalScope(scope) {
		if !LocalEnabled(doc) {
			return nil, DeviceProfile{}, false
		}
		d := LocalDevice(doc)
		for _, p := range DeviceProfiles(d) {
			if p.Scope == scope {
				return d, p, true
			}
		}
		return nil, DeviceProfile{}, false
	}
	for _, d := range doc.Route.Devices {
		if scope != d.ID && !strings.HasPrefix(scope, d.ID+"@") {
			continue
		}
		for _, p := range DeviceProfiles(d) {
			if p.Scope == scope {
				return d, p, true
			}
		}
	}
	return nil, DeviceProfile{}, false
}

// ProfileFinal is the default outbound id of a scope of the device.
func ProfileFinal(d *RouteDevice, scope string) string {
	if scope == d.ID {
		return d.Final
	}
	for _, p := range d.Profiles {
		if p.Scope == scope {
			return p.Final
		}
	}
	return ""
}

// ScopeName names a scope for display: the fixed names, a device's name,
// and "device · profile" for a device with several profiles.
func ScopeName(doc *Doc, scope string) string {
	switch scope {
	case PreDeviceID:
		return "前置规则"
	case OtherDeviceID:
		return "其他设备"
	}
	if d, p, ok := ScopeDevice(doc, scope); ok && d.Name != "" {
		if len(DeviceProfiles(d)) > 1 {
			return d.Name + " · " + p.Name
		}
		return d.Name
	}
	if scope == LocalDeviceID {
		return LocalDeviceName
	}
	return "已删除设备"
}

// UniqueProfileName returns name, or name with a number appended while the
// device already has a profile of that name.
func UniqueProfileName(d *RouteDevice, name string) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(DeviceProfiles(d), func(p DeviceProfile) bool { return p.Name == n })
	}
	if !taken(name) {
		return name
	}
	for n := 2; ; n++ {
		candidate := name + " " + strconv.Itoa(n)
		if !taken(candidate) {
			return candidate
		}
	}
}

// validateProfiles returns why a device's profiles are invalid, "" when
// valid.
func validateProfiles(d *RouteDevice) string {
	if len(d.Profiles) == 0 {
		return ""
	}
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, p := range d.Profiles {
		name := strings.TrimSpace(p.Name)
		if p.ID == "" || ids[p.ID] || p.Scope != d.ID && p.Scope != ProfileScope(d.ID, p.ID) {
			return "配置标识无效"
		}
		ids[p.ID] = true
		if name == "" {
			return "请填写配置名称"
		}
		if names[name] {
			return "配置名称重复"
		}
		names[name] = true
		if p.Scope != d.ID && p.Final == "" {
			return "请选择配置「" + p.Name + "」的默认出口"
		}
	}
	return ""
}

// CoreDefaultFinal is the outbound a scope's connections reach by default:
// its default outbound, and for the original local profile without one
// the core's default (its first usable target), as a copy of that profile
// gets it (AddProfile).
func CoreDefaultFinal(doc *Doc, scope string) string {
	final := RouteFinalFor(doc, scope).ID()
	if final == "" && scope == LocalDeviceID {
		final = firstUsableTarget(doc)
	}
	return final
}

// ValidateLocalProfiles returns why the local profiles are invalid, ""
// when valid or the local scope is removed.
func ValidateLocalProfiles(doc *Doc) string {
	if !LocalEnabled(doc) {
		return ""
	}
	return validateProfiles(LocalDevice(doc))
}

// localFinalsInvalid reports a local profile whose default outbound is not
// usable; the original profile's may be unset (it inherits route.final).
func localFinalsInvalid(doc *Doc) bool {
	if !LocalEnabled(doc) {
		return false
	}
	for _, p := range DeviceProfiles(LocalDevice(doc)) {
		if p.Scope == LocalDeviceID {
			if doc.Route.LocalFinal.Valid() && !IsOutboundUsable(doc, doc.Route.LocalFinal.ID()) {
				return true
			}
		} else if !IsOutboundUsable(doc, p.Final) {
			return true
		}
	}
	return false
}

// storedProfiles is the Profiles field for a device with the implicit
// profile written out.
func storedProfiles(d *RouteDevice) []DeviceProfile {
	if len(d.Profiles) > 0 {
		return slices.Clone(d.Profiles)
	}
	return []DeviceProfile{{ID: MainProfileID, Name: MainProfileName, Scope: d.ID}}
}

// profileDevice finds a profile owner by id: a LAN device, or the local
// scope while it exists.
func profileDevice(doc *Doc, deviceID string) *RouteDevice {
	if deviceID == LocalDeviceID {
		if LocalEnabled(doc) {
			return LocalDevice(doc)
		}
		return nil
	}
	for _, d := range doc.Route.Devices {
		if d.ID == deviceID {
			return d
		}
	}
	return nil
}

// withDevice returns doc with the device edited by fn on a copy. For the
// local scope fn edits LocalDevice and its profiles are written back.
func withDevice(doc *Doc, deviceID string, fn func(c *RouteDevice)) (*Doc, *RouteDevice, error) {
	if deviceID == LocalDeviceID {
		if !LocalEnabled(doc) {
			return nil, nil, ErrProfileDevice
		}
		c := LocalDevice(doc)
		c.Profiles = slices.Clone(c.Profiles)
		fn(c)
		next := *doc
		next.Route.LocalProfiles, next.Route.LocalActiveProfile = c.Profiles, c.ActiveProfile
		return &next, c, nil
	}
	index := slices.IndexFunc(doc.Route.Devices, func(d *RouteDevice) bool { return d.ID == deviceID })
	if index < 0 {
		return nil, nil, ErrProfileDevice
	}
	next := *doc
	next.Route.Devices = slices.Clone(doc.Route.Devices)
	c := *doc.Route.Devices[index]
	fn(&c)
	next.Route.Devices[index] = &c
	return &next, &c, nil
}

// setScopeFinal sets the default outbound of a profile scope (a device's,
// or a local profile's other than the original) on a document the caller
// owns.
func setScopeFinal(doc *Doc, scope, final string) {
	if !IsLocalScope(scope) {
		doc.Route.Devices = withScopeFinal(doc.Route.Devices, scope, final)
		return
	}
	if scope == LocalDeviceID {
		doc.Route.LocalFinal = RefOf(final)
		return
	}
	if j := slices.IndexFunc(doc.Route.LocalProfiles, func(p DeviceProfile) bool { return p.Scope == scope }); j >= 0 {
		doc.Route.LocalProfiles = slices.Clone(doc.Route.LocalProfiles)
		doc.Route.LocalProfiles[j].Final = final
	}
}

// withScopeFinal returns devices with the default outbound of a scope set.
func withScopeFinal(devices []*RouteDevice, scope, final string) []*RouteDevice {
	out := make([]*RouteDevice, len(devices))
	for i, d := range devices {
		if scope == d.ID {
			c := *d
			c.Final = final
			d = &c
		} else if strings.HasPrefix(scope, d.ID+"@") {
			if j := slices.IndexFunc(d.Profiles, func(p DeviceProfile) bool { return p.Scope == scope }); j >= 0 {
				c := *d
				c.Profiles = slices.Clone(d.Profiles)
				c.Profiles[j].Final = final
				d = &c
			}
		}
		out[i] = d
	}
	return out
}

func profileIndex(d *RouteDevice, profileID string) int {
	return slices.IndexFunc(DeviceProfiles(d), func(p DeviceProfile) bool { return p.ID == profileID })
}

func checkProfileName(d *RouteDevice, name, exceptID string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrProfileName
	}
	if slices.ContainsFunc(DeviceProfiles(d), func(p DeviceProfile) bool { return p.Name == name && p.ID != exceptID }) {
		return "", ErrProfileNameTaken
	}
	return name, nil
}

// AddProfile adds a profile to a device. It is blank unless copyFromScope
// names a scope: then its rules, groups, DNS rules and defaults are copied
// with new ids. The new profile is not activated; find it by name.
func AddProfile(doc *Doc, deviceID, name, copyFromScope string) (*Doc, error) {
	device := profileDevice(doc, deviceID)
	if device == nil {
		return nil, ErrProfileDevice
	}
	name, err := checkProfileName(device, name, "")
	if err != nil {
		return nil, err
	}
	if copyFromScope != "" && !slices.Contains(AllPolicyScopes(doc), copyFromScope) {
		return nil, ErrPolicyScope
	}
	id := NewID("pf")
	scope := ProfileScope(deviceID, id)
	final := RouteFinalFor(doc, copyFromScope).ID()
	if copyFromScope == "" || final == "" {
		final = ProfileFinal(device, ActiveScope(device))
	}
	if final == "" {
		final = firstUsableTarget(doc)
	}
	next, _, err := withDevice(doc, deviceID, func(c *RouteDevice) {
		c.Profiles = append(storedProfiles(device), DeviceProfile{ID: id, Name: name, Scope: scope, Final: final})
	})
	if err != nil {
		return nil, err
	}
	if copyFromScope != "" {
		for _, section := range []PolicySection{SectionTraffic, SectionDNS} {
			if next, err = CopyDevicePolicy(next, CopyPolicyOptions{Section: section, Source: copyFromScope, Target: scope, IncludeDefault: true}); err != nil {
				return nil, err
			}
		}
	}
	ensureScopeDefaults(next, scope)
	return next, nil
}

// ensureScopeDefaults gives a scope a default group in both policies and a
// default DNS server, on a document the caller owns.
func ensureScopeDefaults(doc *Doc, scope string) {
	if !slices.ContainsFunc(doc.Route.Groups, func(g *RouteGroup) bool { return DeviceOwner(g.DeviceID) == scope }) {
		doc.Route.Groups = append(slices.Clone(doc.Route.Groups), &RouteGroup{ID: NewID("rg"), Name: "默认分组", DeviceID: scope})
	}
	if !slices.ContainsFunc(doc.DNS.Groups, func(g *RouteGroup) bool { return DeviceOwner(g.DeviceID) == scope }) {
		doc.DNS.Groups = append(slices.Clone(doc.DNS.Groups), &RouteGroup{ID: stable("dg", scope, "default"), Name: "默认分组", DeviceID: scope})
	}
	if _, ok := doc.DNS.DeviceFinals[scope]; !ok {
		doc.DNS.DeviceFinals = copyRefs(doc.DNS.DeviceFinals)
		doc.DNS.DeviceFinals[scope] = doc.DNS.Final.Or(Null())
		if _, ok := doc.DNS.DeviceFinalDetours[scope]; !ok && doc.DNS.FinalDetour.Valid() {
			doc.DNS.DeviceFinalDetours = copyRefs(doc.DNS.DeviceFinalDetours)
			doc.DNS.DeviceFinalDetours[scope] = doc.DNS.FinalDetour
		}
	}
}

// RenameProfile renames a profile of a device.
func RenameProfile(doc *Doc, deviceID, profileID, name string) (*Doc, error) {
	var problem error
	next, _, err := withDevice(doc, deviceID, func(c *RouteDevice) {
		i := profileIndex(c, profileID)
		if i < 0 {
			problem = ErrProfileGone
			return
		}
		name, problem = checkProfileName(c, name, profileID)
		if problem != nil {
			return
		}
		profiles := storedProfiles(c)
		profiles[i].Name = name
		c.Profiles = profiles
	})
	if err != nil {
		return nil, err
	}
	if problem != nil {
		return nil, problem
	}
	return next, nil
}

// DeleteProfile removes a profile with its rules, groups and DNS defaults.
// The last profile stays. Deleting the active profile activates the first
// remaining one.
func DeleteProfile(doc *Doc, deviceID, profileID string) (*Doc, error) {
	var problem error
	var scope string
	next, _, err := withDevice(doc, deviceID, func(c *RouteDevice) {
		profiles := DeviceProfiles(c)
		i := profileIndex(c, profileID)
		switch {
		case i < 0:
			problem = ErrProfileGone
			return
		case len(profiles) < 2:
			problem = ErrProfileLast
			return
		}
		scope = profiles[i].Scope
		wasActive := ActiveProfileOf(c).ID == profileID
		stored := storedProfiles(c)
		c.Profiles = slices.Delete(stored, i, i+1)
		if wasActive {
			c.ActiveProfile = c.Profiles[0].ID
		}
	})
	if err != nil {
		return nil, err
	}
	if problem != nil {
		return nil, problem
	}
	dropOwners(next, []string{scope})
	return next, nil
}

// SetActiveProfile makes a profile the one in effect.
func SetActiveProfile(doc *Doc, deviceID, profileID string) (*Doc, error) {
	var problem error
	next, _, err := withDevice(doc, deviceID, func(c *RouteDevice) {
		if profileIndex(c, profileID) < 0 {
			problem = ErrProfileGone
			return
		}
		if len(c.Profiles) == 0 {
			return
		}
		c.ActiveProfile = profileID
	})
	if err != nil {
		return nil, err
	}
	if problem != nil {
		return nil, problem
	}
	return next, nil
}
