package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultProxyBypass = []string{"127.0.0.1", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "localhost", "*.local", "*.crashlytics.com", "<local>"}

var (
	errSysProxyTun     = errors.New("已启用 TUN 入站，系统代理不可用")
	errSysProxyNoMixed = errors.New("尚未应用包含 Mixed 入站的配置")
	bypassPattern      = regexp.MustCompile(`^[A-Za-z0-9*._-]+$`)
)

type sysProxySettings struct {
	Enabled        bool     `json:"enabled"`
	Host           string   `json:"host"`
	PAC            bool     `json:"pac"`
	Guard          bool     `json:"guard"`
	GuardInterval  int      `json:"guardInterval"`
	DefaultBypass  bool     `json:"defaultBypass"`
	ValidateBypass bool     `json:"validateBypass"`
	Bypass         []string `json:"bypass"`
}

type sysProxyState struct {
	mu       sync.Mutex
	settings sysProxySettings
	applied  bool
}

type sysProxyApply struct {
	Host   string
	Port   int
	PAC    string
	Bypass []string
}

type sysProxyStatus struct {
	Supported bool `json:"supported"`
	sysProxySettings
	EffectiveHost   string   `json:"effectiveHost"`
	EffectiveBypass []string `json:"effectiveBypass"`
	Port            int      `json:"port,omitempty"`
	Tun             bool     `json:"tun"`
	Applied         bool     `json:"applied"`
}

func sysProxySupported() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	case "linux":
		return gsettingsAvailable()
	}
	return false
}

func defaultSysProxySettings() sysProxySettings {
	return sysProxySettings{GuardInterval: 30, DefaultBypass: true, ValidateBypass: true}
}

func (st *sysProxySettings) normalize() {
	st.Host = strings.TrimSpace(st.Host)
	if st.GuardInterval <= 0 {
		st.GuardInterval = 30
	}
	custom := make([]string, 0, len(st.Bypass))
	for _, entry := range st.Bypass {
		if entry = strings.TrimSpace(entry); entry != "" {
			custom = append(custom, entry)
		}
	}
	st.Bypass = custom
}

func (st sysProxySettings) validate() error {
	if st.Host != "" && net.ParseIP(st.Host) == nil && !bypassPattern.MatchString(st.Host) {
		return fmt.Errorf("代理主机无效: %s", st.Host)
	}
	if st.GuardInterval > 3600 {
		return errors.New("代理守卫间隔不能超过 3600 秒")
	}
	if !st.ValidateBypass {
		return nil
	}
	var invalid []string
	for _, entry := range st.Bypass {
		if !validBypassEntry(entry) {
			invalid = append(invalid, entry)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("代理绕过格式无效: %s", strings.Join(invalid, ", "))
	}
	return nil
}

func validBypassEntry(entry string) bool {
	if entry == "<local>" || net.ParseIP(entry) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(entry); err == nil {
		return true
	}
	return bypassPattern.MatchString(entry) && !strings.HasPrefix(entry, "-")
}

func (st sysProxySettings) effectiveBypass() []string {
	var list []string
	if st.DefaultBypass {
		list = append(list, defaultProxyBypass...)
	}
	seen := map[string]bool{}
	out := list[:0:0]
	for _, entry := range append(list, st.Bypass...) {
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out
}

func (s *server) sysProxyPath() string { return filepath.Join(s.dataDir, "system-proxy.json") }
func (s *server) sysProxyRestorePath() string {
	return filepath.Join(s.dataDir, "system-proxy-restore.json")
}

func (s *server) loadSysProxy() {
	s.sys.settings = defaultSysProxySettings()
	_ = readJSON(s.sysProxyPath(), &s.sys.settings)
	s.sys.settings.normalize()
	// Older versions stored the default 127.0.0.1 as an explicit value. It never
	// worked when Mixed listens on a specific IP, so treat it as "automatic".
	if s.sys.settings.Host == "127.0.0.1" {
		s.sys.settings.Host = ""
	}
}

// autoProxyHost returns the address used when the "Proxy host" setting is empty:
// the specific IP Mixed listens on, or loopback when it listens on all addresses
// (or none is given).
func autoProxyHost(listen string) string {
	listen = strings.Trim(strings.TrimSpace(listen), "[]")
	if listen == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(listen); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return listen
}

func (st sysProxySettings) resolveHost(listen string) string {
	if st.Host != "" {
		return st.Host
	}
	return autoProxyHost(listen)
}

func (s *server) proxyTarget() (port int, listen string, tun bool) {
	b, err := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	if err != nil {
		return 0, "", false
	}
	var value struct {
		Inbounds []struct {
			Type       string `json:"type"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(b, &value) != nil {
		return 0, "", false
	}
	for _, in := range value.Inbounds {
		switch in.Type {
		case "tun":
			tun = true
		case "mixed":
			if port == 0 {
				port, listen = in.ListenPort, in.Listen
			}
		}
	}
	return port, listen, tun
}

func (s *server) pacURL() string {
	host, port, err := net.SplitHostPort(s.listenAddr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/system-proxy/pac"
}

func (s *server) buildSysProxyApply(st sysProxySettings) (sysProxyApply, error) {
	port, listen, tun := s.proxyTarget()
	if tun {
		return sysProxyApply{}, errSysProxyTun
	}
	if port == 0 {
		return sysProxyApply{}, errSysProxyNoMixed
	}
	apply := sysProxyApply{Host: st.resolveHost(listen), Port: port, Bypass: st.effectiveBypass()}
	if st.PAC {
		apply.PAC = s.pacURL()
	}
	return apply, nil
}

func (s *server) sysProxyStatus() sysProxyStatus {
	port, listen, tun := s.proxyTarget()
	return sysProxyStatus{
		Supported: sysProxySupported(), sysProxySettings: s.sys.settings, EffectiveHost: s.sys.settings.resolveHost(listen),
		EffectiveBypass: s.sys.settings.effectiveBypass(), Port: port, Tun: tun, Applied: s.sys.applied,
	}
}

func (s *server) getSysProxy(w http.ResponseWriter, r *http.Request) {
	s.sys.mu.Lock()
	defer s.sys.mu.Unlock()
	writeJSON(w, 200, s.sysProxyStatus())
}

func (s *server) putSysProxy(w http.ResponseWriter, r *http.Request) {
	if !sysProxySupported() {
		http.Error(w, "当前系统不支持设置系统代理", http.StatusNotImplemented)
		return
	}
	var in sysProxySettings
	if !decode(w, r, &in) {
		return
	}
	in.normalize()
	if err := in.validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.sys.mu.Lock()
	defer s.sys.mu.Unlock()
	s.mu.Lock()
	closing := s.shuttingDown
	s.mu.Unlock()
	if closing {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if in.Enabled {
		apply, err := s.buildSysProxyApply(in)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err = setSystemProxy(apply, s.sysProxyRestorePath()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.sys.applied = true
	} else if s.sys.applied {
		if err := clearSystemProxy(s.sysProxyRestorePath()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.sys.applied = false
	}
	if err := atomicJSON(s.sysProxyPath(), in); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.sys.settings = in
	writeJSON(w, 200, s.sysProxyStatus())
}

func (s *server) syncSysProxy() {
	if !sysProxySupported() {
		return
	}
	s.sys.mu.Lock()
	defer s.sys.mu.Unlock()
	s.mu.Lock()
	closing := s.shuttingDown
	s.mu.Unlock()
	if closing {
		return
	}
	st := s.sys.settings
	if !st.Enabled {
		return
	}
	apply, err := s.buildSysProxyApply(st)
	if err != nil {
		if clearErr := clearSystemProxy(s.sysProxyRestorePath()); clearErr != nil {
			s.logs.add("[kiko] 关闭系统代理失败: " + clearErr.Error())
		}
		s.sys.applied = false
		s.sys.settings.Enabled = false
		_ = atomicJSON(s.sysProxyPath(), s.sys.settings)
		s.logs.add("[kiko] 系统代理已关闭: " + err.Error())
		return
	}
	if err = setSystemProxy(apply, s.sysProxyRestorePath()); err != nil {
		s.logs.add("[kiko] 设置系统代理失败: " + err.Error())
		return
	}
	s.sys.applied = true
}

func (s *server) releaseSysProxy() {
	s.sys.mu.Lock()
	defer s.sys.mu.Unlock()
	if !s.sys.applied {
		return
	}
	if err := clearSystemProxy(s.sysProxyRestorePath()); err != nil {
		s.logs.add("[kiko] 恢复系统代理失败: " + err.Error())
		return
	}
	s.sys.applied = false
}

func (s *server) sysProxyGuard(ctx context.Context) {
	for {
		s.sys.mu.Lock()
		st := s.sys.settings
		s.sys.mu.Unlock()
		wait := 5 * time.Second
		if st.Enabled && st.Guard {
			wait = time.Duration(st.GuardInterval) * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.guardSysProxyOnce()
	}
}

func (s *server) guardSysProxyOnce() {
	s.sys.mu.Lock()
	defer s.sys.mu.Unlock()
	s.mu.Lock()
	closing := s.shuttingDown
	s.mu.Unlock()
	if closing {
		return
	}
	st := s.sys.settings
	if !st.Enabled || !st.Guard || !s.sys.applied {
		return
	}
	apply, err := s.buildSysProxyApply(st)
	if err != nil || systemProxyMatches(apply) {
		return
	}
	if err = setSystemProxy(apply, s.sysProxyRestorePath()); err != nil {
		s.logs.add("[kiko] 系统代理守卫恢复失败: " + err.Error())
		return
	}
	s.logs.add("[kiko] 系统代理被外部修改，已恢复")
}

func (s *server) sysProxyPAC(w http.ResponseWriter, r *http.Request) {
	s.sys.mu.Lock()
	st := s.sys.settings
	s.sys.mu.Unlock()
	apply, err := s.buildSysProxyApply(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(buildPAC(apply)))
}

func buildPAC(a sysProxyApply) string {
	quote := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	var b strings.Builder
	b.WriteString("function FindProxyForURL(url, host) {\n")
	for _, entry := range a.Bypass {
		if entry == "<local>" {
			b.WriteString("  if (isPlainHostName(host)) return \"DIRECT\";\n")
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && network.IP.To4() != nil {
			mask := net.IP(network.Mask).String()
			fmt.Fprintf(&b, "  if (/^\\d+\\.\\d+\\.\\d+\\.\\d+$/.test(host) && isInNet(host, %s, %s)) return \"DIRECT\";\n", quote(network.IP.String()), quote(mask))
			continue
		}
		pattern := entry
		if strings.HasPrefix(pattern, ".") {
			pattern = "*" + pattern
		}
		fmt.Fprintf(&b, "  if (shExpMatch(host, %s)) return \"DIRECT\";\n", quote(pattern))
	}
	target := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	fmt.Fprintf(&b, "  return \"PROXY %s; SOCKS5 %s; SOCKS %s; DIRECT\";\n}\n", target, target, target)
	return b.String()
}

// winBypass expands CIDRs into the wildcard forms Windows ProxyOverride
// supports.
func winBypass(entries []string) []string {
	var out []string
	for _, entry := range entries {
		_, network, err := net.ParseCIDR(entry)
		ip := net.IP(nil)
		if err == nil {
			ip = network.IP.To4()
		}
		if ip == nil {
			out = append(out, entry)
			continue
		}
		ones, _ := network.Mask.Size()
		fixed, rest := ones/8, ones%8
		if ones < 8 {
			out = append(out, entry)
			continue
		}
		prefix := make([]string, 0, 4)
		for i := 0; i < fixed && i < 4; i++ {
			prefix = append(prefix, strconv.Itoa(int(ip[i])))
		}
		switch {
		case fixed >= 4:
			out = append(out, strings.Join(prefix, "."))
		case rest == 0:
			out = append(out, strings.Join(prefix, ".")+".*")
		default:
			for n := 0; n < 1<<(8-rest); n++ {
				out = append(out, strings.Join(append(append([]string{}, prefix...), strconv.Itoa(int(ip[fixed])+n)), ".")+".*")
			}
		}
	}
	return out
}
