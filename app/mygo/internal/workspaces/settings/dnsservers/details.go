package dnsservers

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// detailsDialog shows a server's details: state, references and TLS.
func (s *Section) detailsDialog(c *ui.Context, doc *model.Doc) {
	server := model.DNSServerByID(doc, s.details)
	if server == nil {
		s.detailsOpen = false
	}
	wasOpen := s.detailsOpen
	title := "DNS 服务器详情"
	if server != nil && server.Tag != "" {
		title = server.Tag
	}
	kit.Dialog(c, &s.detailsOpen, title, 0, func() {
		if server == nil {
			return
		}
		kit.Section(c, "服务器", nil, func() {
			kit.PropertyWith(c, "状态", func() {
				if server.Enabled {
					kit.Status(c, "已启用", kit.SuccessTone)
				} else {
					kit.Status(c, "已停用", kit.Neutral)
				}
			})
			kit.Property(c, "名称", server.Tag)
			kit.Property(c, "协议", protocolLabel(server.Type))
			kit.Property(c, "地址", serverEndpoint(server))
		}).Margin(0, 0, 6)
		kit.Section(c, "引用", nil, func() {
			kit.Property(c, "分流规则", itoa(ruleCount(doc, server.ID))+" 条")
			final := "由 DNS 分流配置"
			if doc.DNS.Final.ID() == server.ID {
				final = "当前服务器"
			}
			kit.Property(c, "兜底 DNS", final)
			resolver := "核心默认"
			if server.DomainResolver.Valid() {
				resolver = "需修正引用"
				if target := model.DNSServerByID(doc, server.DomainResolver.ID()); target != nil && target.Tag != "" {
					resolver = target.Tag
				}
			}
			kit.Property(c, "域名解析器", resolver)
		}).Margin(0, 0, 6)
		if model.IsTLSDNS(server.Type) {
			tls := tlsMap(server)
			kit.Section(c, "安全连接", nil, func() {
				kit.Property(c, "加密传输", protocolLabel(server.Type))
				name := "使用服务器地址"
				if jsv.Truthy(tls["server_name"]) {
					name = jsv.String(tls["server_name"])
				}
				kit.Property(c, "Server Name", name)
				verify := "启用"
				if jsv.Truthy(tls["insecure"]) {
					verify = "已跳过"
				}
				kit.Property(c, "证书验证", verify)
			})
		}
	}, func() {
		if server == nil {
			return
		}
		kit.Button(c, "复制地址", kit.ButtonOptions{Icon: kit.SymCopy}).OnClick(func() { s.copyAddress(c, server) })
		kit.Button(c, "编辑", kit.ButtonOptions{Icon: kit.SymSlidersHorizontal, Variant: kit.Primary}).OnClick(func() {
			s.detailsOpen = false
			s.editor.openExisting(doc, server.ID)
		})
	})
	if wasOpen && !s.detailsOpen && !s.editor.open {
		s.focusServer(s.details, true)
	}
}
