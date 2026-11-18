package connections

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

func jsonUnmarshal(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

func dnsQuery(id string, revision api.Uint64, status string) api.DNSQuery {
	return api.DNSQuery{ID: id, Revision: revision, Seq: revision, StartedAt: fmt.Sprintf("2026-10-16T00:00:%02sZ", id),
		Role: "query", Status: status, Questions: []api.DNSQuestion{{Name: id + ".example.", Type: "A", Class: "IN"}}}
}

func dnsView(queries []api.DNSQuery, seq api.Uint64) api.DNSView {
	return api.DNSView{Instance: "a", Seq: seq, Queries: queries}
}

func keys(s *dnsState) []string {
	var out []string
	for id := range s.queries {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func TestDNSCursors(t *testing.T) {
	var big api.Uint64
	if err := json.Unmarshal([]byte(`"18446744073709551615"`), &big); err != nil || big != 18446744073709551615 {
		t.Fatal(big, err)
	}
	s := newDNSState()
	s.apply(dnsView([]api.DNSQuery{dnsQuery("1", 2, "success")}, 9007199254740993), dnsLimit)
	s.apply(dnsView([]api.DNSQuery{dnsQuery("1", 1, "pending")}, 9007199254740994), dnsLimit)
	if s.queries["1"].Status != "success" || s.seq != 9007199254740994 {
		t.Fatal(s.queries["1"].Status, s.seq)
	}
	if s.apply(dnsView(nil, 1), dnsLimit) {
		t.Fatal("stale view applied")
	}
}

func TestDNSResets(t *testing.T) {
	s := newDNSState()
	s.apply(dnsView([]api.DNSQuery{dnsQuery("1", 1, "pending")}, 1), dnsLimit)
	v := dnsView([]api.DNSQuery{dnsQuery("2", 1, "pending")}, 2)
	v.Gap, v.Reset = true, true
	s.apply(v, dnsLimit)
	if !slices.Equal(keys(s), []string{"2"}) || !s.gap {
		t.Fatal(keys(s), s.gap)
	}
	v = dnsView([]api.DNSQuery{dnsQuery("3", 1, "pending")}, 1)
	v.Instance = "b"
	s.apply(v, dnsLimit)
	if !slices.Equal(keys(s), []string{"3"}) {
		t.Fatal(keys(s))
	}
}

func TestDNSRetention(t *testing.T) {
	s := newDNSState()
	s.apply(dnsView([]api.DNSQuery{dnsQuery("1", 1, "pending"), dnsQuery("2", 1, "pending"), dnsQuery("3", 1, "pending")}, 1), 2)
	if !slices.Equal(keys(s), []string{"2", "3"}) {
		t.Fatal(keys(s))
	}
}

func TestDNSSourceFacets(t *testing.T) {
	sources := []string{"192.168.1.20:53000", "192.168.1.20:53001", "[2001:db8::20]:53002", "[2001:db8::20]:53003", "2001:db8::20", "[fe80::20%en0]:53", ""}
	counts := map[string]int{}
	for i, address := range sources {
		q := dnsQuery(fmt.Sprint(i+1), 1, "pending")
		q.Source.Address = address
		counts[dnsSourceIP(&q)]++
	}
	want := map[string]int{"192.168.1.20": 2, "2001:db8::20": 3, "fe80::20%en0": 1, "核心内部": 1}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatal(counts)
	}
}

func TestDNSLabels(t *testing.T) {
	q := dnsQuery("1", 1, "success")
	if dnsProtocol(&q) != "本地策略" || dnsOutbound(&q) != "—" {
		t.Fatal(dnsProtocol(&q), dnsOutbound(&q))
	}
	q.Cache = "hit"
	if dnsProtocol(&q) != "缓存" {
		t.Fatal(dnsProtocol(&q))
	}
	q.Attempts = []api.DNSAttempt{
		{Protocol: "udp", Connection: &api.DNSConnection{Outbound: []string{"direct"}}},
		{Protocol: "tcp", Connection: &api.DNSConnection{Outbound: []string{"direct"}}},
		{Protocol: "https", Connection: &api.DNSConnection{Outbound: []string{"proxy", "node"}}},
	}
	if dnsProtocol(&q) != "UDP → TCP → HTTPS" || dnsOutbound(&q) != "direct / proxy → node" {
		t.Fatal(dnsProtocol(&q), dnsOutbound(&q))
	}
}

func TestDNSQueryKeepsRaw(t *testing.T) {
	var q api.DNSQuery
	raw := `{"id":"7","seq":"3","revision":2,"started_at":"2026-10-16T00:00:01Z","status":"success","role":"query","source":{},"questions":[],"extra_field":1}`
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(q)
	if q.Seq != 3 || q.Revision != 2 || string(out) != raw {
		t.Fatal(q.Seq, string(out))
	}
}

func TestDNSQueryScripts(t *testing.T) {
	var q api.DNSQuery
	raw := `{"id":"7","status":"success","role":"query","source":{},"questions":[],"scripts":[{"tag":"geo","outcome":"answer","detail":"1.2.3.4","cached":true,"duration_ms":0.4}]}`
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Scripts) != 1 || q.Scripts[0].Tag != "geo" || !q.Scripts[0].Cached || q.Scripts[0].DurationMS != 0.4 {
		t.Fatalf("%+v", q.Scripts)
	}
	if lookup(dnsScriptOutcome, "maybe") != "可能" || dnsScriptTone("error") != kit.DangerTone {
		t.Fatal("labels")
	}
}
