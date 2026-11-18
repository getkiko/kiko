package subscription

import (
	"math"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// ParseInfo reads a Subscription-Userinfo header, as
// "upload=1; download=2; total=3; expire=4"; nil when it is empty.
// Missing or invalid numbers are 0.
func ParseInfo(value string) *model.Traffic {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	fields := map[string]string{}
	for _, part := range strings.Split(value, ";") {
		key, raw, _ := strings.Cut(strings.TrimSpace(part), "=")
		// split("=", 2) keeps only the text up to a second "=".
		raw, _, _ = strings.Cut(raw, "=")
		fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(raw)
	}
	number := func(key string) float64 {
		raw, ok := fields[key]
		if !ok {
			return 0
		}
		n := jsv.StringToNumber(raw)
		if !jsv.IsFinite(n) || n < 0 {
			return 0
		}
		return n
	}
	return &model.Traffic{Upload: number("upload"), Download: number("download"), Total: number("total"), Expire: number("expire")}
}

// FormatTraffic writes bytes in binary units with three significant
// digits, as "3.00 GB" or "100 GB".
func FormatTraffic(value float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	bytes := value
	if !jsv.IsFinite(bytes) || bytes <= 0 {
		bytes = 0
	}
	exponent := 0
	if bytes >= 1 {
		exponent = min(int(math.Floor(math.Log2(bytes)/10)), len(units)-1)
	}
	amount := bytes / math.Pow(1024, float64(exponent))
	var text string
	if amount >= 1000 {
		text = strconv.FormatFloat(amount, 'f', 0, 64)
	} else {
		text = toPrecision3(amount)
	}
	return text + " " + units[exponent]
}

// toPrecision3 is Number.prototype.toPrecision(3) for 0 ≤ v < 1000.
func toPrecision3(v float64) string {
	if v == 0 {
		return "0.00"
	}
	digits := int(math.Floor(math.Log10(v))) + 1
	decimals := max(3-digits, 0)
	text := strconv.FormatFloat(v, 'f', decimals, 64)
	// Rounding up may add a digit (999.5 → "1000"): toPrecision then
	// switches to exponent notation, which amounts under 1000 avoid.
	if decimals > 0 && len(strings.ReplaceAll(strings.TrimLeft(text, "0."), ".", "")) > 3 {
		text = strconv.FormatFloat(v, 'f', decimals-1, 64)
	}
	return text
}

// FormatExpireDate writes an expiry in seconds as a local date,
// "2027-07-28"; empty when there is none.
func FormatExpireDate(value float64) string {
	if !jsv.IsFinite(value) || value <= 0 {
		return ""
	}
	return timefmt.FormatMilli(int64(value*1000), "2006-01-02")
}
