package resources

import (
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"testing"
)

func op(t *testing.T, s string) *a.Operation {
	t.Helper()
	o := &a.Operation{}
	if e := protojson.Unmarshal([]byte(s), o); e != nil {
		t.Fatal(e)
	}
	return o
}
func apply(t *testing.T, base []*a.Operation, ss ...string) []*a.Operation {
	t.Helper()
	edits := []*a.Operation{}
	for _, s := range ss {
		edits = append(edits, op(t, s))
	}
	out, e := Apply(base, edits)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestCanonicalIdentityAndPresence(t *testing.T) {
	base := apply(t, nil, `{"id":"untrusted","code":"BD","action":"CREATE","bridge":{"name":"br10","mtu":1500,"stp":true}}`)
	again := apply(t, base, `{"id":"different","code":"BD","action":"CREATE","bridge":{"name":"br10","mtu":1500,"stp":true}}`)
	if len(again) != 1 || again[0].Id == "untrusted" {
		t.Fatal("identity is not stable")
	}
	if _, e := Apply(base, []*a.Operation{op(t, `{"code":"BD","action":"CREATE","bridge":{"name":"br10","mtu":9000}}`)}); e == nil {
		t.Fatal("different create accepted")
	}
	got := apply(t, base, `{"code":"BD","action":"SET","bridge":{"name":"br10","stp":false,"ageingTime":0}}`)
	if got[0].GetBridge().GetStp() || got[0].GetBridge().GetMtu() != 1500 || got[0].GetBridge().AgeingTime == nil {
		t.Fatal("partial edit lost presence or unrelated fields")
	}
	if !base[0].GetBridge().GetStp() {
		t.Fatal("mutated input")
	}
}
func TestNetworkPartialSettingsAndIPv6Identity(t *testing.T) {
	base := apply(t, nil, `{"code":"NODE_NETWORK","action":"SET","network":{"settings":[{"key":"net.ipv4.ip_forward","value":"1"}]}}`)
	got := apply(t, base, `{"code":"NODE_NETWORK","action":"SET","network":{"settings":[{"key":"net.ipv6.conf.all.forwarding","value":"1"}]}}`)
	if len(got[0].GetNetwork().Settings) != 2 {
		t.Fatal("sysctl merge discarded old keys")
	}
	ips := apply(t, nil, `{"code":"ADDRESS","action":"CREATE","address":{"interface":"eth1","prefix":"2001:0db8:0:0::1/64"}}`, `{"code":"ADDRESS","action":"CREATE","address":{"interface":"eth1","prefix":"2001:db8::1/64"}}`)
	if len(ips) != 1 || ips[0].GetAddress().Prefix != "2001:db8::1/64" {
		t.Fatal("IPv6 identity normalization")
	}
	peers := apply(t, nil, `{"code":"BGP_NEIGHBOR","action":"CREATE","peer":{"asn":65000,"name":"2001:0db8:0:0::2","remoteAs":65001}}`, `{"code":"BGP_NEIGHBOR","action":"CREATE","peer":{"asn":65000,"name":"2001:db8::2","remoteAs":65001}}`)
	if len(peers) != 1 {
		t.Fatal("IPv6 peer identity duplicated")
	}
	removed := apply(t, peers, `{"code":"BGP_NEIGHBOR","action":"DELETE","peer":{"name":"2001:0db8:0:0::2"}}`)
	if len(removed) != 0 {
		t.Fatal("canonical neighbor removal failed")
	}
}
func TestMalformedResourcesFailWithoutPanic(t *testing.T) {
	if _, e := Apply([]*a.Operation{nil}, nil); e == nil {
		t.Fatal("nil resource accepted")
	}
	if _, e := Apply(nil, []*a.Operation{op(t, `{"code":"NODE_NETWORK","action":"SET","network":{"settings":[{"key":"a","value":"1"},{"key":"a","value":"2"}]}}`)}); e == nil {
		t.Fatal("duplicate sysctl accepted")
	}
}
func TestReferencedDeletionAndIndependentTenant(t *testing.T) {
	base := apply(t, nil, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`, `{"code":"BD","action":"CREATE","bridge":{"name":"br10"}}`, `{"code":"BD_TENANT","action":"BIND","binding":{"bridge":"br10","tenant":"A"}}`, `{"code":"ADDRESS","action":"CREATE","address":{"interface":"br10","prefix":"192.0.2.1/24"}}`)
	if e := Validate(base); e != nil {
		t.Fatal(e)
	}
	next := apply(t, base, `{"code":"BD","action":"DELETE","bridge":{"name":"br10"}}`)
	if _, e := CheckRemoved(base, next); e == nil {
		t.Fatal("BD deletion left references")
	}
	next = apply(t, base, `{"code":"TENANT","action":"DELETE","tenant":{"name":"A"}}`)
	if e := Validate(next); e == nil {
		t.Fatal("VRF deletion left binding")
	}
	standalone := apply(t, nil, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`)
	if e := Validate(standalone); e != nil {
		t.Fatal("standalone tenant required EVPN", e)
	}
}
func TestBridgePolicyPersistsBeforeNewVxlan(t *testing.T) {
	base := apply(t, nil, `{"code":"BD","action":"CREATE","bridge":{"name":"br10"}}`, `{"code":"BD_PORT","action":"BIND","bridgePort":{"bridge":"br10","port":"host10"}}`)
	yes := true
	nr := &p.NodeResources{NodeId: "node", Objects: base, BridgePolicies: []*p.BridgePolicy{{Bridge: "br10", NeighSuppress: &yes}}}
	if _, e := Compile(nr); e != nil {
		t.Fatal(e)
	}
	nr.Objects = apply(t, nr.Objects, `{"code":"VXLAN","action":"CREATE","vxlan":{"name":"vx10","vni":10010,"local":"10.0.0.1"}}`, `{"code":"BD_PORT","action":"BIND","bridgePort":{"bridge":"br10","port":"vx10"}}`)
	compiled, e := Compile(nr)
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range compiled {
		if p := o.GetBridgePort(); p != nil {
			if p.Port == "host10" && p.NeighSuppress != nil {
				t.Fatal("host port suppression changed")
			}
			if p.Port == "vx10" && !p.GetNeighSuppress() {
				t.Fatal("new tunnel did not inherit policy")
			}
		}
	}
	for _, o := range nr.Objects {
		if p := o.GetBridgePort(); p != nil && p.NeighSuppress != nil {
			t.Fatal("compile mutated desired")
		}
	}
}
func TestReplayDeletesAndOptionalUpdates(t *testing.T) {
	journal := []*a.Operation{op(t, `{"code":"TENANT","action":"CREATE","tenant":{"name":"old","tableId":1001}}`), op(t, `{"code":"INTERFACE","action":"SET","interface":{"name":"old","up":true}}`), op(t, `{"code":"TENANT","action":"DELETE","tenant":{"name":"old","tableId":1001}}`), op(t, `{"code":"TENANT","action":"CREATE","tenant":{"name":"new","tableId":1002}}`)}
	got, e := Replay(journal)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 1 || got[0].GetTenant().Name != "new" {
		t.Fatal("history was not materialized")
	}
	second, e := Apply(got, nil)
	if e != nil || !proto.Equal(&p.NodeResources{Objects: got}, &p.NodeResources{Objects: second}) {
		t.Fatal("not canonical", e)
	}
}

func TestBridgeInterfaceSharedAttributes(t *testing.T) {
	base := apply(t, nil, `{"code":"BD","action":"CREATE","bridge":{"name":"br10","mtu":1500}}`, `{"code":"INTERFACE","action":"SET","interface":{"name":"br10","mtu":1500,"up":true}}`)
	changed := apply(t, base, `{"code":"BD","action":"SET","bridge":{"name":"br10","mtu":9000}}`, `{"code":"INTERFACE","action":"SET","interface":{"name":"br10","up":false}}`)
	for _, o := range changed {
		if o.GetBridge() != nil && o.GetBridge().GetMtu() != 9000 || o.GetInterface() != nil && o.GetInterface().GetMtu() != 9000 {
			t.Fatal("shared attribute regressed")
		}
	}
	if e := Validate(changed); e != nil {
		t.Fatal(e)
	}
	conflict := []*a.Operation{op(t, `{"code":"BD","action":"CREATE","bridge":{"name":"br10","mtu":9000}}`), op(t, `{"code":"INTERFACE","action":"SET","interface":{"name":"br10","mtu":1500}}`)}
	if e := Validate(conflict); e == nil {
		t.Fatal("contradictory full target accepted")
	}
}
