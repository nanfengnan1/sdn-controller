package planner

import (
	"fmt"
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestDualStackVTEPPlan(t *testing.T) {
	c := example(t)
	for i, n := range c.Nodes {
		if n.Role == "leaf" {
			n.VtepIp = fmt.Sprintf("2001:db8::%d", i+1)
		}
		for j, peer := range n.Peers {
			peer.Address = fmt.Sprintf("2001:db8:1::%d", j+1)
			peer.UpdateSource = fmt.Sprintf("2001:db8:2::%d", i+1)
		}
	}
	ops, e := Compile(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, list := range ops {
		for _, o := range list {
			if o.Code == a.OpCode_NODE_NETWORK {
				for _, s := range o.GetNetwork().Settings {
					if (s.Key == "net.ipv6.conf.all.disable_ipv6" || s.Key == "net.ipv6.conf.default.disable_ipv6") && s.Value != "0" {
						t.Fatal("plan disables IPv6")
					}
				}
			}
		}
	}
}

func TestIPv6RouterIDRejected(t *testing.T) {
	c := example(t)
	c.Nodes[0].RouterId = "2001:db8::1"
	if _, err := Compile(c); err == nil {
		t.Fatal("IPv6 is not a BGP router-id")
	}
}
