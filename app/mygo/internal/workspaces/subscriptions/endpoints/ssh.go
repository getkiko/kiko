package endpoints

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// sshSheet is a Tailscale SSH session: a form of the
// user and terminal type, then a native terminal (MyGo's terminal
// plugin, Ghostty's emulator) over the server's SSH WebSocket.
type sshSheet struct {
	isOpen bool
	tag    string
	peer   api.TailscalePeer

	username, terminalType string
	remember               bool
	loaded                 bool

	session int
	status  string
	banner  string
	term    *terminal.Terminal
	conn    *sshConn
	failed  string // the terminal could not start: the library is missing
}

// prefsStore keeps SSH options in the console's preferences, as the web
// console keeps them in localStorage.
type prefsStore struct{ s *Section }

func (p prefsStore) Get(key string) (string, bool) {
	var v string
	ok := p.s.env.Prefs.Load(key, &v)
	return v, ok && v != ""
}
func (p prefsStore) Set(key, value string) { p.s.env.Prefs.Save(key, value) }
func (p prefsStore) Remove(key string)     { p.s.env.Prefs.Save(key, "") }

func (h *sshSheet) open(tag string, peer api.TailscalePeer) {
	h.close()
	h.isOpen, h.tag, h.peer = true, tag, peer
	h.username, h.terminalType, h.remember, h.loaded = "", "", false, false
	h.session = 0
}

// initOptions fills the form from the remembered options once per
// opening.
func (h *sshSheet) initOptions(s *Section) {
	if h.loaded {
		return
	}
	h.loaded = true
	options := model.LoadSSHOptions(prefsStore{s}, h.tag, peerKey(h.peer))
	h.username, h.terminalType, h.remember = options.Username, options.TerminalType, options.Remember
}

func (h *sshSheet) close() {
	h.disconnect()
	h.isOpen = false
}

// disconnect ends the session and frees the terminal.
func (h *sshSheet) disconnect() {
	if h.conn != nil {
		_ = h.conn.Close()
		h.conn = nil
	}
	if h.term != nil {
		_ = h.term.Close()
		h.term = nil
	}
	h.session, h.status, h.banner, h.failed = 0, "", "", ""
}

// connect starts a session: the WebSocket, then a terminal on it.
func (h *sshSheet) connect(s *Section) {
	username := strings.TrimSpace(h.username)
	if username == "" {
		return
	}
	model.SaveSSHOptions(prefsStore{s}, h.tag, peerKey(h.peer), model.SSHOptions{Username: username,
		TerminalType: firstOf(strings.TrimSpace(h.terminalType), "xterm-256color"), Remember: h.remember})
	h.disconnect()
	h.session++
	session := h.session
	h.status = "正在连接…"
	e := s.env
	start := sshStart{EndpointTag: h.tag, PeerAddress: peerAddress(h.peer), Username: username,
		TerminalType: h.terminalType, HostKeys: h.peer.SSHHostKeys}
	post := func(fn func()) {
		e.Post(func() {
			if h.session == session {
				fn()
			}
		})
	}
	events := sshEvents{
		Banner: func(message string) { post(func() { h.banner = message }) },
		Ready:  func() { post(func() { h.banner, h.status = "", "已连接" }) },
		Ended:  func(status string) { post(func() { h.status = status }) },
	}
	go func() {
		conn, err := dialSSH(e.API.Origin(), e.API.TLSConfig(), e.API.Header(), start, events)
		e.Post(func() {
			if h.session != session {
				// Disconnected, reconnected or closed while dialing.
				if conn != nil {
					_ = conn.Close()
				}
				return
			}
			if err != nil {
				h.status = "连接失败"
				h.failed = err.Error()
				return
			}
			term, err := terminal.New(terminal.Options{Conn: conn, Cursor: terminal.CursorBar})
			if err != nil {
				_ = conn.Close()
				h.status = "终端初始化失败"
				h.failed = err.Error()
				return
			}
			h.conn, h.term = conn, term
		})
	}()
}

// sshCommand is the command a system terminal would run.
func (h *sshSheet) sshCommand() string {
	return "ssh " + strings.TrimSpace(h.username) + "@" + peerAddress(h.peer)
}

var reURL = regexp.MustCompile(`https?://\S+`)

func (h *sshSheet) view(c *ui.Context, s *Section) {
	if !h.isOpen {
		return
	}
	k := kit.T(c)
	h.initOptions(s)
	name := peerName(h.peer)
	open := true
	if h.session == 0 {
		kit.Dialog(c, &open, "SSH 连接", 0, func() {
			ui.Text(c, name+" · "+peerAddress(h.peer)).FontSize(kit.FontBody).TextColor(k.Label3)
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "用户名").FontSize(kit.FontBody).TextColor(k.Label2)
				input := kit.Input(c, "username", &h.username, "").Label("用户名").AutoFocus()
				if input.Shortcut(0, ui.KeyEnter) {
					h.connect(s)
				}
			})
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "终端类型").FontSize(kit.FontBody).TextColor(k.Label2)
				kit.Input(c, "terminal-type", &h.terminalType, "xterm-256color").Label("终端类型")
			})
			ui.Row(c).MinHeight(44).Gap(12).Children(func() {
				ui.Text(c, "记住 SSH 选项").FontSize(kit.FontBody).Grow(1)
				kit.Switch(c, &h.remember, "记住 SSH 选项")
			})
			ui.Text(c, "为此设备保存用户名和终端类型，下次连接时自动填入。").FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
		}, func() {
			kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(h.close)
			kit.Button(c, "连接", kit.ButtonOptions{Variant: kit.Primary, Disabled: strings.TrimSpace(h.username) == ""}).
				OnClick(func() { h.connect(s) })
		})
		if !open {
			h.close()
		}
		return
	}
	w, height := c.Size()
	kit.Dialog(c, &open, strings.TrimSpace(h.username)+"@"+name, min(1100, w-32), func() {
		ui.Column(c).Height(min(800, height*0.85) - 120).Radius(12).Background(ui.Hex("#15171a")).Clip().
			TextColor(ui.Hex("#e5e7eb")).Shrink(0).Children(func() {
			ui.Row(c).MinHeight(44).PaddingX(16).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(ui.RGBA(255, 255, 255, 0.1)).
				FontSize(kit.FontTable).Shrink(0).Children(func() {
				kit.Icon(c, kit.SymTerminal, 15)
				ui.Text(c, strings.TrimSpace(h.username)+"@"+peerAddress(h.peer)).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				ui.Text(c, firstOf(h.status, "正在准备终端…")).TextColor(ui.RGBA(255, 255, 255, 0.5)).Role(ui.RoleStatus)
				kit.IconButton(c, kit.SymClipboardPaste, "粘贴到终端", kit.IconButtonOptions{Size: 28, IconSize: 15}).OnClick(func() {
					if h.term != nil {
						h.term.Paste(c.ReadClipboard())
					}
				})
				kit.IconButton(c, kit.SymRotateCw, "重新连接 SSH", kit.IconButtonOptions{Size: 28, IconSize: 15}).OnClick(func() { h.connect(s) })
				kit.Button(c, "断开", kit.ButtonOptions{Small: true, Variant: kit.TextButton}).OnClick(h.disconnect)
			})
			ui.Box(c).Grow(1).MinHeight(0).Padding(12).Children(func() {
				if h.term != nil {
					terminal.View(c, h.term).Fill().AutoFocus()
				}
				if h.failed != "" {
					ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(10).Padding(24).Children(func() {
						ui.Text(c, h.failed).FontSize(kit.FontBody).TextAlign(ui.Center).Wrap().Selectable()
						ui.Text(c, "可以复制 ssh 命令，在系统终端中连接（需要本机能访问该 Tailscale 地址）。").FontSize(kit.FontTable).
							TextColor(ui.RGBA(255, 255, 255, 0.6)).TextAlign(ui.Center).Wrap()
						kit.Button(c, "复制 SSH 命令", kit.ButtonOptions{Small: true, Icon: kit.SymCopy}).OnClick(func() {
							s.copy(c, h.sshCommand(), "已复制 SSH 命令")
						})
					})
					return
				}
				if h.status == "正在连接…" || h.banner != "" {
					ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(16).Padding(24).
						Background(ui.RGBA(21, 23, 26, 0.9)).Children(func() {
						if h.status == "正在连接…" {
							ui.Text(c, "正在连接 "+name+"…").FontSize(kit.FontBody).TextColor(ui.RGBA(255, 255, 255, 0.6))
						}
						if h.banner != "" {
							ui.Column(c).MaxWidth(520).Radius(12).Border(1, ui.RGBA(255, 255, 255, 0.1)).Background(ui.RGBA(255, 255, 255, 0.05)).
								Padding(16).Gap(8).Children(func() {
								ui.Text(c, h.banner).FontSize(kit.FontBody).Wrap().Selectable()
								for _, link := range reURL.FindAllString(h.banner, -1) {
									kit.Button(c, link, kit.ButtonOptions{Small: true, Variant: kit.TextButton, Icon: kit.SymExternalLink}).
										OnClick(func() { s.openURL(link) })
								}
							})
						}
					})
				}
			})
		})
	}, nil)
	if !open {
		h.close()
	}
}
