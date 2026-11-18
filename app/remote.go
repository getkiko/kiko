package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLocalListen  = "127.0.0.1:9099"
	defaultRemoteListen = ":9443"
	unauthorizedMessage = "未授权：请重新配对此设备"
)

// clientIdentity names the caller of a request. Requests that arrive on the
// loopback listener carry no identity in their context and count as local.
type clientIdentity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Remote bool   `json:"-"`
}

type clientContextKey struct{}

var localClient = clientIdentity{ID: "local", Name: "本机"}

func requestClient(r *http.Request) clientIdentity {
	if value, ok := r.Context().Value(clientContextKey{}).(clientIdentity); ok {
		return value
	}
	return localClient
}

func serverName() string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "kiko-server"
}

// validateLoopbackListen keeps the unauthenticated HTTP API on loopback.
func validateLoopbackListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err == nil {
		_, err = strconv.ParseUint(port, 10, 16)
	}
	if err != nil {
		return fmt.Errorf("--listen %q 无效：%v", address, err)
	}
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--listen 只能使用回环地址（例如 %s）。局域网或公网访问请使用 --remote-listen 开启 TLS 远程访问，再用 kiko-server pair 配对设备", defaultLocalListen)
}

func normalizeRemoteListen(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return defaultRemoteListen, nil
	}
	if _, err := strconv.ParseUint(address, 10, 16); err == nil {
		address = ":" + address
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("监听地址无效：%v", err)
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 && port != "0" {
		return "", fmt.Errorf("端口无效：%s", port)
	}
	return net.JoinHostPort(host, port), nil
}

type remoteConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

type remoteStatus struct {
	Enabled     bool     `json:"enabled"`
	Listen      string   `json:"listen"`
	Running     bool     `json:"running"`
	Error       string   `json:"error"`
	Fingerprint string   `json:"fingerprint"`
	Addresses   []string `json:"addresses"`
}

type remoteListener struct {
	requested string
	addr      net.Addr
	srv       *http.Server
	cancel    context.CancelFunc
	done      chan struct{}
}

func (l *remoteListener) close() {
	if l == nil {
		return
	}
	l.cancel()
	_ = l.srv.Close()
	<-l.done
}

// remoteManager owns the TLS listener for paired remote clients. It can be
// switched on and off while the server runs.
type remoteManager struct {
	s           *server
	dir         string
	mu          sync.Mutex
	cfg         remoteConfig
	handler     http.Handler
	active      *remoteListener
	lastErr     string
	cert        *tls.Certificate
	fingerprint string
	devices     *deviceStore
	pairing     *pairingStore
}

func newRemoteManager(s *server) *remoteManager {
	m := &remoteManager{s: s, dir: s.dataDir, pairing: newPairingStore()}
	_ = readJSON(m.path("remote.json"), &m.cfg)
	if listen, err := normalizeRemoteListen(m.cfg.Listen); err == nil {
		m.cfg.Listen = listen
	} else {
		m.cfg.Listen = defaultRemoteListen
	}
	m.devices = loadDeviceStore(m.path("remote-devices.json"))
	_ = m.loadCertLocked()
	return m
}

func (m *remoteManager) path(name string) string { return filepath.Join(m.dir, name) }

func (m *remoteManager) setHandler(h http.Handler) {
	m.mu.Lock()
	m.handler = h
	m.mu.Unlock()
}

func (m *remoteManager) status() remoteStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *remoteManager) statusLocked() remoteStatus {
	return remoteStatus{Enabled: m.cfg.Enabled, Listen: m.cfg.Listen, Running: m.active != nil, Error: m.lastErr, Fingerprint: m.fingerprint, Addresses: m.addressesLocked()}
}

func (m *remoteManager) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active != nil
}

// startSaved starts the listener when remote.json enables it.
func (m *remoteManager) startSaved() remoteStatus {
	m.mu.Lock()
	enabled := m.cfg.Enabled
	m.mu.Unlock()
	if !enabled {
		return m.status()
	}
	return m.configure(m.currentConfig())
}

func (m *remoteManager) currentConfig() remoteConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// configure persists cfg and applies it, closing a replaced listener at once.
func (m *remoteManager) configure(cfg remoteConfig) remoteStatus {
	status, old := m.apply(cfg)
	old.close()
	return status
}

// apply persists cfg and starts the listener it describes. The replaced
// listener is returned so a caller served by it can answer before it closes.
func (m *remoteManager) apply(cfg remoteConfig) (remoteStatus, *remoteListener) {
	listen, err := normalizeRemoteListen(cfg.Listen)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lastErr = err.Error()
		return m.statusLocked(), nil
	}
	cfg.Listen = listen
	m.cfg = cfg
	if err := atomicJSON(m.path("remote.json"), cfg); err != nil {
		m.lastErr = "保存远程访问设置失败：" + err.Error()
		return m.statusLocked(), nil
	}
	m.lastErr = ""
	old := m.active
	if !cfg.Enabled {
		m.active = nil
		return m.statusLocked(), old
	}
	if old != nil && old.requested == cfg.Listen {
		return m.statusLocked(), nil
	}
	next, err := m.listenLocked(cfg.Listen)
	if err != nil && old != nil {
		// The previous listener may hold the same port on another host form.
		old.close()
		old = nil
		m.active = nil
		next, err = m.listenLocked(cfg.Listen)
	}
	if err != nil {
		m.lastErr = err.Error()
		return m.statusLocked(), old
	}
	m.active = next
	return m.statusLocked(), old
}

func (m *remoteManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	active := m.active
	m.active = nil
	m.mu.Unlock()
	active.close()
	m.devices.flush()
}

func (m *remoteManager) listenLocked(address string) (*remoteListener, error) {
	if err := m.ensureCertLocked(); err != nil {
		return nil, fmt.Errorf("生成远程访问证书失败：%w", err)
	}
	handler := m.handler
	if handler == nil {
		handler = m.s.routes()
	}
	tcp, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("无法监听 %s：%w", address, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &remoteListener{requested: address, addr: tcp.Addr(), cancel: cancel, done: make(chan struct{})}
	l.srv = &http.Server{
		Handler:           m.s.remoteHandler(handler),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{*m.cert}, MinVersion: tls.VersionTLS12}
	go func() {
		_ = l.srv.Serve(tls.NewListener(tcp, tlsConfig))
		close(l.done)
	}()
	if m.s.logs != nil {
		m.s.logs.add("[kiko] 远程访问已开启：" + tcp.Addr().String())
	}
	return l, nil
}

func (m *remoteManager) loadCertLocked() error {
	cert, err := tls.LoadX509KeyPair(m.path("remote-cert.pem"), m.path("remote-key.pem"))
	if err != nil {
		return err
	}
	m.cert = &cert
	sum := sha256.Sum256(cert.Certificate[0])
	m.fingerprint = hex.EncodeToString(sum[:])
	return nil
}

// ensureCertLocked creates the self-signed certificate on first use. Clients
// pin its fingerprint, so it never rotates on its own.
func (m *remoteManager) ensureCertLocked() error {
	if m.cert != nil || m.loadCertLocked() == nil {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	host := serverName()
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"kiko-server"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           append(interfaceIPs(), net.IPv4(127, 0, 0, 1), net.IPv6loopback),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = atomicWrite(m.path("remote-key.pem"), keyPEM, 0o600); err != nil {
		return err
	}
	if err = atomicWrite(m.path("remote-cert.pem"), certPEM, 0o600); err != nil {
		return err
	}
	return m.loadCertLocked()
}

// interfaceIPs lists usable unicast addresses, IPv4 first.
func interfaceIPs() []net.IP {
	addrs, _ := net.InterfaceAddrs()
	var v4, v6 []net.IP
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() || ipNet.IP.IsMulticast() || ipNet.IP.IsUnspecified() {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ipNet.IP)
		}
	}
	return append(v4, v6...)
}

func (m *remoteManager) addressesLocked() []string {
	address := m.cfg.Listen
	if m.active != nil {
		address = m.active.addr.String()
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return []string{}
	}
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		return []string{net.JoinHostPort(host, port)}
	}
	out := []string{}
	for _, ip := range interfaceIPs() {
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out
}

// remoteHandler authenticates every request on the TLS listener. Pairing is
// the only route reachable without a device token.
func (s *server) remoteHandler(next http.Handler) http.Handler {
	m := s.remote
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/pair" {
			m.pair(w, r)
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		device, deviceCtx, ok := m.devices.authenticate(strings.TrimSpace(token), remoteIP(r))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": unauthorizedMessage})
			return
		}
		identity := clientIdentity{ID: device.ID, Name: device.Name, Remote: true}
		ctx, cancel := context.WithCancel(context.WithValue(r.Context(), clientContextKey{}, identity))
		defer cancel()
		// Revoking the device ends its in-flight requests and streams.
		stop := context.AfterFunc(deviceCtx, cancel)
		defer stop()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (m *remoteManager) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "请使用 POST"})
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "application/json required"})
		return
	}
	var in struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
		return
	}
	ip := remoteIP(r)
	switch err := m.pairing.redeem(ip, in.Code); {
	case errors.Is(err, errPairRateLimited):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "尝试次数过多，请稍后再试"})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "配对码无效或已过期"})
		return
	}
	token, device, err := m.devices.add(in.DeviceName, ip)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存设备失败：" + err.Error()})
		return
	}
	if m.s.logs != nil {
		m.s.logs.add(fmt.Sprintf("[kiko] 已配对远程设备「%s」（%s）", device.Name, ip))
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "deviceId": device.ID, "serverName": serverName()})
}

func (s *server) registerRemoteRoutes(m *routeMux) {
	m.get("GET /api/v1/remote", s.getRemote)
	m.handle("PUT /api/v1/remote", routeWrite, "修改远程访问", s.putRemote)
	m.handle("POST /api/v1/remote/pairing", routeWrite, "生成配对码", s.remotePairing)
	m.get("GET /api/v1/remote/devices", s.remoteDevices)
	m.handle("DELETE /api/v1/remote/devices/{id}", routeWrite, "吊销设备", s.revokeRemoteDevice)
}

func (s *server) remoteAvailable(w http.ResponseWriter) bool {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "远程访问不可用"})
		return false
	}
	return true
}

func (s *server) getRemote(w http.ResponseWriter, r *http.Request) {
	if s.remoteAvailable(w) {
		writeJSON(w, http.StatusOK, s.remote.status())
	}
}

func (s *server) putRemote(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAvailable(w) {
		return
	}
	var in remoteConfig
	if !decode(w, r, &in) {
		return
	}
	if _, err := normalizeRemoteListen(in.Listen); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status, old := s.remote.apply(in)
	writeJSON(w, http.StatusOK, status)
	if old == nil {
		return
	}
	if requestClient(r).Remote {
		// This reply travels over the listener being replaced.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		go func() { time.Sleep(300 * time.Millisecond); old.close() }()
		return
	}
	old.close()
}

func (s *server) remotePairing(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAvailable(w) {
		return
	}
	status := s.remote.status()
	if !status.Running {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "远程访问未开启，请先开启远程访问"})
		return
	}
	hostPort := ""
	if requestClient(r).Remote {
		hostPort = r.Host
	}
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		hostPort = serverName() + ":" + remotePort(status.Listen)
		if len(status.Addresses) > 0 {
			hostPort = status.Addresses[0]
		}
	}
	code, expires := s.remote.pairing.issue()
	compact := strings.ReplaceAll(code, "-", "")
	uri := fmt.Sprintf("kiko-pair://%s?fp=%s&code=%s&name=%s", hostPort, status.Fingerprint, compact, url.QueryEscape(serverName()))
	writeJSON(w, http.StatusOK, map[string]string{"code": code, "expiresAt": expires.Format(time.RFC3339), "fingerprint": status.Fingerprint, "uri": uri})
}

func remotePort(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil {
		return port
	}
	return "9443"
}

func (s *server) remoteDevices(w http.ResponseWriter, r *http.Request) {
	if s.remoteAvailable(w) {
		writeJSON(w, http.StatusOK, map[string]any{"devices": s.remote.devices.list()})
	}
}

func (s *server) revokeRemoteDevice(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAvailable(w) {
		return
	}
	device, ok := s.remote.devices.revoke(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "设备不存在"})
		return
	}
	if s.logs != nil {
		s.logs.add(fmt.Sprintf("[kiko] 已吊销远程设备「%s」", device.Name))
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeLocalAPIAddress records the loopback address for `kiko-server pair`.
func (s *server) writeLocalAPIAddress(address string) {
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime", "local-api.json"), map[string]string{"listen": address})
}

// defaultDataDir is <user config dir>/kiko-server. With migrate, a data
// directory left from before the rename to Kiko (sbcfg-server) moves there
// once; without, it is used where it is, so `kiko-server pair` finds a
// server that has not migrated yet.
func defaultDataDir(migrate bool) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return resolveDataDir(base, migrate)
}

// legacyDataDirName is the data directory's name before the rename to Kiko.
const legacyDataDirName = "sbcfg-server"

func resolveDataDir(base string, migrate bool) (string, error) {
	current := filepath.Join(base, "kiko-server")
	if _, err := os.Lstat(current); !errors.Is(err, os.ErrNotExist) {
		return current, nil
	}
	legacy := filepath.Join(base, legacyDataDirName)
	if info, err := os.Lstat(legacy); err != nil || !info.IsDir() {
		return current, nil
	}
	if !migrate {
		return legacy, nil
	}
	if err := os.Rename(legacy, current); err != nil {
		// Keep using the old directory rather than starting empty; the move
		// is retried at the next start.
		log.Printf("[kiko] moving %s to %s failed, using it where it is: %v", legacy, current, err)
		return legacy, nil
	}
	return current, nil
}

// runPairCommand asks the running server for a one-time pairing code.
func runPairCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	data := flags.String("data-dir", "", "数据目录（与服务端一致）")
	listen := flags.String("listen", "", "服务端回环监听地址，默认读取数据目录中的记录")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *data == "" {
		dir, err := defaultDataDir(false)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*data = dir
	}
	address := *listen
	if address == "" {
		var recorded struct {
			Listen string `json:"listen"`
		}
		_ = readJSON(filepath.Join(*data, "runtime", "local-api.json"), &recorded)
		address = recorded.Listen
	}
	if address == "" {
		address = defaultLocalListen
	}
	if err := validateLoopbackListen(address); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/api/v1/remote/pairing", bytes.NewReader([]byte("{}")))
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		fmt.Fprintf(stderr, "无法连接本机 kiko-server（%s）：%v\n", address, err)
		return 1
	}
	defer response.Body.Close()
	var out struct {
		Code        string `json:"code"`
		ExpiresAt   string `json:"expiresAt"`
		Fingerprint string `json:"fingerprint"`
		URI         string `json:"uri"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&out)
	if response.StatusCode != http.StatusOK {
		message := out.Error
		if message == "" {
			message = response.Status
		}
		fmt.Fprintln(stderr, "生成配对码失败："+message)
		if response.StatusCode == http.StatusConflict {
			fmt.Fprintln(stderr, "请使用 --remote-listen 启动服务端，或在客户端设置中开启远程访问。")
		}
		return 1
	}
	short := out.Fingerprint
	if len(short) > 8 {
		short = short[:8]
	}
	expires := out.ExpiresAt
	if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		expires = t.Local().Format("15:04:05")
	}
	fmt.Fprintf(stdout, "配对字符串：%s\n配对码：%s（%s 前有效，仅可使用一次）\n证书指纹：%s\n\n在客户端的「设置 → 服务器 → 添加服务器」中粘贴配对字符串，或填写服务器地址与配对码，并核对证书指纹。\n", out.URI, out.Code, expires, short)
	return 0
}
