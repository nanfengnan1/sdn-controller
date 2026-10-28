package resources

import (
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestVXLANOuterVRFMergePreservesIdentityAndExplicitZero(t *testing.T) {
	base := []*a.Operation{{Code: a.OpCode_VXLAN, Action: a.Action_CREATE, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: "vx1", Vni: 50000, Local: "192.0.2.1", Port: 4799, EncapVrfId: proto.Uint32(100)}}}}
	edit := &a.Operation{Code: a.OpCode_VXLAN, Action: a.Action_SET, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: "vx1", EncapVrfId: proto.Uint32(0)}}}
	got, err := Apply(base, []*a.Operation{edit})
	if err != nil {
		t.Fatal(err)
	}
	x := got[0].GetVxlan()
	if x.Vni != 50000 || x.Local != "192.0.2.1" || x.Port != 4799 || x.EncapVrfId == nil || *x.EncapVrfId != 0 {
		t.Fatal(x)
	}
	if _, err = Apply(nil, []*a.Operation{edit}); err == nil {
		t.Fatal("SET created absent VXLAN")
	}
	edit.GetVxlan().Vni = 3
	if _, err = Apply(base, []*a.Operation{edit}); err == nil {
		t.Fatal("SET changed immutable VNI")
	}
}
func TestVXLANOuterVRFRequiresExistingManagedTable(t *testing.T) {
	vx := &a.Operation{Code: a.OpCode_VXLAN, Action: a.Action_CREATE, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: "vx1", Vni: 50000, Local: "192.0.2.1", EncapVrfId: proto.Uint32(100)}}}
	if Validate([]*a.Operation{vx}) == nil {
		t.Fatal("unknown outer VRF accepted")
	}
	tenant := &a.Operation{Code: a.OpCode_TENANT, Action: a.Action_CREATE, Data: &a.Operation_Tenant{Tenant: &a.Tenant{Name: "OUTER", TableId: 100}}}
	if err := Validate([]*a.Operation{tenant, vx}); err != nil {
		t.Fatal(err)
	}
}
