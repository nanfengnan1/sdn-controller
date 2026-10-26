package planner

import (
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/resources"
	"testing"
)

func TestLegacyTenantWithoutL3VNIAugmentsIncrementally(t *testing.T) {
	full := example(t)
	basic := proto.Clone(full).(*p.Configuration)
	basic.Networks = nil
	for _, t := range basic.Tenants {
		t.L3Vni = 0
		t.ImportRts = nil
		t.ExportRts = nil
		for _, x := range t.Placements {
			x.RouterMac = ""
			x.Rd = ""
		}
	}
	before, e := Compile(basic)
	if e != nil {
		t.Fatal(e)
	}
	after, e := Compile(full)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"leaf1", "leaf2"} {
		ops, e := delta(before[node], after[node])
		if e != nil {
			t.Fatal(e)
		}
		for _, o := range ops {
			if o.Code == a.OpCode_TENANT {
				t.Fatal("existing VRF recreated")
			}
		}
	}
}
func TestMixedResourceOwnershipRejected(t *testing.T) {
	c := example(t)
	c.NodeResources = []*p.NodeResources{{NodeId: c.Nodes[0].NodeId}}
	if _, e := Compile(c); e == nil {
		t.Fatal("overlapping ownership accepted")
	}
}
func TestResourceDeleteOrdersNewProtocols(t *testing.T) {
	old := []*a.Operation{
		{Id: "vrf", Code: a.OpCode_TENANT, Action: a.Action_CREATE, Data: &a.Operation_Tenant{Tenant: &a.Tenant{Name: "A", TableId: 1001}}},
		{Id: "l3", Code: a.OpCode_TENANT_L3VNI, Action: a.Action_BIND, Data: &a.Operation_L3Vni{L3Vni: &a.L3Vni{Tenant: "A", Vni: 50000}}},
		{Id: "bgp", Code: a.OpCode_BGP_INSTANCE, Action: a.Action_CREATE, Data: &a.Operation_Bgp{Bgp: &a.Bgp{Vrf: "A", Asn: 65000}}},
		{Id: "type5", Code: a.OpCode_EVPN_TYPE5, Action: a.Action_SET, Data: &a.Operation_BgpType5{BgpType5: &a.BgpType5{Vrf: "A", Asn: 65000, Family: a.AddressFamily_IPV6}}},
		{Id: "ospf", Code: a.OpCode_OSPF6_INSTANCE, Action: a.Action_CREATE, Data: &a.Operation_Ospf{Ospf: &a.Ospf{Vrf: "A"}}},
		{Id: "ospfif", Code: a.OpCode_OSPF6_INTERFACE, Action: a.Action_SET, Data: &a.Operation_Ospf6Interface{Ospf6Interface: &a.Ospf6Interface{Vrf: "A", Name: "br10"}}},
	}
	resources.Sort(old)
	ops, e := delta(old, nil)
	if e != nil {
		t.Fatal(e)
	}
	at := map[a.OpCode]int{}
	for i, o := range ops {
		at[o.Code] = i
	}
	if at[a.OpCode_EVPN_TYPE5] > at[a.OpCode_TENANT_L3VNI] || at[a.OpCode_TENANT_L3VNI] > at[a.OpCode_BGP_INSTANCE] || at[a.OpCode_OSPF6_INTERFACE] > at[a.OpCode_OSPF6_INSTANCE] || at[a.OpCode_BGP_INSTANCE] > at[a.OpCode_TENANT] {
		t.Fatal("invalid teardown ordering")
	}
}
