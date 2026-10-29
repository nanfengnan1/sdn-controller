package resources

import (
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestIPSRuleLifecycle(t *testing.T) {
	op := &a.Operation{Code: a.OpCode_IPS_RULE, Action: a.Action_CREATE, Data: &a.Operation_IpsRule{IpsRule: &a.IpsRule{Sid: 1000001, Rule: "drop icmp any any -> any any (sid:1000001;)"}}}
	objs, e := Apply(nil, []*a.Operation{op})
	if e != nil {
		t.Fatal(e)
	}
	if objs[0].Id != "resource/ips_rule/1000001" {
		t.Fatal(objs[0].Id)
	}
	if e = Validate(objs); e != nil {
		t.Fatal(e)
	}
	op.Action = a.Action_SET
	op.GetIpsRule().Rule = "alert icmp any any -> any any (sid:1000001;)"
	objs, e = Apply(objs, []*a.Operation{op})
	if e != nil {
		t.Fatal(e)
	}
	if objs[0].GetIpsRule().Rule != op.GetIpsRule().Rule {
		t.Fatal("update lost")
	}
	op.Action = a.Action_DELETE
	objs, e = Apply(objs, []*a.Operation{op})
	if e != nil || len(objs) != 0 {
		t.Fatal(objs, e)
	}
	op.Action = a.Action_SET
	if _, e = Apply(nil, []*a.Operation{op}); e == nil {
		t.Fatal("SET missing rule accepted")
	}
}
func TestIPSBindingAndOrder(t *testing.T) {
	rule := &a.Operation{Code: a.OpCode_IPS_RULE, Action: a.Action_CREATE, Data: &a.Operation_IpsRule{IpsRule: &a.IpsRule{Sid: 1, Rule: "alert ip any any -> any any (sid:1;)"}}}
	bind := &a.Operation{Code: a.OpCode_IPS_INTERFACE, Action: a.Action_CREATE, Data: &a.Operation_IpsInterface{IpsInterface: &a.IpsInterface{Name: "GigabitEthernet3b/0/0", Direction: "input"}}}
	objs, e := Apply(nil, []*a.Operation{bind, rule})
	if e != nil {
		t.Fatal(e)
	}
	if e = Validate(objs); e != nil {
		t.Fatal(e)
	}
	if objs[0].Code != a.OpCode_IPS_RULE {
		t.Fatal("attach must follow rules")
	}
	objs[1].GetIpsInterface().Direction = "invalid"
	if Validate(objs) == nil {
		t.Fatal("invalid direction accepted")
	}
}
