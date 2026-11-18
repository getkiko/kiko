package timefmt

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// The zones load everywhere, Windows included, from the embedded
	// database.
	_ "time/tzdata"
)

// zone is the display time zone, nil for the system's (time.Local).
var zone atomic.Pointer[time.Location]

// zoneName is the name SetZone last took.
var zoneName atomic.Value

// PrefZone is the app preference (Env.AppPrefs) holding the display time
// zone's IANA name, empty for the system's.
const PrefZone = "displayTimeZone"

// ZoneName is the display time zone's IANA name as set, "" while it
// follows the system.
func ZoneName() string {
	name, _ := zoneName.Load().(string)
	return name
}

// SetZone sets the display time zone by its IANA name; "" or a name the
// database lacks follows the system.
func SetZone(name string) {
	if old, _ := zoneName.Load().(string); old == name {
		return
	}
	zoneName.Store(name)
	if name == "" {
		zone.Store(nil)
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		zone.Store(nil)
		return
	}
	zone.Store(loc)
}

// Location is the display time zone: the setting's, or time.Local while
// it follows the system.
func Location() *time.Location {
	if loc := zone.Load(); loc != nil {
		return loc
	}
	return time.Local
}

// In is t in the display time zone.
func In(t time.Time) time.Time { return t.In(Location()) }

// Now is the current time in the display time zone.
func Now() time.Time { return In(time.Now()) }

// Milli is a Unix time in milliseconds in the display time zone.
func Milli(ms int64) time.Time { return In(time.UnixMilli(ms)) }

// Format formats t in the display time zone.
func Format(t time.Time, layout string) string { return In(t).Format(layout) }

// FormatMilli formats a Unix time in milliseconds in the display time
// zone.
func FormatMilli(ms int64, layout string) string { return Milli(ms).Format(layout) }

// Zone is a choice of the display time zone.
type Zone struct {
	// Name is the IANA name, as "Asia/Shanghai".
	Name string
	// City is the readable name, as "Shanghai" in Chinese or "New York".
	City string
	// Offset is the current offset from UTC, as "UTC+08:00".
	Offset  string
	seconds int
}

//go:embed zones.txt
var zoneList string

var (
	zonesMu       sync.Mutex
	zonesHour     int64
	zonesList     []Zone
	zonesBuilding bool
	zoneLocations []*time.Location
)

// Zones are the IANA time zones by their current UTC offset; within an
// offset the cities with Chinese names first, then by IANA name. It never
// blocks: the list is built, and rebuilt each hour for the offsets, on a
// goroutine, and nil until first built.
func Zones() []Zone {
	zonesMu.Lock()
	defer zonesMu.Unlock()
	if hour := time.Now().Unix() / 3600; (zonesList == nil || hour != zonesHour) && !zonesBuilding {
		zonesBuilding = true
		go buildZones()
	}
	return zonesList
}

func buildZones() {
	zonesMu.Lock()
	locations := zoneLocations
	zonesMu.Unlock()
	if locations == nil {
		for name := range strings.Lines(zoneList) {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if loc, err := time.LoadLocation(name); err == nil {
				locations = append(locations, loc)
			}
		}
		locations = append(locations, time.UTC)
	}
	now := time.Now()
	zones := make([]Zone, 0, len(locations))
	for _, loc := range locations {
		_, seconds := now.In(loc).Zone()
		name := loc.String()
		zones = append(zones, Zone{Name: name, City: City(name), Offset: Offset(seconds), seconds: seconds})
	}
	slices.SortStableFunc(zones, func(a, b Zone) int {
		if a.seconds != b.seconds {
			return a.seconds - b.seconds
		}
		_, ca := cityNames[a.Name]
		_, cb := cityNames[b.Name]
		if ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	zonesMu.Lock()
	zoneLocations, zonesList, zonesHour, zonesBuilding = locations, zones, now.Unix()/3600, false
	zonesMu.Unlock()
}

// SystemOffset is the system time zone's current offset, as Offset.
func SystemOffset() string {
	_, seconds := time.Now().Zone()
	return Offset(seconds)
}

// Offset formats an offset from UTC in seconds: "UTC+08:00", "UTC−05:00".
func Offset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "−", -seconds
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

// City is a zone's readable name: the Chinese name of a common city, or
// the IANA name's city, as "New York" or "Center (North Dakota)".
func City(name string) string {
	if city, ok := cityNames[name]; ok {
		return city
	}
	parts := strings.Split(strings.ReplaceAll(name, "_", " "), "/")
	city := parts[len(parts)-1]
	if len(parts) > 2 {
		city += " (" + parts[len(parts)-2] + ")"
	}
	return city
}

// cityNames are the Chinese names of common cities.
var cityNames = map[string]string{
	"Asia/Shanghai":                  "上海",
	"Asia/Hong_Kong":                 "香港",
	"Asia/Macau":                     "澳门",
	"Asia/Taipei":                    "台北",
	"Asia/Urumqi":                    "乌鲁木齐",
	"Asia/Tokyo":                     "东京",
	"Asia/Seoul":                     "首尔",
	"Asia/Pyongyang":                 "平壤",
	"Asia/Singapore":                 "新加坡",
	"Asia/Kuala_Lumpur":              "吉隆坡",
	"Asia/Bangkok":                   "曼谷",
	"Asia/Ho_Chi_Minh":               "胡志明市",
	"Asia/Jakarta":                   "雅加达",
	"Asia/Manila":                    "马尼拉",
	"Asia/Kolkata":                   "加尔各答",
	"Asia/Dubai":                     "迪拜",
	"Asia/Tehran":                    "德黑兰",
	"Asia/Jerusalem":                 "耶路撒冷",
	"Asia/Riyadh":                    "利雅得",
	"Asia/Karachi":                   "卡拉奇",
	"Asia/Dhaka":                     "达卡",
	"Asia/Ulaanbaatar":               "乌兰巴托",
	"Asia/Vladivostok":               "符拉迪沃斯托克",
	"Europe/London":                  "伦敦",
	"Europe/Paris":                   "巴黎",
	"Europe/Berlin":                  "柏林",
	"Europe/Madrid":                  "马德里",
	"Europe/Rome":                    "罗马",
	"Europe/Amsterdam":               "阿姆斯特丹",
	"Europe/Zurich":                  "苏黎世",
	"Europe/Stockholm":               "斯德哥尔摩",
	"Europe/Moscow":                  "莫斯科",
	"Europe/Istanbul":                "伊斯坦布尔",
	"Europe/Kyiv":                    "基辅",
	"Africa/Cairo":                   "开罗",
	"Africa/Johannesburg":            "约翰内斯堡",
	"Africa/Lagos":                   "拉各斯",
	"America/New_York":               "纽约",
	"America/Chicago":                "芝加哥",
	"America/Denver":                 "丹佛",
	"America/Los_Angeles":            "洛杉矶",
	"America/Phoenix":                "菲尼克斯",
	"America/Anchorage":              "安克雷奇",
	"America/Toronto":                "多伦多",
	"America/Vancouver":              "温哥华",
	"America/Mexico_City":            "墨西哥城",
	"America/Sao_Paulo":              "圣保罗",
	"America/Argentina/Buenos_Aires": "布宜诺斯艾利斯",
	"Pacific/Honolulu":               "檀香山",
	"Pacific/Auckland":               "奥克兰",
	"Australia/Sydney":               "悉尼",
	"Australia/Melbourne":            "墨尔本",
	"Australia/Perth":                "珀斯",
}
