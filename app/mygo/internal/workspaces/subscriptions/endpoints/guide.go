package endpoints

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// The USB/IP connection guide's logic: the paths and
// what each side does, the fields a path sets, the checklist, Tailscale
// peers and the connection details to hand to a client.

// guideLevel is a checklist item's severity.
type guideLevel string

const (
	levelOK    guideLevel = "ok"
	levelInfo  guideLevel = "info"
	levelWarn  guideLevel = "warn"
	levelError guideLevel = "error"
)

var levelOrder = map[guideLevel]int{levelError: 0, levelWarn: 1, levelInfo: 2, levelOK: 3}

// guideItem is an item of the checklist.
type guideItem struct {
	ID, Title, Detail, Command string
	Level                      guideLevel
}

// guideContext is what the checklist knows of the running host.
type guideContext struct {
	os            string
	preflight     *api.USBIPPreflight
	tailscale     map[string]*api.TailscaleStatus
	serverRuntime bool
}

// usbIPPath is a way the two machines reach each other.
type usbIPPath struct {
	ID, Title, Short, Summary, Scenario string
	Pros, Cons, Server, Client          []string
}

const (
	pathLAN       = "lan"
	pathTailscale = "tailscale"
	pathVPN       = "system-vpn"
)

var usbIPPaths = []usbIPPath{
	{
		ID: pathLAN, Title: "同一局域网", Short: "局域网",
		Summary:  "两台设备在同一个路由器或交换机下，直接用局域网 IP 连接。",
		Scenario: "家里、办公室、机房内部。",
		Pros:     []string{"配置最简单，不需要任何账号", "延迟低、速度快"},
		Cons: []string{
			"只能在同一个网络内使用",
			"USB/IP 没有认证和加密，局域网里的其他设备也能访问 Server",
			"需要在防火墙放行 TCP 3240 端口",
		},
		Server: []string{
			"创建 USB/IP Server，监听地址用 0.0.0.0，端口 3240",
			"放行防火墙 TCP 3240",
			"记下这台机器的局域网 IP，例如 192.168.1.20",
		},
		Client: []string{
			"创建 USB/IP Client",
			"服务器地址填 Server 的局域网 IP，端口 3240",
			"不需要设置前置代理 detour",
		},
	},
	{
		ID: pathTailscale, Title: "Tailscale（核心内置）", Short: "Tailscale",
		Summary:  "没有公网 IP 也能跨网络使用。两台机器各自在 Kiko 里加一个 Tailscale 端点，Client 通过它连到 Server 的 Tailscale 地址（100.x.x.x）。",
		Scenario: "Server 在家里或公司内网、没有公网 IP，Client 在别的网络。",
		Pros: []string{
			"不需要公网 IP，也不需要端口映射",
			"不必安装系统级 Tailscale，只用 Kiko 就够",
			"Server 只监听 127.0.0.1，局域网和公网都无法直接访问，更安全",
		},
		Cons: []string{
			"两台机器都需要一个已登录的 Tailscale 端点（首次登录账号或填 auth_key）",
			"走 Tailscale 中继时延迟较高，可能影响 USB 设备的速度",
		},
		Server: []string{
			"创建 Tailscale 端点并登录（端点页卡片 → 状态 → 打开登录页面）",
			"创建 USB/IP Server，监听地址用 127.0.0.1（Tailscale 会把发到本机 tailnet 地址的连接转给它）",
			"Kiko 生成配置时会自动补一条路由规则，让来自 Tailscale 的连接直连",
			"记下本机的 Tailscale 地址（100.x.x.x）或 MagicDNS 名称",
		},
		Client: []string{
			"创建 Tailscale 端点并登录（登录同一个 tailnet）",
			"创建 USB/IP Client，「前置代理 detour」选这个 Tailscale 端点",
			"服务器地址填 Server 的 Tailscale 地址（100.x.x.x）或 MagicDNS 名称",
		},
	},
	{
		ID: pathVPN, Title: "系统级 VPN 或公网地址", Short: "VPN/公网",
		Summary:  "两台机器已经能互相访问：系统里装了 Tailscale、ZeroTier、WireGuard，或者 Server 有公网 IP 并做了端口映射。",
		Scenario: "已经在用系统级 VPN，或 Server 有公网地址。",
		Pros:     []string{"不依赖核心内置的 Tailscale", "组网一次，其他应用也能用"},
		Cons: []string{
			"USB/IP 没有认证和加密，不要直接暴露到公网",
			"需要自己保证两端网络互通、防火墙放行",
			"需要单独安装并登录系统级 VPN",
		},
		Server: []string{
			"创建 USB/IP Server，监听地址用 0.0.0.0，端口 3240",
			"让防火墙只允许 VPN 网段访问 3240",
			"记下这台机器在 VPN 里的地址（或公网地址）",
		},
		Client: []string{
			"创建 USB/IP Client，不需要设置前置代理 detour",
			"服务器地址填 Server 的 VPN 地址或公网地址",
		},
	},
}

// pathInfo returns a path by id.
func pathInfo(id string) usbIPPath {
	for _, p := range usbIPPaths {
		if p.ID == id {
			return p
		}
	}
	return usbIPPaths[0]
}

func bare(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")
}

func text(v any) string { return jsv.Text(v) }

// tailscaleEndpoints are the document's Tailscale endpoints.
func tailscaleEndpoints(doc *model.Doc, enabledOnly bool) []*model.Endpoint {
	var out []*model.Endpoint
	for _, e := range doc.Endpoints {
		if e.Type == "tailscale" && (!enabledOnly || e.Enabled) {
			out = append(out, e)
		}
	}
	return out
}

func isPathID(v any) bool {
	s, ok := v.(string)
	return ok && (s == pathLAN || s == pathTailscale || s == pathVPN)
}

// detectPath is a service's path: the guide's mark, else inferred.
func detectPath(doc *model.Doc, service *model.Service) string {
	if marked := service.Fields["guide_path"]; isPathID(marked) {
		return marked.(string)
	}
	if service.Type == "usbip-server" {
		if model.IsLoopbackHost(text(service.Fields["listen"])) {
			return pathTailscale
		}
		return pathLAN
	}
	detour := text(service.Fields["detour"])
	if detour != "" && slices.ContainsFunc(tailscaleEndpoints(doc, false), func(e *model.Endpoint) bool { return e.ID == detour }) {
		return pathTailscale
	}
	server := text(service.Fields["server"])
	if server != "" && model.IsTailnetHost(server) {
		return pathVPN
	}
	if server != "" && !model.IsPrivateLanHost(server) && !model.IsLoopbackHost(server) {
		return pathVPN
	}
	return pathLAN
}

// applyPath is the fields a path sets on a service; tailscaleEndpointID
// names the endpoint a client dials through, the first enabled one when
// empty.
func applyPath(doc *model.Doc, serviceType, path, tailscaleEndpointID string) map[string]any {
	patch := map[string]any{"guide_path": path}
	if serviceType == "usbip-server" {
		if path == pathTailscale {
			patch["listen"] = "127.0.0.1"
		} else {
			patch["listen"] = "0.0.0.0"
		}
		return patch
	}
	detour := ""
	if path == pathTailscale {
		detour = tailscaleEndpointID
		if detour == "" {
			if enabled := tailscaleEndpoints(doc, true); len(enabled) > 0 {
				detour = enabled[0].ID
			}
		}
	}
	patch["detour"] = detour
	return patch
}

// tailnetPeer is a device of the tailnet a client may connect to.
type tailnetPeer struct {
	Name, Address, OS, DNSName string
	Online                     bool
}

func dnsTrim(name string) string { return strings.TrimSuffix(strings.TrimSpace(name), ".") }

func dnsLabel(name string) string {
	label, _, _ := strings.Cut(dnsTrim(name), ".")
	return label
}

func preferV4(ips []string) string {
	for _, ip := range ips {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func peerToTailnet(p api.TailscalePeer) (tailnetPeer, bool) {
	address := preferV4(p.TailscaleIPs)
	if address == "" {
		return tailnetPeer{}, false
	}
	name := dnsLabel(p.DNSName)
	if name == "" {
		name = dnsTrim(p.HostName)
	}
	if name == "" {
		name = address
	}
	return tailnetPeer{Name: name, Address: address, Online: p.Online, OS: p.OS, DNSName: dnsTrim(p.DNSName)}, true
}

// tailnetPeers are the other devices, online first.
func tailnetPeers(status *api.TailscaleStatus) []tailnetPeer {
	if status == nil {
		return nil
	}
	var out []tailnetPeer
	for _, g := range status.UserGroups {
		for _, p := range g.Peers {
			if peer, ok := peerToTailnet(p); ok {
				out = append(out, peer)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b tailnetPeer) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// tailnetSelfInfo is this machine in its tailnet.
type tailnetSelfInfo struct {
	Name, DNSName string
	IPs           []string
}

func tailnetSelf(status *api.TailscaleStatus) (tailnetSelfInfo, bool) {
	if status == nil || status.Self == nil || len(status.Self.TailscaleIPs) == 0 {
		return tailnetSelfInfo{}, false
	}
	self := status.Self
	name := dnsLabel(self.DNSName)
	if name == "" {
		name = dnsTrim(self.HostName)
	}
	if name == "" {
		name = self.TailscaleIPs[0]
	}
	return tailnetSelfInfo{Name: name, IPs: self.TailscaleIPs, DNSName: dnsTrim(self.DNSName)}, true
}

var (
	reRunning = regexp.MustCompile(`(?i)^running$`)
	reLogin   = regexp.MustCompile(`(?i)login`)
)

func tsRunning(s *api.TailscaleStatus) bool { return s != nil && reRunning.MatchString(s.BackendState) }
func tsNeedsLogin(s *api.TailscaleStatus) bool {
	return s != nil && reLogin.MatchString(s.BackendState)
}

// worstLevel is the checklist's worst problem: error, warn or none.
func worstLevel(items []guideItem) guideLevel {
	worst := guideLevel("")
	for _, item := range items {
		if item.Level == levelError {
			return levelError
		}
		if item.Level == levelWarn {
			worst = levelWarn
		}
	}
	return worst
}

// sortItems orders a checklist: errors, warnings, notes, passes.
func sortItems(items []guideItem) []guideItem {
	out := slices.Clone(items)
	slices.SortStableFunc(out, func(a, b guideItem) int { return levelOrder[a.Level] - levelOrder[b.Level] })
	return out
}

const (
	modprobeServer = "sudo modprobe usbip-host"
	modprobeClient = "sudo modprobe vhci-hcd"
)

var (
	reBadServer  = regexp.MustCompile(`[/\s]`)
	reScheme     = regexp.MustCompile(`^\w+://`)
	reHostPort   = regexp.MustCompile(`^[^:\[]+:\d+$`)
	platformName = func(p string) string { return schema.PlatformLabel[p] }
)

// analyzeService is a service's checklist.
func analyzeService(doc *model.Doc, service *model.Service, ctx guideContext) []guideItem {
	var items []guideItem
	add := func(level guideLevel, id, title string, rest ...string) {
		item := guideItem{ID: id, Level: level, Title: title}
		if len(rest) > 0 {
			item.Detail = rest[0]
		}
		if len(rest) > 1 {
			item.Command = rest[1]
		}
		items = append(items, item)
	}
	platform := doc.Platform
	if platform == "" {
		platform = model.PlatformLinux
	}
	isServer := service.Type == "usbip-server"
	role := "Client"
	if isServer {
		role = "Server"
	}
	path := detectPath(doc, service)
	fields := service.Fields
	enabledTS := tailscaleEndpoints(doc, true)
	preflight := ctx.preflight
	portKey := "server_port"
	if isServer {
		portKey = "listen_port"
	}
	port := text(fields[portKey])
	if port == "" {
		port = "3240"
	}

	// Platform and build.
	if !schema.ServiceAvailable(service.Type, platform) {
		add(levelError, "platform", platformName(platform)+" 不支持 USB/IP "+role, schema.ServiceUnavailableReason(service.Type, platform))
	} else {
		add(levelOK, "platform", platformName(platform)+" 支持 USB/IP "+role)
	}
	if preflight != nil && !preflight.SingBoxUSBIP {
		add(levelError, "build", "当前核心未编入 USB/IP",
			"需要带 with_usbip 标签构建的核心（随 Kiko 发布的 kiko-core 默认包含）。请更换核心后重试。")
	} else if preflight == nil {
		add(levelInfo, "build", "需要带 USB/IP 支持的核心",
			"核心需以 with_usbip 标签构建，随 Kiko 发布的 kiko-core 默认包含；macOS 还需要 CGO 构建。")
	}

	// Permissions and drivers.
	where := "运行核心的机器"
	if ctx.serverRuntime {
		where = "运行 Kiko 服务端的这台机器"
	}
	action := "挂载"
	if isServer {
		action = "导出"
	}
	if preflight != nil {
		switch {
		case preflight.Elevated:
			user := preflight.User
			if user == "" {
				user = "当前用户"
			}
			add(levelOK, "privilege", "已具备管理员权限（"+user+"）")
		case preflight.OS == "windows":
			add(levelWarn, "privilege", "当前不是管理员",
				action+"设备需要管理员权限，请以管理员身份运行 Kiko（start.cmd）。以上检查针对"+where+"。")
		default:
			what := "挂载远程设备"
			if isServer {
				what = "导出设备、绑定 usbip-host 驱动"
			}
			add(levelWarn, "privilege", "当前不是 root",
				what+"需要 root 权限。请用 sudo 运行 Kiko 服务端；若通过 systemd 安装，需要把服务改为 root 运行。以上检查针对"+where+"。")
		}
		if preflight.Kernel != nil {
			module, name, command := preflight.Kernel.VHCIHCD, "vhci-hcd", modprobeClient
			if isServer {
				module, name, command = preflight.Kernel.USBIPHost, "usbip-host", modprobeServer
			}
			switch {
			case module.Loaded:
				add(levelOK, "kernel", "内核模块 "+name+" 已加载")
			case module.Available:
				add(levelWarn, "kernel", "内核模块 "+name+" 尚未加载", "非 root 运行时核心无法自动加载，请先手动加载：", command)
			default:
				add(levelError, "kernel", "内核里没有 "+name+" 模块",
					"需要内核包含 USB/IP 支持（Debian/Ubuntu 可安装 linux-modules-extra 一类的包）。容器、WSL 和部分精简内核默认没有该模块。")
			}
		}
	} else if platform == "linux" {
		name, command := "vhci-hcd", modprobeClient
		if isServer {
			name, command = "usbip-host", modprobeServer
		}
		add(levelInfo, "kernel", "Linux 需要内核模块 "+name+" 和 root 权限", "核心以 root 运行时会自动加载；非 root 需要先手动加载：", command)
	} else if platform == "windows" {
		detail := "使用内置的 usbip-win2 虚拟控制器驱动挂载设备。"
		if isServer {
			detail = "使用内置的 VBoxUSB 驱动导出设备。"
		}
		add(levelInfo, "driver", "Windows 需要管理员权限，首次运行会安装 USB/IP 驱动", detail)
	}
	if platform == "darwin" && !isServer {
		add(levelInfo, "macos-client", "macOS 上使用 Client 需要 CGO 构建的核心",
			"通过 IOUSBHostControllerInterface 创建虚拟 USB 控制器来挂载远程设备，不需要关闭 SIP。")
	}

	// Peer requirements.
	if isServer {
		add(levelInfo, "peer", "Client 端也必须使用 kiko-core、sing-box 或 sing-usbip", "这类 Client 通过扩展控制通道导入设备，标准 USB/IP 服务端不兼容。")
	} else {
		add(levelInfo, "peer", "Server 端必须是 kiko-core、sing-box 或 sing-usbip", "请先在共享设备的那台机器上启动 USB/IP Server，再启动 Client。")
	}

	devicesOf := func() []any {
		if list, ok := fields["devices"].([]any); ok {
			return list
		}
		return nil
	}

	if isServer {
		listen := text(fields["listen"])
		dynamic := fields["provider"] == "dynamic"
		if dynamic {
			add(levelInfo, "dynamic", "dynamic 模式由外部提供设备",
				"设备由 API 客户端在运行时注入。Kiko 桌面版会自动启用 api 服务，并可在卡片底部的「设备与共享」里导出本机设备。")
		}
		if path == pathTailscale {
			if len(enabledTS) == 0 {
				add(levelError, "ts-endpoint", "缺少已启用的 Tailscale 端点", "Tailscale 途径需要一个 Tailscale 端点。在「端点」页添加，或用连接向导自动创建。")
			} else {
				add(levelOK, "ts-endpoint", "已启用 Tailscale 端点")
				for _, endpoint := range enabledTS {
					status := ctx.tailscale[endpoint.Tag]
					switch {
					case tsRunning(status):
						add(levelOK, "ts-login-"+endpoint.ID, "Tailscale 已登录并在线")
					case tsNeedsLogin(status):
						add(levelWarn, "ts-login-"+endpoint.ID, "Tailscale 还没有登录", "在「端点」页点开该端点底部的状态，点击「打开登录页面」完成登录。")
					case ctx.serverRuntime:
						add(levelInfo, "ts-login-"+endpoint.ID, "Tailscale 暂无运行状态", "启动核心并应用配置后，这里会显示登录状态和本机的 Tailscale 地址。")
					}
				}
				add(levelInfo, "route-rule", "Kiko 会自动添加 Tailscale → USB/IP 直连路由规则",
					"来自 Tailscale 端点、目标为本机（127.0.0.0/8）的连接会走直连，不会被默认出口拦截。该规则只出现在生成的配置里。")
			}
			if !model.IsLoopbackHost(listen) {
				if model.IsWildcardHost(listen) {
					add(levelWarn, "listen", "已选择 Tailscale，但监听地址不是 127.0.0.1",
						"监听 0.0.0.0 时局域网内的设备也能直接访问 Server（USB/IP 没有认证）。建议改为 127.0.0.1，只允许通过 Tailscale 访问。")
				} else {
					add(levelError, "listen", "监听地址是具体的网卡 IP，Tailscale 连接会失败",
						"Tailscale 会把连接改写到 127.0.0.1，监听在其他 IP 上收不到。请改为 127.0.0.1 或 0.0.0.0。")
				}
			} else {
				add(levelOK, "listen", "监听 127.0.0.1：只有经过 Tailscale 的连接可以访问")
			}
		} else {
			switch {
			case model.IsLoopbackHost(listen):
				detail := "只有本机能访问这个 Server。局域网或 VPN 使用请改为 0.0.0.0。"
				if len(enabledTS) > 0 {
					detail = "如果打算通过 Tailscale 访问，请把连接途径改成 Tailscale；否则改为 0.0.0.0。"
				}
				add(levelWarn, "listen", "监听 127.0.0.1：其他设备无法访问", detail)
			case path == pathLAN:
				add(levelWarn, "listen", "局域网内所有设备都能访问这个 Server",
					"USB/IP 没有认证和加密，只应在可信的局域网里使用；共享的 USB 设备可以被网络上的任何人挂载。")
				detail := "如果启用了 ufw、firewalld 等防火墙，请放行该端口。"
				if platform == "windows" {
					detail = "在 Windows 防火墙里为该端口添加入站规则。"
				}
				add(levelInfo, "firewall", "需要放行 TCP "+port, detail)
			default:
				add(levelWarn, "listen", "监听 0.0.0.0：请限制只有 VPN 网段能访问", "USB/IP 没有认证和加密，不要把端口直接暴露到公网；请在防火墙中限制来源。")
			}
			if isSpecificNonLoopback(listen) && len(enabledTS) > 0 {
				add(levelWarn, "ts-listen", "监听具体 IP 时，Tailscale 端点的连接无法到达", "Tailscale 端点会把连接改写到 127.0.0.1，只监听在具体 IP 上收不到。")
			}
		}
		if !dynamic {
			if devices := devicesOf(); len(devices) == 0 {
				add(levelError, "devices", "还没有要导出的设备", "default 模式必须至少匹配一个设备。可以用「从本机选择」，同一行的条件同时满足才匹配。")
			} else {
				add(levelOK, "devices", fmt.Sprintf("已选择 %d 项设备匹配", len(devices)))
			}
		}
		return items
	}

	server := text(fields["server"])
	detourID := text(fields["detour"])
	var detourEndpoint *model.Endpoint
	if detourID != "" {
		for _, e := range doc.Endpoints {
			if e.ID == detourID {
				detourEndpoint = e
				break
			}
		}
	}
	var detourTS *model.Endpoint
	if detourEndpoint != nil && detourEndpoint.Type == "tailscale" {
		detourTS = detourEndpoint
	}
	var status *api.TailscaleStatus
	if detourTS != nil {
		status = ctx.tailscale[detourTS.Tag]
	}
	switch {
	case server == "":
		detail := "填 Server 所在机器的 IP 或域名。"
		if path == pathTailscale {
			detail = "填 Server 的 Tailscale 地址（100.x.x.x）或 MagicDNS 名称。"
		}
		add(levelError, "server", "还没有填写 Server 的地址", detail)
	case reBadServer.MatchString(server) || reScheme.MatchString(server):
		add(levelError, "server", "服务器地址格式不对", "只填主机名或 IP，不要带协议头或路径；端口请填在「端口」栏。")
	case reHostPort.MatchString(server):
		add(levelWarn, "server", "地址里似乎带了端口", "端口请填在「端口」栏，地址栏只写主机名或 IP。")
	case model.IsLoopbackHost(server):
		add(levelWarn, "server", "Server 地址是本机", "Client 要连接的是另一台机器上的 Server，请填对方的地址。")
	default:
		add(levelOK, "server", "将连接 "+server+":"+port)
	}
	if path == pathTailscale {
		if len(enabledTS) == 0 {
			add(levelError, "ts-endpoint", "缺少已启用的 Tailscale 端点", "Tailscale 途径需要一个 Tailscale 端点。在「端点」页添加，或用连接向导自动创建。")
		}
		if detourID == "" {
			add(levelError, "detour", "没有选择前置代理 detour", "没有 detour 时系统里没有通往 Tailscale 地址的路由，连接会超时。请选择你的 Tailscale 端点。")
		}
	}
	switch {
	case detourID != "" && detourEndpoint == nil:
		add(levelError, "detour", "前置代理指向了已删除的对象", "请重新选择 Tailscale 端点。")
	case detourEndpoint != nil && !detourEndpoint.Enabled:
		add(levelError, "detour", "前置代理「"+detourEndpoint.Tag+"」已停用", "请启用该端点，否则生成的配置里不会有它。")
	case detourEndpoint != nil && detourTS == nil:
		add(levelInfo, "detour", "前置代理是「"+detourEndpoint.Tag+"」（"+detourEndpoint.Type+"）", "USB/IP Client 会通过它拨号，请确认它能到达 Server。")
	case detourTS != nil:
		add(levelOK, "detour", "通过 Tailscale 端点拨号")
		switch {
		case tsRunning(status):
			add(levelOK, "ts-login", "Tailscale 已登录并在线")
		case tsNeedsLogin(status):
			add(levelWarn, "ts-login", "Tailscale 还没有登录", "在「端点」页点开该端点底部的状态，点击「打开登录页面」完成登录。")
		case ctx.serverRuntime:
			add(levelInfo, "ts-login", "Tailscale 暂无运行状态", "启动核心并应用配置后才能看到登录状态和可选的对端设备。")
		}
		peers := tailnetPeers(status)
		if server != "" && len(peers) > 0 {
			i := slices.IndexFunc(peers, func(p tailnetPeer) bool {
				return p.Address == bare(server) || p.Name == server || p.DNSName != "" && p.DNSName == server
			})
			switch {
			case i >= 0 && peers[i].Online:
				add(levelOK, "peer-online", "对端「"+peers[i].Name+"」在线")
			case i >= 0:
				add(levelWarn, "peer-online", "对端「"+peers[i].Name+"」当前离线", "请确认 Server 机器已开机、核心与 Tailscale 端点已启动。")
			default:
				add(levelInfo, "peer-online", "在 tailnet 设备列表里没有找到这个地址", "请核对 Server 的 Tailscale 地址，或用「从 Tailscale 对端选择」。")
			}
		}
	case server != "" && model.IsTailnetHost(server):
		add(levelWarn, "detour", "这是 Tailscale 地址，但没有设置前置代理 detour",
			"如果本机安装了系统级 Tailscale 可以忽略；只用 Kiko 的话，连接会超时。请选择 Tailscale 端点作为 detour。")
	}
	title := "导入 Server 导出的全部设备"
	if devices := devicesOf(); len(devices) > 0 {
		title = fmt.Sprintf("只导入匹配的 %d 项设备", len(devices))
	}
	add(levelInfo, "devices", title, "设备匹配留空表示导入全部；需要限制时可用设备匹配（同一行同时满足才匹配）。")
	return items
}

func isSpecificNonLoopback(listen string) bool {
	return bare(listen) != "" && !model.IsWildcardHost(listen) && !model.IsLoopbackHost(listen)
}

// addressOption is an address a client may use to reach a server.
type addressOption struct{ Label, Address string }

// serverAddressOptions are the server's addresses to give to a client.
func serverAddressOptions(doc *model.Doc, service *model.Service, ctx guideContext) []addressOption {
	var out []addressOption
	if detectPath(doc, service) == pathTailscale {
		for _, endpoint := range tailscaleEndpoints(doc, true) {
			me, ok := tailnetSelf(ctx.tailscale[endpoint.Tag])
			if !ok {
				continue
			}
			if v4 := preferV4(me.IPs); v4 != "" {
				out = append(out, addressOption{"Tailscale 地址", v4})
			}
			if me.DNSName != "" {
				out = append(out, addressOption{"MagicDNS 名称", me.DNSName})
			}
		}
		return out
	}
	listen := text(service.Fields["listen"])
	if listen != "" && !model.IsWildcardHost(listen) && !model.IsLoopbackHost(listen) {
		out = append(out, addressOption{"监听地址", listen})
	}
	return out
}

func portNumber(port string) int {
	n, err := strconv.ParseFloat(strings.TrimSpace(port), 64)
	if err != nil || n == 0 {
		return 3240
	}
	return int(n)
}

// clientConfigSnippet is a sing-box config a client may paste.
func clientConfigSnippet(path, address, port string) string {
	if address == "" {
		address = "<Server 的地址>"
		if path == pathTailscale {
			address = "<Server 的 Tailscale 地址>"
		}
	}
	service := orderedJSON{{"type", "usbip-client"}, {"tag", "usb-client"}, {"server", address}, {"server_port", portNumber(port)}}
	var config orderedJSON
	if path == pathTailscale {
		config = append(config, kv{"endpoints", []any{orderedJSON{{"type", "tailscale"}, {"tag", "ts"}, {"state_directory", "tailscale"},
			{"hostname", "usb-client"}, {"auth_key", "<可选：Tailscale auth key，留空则在日志里显示登录链接>"}}}})
		service = append(service, kv{"detour", "ts"})
	}
	config = append(config, kv{"services", []any{service}})
	raw, _ := json.MarshalIndent(config, "", "  ")
	return string(raw)
}

// clientConnectionText is the connection details to hand to a client.
func clientConnectionText(path, address, port, dnsName string) string {
	info := pathInfo(path)
	shown := address
	if shown == "" {
		shown = "（请填 Server 的地址）"
	}
	if dnsName != "" {
		shown += "（MagicDNS：" + dnsName + "）"
	}
	lines := []string{
		"USB/IP Server 连接信息",
		"地址：" + shown,
		"端口：" + strconv.Itoa(portNumber(port)),
		"连接方式：" + info.Title,
		"",
		"在使用设备的那台机器上：",
	}
	for i, line := range info.Client {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, line))
	}
	return strings.Join(lines, "\n")
}

// nextSteps are the wizard's next steps for a role and path.
func nextSteps(role, path string) []string {
	if role == "server" {
		steps := []string{"在「设备匹配」里选择要共享的设备（可用「从本机选择」）"}
		if path == pathTailscale {
			steps = append(steps, "启动核心，打开 Tailscale 端点的状态完成登录",
				"在端点页 USB/IP 卡片里复制 Server 的 Tailscale 地址，告诉 Client 一侧")
		} else {
			steps = append(steps, "启动核心（需要 root/管理员权限）", "记下这台机器的 IP 和端口 3240，告诉 Client 一侧")
		}
		return steps
	}
	if path == pathTailscale {
		return []string{"在 Client 这台机器启动核心，并在 Tailscale 端点状态里完成登录",
			"「服务器地址」填 Server 的 Tailscale 地址，可用「从 Tailscale 对端选择」",
			"连接成功后，远程设备会作为本机 USB 设备出现（Linux 用 lsusb 查看，macOS 看「系统信息 → USB」，Windows 看设备管理器）"}
	}
	return []string{"在 Client 这台机器启动核心（需要 root/管理员权限）", "「服务器地址」填 Server 的地址",
		"连接成功后，远程设备会作为本机 USB 设备出现（Linux 用 lsusb 查看，macOS 看「系统信息 → USB」，Windows 看设备管理器）"}
}

// usbIPPlan is what the wizard creates: the service, and the Tailscale
// endpoint it needs when the document has none enabled.
type usbIPPlan struct {
	Service  *model.Service
	Endpoint *model.Endpoint
	// Doc is the document with them, for the checklist's preview.
	Doc *model.Doc
}

// planUsbIP prefills a service for a role and path.
func planUsbIP(doc *model.Doc, role, path string) usbIPPlan {
	var endpoint *model.Endpoint
	endpointID := ""
	if enabled := tailscaleEndpoints(doc, true); len(enabled) > 0 {
		endpointID = enabled[0].ID
	}
	if existing := model.TailscaleEndpoint(doc); path == pathTailscale && endpointID == "" && existing != nil {
		// A document keeps one Tailscale endpoint: a disabled one is
		// reused, for the checklist to ask to enable it.
		endpointID = existing.ID
	}
	if path == pathTailscale && endpointID == "" {
		// Named as Tailscale names the system hostname.
		endpoint = model.MakeEndpoint("tailscale", model.TailscaleTagFor(doc, ""), doc.IPv6Enabled)
		endpointID = endpoint.ID
	}
	typ := "usbip-client"
	if role == "server" {
		typ = "usbip-server"
	}
	var taken []string
	for _, s := range doc.Services {
		taken = append(taken, s.Tag)
	}
	service := model.MakeService(typ, model.UniqueTag(typ, taken))
	service.Fields = cloneFields(service.Fields)
	for k, v := range applyPath(doc, typ, path, endpointID) {
		service.Fields[k] = v
	}
	next := model.Clone(doc)
	if endpoint != nil {
		next.Endpoints = append(next.Endpoints, endpoint)
	}
	next.Services = append(next.Services, service)
	return usbIPPlan{Service: service, Endpoint: endpoint, Doc: next}
}

// allTags are the tags inbounds, outbounds, endpoints and DNS servers
// share.
func allTags(doc *model.Doc) []string {
	var tags []string
	for _, in := range doc.Inbounds {
		tags = append(tags, in.Tag)
	}
	for _, o := range doc.Outbounds {
		tags = append(tags, o.Tag)
	}
	for _, e := range doc.Endpoints {
		tags = append(tags, e.Tag)
	}
	for _, s := range doc.DNS.Servers {
		tags = append(tags, s.Tag)
	}
	return tags
}

func cloneFields(f model.Fields) model.Fields {
	out := model.Fields{}
	for k, v := range f {
		out[k] = v
	}
	return out
}

// kv and orderedJSON write JSON objects in a fixed key order, as
// JSON.stringify keeps insertion order.
type kv struct {
	Key   string
	Value any
}

type orderedJSON []kv

// MarshalJSON writes the pairs in order.
func (o orderedJSON) MarshalJSON() ([]byte, error) {
	buf := []byte{'{'}
	for i, p := range o {
		if i > 0 {
			buf = append(buf, ',')
		}
		key, _ := json.Marshal(p.Key)
		value, err := json.Marshal(p.Value)
		if err != nil {
			return nil, err
		}
		buf = append(buf, key...)
		buf = append(buf, ':')
		buf = append(buf, value...)
	}
	return append(buf, '}'), nil
}
