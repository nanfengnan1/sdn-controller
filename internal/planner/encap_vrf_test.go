package planner

import (
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestVXLANOuterVRFDeltaPreservesNativeInterface(t *testing.T) {
	before := &a.Operation{Id: "vx1", Code: a.OpCode_VXLAN, Action: a.Action_CREATE, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: "vx1", Vni: 50000, Local: "192.0.2.1", Port: 4789, EncapVrfId: proto.Uint32(100)}}}
	after := proto.Clone(before).(*a.Operation)
	after.GetVxlan().EncapVrfId = proto.Uint32(0)
	ops, err := delta([]*a.Operation{before}, []*a.Operation{after})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Action != a.Action_SET || ops[0].GetVxlan().EncapVrfId == nil || ops[0].GetVxlan().Vni != 0 {
		t.Fatal(ops)
	}
	after.GetVxlan().Vni = 60000
	if _, err = delta([]*a.Operation{before}, []*a.Operation{after}); err == nil {
		t.Fatal("VNI was changed in place")
	}
}

func TestVXLANOuterVRFRemovalRequiresSeparateSwitch(t *testing.T) {
	tenant := &a.Operation{Id: "outer", Code: a.OpCode_TENANT, Action: a.Action_CREATE, Data: &a.Operation_Tenant{Tenant: &a.Tenant{Name: "OUTER", TableId: 100}}}
	vx := &a.Operation{Id: "vx", Code: a.OpCode_VXLAN, Action: a.Action_CREATE, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: "vx1", Vni: 10, Local: "192.0.2.1", EncapVrfId: proto.Uint32(100)}}}
	next := proto.Clone(vx).(*a.Operation)
	next.GetVxlan().EncapVrfId = proto.Uint32(0)
	if _, err := delta([]*a.Operation{tenant, vx}, []*a.Operation{next}); err == nil {
		t.Fatal("removed outer table before surviving tunnel moved")
	}
	ops, err := delta([]*a.Operation{tenant, vx}, nil)
	if err != nil || len(ops) != 2 || ops[0].Code != a.OpCode_VXLAN || ops[1].Code != a.OpCode_TENANT {
		t.Fatal(ops, err)
	}
}
