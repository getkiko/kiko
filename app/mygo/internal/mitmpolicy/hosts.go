package mitmpolicy

import (
	"errors"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// HostList picks the include or exclude list of settings.
func HostList(m model.MITMSettings, exclude bool) []string {
	if m.Hostnames == nil {
		return nil
	}
	if exclude {
		return m.Hostnames.Exclude
	}
	return m.Hostnames.Include
}

// SetHostList stores a list back.
func SetHostList(m *model.MITMSettings, exclude bool, list []string) {
	if m.Hostnames == nil {
		m.Hostnames = &model.MITMHostnames{}
	}
	if exclude {
		m.Hostnames.Exclude = list
	} else {
		m.Hostnames.Include = list
	}
}

// echPublicNames are the public names of encrypted ClientHello; their
// outer SNI is not the site, so a pattern naming them never matches.
var echPublicNames = []string{"cloudflare-ech.com"}

// NormalizeHost cleans one host pattern with the model's validator: a
// trailing dot is dropped and the known ECH public names are refused on
// top.
func NormalizeHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if strings.TrimSpace(host) == "" {
		return "", errors.New("请输入主机名")
	}
	if strings.TrimSpace(host) == "*" {
		return "", errors.New("不接受 *")
	}
	cleaned := host
	host, ok := model.NormalizeMITMHostname(cleaned)
	if !ok {
		if rest, wild := strings.CutPrefix(strings.ToLower(strings.TrimSpace(cleaned)), "*."); wild && !strings.Contains(rest, ".") {
			return "", errors.New("通配符至少覆盖两级域名")
		}
		if strings.Contains(raw, "*") {
			return "", errors.New("通配符只能写在开头，如 *.example.com")
		}
		return "", errors.New("只填写主机名")
	}
	name := strings.TrimPrefix(host, "*.")
	for _, ech := range echPublicNames {
		if name == ech || strings.HasSuffix(name, "."+ech) {
			return "", errors.New("加密 ClientHello 公共名无法解密")
		}
	}
	return host, nil
}

// HasHost reports whether list holds host, comparing normalised forms.
func HasHost(list []string, host string) bool {
	return slices.ContainsFunc(list, func(h string) bool {
		n, ok := model.NormalizeMITMHostname(h)
		return h == host || ok && n == host
	})
}

// AddHost appends a normalised pattern to list. A duplicate in list is
// refused, and so is one already in other (a host is either included or
// excluded).
func AddHost(list, other []string, raw string) ([]string, error) {
	host, err := NormalizeHost(raw)
	if err != nil {
		return list, err
	}
	if HasHost(list, host) {
		return list, errors.New("已在列表中")
	}
	if HasHost(other, host) {
		return list, errors.New("已在另一个列表中")
	}
	return append(slices.Clone(list), host), nil
}

// RemoveHost drops host from list.
func RemoveHost(list []string, host string) []string {
	out := make([]string, 0, len(list))
	for _, h := range list {
		if h != host {
			out = append(out, h)
		}
	}
	return out
}

// ExcludeHost adds host to the exclude list. A host already excluded stays
// as it is; an exact entry in the include list moves over.
func ExcludeHost(m *model.MITMSettings, raw string) error {
	host, err := NormalizeHost(raw)
	if err != nil {
		return err
	}
	if HasHost(HostList(*m, true), host) {
		return nil
	}
	SetHostList(m, false, RemoveHost(HostList(*m, false), host))
	next, err := AddHost(HostList(*m, true), nil, host)
	if err != nil {
		return err
	}
	SetHostList(m, true, next)
	return nil
}
