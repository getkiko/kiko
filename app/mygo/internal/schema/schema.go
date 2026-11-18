package schema

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// FieldActionSpec is a generator button: its accessible name and what it
// produces. Run returns "" for actions the form computes itself.
type FieldActionSpec struct {
	Label string
	Run   func() string
}

// FieldActions are the generator buttons inside inputs (FIELD_ACTIONS).
var FieldActions = map[Action]FieldActionSpec{
	ActionUUID:       {Label: "随机生成", Run: randomUUID},
	ActionRandomPort: {Label: "随机端口", Run: func() string { return "" }},
	ActionBase64:     {Label: "Base64", Run: func() string { return "" }},
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// ItemSpec is how list items of a kind are shown and checked.
type ItemSpec struct {
	Label       string // "Add XX"
	Placeholder string
	Hint        string
	Examples    []string
	Numeric     bool // written to JSON as numbers
	// Validate returns the error of an item, "" when valid; nil accepts
	// anything.
	Validate func(string) string
}

var (
	reRuleSetTag   = regexp.MustCompile(`^[\w.-]+$`)
	reInterface    = regexp.MustCompile(`^[\w.:-]+$`)
	reDigits       = regexp.MustCompile(`^\d+$`)
	reUIDRange     = regexp.MustCompile(`^(\d+):(\d+)$`)
	rePackage      = regexp.MustCompile(`^[a-zA-Z][\w]*(\.[a-zA-Z][\w]*)+$`)
	reOctet        = regexp.MustCompile(`^\d{1,3}$`)
	reHostLabel    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
	reBits         = regexp.MustCompile(`^\d{1,3}$`)
	reBase64       = regexp.MustCompile(`^[A-Za-z0-9+/_-]+={0,2}$`)
	reSpaceAnyKind = regexp.MustCompile(`\s`)
)

// ItemSpecs are the formats of list items (ITEM_SPEC).
var ItemSpecs = map[ItemKind]ItemSpec{
	ItemCIDR: {
		Label: "网段", Placeholder: "172.18.0.1/30", Hint: "CIDR 格式，IPv4 或 IPv6 均可",
		Examples: []string{"192.168.0.0/16", "10.0.0.0/8", "fd00::/8"},
		Validate: func(s string) string {
			if IsCIDR(s) {
				return ""
			}
			return "不是合法的 CIDR，例：192.168.0.0/16"
		},
	},
	ItemIP: {
		Label: "IP 地址", Placeholder: "127.0.0.1", Hint: "IPv4 或 IPv6 地址",
		Examples: []string{"127.0.0.1", "::1"},
		Validate: func(s string) string {
			if IsIPv4(s) || IsIPv6(s) {
				return ""
			}
			return "不是合法的 IP 地址"
		},
	},
	ItemRuleSet: {
		Label: "规则集", Placeholder: "geoip-cn", Hint: "填规则集的 tag，需在「路由」里已定义",
		Validate: func(s string) string {
			if reRuleSetTag.MatchString(s) {
				return ""
			}
			return "只能包含字母、数字、_ . -"
		},
	},
	ItemInterface: {
		Label: "网卡", Placeholder: "eth0", Hint: "网络接口名称",
		Examples: []string{"eth0", "wlan0", "en0"},
		Validate: func(s string) string {
			if reInterface.MatchString(s) {
				return ""
			}
			return "网卡名含非法字符"
		},
	},
	ItemUID: {
		Label: "UID", Placeholder: "1000", Hint: "用户 id，纯数字", Numeric: true,
		Validate: digitsOnly,
	},
	ItemUIDRange: {
		Label: "UID 区间", Placeholder: "1000:2000", Hint: "格式 起始:结束，闭区间",
		Examples: []string{"0:999", "10000:19999"},
		Validate: func(s string) string {
			m := reUIDRange.FindStringSubmatch(s)
			if m == nil {
				return "格式应为 起始:结束，例：1000:2000"
			}
			if jsv.StringToNumber(m[1]) > jsv.StringToNumber(m[2]) {
				return "起始值不能大于结束值"
			}
			return ""
		},
	},
	ItemPackage: {
		Label: "应用包名", Placeholder: "com.android.chrome", Hint: "Android 应用包名",
		Examples: []string{"com.android.chrome", "com.tencent.mm"},
		Validate: func(s string) string {
			if rePackage.MatchString(s) {
				return ""
			}
			return "包名格式形如 com.example.app"
		},
	},
	ItemAndroidUser: {
		Label: "Android 用户", Placeholder: "0", Hint: "用户 id，主用户为 0，工作资料通常是 10", Numeric: true,
		Examples: []string{"0", "10"},
		Validate: digitsOnly,
	},
	ItemText: {Label: "条目"},
}

func digitsOnly(s string) string {
	if reDigits.MatchString(s) {
		return ""
	}
	return "必须是纯数字"
}

// IsIPv4 reports a dotted IPv4 address. Octets with leading zeros are
// refused, as sing-box (netip) refuses them.
func IsIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if !reOctet.MatchString(p) || len(p) > 1 && p[0] == '0' {
			return false
		}
		if n, _ := strconv.Atoi(p); n > 255 {
			return false
		}
	}
	return true
}

// IsIPv6 reports an IPv6 address: eight groups, or fewer with one ::,
// an embedded IPv4 address counting as two trailing groups.
func IsIPv6(s string) bool { return hpIPv6(s) }

func isHostname(s string) bool {
	value := strings.TrimSuffix(strings.TrimSpace(s), ".")
	if value == "" || len(value) > 253 || IsIPv4(value) || IsIPv6(value) {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || !reHostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// IsCIDR reports an IPv4 or IPv6 network in CIDR notation (isCidr).
func IsCIDR(s string) bool {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return false
	}
	addr, bits := s[:i], s[i+1:]
	if !reBits.MatchString(bits) {
		return false
	}
	n, _ := strconv.Atoi(bits)
	if IsIPv4(addr) {
		return n <= 32
	}
	if IsIPv6(addr) {
		return n <= 128
	}
	return false
}

// FieldAvailable reports whether a field is supported on platform.
func FieldAvailable(f Field, platform string) bool {
	return f.Platforms == nil || slices.Contains(f.Platforms, platform)
}

// FieldHint returns a field's hint on platform.
func FieldHint(f Field, platform string) string {
	if hint, ok := f.HintByPlatform[platform]; ok {
		return hint
	}
	return f.Hint
}

// schemaIndex caches, per field table, each field's position and the
// nearest enabled switch along its dotted path. Only the package's tables
// are indexed: they never change, while slices callers build (a form's
// presented fields, rebuilt each frame) may be changed or dropped, and
// caching them would keep them alive and could return stale positions.
type schemaIndex struct {
	byKey  map[string]int
	parent map[string]int // -1 when none
}

type tableKey struct {
	first *Field
	n     int
}

var (
	indexMu sync.Mutex
	indexes map[tableKey]*schemaIndex
)

// packageTables lists the tables of this package, keyed by their backing
// array.
func packageTables() map[tableKey]*schemaIndex {
	out := map[tableKey]*schemaIndex{}
	add := func(table []Field) {
		if len(table) > 0 {
			out[tableKey{&table[0], len(table)}] = nil
		}
	}
	for _, tables := range []map[string][]Field{InboundSchema, OutboundSchema, EndpointSchema, ServiceSchema} {
		for _, table := range tables {
			add(table)
		}
	}
	for _, table := range [][]Field{DNSDialerSchema, OutboundTLS, InboundTLS, TLSClientSchema, TLSServerSchema, DNSTLSSchema, NetworkSchema} {
		add(table)
	}
	return out
}

// indexOf returns the index of one of the package's tables, nil for any
// other slice.
func indexOf(schema []Field) *schemaIndex {
	if len(schema) == 0 {
		return nil
	}
	key := tableKey{&schema[0], len(schema)}
	indexMu.Lock()
	defer indexMu.Unlock()
	if indexes == nil {
		indexes = packageTables()
	}
	ix, known := indexes[key]
	if !known {
		return nil
	}
	if ix != nil {
		return ix
	}
	ix = &schemaIndex{byKey: map[string]int{}, parent: map[string]int{}}
	for i := len(schema) - 1; i >= 0; i-- {
		ix.byKey[schema[i].Key] = i
	}
	bools := map[string]int{}
	for i := len(schema) - 1; i >= 0; i-- {
		if schema[i].Type == Bool {
			bools[schema[i].Key] = i
		}
	}
	for _, f := range schema {
		ix.parent[f.Key] = -1
		parts := strings.Split(f.Key, ".")
		for length := len(parts) - 1; length > 0; length-- {
			parentKey := strings.Join(parts[:length], ".") + ".enabled"
			if parentKey == f.Key {
				continue
			}
			if i, ok := bools[parentKey]; ok {
				ix.parent[f.Key] = i
				break
			}
		}
	}
	indexes[key] = ix
	return ix
}

// fieldIndex is the position of the first field with key, -1 if none.
func fieldIndex(schema []Field, key string) int {
	if ix := indexOf(schema); ix != nil {
		if i, ok := ix.byKey[key]; ok {
			return i
		}
		return -1
	}
	return slices.IndexFunc(schema, func(f Field) bool { return f.Key == key })
}

// hierarchyEnabledParent finds the nearest enabled switch along a field's
// dotted path, e.g. tls.ech.config → tls.ech.enabled.
func hierarchyEnabledParent(field Field, schema []Field) (Field, bool) {
	if ix := indexOf(schema); ix != nil {
		if i, ok := ix.parent[field.Key]; ok {
			if i < 0 {
				return Field{}, false
			}
			return schema[i], true
		}
	}
	parts := strings.Split(field.Key, ".")
	for length := len(parts) - 1; length > 0; length-- {
		parentKey := strings.Join(parts[:length], ".") + ".enabled"
		if parentKey == field.Key {
			continue
		}
		for _, candidate := range schema {
			if candidate.Key == parentKey && candidate.Type == Bool {
				return candidate, true
			}
		}
	}
	return Field{}, false
}

// FieldVisible reports whether a field's whole condition chain holds, so a
// REALITY key hides when either REALITY or TLS is off.
func FieldVisible(field Field, schema []Field, values map[string]any) bool {
	return fieldVisible(field, schema, values, nil)
}

func fieldVisible(field Field, schema []Field, values map[string]any, ancestors map[string]bool) bool {
	with := func(key string) map[string]bool {
		next := make(map[string]bool, len(ancestors)+1)
		for k := range ancestors {
			next[k] = true
		}
		next[key] = true
		return next
	}
	if parent, ok := hierarchyEnabledParent(field, schema); ok {
		if !jsv.Truthy(values[parent.Key]) || ancestors[parent.Key] {
			return false
		}
		if !fieldVisible(parent, schema, values, with(field.Key)) {
			return false
		}
	}
	condition := field.ShowIf
	if condition == nil {
		return true
	}
	value := values[condition.Key]
	matches := true
	switch {
	case condition.Truthy:
		matches = jsv.Truthy(value)
	case condition.HasEq:
		matches = jsv.StrictEqual(value, condition.Eq)
	case condition.OneOf != nil:
		matches = slices.ContainsFunc(condition.OneOf, func(v any) bool { return jsv.StrictEqual(v, value) })
	}
	if condition.Domain {
		s, ok := value.(string)
		domain := ok && (isHostname(s) || IsBurrowDomainAddress(s))
		matches = matches && domain
	}
	if !matches {
		return false
	}
	if i := fieldIndex(schema, condition.Key); i >= 0 {
		parent := schema[i]
		if ancestors[parent.Key] {
			return false
		}
		return fieldVisible(parent, schema, values, with(field.Key))
	}
	return true
}

func emptyFieldValue(value any) bool {
	switch x := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	}
	if a, ok := jsv.Array(value); ok {
		return len(a) == 0
	}
	return false
}

// FieldValidationError returns the error of a field's value, "" when it
// is valid. onSave also runs the checks deferred to saving.
func FieldValidationError(field Field, schema []Field, values map[string]any, platform string, onSave bool) string {
	if !FieldAvailable(field, platform) || !FieldVisible(field, schema, values) {
		return ""
	}
	value := values[field.Key]
	if field.Required && emptyFieldValue(value) {
		return "必填"
	}
	if emptyFieldValue(value) {
		return ""
	}
	if field.Type == Number {
		n := jsv.Number(value)
		if !jsv.IsFinite(n) {
			return "请输入有效数字"
		}
		if field.Min != nil && n < *field.Min {
			return "最小值为 " + jsv.FormatNumber(*field.Min)
		}
		if field.Max != nil && n > *field.Max {
			return "最大值为 " + jsv.FormatNumber(*field.Max)
		}
		// sing-box reads every numeric option as an integer.
		if !jsv.IsInteger(n) {
			return "请输入整数"
		}
	}
	if field.Type == Duration && !IsDuration(strings.TrimSpace(jsv.String(value))) {
		return "请输入有效时长，例如 30s、5m"
	}
	if field.ValidateOnSave && !onSave {
		return ""
	}
	if field.Validate == nil {
		return ""
	}
	return field.Validate(strings.TrimSpace(jsv.String(value)), values)
}

var reDuration = regexp.MustCompile(`^[-+]?(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h|d))+$`)

// IsDuration reports a duration sing-box parses: Go's time.ParseDuration
// syntax with days ("d") added, or "0".
func IsDuration(s string) bool {
	return s == "0" || reDuration.MatchString(s)
}

// FieldError is a field with the error of its value.
type FieldError struct {
	Field   Field
	Message string
}

// FormValidationErrors returns the errors of a whole form, as checked on
// saving.
func FormValidationErrors(schema []Field, values map[string]any, platform string) []FieldError {
	var out []FieldError
	for _, field := range schema {
		if message := FieldValidationError(field, schema, values, platform, true); message != "" {
			out = append(out, FieldError{Field: field, Message: message})
		}
	}
	return out
}

// realityRequiresUTLS is the uTLS switch's validator.
func realityRequiresUTLS(_ string, values map[string]any) string {
	if jsv.Truthy(values["tls.reality.enabled"]) && values["tls.utls.enabled"] != true {
		return "REALITY 客户端必须启用 uTLS"
	}
	return ""
}

// shadowsocks2022PasswordError checks a Shadowsocks 2022 key's length.
func shadowsocks2022PasswordError(value string, values map[string]any) string {
	method := jsv.StringOr(values["method"], "")
	if !strings.HasPrefix(method, "2022-") {
		return ""
	}
	requiredBytes := 32
	if strings.Contains(method, "aes-128") {
		requiredBytes = 16
	}
	compact := reSpaceAnyKind.ReplaceAllString(strings.TrimSpace(value), "")
	if compact == "" || !reBase64.MatchString(compact) {
		return "请输入有效的 Base64 密码"
	}
	normalized := strings.NewReplacer("-", "+", "_", "/").Replace(compact)
	for len(normalized)%4 != 0 {
		normalized += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return "请输入有效的 Base64 密码"
	}
	if len(decoded) != requiredBytes {
		return fmt.Sprintf("Base64 解码后必须是 %d 字节，当前为 %d 字节", requiredBytes, len(decoded))
	}
	return ""
}

// EndpointAddress is the address an endpoint's summary shows.
func EndpointAddress(typ string, fields map[string]any) string {
	join := func(host, port string) string {
		if host == "" {
			return ""
		}
		if port != "" {
			return host + ":" + port
		}
		return host
	}
	switch typ {
	case "openvpn-server":
		host := strings.TrimSpace(jsString(fields["listen"]))
		port := jsv.Text(fields["listen_port"])
		if port == "" {
			return host
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return strings.TrimPrefix(host+":"+port, ":")
	case "tailscale":
		if jsv.Truthy(fields["hostname"]) {
			return strings.TrimSpace(jsv.String(fields["hostname"]))
		}
		return strings.TrimSpace(jsString(fields["control_url"]))
	case "wireguard":
		peers, _ := jsv.Array(fields["peers"])
		if len(peers) == 0 {
			return ""
		}
		first := jsv.Object(peers[0])
		return join(jsv.Text(first["address"]), jsv.Text(first["port"]))
	}
	return join(jsv.Text(fields["server"]), jsv.Text(fields["server_port"]))
}

// jsString is String(v || "").
func jsString(v any) string {
	if !jsv.Truthy(v) {
		return ""
	}
	return jsv.String(v)
}

// ServiceAvailable reports whether a service type runs on platform.
func ServiceAvailable(typ, platform string) bool {
	return slices.Contains(ServicePlatforms[typ], platform)
}

// ServiceUnavailableReason explains why a service type does not run on
// platform.
func ServiceUnavailableReason(typ, platform string) string {
	if hint, ok := ServicePlatformHint[typ][platform]; ok {
		return hint
	}
	role := "Client"
	if typ == "usbip-server" {
		role = "Server"
	}
	return PlatformLabel[platform] + " 不支持 USB/IP " + role
}

// UsbDeviceMatch selects USB devices of a USB/IP service; numbers are
// nil when absent.
type UsbDeviceMatch struct {
	BusID     string   `json:"bus_id,omitempty"`
	VendorID  *float64 `json:"vendor_id,omitempty"`
	ProductID *float64 `json:"product_id,omitempty"`
	Serial    string   `json:"serial,omitempty"`
}

func usbID(item map[string]any, key string) (float64, bool, bool) {
	text := jsv.Text(item[key])
	if text == "" {
		return 0, false, true
	}
	n := jsv.StringToNumber(text)
	valid := jsv.IsInteger(n) && n >= 0 && n <= 65535
	return n, true, valid
}

// NormalizeUsbDevices drops empty entries and invalid numbers;
// vendor_id and product_id must be integers in 0–65535.
func NormalizeUsbDevices(value any) []UsbDeviceMatch {
	list, ok := value.([]any)
	if !ok {
		return []UsbDeviceMatch{}
	}
	result := []UsbDeviceMatch{}
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var device UsbDeviceMatch
		device.BusID = jsv.Text(item["bus_id"])
		if n, set, valid := usbID(item, "vendor_id"); set && valid {
			device.VendorID = &n
		}
		if n, set, valid := usbID(item, "product_id"); set && valid {
			device.ProductID = &n
		}
		device.Serial = jsv.Text(item["serial"])
		if device.BusID != "" || device.VendorID != nil || device.ProductID != nil || device.Serial != "" {
			result = append(result, device)
		}
	}
	return result
}

// UsbDevicesError returns the error of a service's devices: numbers out
// of range, empty entries, and a default server without devices.
func UsbDevicesError(typ string, fields map[string]any) string {
	raw, _ := fields["devices"].([]any)
	for index, entry := range raw {
		item, _ := entry.(map[string]any)
		for _, key := range []string{"vendor_id", "product_id"} {
			if _, set, valid := usbID(item, key); set && !valid {
				return fmt.Sprintf("第 %d 项的 %s 需为 0–65535 的十进制整数", index+1, key)
			}
		}
		filled := false
		for _, key := range []string{"bus_id", "vendor_id", "product_id", "serial"} {
			if jsv.Text(item[key]) != "" {
				filled = true
			}
		}
		if !filled {
			return fmt.Sprintf("第 %d 项至少需要填写一个匹配条件", index+1)
		}
	}
	provider := "default"
	if jsv.Truthy(fields["provider"]) {
		provider = jsv.String(fields["provider"])
	}
	if typ == "usbip-server" && provider == "default" && len(NormalizeUsbDevices(anyList(raw))) == 0 {
		return "default 模式至少需要一个要导出的设备"
	}
	return ""
}

func anyList(raw []any) any {
	if raw == nil {
		return []any{}
	}
	return raw
}

// HasServer reports whether an outbound type has a server address.
func HasServer(typ string) bool {
	return !slices.Contains([]string{"direct", "block", "bridge", "tor", "selector", "urltest", "fallback", "loadbalance", "smart"}, typ)
}
