package subscriptions

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

func nodesDoc() *model.Doc {
	doc := model.EmptyDoc("linux")
	sub := &model.Subscription{ID: "s1", Tag: "S", NamePrefix: "P-", Detour: model.Some("out_d"), Enabled: true}
	doc.Subscriptions = []*model.Subscription{sub}
	keep := outboundOf("out_keep", "trojan", "P-keep", model.Fields{"__key": "trojan|k.example|443|pw", "__subscription_name": "keep"})
	keep.SubID, keep.Enabled = "s1", false
	gone := outboundOf("out_gone", "trojan", "P-gone", model.Fields{"__key": "trojan|g.example|443|pw"})
	gone.SubID = "s1"
	group := outboundOf("out_group", "selector", "G", model.Fields{})
	group.Members = []string{"out_keep", "out_gone"}
	taken := outboundOf("out_taken", "direct", "P-new", model.Fields{})
	doc.Outbounds = append(doc.Outbounds, keep, gone, group, taken)
	return doc
}

func TestApplyNodesKeepsIdentity(t *testing.T) {
	doc := nodesDoc()
	result := subscription.Result{Nodes: []subscription.Node{
		{Type: "trojan", Name: "keep", Key: "trojan|k.example|443|pw", Fields: map[string]any{"server": "k.example", "server_port": 443.0, "password": "pw"}},
		{Type: "trojan", Name: "new", Key: "trojan|n.example|443|pw", Fields: map[string]any{"server": "n.example", "server_port": 443.0, "password": "pw"}},
	}, Skipped: 1}
	msg := applyNodes(doc, "s1", result, 2, nil, 42)
	if msg != "解析到 2 个节点，跳过 3 个不支持的协议" {
		t.Errorf("message %q", msg)
	}
	nodes := subscriptionNodes(doc, "s1")
	if len(nodes) != 2 || nodes[0].ID != "out_keep" || nodes[0].Enabled || nodes[0].Tag != "P-keep" {
		t.Fatalf("kept node %+v", nodes[0])
	}
	if nodes[1].Tag != "P-new 2" || nodes[1].Fields["detour"] != "out_d" || nodes[1].Fields["__subscription_name"] != "new" {
		t.Errorf("new node %q %v", nodes[1].Tag, nodes[1].Fields)
	}
	for _, o := range doc.Outbounds {
		if o.ID == "out_group" && !slices.Equal(o.Members, []string{"out_keep"}) {
			t.Errorf("members %v", o.Members)
		}
	}
	if s := doc.Subscriptions[0]; s.UpdatedAt != 42 || s.LastError != "" {
		t.Errorf("subscription %+v", s)
	}
}

func TestApplyNodesWithoutNodes(t *testing.T) {
	doc := nodesDoc()
	if msg := applyNodes(doc, "s1", subscription.Result{Skipped: 2}, 0, nil, 1); msg != "没有解析到支持的节点（跳过 2 个）" {
		t.Errorf("message %q", msg)
	}
	if msg := applyNodes(doc, "s1", subscription.Result{}, 0, nil, 1); msg != "没有解析到任何节点" {
		t.Errorf("message %q", msg)
	}
	if len(subscriptionNodes(doc, "s1")) != 2 {
		t.Error("nodes changed")
	}
}

func TestRetagAndDetour(t *testing.T) {
	doc := nodesDoc()
	retagNodes(doc, "s1", "Q-")
	nodes := subscriptionNodes(doc, "s1")
	if nodes[0].Tag != "Q-keep" || nodes[1].Tag != "Q-P-gone" {
		t.Errorf("tags %q %q", nodes[0].Tag, nodes[1].Tag)
	}
	setNodesDetour(doc, "s1", "")
	if _, ok := nodes[0].Fields["detour"]; ok {
		t.Error("detour kept")
	}
}

func TestReorderTopLevel(t *testing.T) {
	doc := nodesDoc()
	keys := topLevelKeys(doc)
	if !slices.Equal(keys, []string{doc.Outbounds[0].ID, "out_group", "out_taken", "sub:s1"}) {
		t.Fatalf("keys %v", keys)
	}
	reorderTopLevelTo(doc, "sub:s1", doc.Outbounds[0].ID)
	if doc.OutboundOrder[0] != "sub:s1" {
		t.Errorf("order %v", doc.OutboundOrder)
	}
}

// TestApplyNodesDuplicateKeys: two fetched nodes with one key must not
// share the stored node's id.
func TestApplyNodesDuplicateKeys(t *testing.T) {
	doc := nodesDoc()
	key := "trojan|k.example|443|pw"
	result := subscription.Result{Nodes: []subscription.Node{
		{Type: "trojan", Name: "keep", Key: key, Fields: map[string]any{"server": "k.example"}},
		{Type: "trojan", Name: "keep copy", Key: key, Fields: map[string]any{"server": "k.example"}},
	}}
	applyNodes(doc, "s1", result, 0, nil, 1)
	nodes := subscriptionNodes(doc, "s1")
	if len(nodes) != 2 || nodes[0].ID != "out_keep" || nodes[1].ID == "out_keep" {
		t.Fatalf("ids %q %q", nodes[0].ID, nodes[1].ID)
	}
	if !nodes[1].Enabled {
		t.Error("the copy took the stored node's state")
	}
}

// TestApplyNodesKeyChanged: a node whose key changed (a new password)
// keeps its id, state and group membership by its name.
func TestApplyNodesKeyChanged(t *testing.T) {
	doc := nodesDoc()
	result := subscription.Result{Nodes: []subscription.Node{
		{Type: "trojan", Name: "keep", Key: "trojan|k.example|443|rotated", Fields: map[string]any{"server": "k.example", "password": "rotated"}},
	}}
	applyNodes(doc, "s1", result, 0, nil, 1)
	nodes := subscriptionNodes(doc, "s1")
	if len(nodes) != 1 || nodes[0].ID != "out_keep" || nodes[0].Enabled || nodes[0].Fields["password"] != "rotated" {
		t.Fatalf("node %+v", nodes[0])
	}
	for _, o := range doc.Outbounds {
		if o.ID == "out_group" && !slices.Equal(o.Members, []string{"out_keep"}) {
			t.Errorf("members %v", o.Members)
		}
	}
}

// TestApplyNodesClearsRemovedDefault: a group's default that left the
// subscription goes with its membership, so the group needs no fix.
func TestApplyNodesClearsRemovedDefault(t *testing.T) {
	doc := nodesDoc()
	var group *model.Outbound
	for _, o := range doc.Outbounds {
		if o.ID == "out_group" {
			group = o
		}
	}
	group.Fields["default_ref"] = "out_gone"
	other := outboundOf("out_other", "selector", "H", model.Fields{"default_ref": "out_keep"})
	other.Members = []string{"out_keep"}
	doc.Outbounds = append(doc.Outbounds, other)
	result := subscription.Result{Nodes: []subscription.Node{{Type: "trojan", Name: "keep", Key: "trojan|k.example|443|pw"}}}
	applyNodes(doc, "s1", result, 0, nil, 1)
	for _, o := range doc.Outbounds {
		switch o.ID {
		case "out_group":
			if _, ok := o.Fields["default_ref"]; ok {
				t.Errorf("group %v %v", o.Members, o.Fields)
			}
		case "out_other":
			if o.Fields["default_ref"] != "out_keep" {
				t.Errorf("kept default lost: %v", o.Fields)
			}
		}
	}
}

// TestPortOwnerIgnoresInboundsWithoutPort: a cleared port is not taken
// by a TUN inbound, which has none.
func TestPortOwnerIgnoresInboundsWithoutPort(t *testing.T) {
	doc := model.EmptyDoc("linux")
	tun := model.MakeInbound("tun", "tun-in", "linux", false)
	mixed := model.MakeInbound("mixed", "mixed-2", "linux", false)
	mixed.Fields["listen_port"] = ""
	doc.Inbounds = []*model.Inbound{tun}
	if owner := portOwner(doc, mixed); owner != nil {
		t.Fatalf("owner %q", owner.Tag)
	}
	other := model.MakeInbound("mixed", "mixed-1", "linux", false)
	other.Fields["listen_port"] = 7890.0
	mixed.Fields["listen_port"] = "7890"
	doc.Inbounds = append(doc.Inbounds, other)
	if owner := portOwner(doc, mixed); owner == nil || owner.Tag != "mixed-1" {
		t.Fatalf("owner %v", owner)
	}
}

// pointsAt reports a group member, default or order entry naming id.
func pointsAt(doc *model.Doc, id string) bool {
	for _, o := range doc.Outbounds {
		if slices.Contains(o.Members, id) || o.Fields["default_ref"] == id {
			return true
		}
	}
	return slices.Contains(doc.OutboundOrder, id)
}

// TestRemovePrunesReferences: deleting an outbound or a subscription, or
// a refresh dropping a node, leaves no member, default or order entry
// pointing at what is gone.
func TestRemovePrunesReferences(t *testing.T) {
	doc := nodesDoc()
	var group *model.Outbound
	for _, o := range doc.Outbounds {
		if o.ID == "out_group" {
			group = o
		}
	}
	group.Members = append(group.Members, "out_taken")
	group.Fields["default_ref"] = "out_taken"
	doc.OutboundOrder = []string{"out_taken", "out_group", "sub:s1"}
	removeOutbound(doc, "out_taken")
	if pointsAt(doc, "out_taken") {
		t.Errorf("outbound left: %v %v %v", group.Members, doc.Outbounds, doc.OutboundOrder)
	}

	result := subscription.Result{Nodes: []subscription.Node{{Type: "trojan", Name: "keep", Key: "trojan|k.example|443|pw"}}}
	applyNodes(doc, "s1", result, 0, nil, 1)
	if pointsAt(doc, "out_gone") {
		t.Error("dropped node left")
	}

	removeSubscription(doc, "s1")
	if pointsAt(doc, "out_keep") || pointsAt(doc, "sub:s1") || len(subscriptionNodes(doc, "s1")) != 0 {
		t.Errorf("subscription left: %v", doc.OutboundOrder)
	}
}
