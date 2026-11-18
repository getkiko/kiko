package lanscan

import (
	"context"
	"net/netip"
	"strings"
	"time"
)

// NetBIOS asks every host for its NetBIOS name table (NBSTAT, UDP 137).
type NetBIOS struct {
	Port int           // default 137
	Wait time.Duration // default 1s
}

func (NetBIOS) Name() string { return SourceNetBIOS }

func (n NetBIOS) Identify(ctx context.Context, req IdentifyRequest) error {
	port, wait := n.Port, n.Wait
	if port == 0 {
		port = 137
	}
	if wait == 0 {
		wait = time.Second
	}
	return udpExchange(ctx, req.Subnets, req.Hosts, port, req.Pacer, wait,
		func(netip.Addr) [][]byte { return [][]byte{BuildNBSTAT(0x4e42)} },
		func(src netip.Addr, pkt []byte, _ func([]byte)) {
			if name := ParseNBSTAT(pkt); name != "" {
				req.Report(Hint{IP: src, Source: SourceNetBIOS, Name: name, NetBIOS: true})
			}
		})
}

// BuildNBSTAT builds a node status request for the wildcard name "*".
func BuildNBSTAT(id uint16) []byte {
	b := []byte{byte(id >> 8), byte(id), 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x20}
	b = append(b, "CKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"...)
	return append(b, 0, 0, 0x21, 0, 1)
}

// ParseNBSTAT returns the workstation (suffix 0x00, unique) name of a node
// status response, or "".
func ParseNBSTAT(pkt []byte) string {
	if len(pkt) < 12 || pkt[2]&0x80 == 0 || pkt[7] == 0 { // not a response / no answers
		return ""
	}
	pos := 12
	// Skip the question section if echoed.
	for q := int(pkt[4])<<8 | int(pkt[5]); q > 0; q-- {
		pos = skipName(pkt, pos)
		if pos < 0 {
			return ""
		}
		pos += 4
	}
	pos = skipName(pkt, pos)
	if pos < 0 || pos+10 > len(pkt) {
		return ""
	}
	if uint16(pkt[pos])<<8|uint16(pkt[pos+1]) != 0x0021 {
		return ""
	}
	pos += 10 // type, class, ttl, rdlength
	if pos >= len(pkt) {
		return ""
	}
	count := int(pkt[pos])
	pos++
	for i := 0; i < count && pos+18 <= len(pkt); i++ {
		name, suffix, flags := string(pkt[pos:pos+15]), pkt[pos+15], uint16(pkt[pos+16])<<8|uint16(pkt[pos+17])
		pos += 18
		if suffix == 0 && flags&0x8000 == 0 {
			if name = strings.TrimSpace(strings.Trim(name, "\x00")); name != "" {
				return name
			}
		}
	}
	return ""
}

func skipName(pkt []byte, pos int) int {
	for pos < len(pkt) {
		l := int(pkt[pos])
		switch {
		case l == 0:
			return pos + 1
		case l&0xC0 == 0xC0:
			return pos + 2
		default:
			pos += l + 1
		}
	}
	return -1
}
