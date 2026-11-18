package model

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// This file holds small helpers: group members, tags, SSH options and the
// address checks of the USB/IP guide.

// ---- group members ----

// StableGroupMemberOrder orders a group editor's candidates: the selected
// members first in member order, then the rest in their own order.
func StableGroupMemberOrder[T any](candidates []T, id func(T) string, members []string) []T {
	memberOrder := map[string]int{}
	for i, m := range members {
		if _, ok := memberOrder[m]; !ok {
			memberOrder[m] = i
		}
	}
	out := append([]T{}, candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lok := memberOrder[id(out[i])]
		ri, rok := memberOrder[id(out[j])]
		if lok != rok {
			return lok
		}
		if lok {
			return li < ri
		}
		return false
	})
	return out
}

// Member source filters of the group editor.
const (
	MemberSourceAll    = "all"
	MemberSourceManual = "manual"
)

// MemberSourceSubscription is the filter of a subscription's nodes.
func MemberSourceSubscription(id string) string { return "subscription:" + id }

// MatchesMemberSource reports whether an outbound passes a source filter:
// all, manual (not from a subscription) or subscription:<id>.
func MatchesMemberSource(outbound *Outbound, filter string) bool {
	switch filter {
	case MemberSourceAll:
		return true
	case MemberSourceManual:
		return outbound.SubID == ""
	}
	return outbound.SubID == strings.TrimPrefix(filter, "subscription:")
}

// ---- tags ----

// TagValidationError checks a tag in the namespace inbounds, outbounds,
// endpoints and DNS servers share; selfID is the edited entity.
func TagValidationError(doc *Doc, tag, selfID string) string {
	value := strings.TrimSpace(tag)
	if value == "" {
		return "请填写名称"
	}
	taken := func(id, t string) bool { return id != selfID && strings.TrimSpace(t) == value }
	duplicate := false
	for _, in := range doc.Inbounds {
		duplicate = duplicate || taken(in.ID, in.Tag)
	}
	for _, o := range doc.Outbounds {
		duplicate = duplicate || taken(o.ID, o.Tag)
	}
	for _, e := range doc.Endpoints {
		duplicate = duplicate || taken(e.ID, e.Tag)
	}
	for _, s := range doc.DNS.Servers {
		duplicate = duplicate || taken(s.ID, s.Tag)
	}
	if duplicate {
		return "名称「" + value + "」已被使用"
	}
	return ""
}

// ServiceTagValidationError checks a service tag; services have their
// own namespace.
func ServiceTagValidationError(doc *Doc, tag, selfID string) string {
	value := strings.TrimSpace(tag)
	if value == "" {
		return "请填写名称"
	}
	for _, s := range doc.Services {
		if s.ID != selfID && strings.TrimSpace(s.Tag) == value {
			return "名称「" + value + "」已被使用"
		}
	}
	return ""
}

// ---- SSH options ----

// SSHOptions are the Tailscale SSH dialog's options.
type SSHOptions struct {
	Username     string `json:"username"`
	TerminalType string `json:"terminalType"`
	Remember     bool   `json:"remember"`
}

// DefaultSSHOptions are the options of a first connection.
var DefaultSSHOptions = SSHOptions{Username: "root", TerminalType: "xterm-256color"}

// KeyValueStore persists small strings, as the browser's localStorage.
type KeyValueStore interface {
	Get(key string) (string, bool)
	Set(key, value string)
	Remove(key string)
}

func sshOptionsKey(endpoint, peer string) string {
	raw, _ := json.Marshal([]string{endpoint, peer})
	return "kiko:ssh:" + string(raw)
}

// LoadSSHOptions returns the remembered options of a peer, or the
// defaults.
func LoadSSHOptions(store KeyValueStore, endpoint, peer string) SSHOptions {
	if store != nil {
		if raw, ok := store.Get(sshOptionsKey(endpoint, peer)); ok {
			var v struct {
				Username     *string `json:"username"`
				TerminalType *string `json:"terminalType"`
				Remember     bool    `json:"remember"`
			}
			if json.Unmarshal([]byte(raw), &v) == nil && v.Remember && v.Username != nil &&
				strings.TrimSpace(*v.Username) != "" && v.TerminalType != nil {
				terminal := *v.TerminalType
				if terminal == "" {
					terminal = DefaultSSHOptions.TerminalType
				}
				return SSHOptions{Username: *v.Username, TerminalType: terminal, Remember: true}
			}
		}
	}
	return DefaultSSHOptions
}

// SaveSSHOptions remembers a peer's options, or forgets them when
// Remember is off.
func SaveSSHOptions(store KeyValueStore, endpoint, peer string, options SSHOptions) {
	if store == nil {
		return
	}
	key := sshOptionsKey(endpoint, peer)
	if options.Remember {
		raw, _ := json.Marshal(options)
		store.Set(key, string(raw))
	} else {
		store.Remove(key)
	}
}

// ---- USB/IP addresses ----

func bareHost(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")
}

// IsWildcardHost reports a listen address on every interface.
func IsWildcardHost(host string) bool {
	v := bareHost(host)
	return v == "" || v == "0.0.0.0" || v == "::"
}

// IsLoopbackHost reports a loopback address.
func IsLoopbackHost(host string) bool {
	v := strings.ToLower(bareHost(host))
	return v == "localhost" || v == "::1" || strings.HasPrefix(v, "127.")
}

var (
	reDottedQuad   = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)\.(\d+)$`)
	rePrivateName  = regexp.MustCompile(`\.(local|lan|home|internal)$`)
	reDottedPrefix = regexp.MustCompile(`^(\d+)\.(\d+)\.\d+\.\d+$`)
)

// IsTailnetHost reports a Tailscale address: 100.64.0.0/10,
// fd7a:115c:a1e0::/48 or *.ts.net.
func IsTailnetHost(host string) bool {
	v := strings.ToLower(bareHost(host))
	if strings.HasSuffix(v, ".ts.net") || strings.HasPrefix(v, "fd7a:115c:a1e0:") {
		return true
	}
	m := reDottedQuad.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	a, b := atoiOr(m[1], -1), atoiOr(m[2], -1)
	return a == 100 && b >= 64 && b <= 127
}

// IsPrivateLanHost reports a private LAN address or local name.
func IsPrivateLanHost(host string) bool {
	v := strings.ToLower(bareHost(host))
	if rePrivateName.MatchString(v) {
		return true
	}
	m := reDottedPrefix.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	a, b := atoiOr(m[1], -1), atoiOr(m[2], -1)
	return a == 10 || a == 172 && b >= 16 && b <= 31 || a == 192 && b == 168 || a == 169 && b == 254
}
