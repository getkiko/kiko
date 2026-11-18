package lanscan

import (
	"net"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		in        ClassifyInput
		typ, img  string
		wantModel string
	}{
		{"iphone model", ClassifyInput{Model: "iPhone14,2"}, TypePhone, ImageIPhone, "iPhone14,2"},
		{"ipad model", ClassifyInput{Model: "iPad13,4"}, TypeTablet, ImageIPad, "iPad13,4"},
		{"watch model", ClassifyInput{Model: "Watch6,1"}, TypeWatch, ImageWatch, "Watch6,1"},
		{"macbook model", ClassifyInput{Model: "MacBookPro18,3"}, TypeComputer, ImageMac, "MacBookPro18,3"},
		{"imac model", ClassifyInput{Model: "iMac21,1"}, TypeComputer, ImageMac, "iMac21,1"},
		{"mac mini model", ClassifyInput{Model: "Macmini9,1"}, TypeComputer, ImageMac, "Macmini9,1"},
		{"mac plain", ClassifyInput{Model: "Mac14,3"}, TypeComputer, ImageMac, "Mac14,3"},
		{"appletv", ClassifyInput{Model: "AppleTV11,1"}, TypeTV, ImageAppleTV, "AppleTV11,1"},
		{"homepod", ClassifyInput{Model: "AudioAccessory5,1"}, TypeSpeaker, ImageHomePod, "AudioAccessory5,1"},
		{"airpods", ClassifyInput{Model: "AirPods3,1"}, TypeSpeaker, ImageAirPods, "AirPods3,1"},
		{"vision", ClassifyInput{Model: "RealityDevice14,1"}, TypeComputer, ImageVision, "RealityDevice14,1"},
		{"model beats gateway", ClassifyInput{Model: "iPhone14,2", Gateway: true}, TypePhone, ImageIPhone, "iPhone14,2"},
		{"iphone hotspot gateway", ClassifyInput{Hostname: "Zhang-iPhone", Gateway: true}, TypePhone, ImageIPhone, ""},
		{"ipad hostname", ClassifyInput{Hostname: "Lisas-iPad"}, TypeTablet, ImageIPad, ""},
		{"macbook hostname", ClassifyInput{Hostname: "Bobs-MacBook-Air"}, TypeComputer, ImageMac, ""},
		{"apple tv hostname", ClassifyInput{Hostname: "Living-Room-Apple-TV"}, TypeTV, ImageAppleTV, ""},
		{"gateway", ClassifyInput{Gateway: true}, TypeRouter, ImageRouter, ""},
		{"router vendor", ClassifyInput{Vendor: "TP-Link", VendorKind: kindRouter}, TypeRouter, ImageRouter, ""},
		{"windows host", ClassifyInput{Hostname: "DESKTOP-AB12CD"}, TypeComputer, ImagePC, ""},
		{"linux host", ClassifyInput{Hostname: "ubuntu-server"}, TypeComputer, ImagePC, ""},
		{"pc vendor", ClassifyInput{Vendor: "Dell", VendorKind: kindPC}, TypeComputer, ImagePC, ""},
		{"netbios only", ClassifyInput{NetBIOS: true}, TypeComputer, ImagePC, ""},
		{"printer service", ClassifyInput{Services: []string{"_ipp._tcp"}}, TypePrinter, "", ""},
		{"printer vendor beats netbios", ClassifyInput{VendorKind: kindPrinter, NetBIOS: true}, TypePrinter, "", ""},
		{"igd service", ClassifyInput{Services: []string{"_upnp-igd._tcp"}}, TypeRouter, ImageRouter, ""},
		{"android", ClassifyInput{Hostname: "android-1a2b3c"}, TypePhone, "", ""},
		{"iot vendor", ClassifyInput{VendorKind: kindIoT}, TypeIoT, "", ""},
		{"apple vendor alone", ClassifyInput{Vendor: "Apple", VendorKind: kindApple}, TypeUnknown, "", ""},
		{"mixed vendor", ClassifyInput{Vendor: "Samsung", VendorKind: kindAny}, TypeUnknown, "", ""},
		{"nothing", ClassifyInput{}, TypeUnknown, "", ""},
		{"unknown model keeps text", ClassifyInput{Model: "Xiaomi TV"}, TypeUnknown, "", "Xiaomi TV"},
	}
	for _, c := range cases {
		got := Classify(c.in)
		if got.Type != c.typ || got.Image != c.img || got.Model != c.wantModel {
			t.Errorf("%s: got %+v, want type=%s image=%q model=%q", c.name, got, c.typ, c.img, c.wantModel)
		}
	}
}

func TestIsRandomMAC(t *testing.T) {
	for s, want := range map[string]bool{
		"02:00:00:00:00:00": true, "06:11:22:33:44:55": true, "aa:bb:cc:dd:ee:ff": true, "ae:bb:cc:dd:ee:ff": true,
		"a4:83:e7:11:22:33": false, "00:1a:2b:3c:4d:5e": false, "3c:22:fb:00:00:01": false,
	} {
		mac, _ := net.ParseMAC(s)
		if IsRandomMAC(mac) != want {
			t.Errorf("%s: want %v", s, want)
		}
	}
}

func TestLookupVendor(t *testing.T) {
	mac, _ := net.ParseMAC("3c:22:fb:00:00:01") // Apple
	if name, kind := LookupVendor(mac); name != "Apple" || kind != kindApple {
		t.Errorf("apple oui: %q %q", name, kind)
	}
	random, _ := net.ParseMAC("3e:22:fb:00:00:01")
	if name, _ := LookupVendor(random); name != "" {
		t.Errorf("random MAC must not resolve a vendor, got %q", name)
	}
	if len(ouiData) > 100<<10 {
		t.Errorf("oui table too large: %d", len(ouiData))
	}
	ouiOnce.Do(loadOUI)
	if len(ouiTable) < 5000 {
		t.Errorf("oui table suspiciously small: %d", len(ouiTable))
	}
}
