package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Exercise the same store and encrypted profile sync used by the web editor.
func TestRoutePolicyStoreReloadAndEncryptedSync(t *testing.T) {
	var policy map[string]any
	if err := json.Unmarshal([]byte(`{"version":1,"route":{"devices":[{"id":"phone","name":"iPhone","icon":"phone","addresses":["192.168.1.20","2001:db8::20"],"final":"proxy"}],"groups":[{"id":"phone-group","deviceId":"phone","name":"应用"},{"id":"global-group","name":"默认"}],"rules":[{"id":"policy","deviceId":"phone","groupId":"phone-group","name":"工作","enabled":true,"action":"route","outbound":"direct","match":{},"conditionTree":{"kind":"group","mode":"and","invert":true,"rules":[{"kind":"match","uiField":"ip_cidr","match":{"rule_set":["work"]}},{"kind":"group","mode":"or","rules":[{"kind":"match","match":{"port":[443]},"invert":true}]}]}}],"final":"direct","defaultDomainResolver":"dns-local","findProcess":true},"dns":{"servers":[{"id":"dns-local","enabled":false,"type":"https","server":"1.1.1.1","detour":"proxy"}],"rules":[{"id":"dns-policy","enabled":true,"server":"dns-local","conditionTree":{"kind":"group","mode":"or","rules":[{"kind":"match","match":{"query_type":["A","AAAA"]}}]}}],"final":"dns-local","cacheCapacity":2048},"futureField":{"preserve":true}}`), &policy); err != nil {
		t.Fatal(err)
	}
	// Device-scoped DNS and independent copied conditions survive the same wire/storage path.
	dns := policy["dns"].(map[string]any)
	dns["devicePolicyVersion"] = float64(1)
	dns["deviceFinals"] = map[string]any{"local": "dns-local", "phone": nil}
	dns["groups"] = []any{map[string]any{"id": "phone-dns", "deviceId": "phone", "name": "工作 DNS"}}
	rule := dns["rules"].([]any)[0].(map[string]any)
	rule["deviceId"] = "phone"
	rule["groupId"] = "phone-dns"
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "stage1", testProfile{ID: "stage1", Name: "设备路由", Doc: policy})
	reloaded, err := newServer("/nonexistent", a.dataDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err = json.Unmarshal(reloaded.store.Doc, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, restored) {
		t.Fatalf("store reload changed policy: %s", reloaded.store.Doc)
	}
	var profile map[string]any
	if err = json.Unmarshal([]byte(storeProfileDoc(reloaded, "stage1")), &profile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, profile) {
		t.Fatal("named profile lost device scope or conditions")
	}
	createVault(t, a)
	b := newSyncTestServer(t, backend, clock)
	unlockVault(t, b)
	clock.Add(time.Minute)
	mustSync(t, a)
	assertSameProfiles(t, a, b)
	if err = json.Unmarshal([]byte(storeProfileDoc(b, "stage1")), &profile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, profile) {
		t.Fatal("encrypted sync changed device rules, defaults or DNS settings")
	}
}
