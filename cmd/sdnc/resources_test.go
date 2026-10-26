package main

import (
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestResourceCLIStructuredCommands(t *testing.T) {
	cases := []struct {
		args []string
		code a.OpCode
	}{
		{[]string{"tenant", "create", "A", "--table-id", "1001"}, a.OpCode_TENANT},
		{[]string{"bd", "address-add", "br10", "--prefix", "2001:db8::1/64"}, a.OpCode_ADDRESS},
		{[]string{"vxlan", "bind", "vx10", "--bridge", "br10"}, a.OpCode_BD_PORT},
		{[]string{"bgp", "instance", "create", "--asn", "65000", "--router-id", "1.1.1.1"}, a.OpCode_BGP_INSTANCE},
		{[]string{"bgp", "af-peer", "set", "2001:db8::2", "--asn", "65000", "--family", "ipv6", "--active", "false"}, a.OpCode_BGP_AF_PEER},
		{[]string{"bgp", "type5", "set", "--asn", "65000", "--vrf", "A", "--family", "ipv6", "--enabled", "true"}, a.OpCode_EVPN_TYPE5},
		{[]string{"ospf", "interface", "set", "eth1", "--version", "3", "--area", "0", "--passive", "false"}, a.OpCode_OSPF6_INTERFACE},
	}
	for _, tt := range cases {
		got, e := parseResourceCLI(tt.args)
		if e != nil || got.op == nil || got.op.Code != tt.code {
			t.Fatalf("%v: %v %v", tt.args, got, e)
		}
	}
	r, e := parseResourceCLI([]string{"bgp", "af-peer", "set", "10.0.0.1", "--asn", "65000", "--family", "ipv6", "--active", "false"})
	if e != nil || r.op.GetPeer().Active == nil || r.op.GetPeer().GetActive() {
		t.Fatal("false presence lost")
	}
}
func TestCLIQueriesAndUnknownFlags(t *testing.T) {
	r, e := parseResourceCLI([]string{"bd", "fdb", "br10", "--page-size", "50"})
	if e != nil || r.query.Kind != a.QueryKind_FDB || r.query.Bridge != "br10" {
		t.Fatal(r, e)
	}
	r, e = parseResourceCLI([]string{"ospf", "neighbors", "--version", "3"})
	if e != nil || r.query.OspfVersion != 3 {
		t.Fatal(r, e)
	}
	if _, e = parseResourceCLI([]string{"tenant", "create", "A", "--tableid", "1001"}); e == nil {
		t.Fatal("typo ignored")
	}
	if _, e = parseResourceCLI([]string{"bgp", "network", "create", "192.0.2.0/24", "--family", "banana"}); e == nil {
		t.Fatal("invalid AF")
	}
}
