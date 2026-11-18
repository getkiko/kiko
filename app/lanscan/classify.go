package lanscan

import "strings"

// Device types.
const (
	TypePhone    = "phone"
	TypeTablet   = "tablet"
	TypeWatch    = "watch"
	TypeComputer = "computer"
	TypeTV       = "tv"
	TypeSpeaker  = "speaker"
	TypeRouter   = "router"
	TypePrinter  = "printer"
	TypeIoT      = "iot"
	TypeUnknown  = "unknown"
)

// Built-in image keys.
const (
	ImageIPhone  = "iphone"
	ImageIPad    = "ipad"
	ImageWatch   = "watch"
	ImageMac     = "mac"
	ImageAppleTV = "appletv"
	ImageHomePod = "homepod"
	ImageAirPods = "airpods"
	ImageVision  = "vision"
	ImagePC      = "pc"
	ImageRouter  = "router"
)

// ClassifyInput is everything known about a host.
type ClassifyInput struct {
	Hostname   string
	Model      string   // mDNS _device-info model, e.g. MacBookPro18,3
	Vendor     string   // OUI vendor name
	VendorKind string   // OUI vendor kind
	Gateway    bool     // default gateway
	Services   []string // mDNS/SSDP service types, e.g. _ipp._tcp
	NetBIOS    bool     // answered NetBIOS (Windows/Samba)
	Ports      []int    // open TCP ports
}

// Classification is the verdict.
type Classification struct {
	Type  string
	Model string
	Image string
}

// Classify decides device type and built-in image. It is pure.
func Classify(in ClassifyInput) Classification {
	out := Classification{Type: TypeUnknown, Model: strings.TrimSpace(in.Model)}
	set := func(t, img string) Classification { out.Type, out.Image = t, img; return out }

	// 1. mDNS model identifier.
	m := out.Model
	switch {
	case strings.HasPrefix(m, "iPhone"):
		return set(TypePhone, ImageIPhone)
	case strings.HasPrefix(m, "iPod"):
		return set(TypePhone, "")
	case strings.HasPrefix(m, "iPad"):
		return set(TypeTablet, ImageIPad)
	case strings.HasPrefix(m, "Watch"):
		return set(TypeWatch, ImageWatch)
	case strings.HasPrefix(m, "AppleTV"):
		return set(TypeTV, ImageAppleTV)
	case strings.HasPrefix(m, "AudioAccessory"):
		return set(TypeSpeaker, ImageHomePod)
	case strings.HasPrefix(m, "AirPods"):
		return set(TypeSpeaker, ImageAirPods)
	case strings.HasPrefix(m, "RealityDevice"), strings.HasPrefix(m, "Vision"):
		return set(TypeComputer, ImageVision)
	case strings.HasPrefix(m, "Mac"), strings.HasPrefix(m, "iMac"), strings.HasPrefix(m, "Xserve"):
		return set(TypeComputer, ImageMac)
	}

	host := strings.ToLower(in.Hostname)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(host, w) {
				return true
			}
		}
		return false
	}
	hasPrefix := func(words ...string) bool {
		for _, w := range words {
			if strings.HasPrefix(host, w) {
				return true
			}
		}
		return false
	}

	// 2. Apple hostnames (also a phone hotspot that is the gateway).
	switch {
	case has("iphone"):
		return set(TypePhone, ImageIPhone)
	case has("ipad"):
		return set(TypeTablet, ImageIPad)
	case has("apple-watch", "applewatch", "apple watch"):
		return set(TypeWatch, ImageWatch)
	case has("apple-tv", "appletv", "apple tv"):
		return set(TypeTV, ImageAppleTV)
	case has("homepod"):
		return set(TypeSpeaker, ImageHomePod)
	case has("airpods"):
		return set(TypeSpeaker, ImageAirPods)
	case has("vision-pro", "visionpro", "apple-vision"):
		return set(TypeComputer, ImageVision)
	case has("macbook", "imac", "mac-mini", "macmini", "mac mini", "mac-studio", "macstudio", "mac-pro"):
		return set(TypeComputer, ImageMac)
	}

	hasPort := func(ports ...int) bool {
		for _, p := range ports {
			for _, q := range in.Ports {
				if p == q {
					return true
				}
			}
		}
		return false
	}
	hasSvc := func(svcs ...string) bool {
		for _, want := range svcs {
			for _, s := range in.Services {
				if strings.EqualFold(s, want) {
					return true
				}
			}
		}
		return false
	}

	// 3. lockdownd: iPhone / iPad (the hostname above tells the iPad apart),
	// unless the host is an AirPlay receiver, which a phone never is: an
	// Apple TV also runs lockdownd.
	if hasPort(portLockdown) || hasSvc("_apple-mobdev2._tcp") {
		if hasSvc("_airplay._tcp", "_raop._tcp") && !hasSvc("_apple-mobdev2._tcp") {
			return set(TypeTV, ImageAppleTV)
		}
		return set(TypePhone, ImageIPhone)
	}

	// 4. Gateway or router vendor.
	if in.Gateway || in.VendorKind == kindRouter {
		return set(TypeRouter, ImageRouter)
	}
	if hasPrefix("router", "gateway", "openwrt", "unifi", "mikrotik", "tplink", "tp-link", "ap-") {
		return set(TypeRouter, ImageRouter)
	}

	// 5. Generic hostnames.
	switch {
	case hasPrefix("desktop-", "laptop-", "win-", "win10", "win11", "pc-") || has("windows", "thinkpad", "ubuntu", "debian", "fedora", "archlinux", "raspberrypi", "proxmox", "pve"):
		return set(TypeComputer, ImagePC)
	case hasPrefix("android-", "galaxy", "pixel", "redmi", "xiaomi", "oneplus", "oppo", "huawei", "honor", "iqoo", "vivo"):
		return set(TypePhone, "")
	case has("printer", "laserjet", "officejet", "deskjet", "epson", "brother", "canon"):
		return set(TypePrinter, "")
	case has("roku", "bravia", "webos", "chromecast", "firetv", "fire-tv", "smarttv", "smart-tv", "tizen") || hasPrefix("tv-", "lg-tv"):
		return set(TypeTV, "")
	case has("echo", "sonos", "google-home", "nest-", "homepod-"):
		return set(TypeSpeaker, "")
	case has("synology", "diskstation", "qnap", "truenas", "freenas", "unraid") || hasPrefix("nas-"):
		return set(TypeComputer, "")
	}

	// 6. A Mac shares files or a shell. Only signals that Apple alone
	// sends count (an AirPlay receiver may be shairport-sync on Linux).
	apple := in.Vendor == "Apple" || hasSvc("_companion-link._tcp", "_device-info._tcp")
	if apple && hasPort(22, 445, 548) {
		return set(TypeComputer, ImageMac)
	}

	// 7. Services.
	for _, s := range in.Services {
		switch strings.ToLower(s) {
		case "_ipp._tcp", "_ipps._tcp", "_printer._tcp", "_pdl-datastream._tcp", "_scanner._tcp":
			return set(TypePrinter, "")
		case "_upnp-igd._tcp":
			return set(TypeRouter, ImageRouter)
		case "_googlecast._tcp":
			return set(TypeTV, "")
		case "_spotify-connect._tcp", "_sonos._tcp":
			return set(TypeSpeaker, "")
		case "_hap._tcp", "_homekit._tcp", "_matter._tcp", "_hue._tcp":
			return set(TypeIoT, "")
		}
	}

	// 8. Ports of PCs, printers and routers.
	switch {
	case hasPort(3389):
		return set(TypeComputer, ImagePC)
	case hasPort(9100, 631):
		return set(TypePrinter, "")
	case hasPort(445, 139) && !hasPort(53):
		return set(TypeComputer, ImagePC)
	case hasPort(53) && hasPort(80, 443):
		return set(TypeRouter, ImageRouter)
	}

	// 9. Other vendors.
	switch in.VendorKind {
	case kindPC:
		return set(TypeComputer, ImagePC)
	case kindPrinter:
		return set(TypePrinter, "")
	case kindPhone:
		return set(TypePhone, "")
	case kindIoT:
		return set(TypeIoT, "")
	}
	if in.NetBIOS {
		return set(TypeComputer, ImagePC)
	}
	return out
}
