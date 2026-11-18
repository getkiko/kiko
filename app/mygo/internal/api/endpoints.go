package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// Endpoint runtime: the live state of Tailscale, OpenConnect and OpenVPN
// endpoints, Taildrop and USB/IP, from GET /endpoints/status and the
// "tailscale", "openconnect", "openvpn", "taildrop", "taildropSend",
// "usbip" and "usbipShare" events.

// TailscalePeer is a device of a tailnet.
type TailscalePeer struct {
	HostName        string   `json:"hostName,omitempty"`
	DNSName         string   `json:"dnsName,omitempty"`
	OS              string   `json:"os,omitempty"`
	TailscaleIPs    []string `json:"tailscaleIPs,omitempty"`
	Online          bool     `json:"online,omitempty"`
	ExitNode        bool     `json:"exitNode,omitempty"`
	ExitNodeOption  bool     `json:"exitNodeOption,omitempty"`
	Active          bool     `json:"active,omitempty"`
	RxBytes         Int64    `json:"rxBytes,omitempty"`
	TxBytes         Int64    `json:"txBytes,omitempty"`
	KeyExpiry       string   `json:"keyExpiry,omitempty"`
	StableID        string   `json:"stableID,omitempty"`
	Expired         bool     `json:"expired,omitempty"`
	SSHHostKeys     []string `json:"sshHostKeys,omitempty"`
	ShareeNode      bool     `json:"shareeNode,omitempty"`
	LastSeen        string   `json:"lastSeen,omitempty"`
	CanReceiveFiles bool     `json:"canReceiveFiles,omitempty"`
	// CurAddr is the current direct UDP endpoint; empty when the peer is
	// only reachable through DERP.
	CurAddr string `json:"curAddr,omitempty"`
	// Relay is the home DERP region code.
	Relay string `json:"relay,omitempty"`
	// UDPPath is the udp_path decision for CurAddr: detour, direct, relay,
	// or empty when udp_path is not active.
	UDPPath string `json:"udpPath,omitempty"`
}

// TailscaleUserGroup is the devices of one tailnet user.
type TailscaleUserGroup struct {
	UserID        string          `json:"userID,omitempty"`
	LoginName     string          `json:"loginName,omitempty"`
	DisplayName   string          `json:"displayName,omitempty"`
	ProfilePicURL string          `json:"profilePicURL,omitempty"`
	Peers         []TailscalePeer `json:"peers,omitempty"`
}

// TailscaleStatus is a Tailscale endpoint's state.
type TailscaleStatus struct {
	Health             []string             `json:"health,omitempty"`
	ObservedAt         string               `json:"observedAt,omitempty"`
	EndpointTag        string               `json:"endpointTag"`
	BackendState       string               `json:"backendState,omitempty"`
	StateText          string               `json:"stateText,omitempty"`
	AuthURL            string               `json:"authURL,omitempty"`
	NetworkName        string               `json:"networkName,omitempty"`
	MagicDNSSuffix     string               `json:"magicDNSSuffix,omitempty"`
	Self               *TailscalePeer       `json:"self,omitempty"`
	UserGroups         []TailscaleUserGroup `json:"userGroups,omitempty"`
	ExitNode           *TailscalePeer       `json:"exitNode,omitempty"`
	KeyAuth            bool                 `json:"keyAuth,omitempty"`
	CanShareFiles      bool                 `json:"canShareFiles,omitempty"`
	WaitingFileCount   int                  `json:"waitingFileCount,omitempty"`
	ReceivingFileCount int                  `json:"receivingFileCount,omitempty"`
	UnreadFileCount    int                  `json:"unreadFileCount,omitempty"`
	CertDomains        []string             `json:"certDomains,omitempty"`
}

// TailscaleUpdate is the "tailscale" event and snapshot.
type TailscaleUpdate struct {
	Endpoints []TailscaleStatus `json:"endpoints,omitempty"`
}

// TailscalePingResult is one reply of POST /tailscale/ping.
type TailscalePingResult struct {
	LatencyMs      float64 `json:"latencyMs,omitempty"`
	IsDirect       bool    `json:"isDirect,omitempty"`
	Endpoint       string  `json:"endpoint,omitempty"`
	DERPRegionID   int     `json:"derpRegionID,omitempty"`
	DERPRegionCode string  `json:"derpRegionCode,omitempty"`
	Error          string  `json:"error,omitempty"`
	PeerRelay      string  `json:"peerRelay,omitempty"`
}

// TunnelInfo is an established VPN tunnel.
type TunnelInfo struct {
	Server         string   `json:"server,omitempty"`
	Flavor         string   `json:"flavor,omitempty"`
	Transport      string   `json:"transport,omitempty"`
	Network        string   `json:"network,omitempty"`
	IPv4           []string `json:"ipv4,omitempty"`
	IPv6           []string `json:"ipv6,omitempty"`
	DNS            []string `json:"dns,omitempty"`
	MTU            int      `json:"mtu,omitempty"`
	ConnectedSince string   `json:"connectedSince,omitempty"`
	Cipher         string   `json:"cipher,omitempty"`
}

// OpenConnectOption is a choice of an OpenConnect form field.
type OpenConnectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OpenConnectField is a field of an OpenConnect authentication form.
type OpenConnectField struct {
	SubmissionKey string              `json:"submissionKey"`
	Name          string              `json:"name,omitempty"`
	Label         string              `json:"label,omitempty"`
	Kind          string              `json:"kind,omitempty"`
	Value         string              `json:"value,omitempty"`
	Options       []OpenConnectOption `json:"options,omitempty"`
}

// OpenConnectChallenge asks for credentials.
type OpenConnectChallenge struct {
	ID      string `json:"id"`
	Banner  string `json:"banner,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Form    *struct {
		Fields []OpenConnectField `json:"fields,omitempty"`
	} `json:"form,omitempty"`
	Browser *struct {
		URL string `json:"url,omitempty"`
	} `json:"browser,omitempty"`
}

// OpenConnectStatus is an OpenConnect endpoint's state.
type OpenConnectStatus struct {
	EndpointTag   string                `json:"endpointTag"`
	State         string                `json:"state,omitempty"`
	StateText     string                `json:"stateText,omitempty"`
	AuthChallenge *OpenConnectChallenge `json:"authChallenge,omitempty"`
	Error         string                `json:"error,omitempty"`
	TunnelInfo    *TunnelInfo           `json:"tunnelInfo,omitempty"`
}

// OpenConnectUpdate is the "openconnect" event and snapshot.
type OpenConnectUpdate struct {
	Endpoints []OpenConnectStatus `json:"endpoints,omitempty"`
}

// OpenVPNChallenge asks for credentials or a one-time secret.
type OpenVPNChallenge struct {
	ID            string `json:"id"`
	Kind          string `json:"kind,omitempty"`
	Username      string `json:"username,omitempty"`
	Message       string `json:"message,omitempty"`
	URL           string `json:"url,omitempty"`
	SecretMessage string `json:"secretMessage,omitempty"`
	Echo          bool   `json:"echo,omitempty"`
	PreviousError string `json:"previousError,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
}

// OpenVPNStatus is an OpenVPN client endpoint's state.
type OpenVPNStatus struct {
	EndpointTag string            `json:"endpointTag"`
	State       string            `json:"state,omitempty"`
	StateText   string            `json:"stateText,omitempty"`
	Challenge   *OpenVPNChallenge `json:"challenge,omitempty"`
	Error       string            `json:"error,omitempty"`
	TunnelInfo  *TunnelInfo       `json:"tunnelInfo,omitempty"`
}

// OpenVPNUpdate is the "openvpn" event and snapshot.
type OpenVPNUpdate struct {
	Endpoints []OpenVPNStatus `json:"endpoints,omitempty"`
}

// TaildropFile is a received file waiting in the inbox.
type TaildropFile struct {
	Name       string `json:"name"`
	Size       Int64  `json:"size,omitempty"`
	SenderName string `json:"senderName,omitempty"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
}

// TaildropReceivingFile is a file being received.
type TaildropReceivingFile struct {
	Name          string `json:"name"`
	Size          Int64  `json:"size,omitempty"`
	ReceivedBytes Int64  `json:"receivedBytes,omitempty"`
	SenderID      string `json:"senderID,omitempty"`
	SenderName    string `json:"senderName,omitempty"`
}

// TaildropInbox is an endpoint's Taildrop inbox, the "taildrop" event.
type TaildropInbox struct {
	EndpointTag string                  `json:"endpointTag"`
	Files       []TaildropFile          `json:"files,omitempty"`
	Receiving   []TaildropReceivingFile `json:"receiving,omitempty"`
}

// TaildropSendFile is a file of a send.
type TaildropSendFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// TaildropSendTask is a running or finished Taildrop send, the
// "taildropSend" event.
type TaildropSendTask struct {
	ID          string             `json:"id"`
	EndpointTag string             `json:"endpointTag"`
	Peer        string             `json:"peer"`
	Files       []TaildropSendFile `json:"files"`
	SentBytes   int64              `json:"sentBytes"`
	TotalBytes  int64              `json:"totalBytes"`
	// State is uploading, sending, completed, failed or canceled.
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	Revision  uint64 `json:"revision"`
}

// USBDescriptor describes an exported USB device.
type USBDescriptor struct {
	VendorID  int    `json:"vendorId,omitempty"`
	ProductID int    `json:"productId,omitempty"`
	Product   string `json:"product,omitempty"`
	Serial    string `json:"serial,omitempty"`
}

// USBSharedDevice is a device a USB/IP server exports.
type USBSharedDevice struct {
	Descriptor *USBDescriptor `json:"descriptor,omitempty"`
	BusID      string         `json:"busId,omitempty"`
	StableID   string         `json:"stableId,omitempty"`
	Backend    string         `json:"backend,omitempty"`
	State      string         `json:"state,omitempty"`
}

// USBIPServerStatus is a USB/IP server's exported devices.
type USBIPServerStatus struct {
	ServerTag string            `json:"serverTag"`
	Devices   []USBSharedDevice `json:"devices,omitempty"`
}

// USBIPUpdate is the "usbip" event and snapshot.
type USBIPUpdate struct {
	Servers []USBIPServerStatus `json:"servers,omitempty"`
}

// USBID is a vendor or product id: the API writes numbers, the CLI may
// write hexadecimal strings. Numeric tells which it was.
type USBID struct {
	Text    string
	Numeric bool
}

// UnmarshalJSON reads a number or a string.
func (v *USBID) UnmarshalJSON(data []byte) error {
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil && len(data) > 0 && data[0] != '"' {
		*v = USBID{Text: n.String(), Numeric: true}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		*v = USBID{}
		return nil
	}
	*v = USBID{Text: s}
	return nil
}

// MarshalJSON writes the id as it was read.
func (v USBID) MarshalJSON() ([]byte, error) {
	if v.Numeric {
		return []byte(v.Text), nil
	}
	return json.Marshal(v.Text)
}

// USBLocalDevice is a USB device of the server's host.
type USBLocalDevice struct {
	BusID     string `json:"busId"`
	VendorID  USBID  `json:"vendorId,omitzero"`
	ProductID USBID  `json:"productId,omitzero"`
	Serial    string `json:"serial,omitempty"`
	Product   string `json:"product,omitempty"`
}

// USBIPKernelModule is a kernel module's state.
type USBIPKernelModule struct {
	Loaded    bool `json:"loaded"`
	Available bool `json:"available"`
}

// USBIPPreflight is the host's readiness for USB/IP.
type USBIPPreflight struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	User            string `json:"user,omitempty"`
	Elevated        bool   `json:"elevated"`
	ServerSupported bool   `json:"serverSupported"`
	SingBoxUSBIP    bool   `json:"singBoxUSBIP"`
	Kernel          *struct {
		USBIPHost USBIPKernelModule `json:"usbipHost"`
		VHCIHCD   USBIPKernelModule `json:"vhciHcd"`
	} `json:"kernel,omitempty"`
}

// USBIPShareStatus is the managed `kiko-core api usbip share` process.
type USBIPShareStatus struct {
	Running    bool   `json:"running"`
	ServiceTag string `json:"serviceTag,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Error      string `json:"error,omitempty"`
}

// EndpointsSnapshot is GET /endpoints/status: the latest of each update,
// null before the core sent one.
type EndpointsSnapshot struct {
	Tailscale   *TailscaleUpdate         `json:"tailscale"`
	OpenConnect *OpenConnectUpdate       `json:"openconnect"`
	OpenVPN     *OpenVPNUpdate           `json:"openvpn"`
	USBIP       *USBIPUpdate             `json:"usbip"`
	Taildrop    map[string]TaildropInbox `json:"taildrop,omitempty"`
}

// EndpointsStatus is GET /endpoints/status.
func (c *Client) EndpointsStatus(ctx context.Context) (EndpointsSnapshot, error) {
	var out EndpointsSnapshot
	return out, c.Get(ctx, "/endpoints/status", &out)
}

type endpointTag struct {
	EndpointTag string `json:"endpointTag"`
}

// TailscaleExitNode sets the endpoint's exit node; "" clears it.
func (c *Client) TailscaleExitNode(ctx context.Context, tag, stableID string) error {
	return c.Send(ctx, http.MethodPost, "/tailscale/exit-node", map[string]string{"endpointTag": tag, "stableID": stableID}, nil)
}

// TailscaleLogout signs the endpoint out of its tailnet.
func (c *Client) TailscaleLogout(ctx context.Context, tag string) error {
	return c.Send(ctx, http.MethodPost, "/tailscale/logout", endpointTag{tag}, nil)
}

// TailscalePing pings a peer continuously until ctx ends, passing each
// reply, failed ones included, to result.
func (c *Client) TailscalePing(ctx context.Context, tag, peerIP string, result func(TailscalePingResult)) error {
	body := map[string]any{"endpointTag": tag, "peerIP": peerIP, "continuous": true}
	return c.NDJSON(ctx, http.MethodPost, "/tailscale/ping", body, true, func(raw json.RawMessage) error {
		var r TailscalePingResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		result(r)
		return nil
	})
}

// TailscaleDiagnostics is GET /tailscale/diagnostics: how the endpoint
// reaches the network (`tailscale netcheck`) and each peer.
type TailscaleDiagnostics struct {
	Netcheck *TailscaleNetcheck        `json:"netcheck"`
	Peers    []TailscalePeerDiagnostic `json:"peers"`
}

// TailscaleNetcheck is the latest network check. Tri-state fields are
// "true", "false" or "" when not checked.
type TailscaleNetcheck struct {
	CheckedAt             Int64                 `json:"checkedAt"`
	UDP                   bool                  `json:"udp"`
	IPv4                  bool                  `json:"ipv4"`
	IPv6                  bool                  `json:"ipv6"`
	IPv4CanSend           bool                  `json:"ipv4CanSend"`
	IPv6CanSend           bool                  `json:"ipv6CanSend"`
	OSHasIPv6             bool                  `json:"osHasIPv6"`
	GlobalV4              string                `json:"globalV4"`
	GlobalV6              string                `json:"globalV6"`
	MappingVariesByDestIP string                `json:"mappingVariesByDestIP"`
	UPnP                  string                `json:"upnp"`
	PMP                   string                `json:"pmp"`
	PCP                   string                `json:"pcp"`
	CaptivePortal         string                `json:"captivePortal"`
	PreferredDERP         int                   `json:"preferredDERP"`
	Regions               []TailscaleDERPRegion `json:"regions"`
	SelfEndpoints         []string              `json:"selfEndpoints"`
	// STUNPath is the udp_path network checks take, empty without one.
	STUNPath string `json:"stunPath"`
}

// TailscaleDERPRegion is a DERP region; LatencyMs is 0 without a reply.
type TailscaleDERPRegion struct {
	ID        int     `json:"id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	LatencyMs float64 `json:"latencyMs"`
}

// TailscalePeerDiagnostic is a peer's candidates, handshake and recent
// path changes (newest first).
type TailscalePeerDiagnostic struct {
	StableID      string                  `json:"stableID"`
	Endpoints     []TailscalePeerEndpoint `json:"endpoints"`
	LastHandshake Int64                   `json:"lastHandshake"`
	PeerRelay     string                  `json:"peerRelay"`
	Changes       []TailscalePathChange   `json:"changes"`
}

// TailscalePeerEndpoint is a candidate address and its udp_path.
type TailscalePeerEndpoint struct {
	Address string `json:"address"`
	Path    string `json:"path"`
}

// TailscalePathChange is a change of a peer's path.
type TailscalePathChange struct {
	At   Int64  `json:"at"`
	What string `json:"what"`
	From string `json:"from"`
	To   string `json:"to"`
}

// TailscaleDiagnostics reads the endpoint's diagnostics; refresh runs a
// new network check first.
func (c *Client) TailscaleDiagnostics(ctx context.Context, tag string, refresh bool) (TailscaleDiagnostics, error) {
	query := url.Values{"endpointTag": {tag}}
	if refresh {
		query.Set("refresh", "1")
	}
	var out TailscaleDiagnostics
	return out, c.Get(ctx, "/tailscale/diagnostics?"+query.Encode(), &out)
}

// OpenConnectAuth answers an authentication form.
func (c *Client) OpenConnectAuth(ctx context.Context, tag, challengeID string, values map[string]string) error {
	body := map[string]any{"endpointTag": tag, "challengeID": challengeID, "form": map[string]any{"values": values}}
	return c.Send(ctx, http.MethodPost, "/openconnect/auth", body, nil)
}

// OpenConnectCancel cancels an authentication.
func (c *Client) OpenConnectCancel(ctx context.Context, tag, challengeID string) error {
	return c.Send(ctx, http.MethodPost, "/openconnect/cancel", map[string]string{"endpointTag": tag, "challengeID": challengeID}, nil)
}

// OpenVPNAnswer is the reply to an OpenVPN challenge.
type OpenVPNAnswer struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Secret   string `json:"secret"`
}

// OpenVPNChallengeAnswer answers an OpenVPN challenge.
func (c *Client) OpenVPNChallengeAnswer(ctx context.Context, tag, challengeID string, answer OpenVPNAnswer) error {
	body := map[string]string{"endpointTag": tag, "challengeID": challengeID,
		"username": answer.Username, "password": answer.Password, "secret": answer.Secret}
	return c.Send(ctx, http.MethodPost, "/openvpn/challenge", body, nil)
}

// OpenVPNCancel cancels an OpenVPN challenge.
func (c *Client) OpenVPNCancel(ctx context.Context, tag, challengeID string) error {
	return c.Send(ctx, http.MethodPost, "/openvpn/cancel", map[string]string{"endpointTag": tag, "challengeID": challengeID}, nil)
}

// TaildropRead marks the endpoint's received files read.
func (c *Client) TaildropRead(ctx context.Context, tag string) error {
	return c.Send(ctx, http.MethodPost, "/taildrop/read", endpointTag{tag}, nil)
}

// TaildropDelete deletes a received file.
func (c *Client) TaildropDelete(ctx context.Context, tag, name string) error {
	return c.Send(ctx, http.MethodDelete, "/taildrop/file", map[string]string{"endpointTag": tag, "name": name}, nil)
}

// TaildropCancel cancels a file being received.
func (c *Client) TaildropCancel(ctx context.Context, tag, senderID, name string) error {
	return c.Send(ctx, http.MethodPost, "/taildrop/cancel", map[string]string{"endpointTag": tag, "senderID": senderID, "name": name}, nil)
}

// TaildropFilePath is the path of GET /taildrop/file for a received file.
func TaildropFilePath(tag, name string) string {
	return "/taildrop/file" + Query("endpointTag", tag, "name", name)
}

// TaildropDownload streams a received file to w, reporting the bytes
// written so far to progress (which may be nil). It has no overall
// timeout: files may be large.
func (c *Client) TaildropDownload(ctx context.Context, tag, name string, w io.Writer, progress func(int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(TaildropFilePath(tag, name)), nil)
	if err != nil {
		return err
	}
	resp, err := c.stream().Do(req)
	err = transportError(err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFrom(resp)
	}
	buf := make([]byte, 64<<10)
	var total int64
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			total += int64(n)
			if progress != nil {
				progress(total)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TaildropDirectory is the endpoint's receive directory.
func (c *Client) TaildropDirectory(ctx context.Context, tag string) (string, error) {
	var out struct {
		Path string `json:"path"`
	}
	err := c.Get(ctx, "/taildrop/directory"+Query("endpointTag", tag), &out)
	return out.Path, err
}

// TaildropOpenDirectory reveals the receive directory on the server's
// host.
func (c *Client) TaildropOpenDirectory(ctx context.Context, tag string) error {
	return c.Send(ctx, http.MethodPost, "/taildrop/directory/open", endpointTag{tag}, nil)
}

// TaildropSends lists the sends the server keeps.
func (c *Client) TaildropSends(ctx context.Context) ([]TaildropSendTask, error) {
	var out []TaildropSendTask
	return out, c.Get(ctx, "/taildrop/sends", &out)
}

// TaildropSendCancel cancels a send.
func (c *Client) TaildropSendCancel(ctx context.Context, id string) error {
	return c.Send(ctx, http.MethodPost, "/taildrop/send/cancel", map[string]string{"id": id}, nil)
}

// TaildropUpload is a file to send: its name, size and contents.
type TaildropUpload struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}

// UploadBytes is an upload of data in memory.
func UploadBytes(name string, data []byte) TaildropUpload {
	return TaildropUpload{Name: name, Size: int64(len(data)), Open: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}}
}

// UploadFile is an upload of a file on this machine, read as it is sent.
func UploadFile(path string) (TaildropUpload, error) {
	info, err := os.Stat(path)
	if err != nil {
		return TaildropUpload{}, err
	}
	if info.IsDir() {
		return TaildropUpload{}, fmt.Errorf("%s 是文件夹", filepath.Base(path))
	}
	return TaildropUpload{Name: filepath.Base(path), Size: info.Size(), Open: func() (io.ReadCloser, error) {
		return os.Open(path)
	}}, nil
}

// TaildropSend uploads files for the server to send to peer
// (multipart: a manifest, then each file), returning the task at once.
func (c *Client) TaildropSend(ctx context.Context, tag, peer string, files []TaildropUpload) (TaildropSendTask, error) {
	var task TaildropSendTask
	manifest := make([]TaildropSendFile, len(files))
	for i, f := range files {
		manifest[i] = TaildropSendFile{Name: f.Name, Size: f.Size}
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		return task, err
	}
	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := form.WriteField("manifest", string(rawManifest)); err != nil {
				return err
			}
			for _, f := range files {
				part, err := form.CreateFormFile("file", f.Name)
				if err != nil {
					return err
				}
				r, err := f.Open()
				if err != nil {
					return err
				}
				_, err = io.Copy(part, r)
				r.Close()
				if err != nil {
					return err
				}
			}
			return form.Close()
		}()
		pw.CloseWithError(err)
	}()
	path := "/taildrop/send" + Query("async", "true", "endpointTag", tag, "peer", peer)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(path), pr)
	if err != nil {
		pr.Close()
		return task, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.stream().Do(req)
	err = transportError(err)
	if err != nil {
		pr.Close()
		return task, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return task, errorFrom(resp)
	}
	return task, json.NewDecoder(resp.Body).Decode(&task)
}

// USBDevices lists the server host's USB devices.
func (c *Client) USBDevices(ctx context.Context) ([]USBLocalDevice, error) {
	var out struct {
		Devices []USBLocalDevice `json:"devices"`
	}
	return out.Devices, c.Get(ctx, "/usb/devices", &out)
}

// USBIPPreflightCheck is GET /usbip/preflight.
func (c *Client) USBIPPreflightCheck(ctx context.Context) (USBIPPreflight, error) {
	var out USBIPPreflight
	return out, c.Get(ctx, "/usbip/preflight", &out)
}

// USBIPShare is GET /usbip/share.
func (c *Client) USBIPShare(ctx context.Context) (USBIPShareStatus, error) {
	var out USBIPShareStatus
	return out, c.Get(ctx, "/usbip/share", &out)
}

// StartUSBIPShare exports devices (bus ids, all when empty) for a dynamic
// USB/IP server.
func (c *Client) StartUSBIPShare(ctx context.Context, serviceTag string, devices []string) (USBIPShareStatus, error) {
	var out USBIPShareStatus
	if devices == nil {
		devices = []string{}
	}
	body := map[string]any{"serviceTag": serviceTag, "devices": devices}
	return out, c.Send(ctx, http.MethodPost, "/usbip/share", body, &out)
}

// StopUSBIPShare stops the export.
func (c *Client) StopUSBIPShare(ctx context.Context) (USBIPShareStatus, error) {
	var out USBIPShareStatus
	return out, c.Send(ctx, http.MethodDelete, "/usbip/share", struct{}{}, &out)
}

// TailscaleSSHPath is the WebSocket of Tailscale SSH sessions.
const TailscaleSSHPath = "/tailscale/ssh"
