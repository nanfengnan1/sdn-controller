package planner

import (
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"os"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"testing"
)

func example(t *testing.T) *p.Configuration {
	t.Helper()
	b, e := os.ReadFile("../../examples/evpn.json")
	if e != nil {
		t.Fatal(e)
	}
	c := &p.Configuration{}
	if e = protojson.Unmarshal(b, c); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestExampleCompilesEveryEVPNLayer(t *testing.T) {
	ops, e := Compile(example(t))
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"leaf1", "leaf2"} {
		seen := map[a.OpCode]bool{}
		for _, o := range ops[node] {
			seen[o.Code] = true
		}
		for _, code := range []a.OpCode{a.OpCode_TENANT, a.OpCode_BD, a.OpCode_BD_PORT, a.OpCode_BD_TENANT, a.OpCode_VXLAN, a.OpCode_TENANT_L3VNI, a.OpCode_OSPF_INSTANCE, a.OpCode_BGP_INSTANCE, a.OpCode_EVPN_L2VNI, a.OpCode_EVPN_TENANT} {
			if !seen[code] {
				t.Errorf("%s missing %s", node, code)
			}
		}
	}
	if len(ops["spine"]) == 0 {
		t.Fatal("missing RR")
	}
}
func TestGlobalConflictValidation(t *testing.T) {
	cases := map[string]func(*p.Configuration){
		"duplicate vni":               func(c *p.Configuration) { c.Networks[0].L2Vni = 50000 },
		"duplicate RD":                func(c *p.Configuration) { c.Networks[0].Placements[0].Rd = "10.255.0.11:50000" },
		"duplicate VTEP":              func(c *p.Configuration) { c.Nodes[2].VtepIp = c.Nodes[1].VtepIp },
		"duplicate router ID":         func(c *p.Configuration) { c.Nodes[2].RouterId = c.Nodes[1].RouterId },
		"overlapping subnet":          func(c *p.Configuration) { c.Networks[1].Placements[0].Gateways = []string{"192.168.10.254/25"} },
		"invalid RT range":            func(c *p.Configuration) { c.Tenants[0].ImportRts = []string{"70000:70000"} },
		"large VNI needs explicit RD": func(c *p.Configuration) { c.Networks[0].L2Vni = 75546; c.Networks[0].Placements[0].Rd = "" },
		"routed port membership":      func(c *p.Configuration) { c.Networks[0].Placements[0].Ports = []string{"uplink"} },
		"nil placement":               func(c *p.Configuration) { c.Tenants[0].Placements[0] = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := example(t)
			change(c)
			if _, e := Compile(c); e == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
func TestDeletionOrderingAndNoop(t *testing.T) {
	c := example(t)
	old, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	same, e := delta(old["leaf1"], old["leaf1"])
	if e != nil || len(same) != 0 {
		t.Fatal(e, same)
	}
	c.Networks = nil
	c.Tenants = nil
	next, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	ops, e := delta(old["leaf1"], next["leaf1"])
	if e != nil {
		t.Fatal(e)
	}
	lastTenant := -1
	lastVX := -1
	for i, o := range ops {
		if o.Code == a.OpCode_TENANT {
			lastTenant = i
		}
		if o.Code == a.OpCode_VXLAN {
			lastVX = i
		}
		if i > 0 && (len(o.DependsOn) != 1 || o.DependsOn[0] != ops[i-1].Id) {
			t.Fatal("broken dependency chain")
		}
	}
	if lastTenant < lastVX || lastTenant < 0 {
		t.Fatal("VRF deleted before VXLAN")
	}
}
func TestRejectInPlaceASNChange(t *testing.T) {
	c := example(t)
	old, _ := Compile(c)
	changed := proto.Clone(c).(*p.Configuration)
	changed.Nodes[1].Asn = 65001
	next, e := Compile(changed)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = delta(old["leaf1"], next["leaf1"]); e == nil {
		t.Fatal("ASN change silently leaves old BGP instance")
	}
}
func TestEVPNRTChangeRemovesOldValues(t *testing.T) {
	c := example(t)
	old, _ := Compile(c)
	c.Networks[0].ImportRts = []string{"65000:20010"}
	next, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	ops, e := delta(old["leaf1"], next["leaf1"])
	if e != nil || len(ops) != 2 || ops[0].Action != a.Action_DELETE || ops[1].Action != a.Action_SET {
		t.Fatal("RT replacement not complete", ops, e)
	}
}

// Regression from the real FRR 10 lab: deleting router bgp ... vrf before
// unbinding the tenant VNI fails with "Please unconfigure l3vni".
func TestTenantTeardownHonorsFRRAndLinuxDependencies(t *testing.T) {
	c := example(t)
	old, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	c.Networks = nil
	c.Tenants = nil
	next, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"leaf1", "leaf2"} {
		t.Run(node, func(t *testing.T) {
			ops, e := delta(old[node], next[node])
			if e != nil {
				t.Fatal(e)
			}
			positions := map[string]int{}
			for i, o := range ops {
				positions[o.Id] = i
				if i > 0 && (len(o.DependsOn) != 1 || o.DependsOn[0] != ops[i-1].Id) {
					t.Fatal("final Agent DAG does not preserve teardown ordering")
				}
			}
			before := func(first, second string) {
				t.Helper()
				a, okA := positions[first]
				b, okB := positions[second]
				if !okA || !okB || a >= b {
					t.Fatalf("expected %s before %s; positions=%v", first, second, positions)
				}
			}
			before("remove/evpn-tenant/tenant-a", "remove/l3vni/TENANT-A")
			before("remove/l3vni/TENANT-A", "remove/bgp/TENANT-A")
			before("remove/bgp/TENANT-A", "remove/tenant/TENANT-A")
			before("remove/port/vx50000", "remove/vxlan/vx50000")
			before("remove/port/vx50000", "remove/bd/br50000")
			before("remove/binding/br50000", "remove/bd/br50000")
			before("remove/bd/br50000", "remove/tenant/TENANT-A")
			segment := "10"
			if node == "leaf2" {
				segment = "20"
			}
			before("remove/port/host"+segment, "remove/bd/br"+segment)
			before("remove/address/br"+segment+"/192.168."+segment+".254/24", "remove/bd/br"+segment)
		})
	}
}
