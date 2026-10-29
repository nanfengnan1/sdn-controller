// Package planner compiles typed global resources into the existing Agent protocol.
package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/resources"
)

var ifaceRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)
var idRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,63}$`)
var rtRE = regexp.MustCompile(`^([0-9]+|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+):[0-9]+$`)

func validRT(s string) bool {
	if !rtRE.MatchString(s) {
		return false
	}
	parts := strings.Split(s, ":")
	suffix, e := strconv.ParseUint(parts[1], 10, 32)
	if e != nil {
		return false
	}
	if strings.Contains(parts[0], ".") {
		return ipv4(parts[0]) && suffix <= 65535
	}
	asn, e := strconv.ParseUint(parts[0], 10, 32)
	return e == nil && asn > 0 && (asn <= 65535 || suffix <= 65535)
}

func ptr[T any](x T) *T  { return &x }
func ipv4(s string) bool { x, e := netip.ParseAddr(s); return e == nil && x.Is4() }
func unicastIP(s string) bool {
	x, e := netip.ParseAddr(s)
	return e == nil && !x.IsUnspecified() && !x.IsMulticast() && !x.IsLinkLocalUnicast() && x.Zone() == ""
}
func validMAC(s string) bool { x, e := net.ParseMAC(s); return e == nil && len(x) == 6 && x[0]&1 == 0 }
func vni(v uint32) bool      { return v > 0 && v <= 16777215 }
func Compile(c *p.Configuration) (map[string][]*a.Operation, error) {
	if c == nil {
		return nil, fmt.Errorf("complete configuration is required")
	}
	if len(c.Nodes)+len(c.NodeResources) > 64 || len(c.Tenants) > 128 || len(c.Networks) > 256 {
		return nil, fmt.Errorf("v1 configuration size limit exceeded")
	}
	nodes := map[string]*p.NodeProfile{}
	tenants := map[string]*p.Tenant{}
	usedVNI := map[string]string{}
	names := map[string]map[string]string{}
	tables := map[string]bool{}
	ports := map[string]bool{}
	addresses := map[string]bool{}
	rds := map[string]bool{}
	claimRD := func(node, rd string, v uint32) error {
		if rd == "" {
			if v > 65535 {
				return fmt.Errorf("VNI %d requires an explicit unique RD", v)
			}
			rd = fmt.Sprintf("%s:%d", nodes[node].RouterId, v)
		}
		if !validRT(rd) {
			return fmt.Errorf("invalid RD %s", rd)
		}
		if rds[rd] {
			return fmt.Errorf("RD %s must be unique per EVPN instance", rd)
		}
		rds[rd] = true
		return nil
	}
	reserve := func(node, name, owner string) error {
		if !ifaceRE.MatchString(name) {
			return fmt.Errorf("invalid interface name %q", name)
		}
		if names[node] == nil {
			names[node] = map[string]string{}
		}
		if prev := names[node][name]; prev != "" {
			return fmt.Errorf("node %s interface %s belongs to both %s and %s", node, name, prev, owner)
		}
		names[node][name] = owner
		return nil
	}
	claimVNI := func(f string, v uint32, owner string) error {
		if !vni(v) {
			return fmt.Errorf("invalid VNI %d", v)
		}
		k := fmt.Sprintf("%s/%d", f, v)
		if usedVNI[k] != "" {
			return fmt.Errorf("VNI %d conflicts in fabric %s", v, f)
		}
		usedVNI[k] = owner
		return nil
	}
	for _, n := range c.Nodes {
		if n == nil || n.NodeId == "" || nodes[n.NodeId] != nil {
			return nil, fmt.Errorf("duplicate or empty node_id")
		}
		if n.FabricId == "" || !ipv4(n.RouterId) || n.Asn == 0 {
			return nil, fmt.Errorf("node %s requires fabric_id, IPv4 router_id, ASN", n.NodeId)
		}
		if n.Role != "leaf" && n.Role != "spine" {
			return nil, fmt.Errorf("node role must be leaf or spine")
		}
		if n.Role == "leaf" && !unicastIP(n.VtepIp) {
			return nil, fmt.Errorf("leaf VTEP requires unicast IPv4/IPv6")
		}
		nodes[n.NodeId] = n
		for _, kind := range []string{"router", "vtep"} {
			s := n.RouterId
			if kind == "vtep" {
				s = n.VtepIp
			}
			if s != "" {
				key := n.FabricId + "/" + kind + "/" + s
				if addresses[key] {
					return nil, fmt.Errorf("duplicate %s IP %s in fabric", kind, s)
				}
				addresses[key] = true
			}
		}
		for _, i := range n.Interfaces {
			if i == nil {
				return nil, fmt.Errorf("nil interface")
			}
			if e := reserve(n.NodeId, i.Name, "interface"); e != nil {
				return nil, e
			}
			if i.Mtu != 0 && (i.Mtu < 68 || i.Mtu > 65535) {
				return nil, fmt.Errorf("invalid MTU")
			}
			if i.Mac != "" && !validMAC(i.Mac) {
				return nil, fmt.Errorf("invalid MAC")
			}
			seen := map[string]bool{}
			for _, prefix := range i.Addresses {
				x, e := netip.ParsePrefix(prefix)
				if e != nil || seen[prefix] {
					return nil, fmt.Errorf("invalid/duplicate prefix %s", prefix)
				}
				if i.Ospf && !x.Addr().Is4() {
					return nil, fmt.Errorf("OSPFv2 requires IPv4")
				}
				seen[prefix] = true
			}
		}
		seenPeer := map[string]bool{}
		for _, peer := range n.Peers {
			if peer == nil || !unicastIP(peer.Address) || peer.RemoteAs == 0 || seenPeer[peer.Address] {
				return nil, fmt.Errorf("invalid/duplicate BGP peer")
			}
			if peer.UpdateSource != "" && !ifaceRE.MatchString(peer.UpdateSource) && !unicastIP(peer.UpdateSource) {
				return nil, fmt.Errorf("invalid BGP update_source")
			}
			seenPeer[peer.Address] = true
		}
	}
	tenantNodes := map[string]map[string]*p.TenantPlacement{}
	for _, t := range c.Tenants {
		if t == nil || !idRE.MatchString(t.Id) || tenants[t.Id] != nil || !ifaceRE.MatchString(t.Name) || t.FabricId == "" || len(t.Placements) == 0 {
			return nil, fmt.Errorf("invalid/duplicate tenant or empty placements")
		}
		tenants[t.Id] = t
		if t.L3Vni != 0 {
			if e := claimVNI(t.FabricId, t.L3Vni, t.Id); e != nil {
				return nil, e
			}
		}
		tenantNodes[t.Id] = map[string]*p.TenantPlacement{}
		if t.L3Vni != 0 && (len(t.ImportRts) == 0 || len(t.ExportRts) == 0) {
			return nil, fmt.Errorf("tenant RTs must be explicit")
		}
		for _, s := range append(append([]string{}, t.ImportRts...), t.ExportRts...) {
			if !validRT(s) {
				return nil, fmt.Errorf("invalid RT %s", s)
			}
		}
		for _, x := range t.Placements {
			if x == nil {
				return nil, fmt.Errorf("nil placement")
			}
			n := nodes[x.NodeId]
			if n == nil || n.Role != "leaf" || n.FabricId != t.FabricId || tenantNodes[t.Id][x.NodeId] != nil {
				return nil, fmt.Errorf("invalid/duplicate tenant placement %s", x.NodeId)
			}
			if x.TableId == 0 || (x.TableId >= 253 && x.TableId <= 255) || (t.L3Vni != 0 && !validMAC(x.RouterMac)) {
				return nil, fmt.Errorf("invalid table_id/router_mac")
			}
			k := fmt.Sprintf("%s/%d", x.NodeId, x.TableId)
			if tables[k] {
				return nil, fmt.Errorf("VRF table conflict")
			}
			tables[k] = true
			if t.L3Vni != 0 {
				if e := claimRD(x.NodeId, x.Rd, t.L3Vni); e != nil {
					return nil, e
				}
			}
			resourceNames := []string{t.Name}
			if t.L3Vni != 0 {
				resourceNames = append(resourceNames, l3Bridge(t, x), l3VX(t, x))
			}
			for _, name := range resourceNames {
				if e := reserve(x.NodeId, name, t.Id); e != nil {
					return nil, e
				}
			}
			tenantNodes[t.Id][x.NodeId] = x
		}
	}
	networkIDs := map[string]bool{}
	type subnet struct {
		tenant, network string
		prefix          netip.Prefix
	}
	var subnets []subnet
	for _, nw := range c.Networks {
		if nw == nil || !idRE.MatchString(nw.Id) || networkIDs[nw.Id] || tenants[nw.TenantId] == nil || len(nw.Placements) == 0 {
			return nil, fmt.Errorf("invalid network or missing tenant")
		}
		networkIDs[nw.Id] = true
		t := tenants[nw.TenantId]
		if e := claimVNI(t.FabricId, nw.L2Vni, nw.Id); e != nil {
			return nil, e
		}
		for _, s := range append(append([]string{}, nw.ImportRts...), nw.ExportRts...) {
			if !validRT(s) {
				return nil, fmt.Errorf("invalid network RT")
			}
		}
		placed := map[string]bool{}
		for _, x := range nw.Placements {
			if x == nil || tenantNodes[t.Id][x.NodeId] == nil || placed[x.NodeId] {
				return nil, fmt.Errorf("network placement needs matching tenant placement")
			}
			placed[x.NodeId] = true
			if !validMAC(x.Mac) {
				return nil, fmt.Errorf("network gateway MAC required")
			}
			if e := claimRD(x.NodeId, x.Rd, nw.L2Vni); e != nil {
				return nil, e
			}
			if (len(nw.ImportRts) == 0 || len(nw.ExportRts) == 0) && !validRT(fmt.Sprintf("%d:%d", nodes[x.NodeId].Asn, nw.L2Vni)) {
				return nil, fmt.Errorf("network requires explicit RT for four-byte ASN / large VNI")
			}
			for _, name := range []string{l2Bridge(nw, x), l2VX(nw, x)} {
				if e := reserve(x.NodeId, name, nw.Id); e != nil {
					return nil, e
				}
			}
			seenGw := map[string]bool{}
			for _, g := range x.Gateways {
				if _, e := netip.ParsePrefix(g); e != nil || seenGw[g] {
					return nil, fmt.Errorf("invalid/duplicate gateway %s", g)
				}
				seenGw[g] = true
				prefix, _ := netip.ParsePrefix(g)
				for _, old := range subnets {
					if old.tenant == nw.TenantId && old.network != nw.Id && old.prefix.Overlaps(prefix) {
						return nil, fmt.Errorf("overlapping subnets in tenant %s", nw.TenantId)
					}
				}
				subnets = append(subnets, subnet{nw.TenantId, nw.Id, prefix.Masked()})
			}
			for _, port := range x.Ports {
				key := x.NodeId + "/" + port
				if ports[key] || names[x.NodeId][port] != "interface" || port == "lo" {
					return nil, fmt.Errorf("port %s must exist once in NodeProfile interfaces and belong to one network", port)
				}
				for _, i := range nodes[x.NodeId].Interfaces {
					if i.Name == port && (len(i.Addresses) > 0 || i.Ospf) {
						return nil, fmt.Errorf("routed interface cannot be a bridge port")
					}
				}
				ports[key] = true
			}
		}
	}
	result := map[string][]*a.Operation{}
	for _, n := range c.Nodes {
		var ops []*a.Operation
		add := func(id string, o *a.Operation) { o.Id = id; ops = append(ops, o) }
		intf := func(name string, mtu uint32, mac string, none bool) {
			x := &a.Interface{Name: name, Up: ptr(true)}
			if mtu != 0 {
				x.Mtu = ptr(mtu)
			}
			if mac != "" {
				x.Mac = ptr(mac)
			}
			if none {
				x.Addrgenmode = ptr("none")
			}
			add("interface/"+name, &a.Operation{Code: a.OpCode_INTERFACE, Action: a.Action_SET, Data: &a.Operation_Interface{Interface: x}})
		}
		addr := func(name, prefix string) {
			add("address/"+name+"/"+prefix, &a.Operation{Code: a.OpCode_ADDRESS, Action: a.Action_CREATE, Data: &a.Operation_Address{Address: &a.Address{Interface: name, Prefix: prefix}}})
		}
		bind := func(bridge, port string, vx bool) {
			x := &a.BridgePort{Bridge: bridge, Port: port}
			if vx {
				x.Learning = ptr(false)
				x.NeighSuppress = ptr(true)
			}
			add("port/"+port, &a.Operation{Code: a.OpCode_BD_PORT, Action: a.Action_BIND, Data: &a.Operation_BridgePort{BridgePort: x}})
		}
		bridge := func(name, mac, tenant string) {
			add("bd/"+name, &a.Operation{Code: a.OpCode_BD, Action: a.Action_CREATE, Data: &a.Operation_Bridge{Bridge: &a.Bridge{Name: name, Stp: ptr(false), Mtu: ptr(uint32(1500)), Mac: ptr(mac)}}})
			add("binding/"+name, &a.Operation{Code: a.OpCode_BD_TENANT, Action: a.Action_BIND, Data: &a.Operation_Binding{Binding: &a.Binding{Bridge: name, Tenant: tenant}}})
			intf(name, 1500, "", true)
		}
		vxlan := func(name string, v uint32, bd string) {
			add("vxlan/"+name, &a.Operation{Code: a.OpCode_VXLAN, Action: a.Action_CREATE, Data: &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: name, Vni: v, Local: n.VtepIp, Port: 4789}}})
			bind(bd, name, true)
			intf(name, 1500, "", true)
		}
		if n.ConfigureSystem {
			settings := []*a.Sysctl{}
			for _, s := range [][2]string{{"net.ipv4.conf.all.rp_filter", "0"}, {"net.ipv4.conf.default.rp_filter", "0"}, {"net.ipv6.conf.all.disable_ipv6", "0"}, {"net.ipv6.conf.default.disable_ipv6", "0"}, {"net.ipv4.ip_forward", "1"}, {"net.ipv6.conf.all.forwarding", "1"}} {
				settings = append(settings, &a.Sysctl{Key: s[0], Value: s[1]})
			}
			for _, f := range []string{"iptables", "ip6tables", "arptables"} {
				settings = append(settings, &a.Sysctl{Key: "net.bridge.bridge-nf-call-" + f, Value: "0", Optional: true})
			}
			add("system", &a.Operation{Code: a.OpCode_NODE_NETWORK, Action: a.Action_SET, Data: &a.Operation_Network{Network: &a.Network{Settings: settings}}})
		}
		for _, i := range n.Interfaces {
			intf(i.Name, i.Mtu, i.Mac, false)
			for _, prefix := range i.Addresses {
				addr(i.Name, prefix)
			}
		}
		for _, t := range c.Tenants {
			if x := tenantNodes[t.Id][n.NodeId]; x != nil {
				add("tenant/"+t.Name, &a.Operation{Code: a.OpCode_TENANT, Action: a.Action_CREATE, Data: &a.Operation_Tenant{Tenant: &a.Tenant{Name: t.Name, TableId: x.TableId}}})
				intf(t.Name, 0, "", false)
				if t.L3Vni == 0 {
					continue
				}
				bridge(l3Bridge(t, x), x.RouterMac, t.Name)
				vxlan(l3VX(t, x), t.L3Vni, l3Bridge(t, x))
				add("l3vni/"+t.Name, &a.Operation{Code: a.OpCode_TENANT_L3VNI, Action: a.Action_BIND, Data: &a.Operation_L3Vni{L3Vni: &a.L3Vni{Tenant: t.Name, Vni: t.L3Vni}}})
			}
		}
		for _, nw := range c.Networks {
			for _, x := range nw.Placements {
				if x.NodeId != n.NodeId {
					continue
				}
				t := tenants[nw.TenantId]
				bd := l2Bridge(nw, x)
				bridge(bd, x.Mac, t.Name)
				for _, g := range x.Gateways {
					addr(bd, g)
				}
				for _, port := range x.Ports {
					bind(bd, port, false)
				}
				vxlan(l2VX(nw, x), nw.L2Vni, bd)
			}
		}
		if n.Ospf {
			add("ospf", &a.Operation{Code: a.OpCode_OSPF_INSTANCE, Action: a.Action_CREATE, Data: &a.Operation_Ospf{Ospf: &a.Ospf{RouterId: n.RouterId}}})
			for _, i := range n.Interfaces {
				if !i.Ospf {
					continue
				}
				x := &a.OspfInterface{Name: i.Name, Passive: ptr(i.Passive)}
				if !i.Passive {
					x.NetworkType = "point-to-point"
				}
				add("ospf-if/"+i.Name, &a.Operation{Code: a.OpCode_OSPF_INTERFACE, Action: a.Action_SET, Data: &a.Operation_OspfInterface{OspfInterface: x}})
				for _, prefix := range i.Addresses {
					pfx, _ := netip.ParsePrefix(prefix)
					add("ospf-net/"+pfx.Masked().String(), &a.Operation{Code: a.OpCode_OSPF_NETWORK, Action: a.Action_CREATE, Data: &a.Operation_OspfNetwork{OspfNetwork: &a.OspfNetwork{Prefix: pfx.Masked().String(), Area: "0"}}})
				}
			}
		}
		bgp := func(vrf string) {
			add("bgp/"+vrf, &a.Operation{Code: a.OpCode_BGP_INSTANCE, Action: a.Action_CREATE, Data: &a.Operation_Bgp{Bgp: &a.Bgp{Asn: n.Asn, Vrf: vrf, RouterId: n.RouterId, DefaultIpv4Unicast: ptr(false)}}})
		}
		bgp("")
		for _, peer := range n.Peers {
			source := peer.UpdateSource
			if source == "" {
				source = "lo"
			}
			add("peer/"+peer.Address, &a.Operation{Code: a.OpCode_BGP_NEIGHBOR, Action: a.Action_CREATE, Data: &a.Operation_Peer{Peer: &a.Peer{Asn: n.Asn, Name: peer.Address, RemoteAs: peer.RemoteAs, UpdateSource: source}}})
			add("evpn-peer/"+peer.Address, &a.Operation{Code: a.OpCode_BGP_EVPN_PEER, Action: a.Action_SET, Data: &a.Operation_Peer{Peer: &a.Peer{Asn: n.Asn, Name: peer.Address, Active: ptr(true), RrClient: ptr(peer.RrClient)}}})
		}
		if n.Role == "leaf" {
			add("evpn-global", &a.Operation{Code: a.OpCode_BGP_EVPN_GLOBAL, Action: a.Action_SET, Data: &a.Operation_EvpnGlobal{EvpnGlobal: &a.EvpnGlobal{Asn: n.Asn, AdvertiseAllVni: ptr(true), AdvertiseSviIp: ptr(true)}}})
		}
		for _, nw := range c.Networks {
			for _, x := range nw.Placements {
				if x.NodeId != n.NodeId {
					continue
				}
				rd := x.Rd
				if rd == "" {
					rd = fmt.Sprintf("%s:%d", n.RouterId, nw.L2Vni)
				}
				irt, ert := nw.ImportRts, nw.ExportRts
				if len(irt) == 0 {
					irt = []string{fmt.Sprintf("%d:%d", n.Asn, nw.L2Vni)}
				}
				if len(ert) == 0 {
					ert = []string{fmt.Sprintf("%d:%d", n.Asn, nw.L2Vni)}
				}
				add("evpn-l2/"+nw.Id, &a.Operation{Code: a.OpCode_EVPN_L2VNI, Action: a.Action_SET, Data: &a.Operation_Evpn{Evpn: &a.Evpn{Asn: n.Asn, Vni: nw.L2Vni, Rd: rd, ImportRts: irt, ExportRts: ert}}})
			}
		}
		for _, t := range c.Tenants {
			if x := tenantNodes[t.Id][n.NodeId]; x != nil {
				if t.L3Vni == 0 {
					continue
				}
				bgp(t.Name)
				rd := x.Rd
				if rd == "" {
					rd = fmt.Sprintf("%s:%d", n.RouterId, t.L3Vni)
				}
				add("evpn-tenant/"+t.Id, &a.Operation{Code: a.OpCode_EVPN_TENANT, Action: a.Action_SET, Data: &a.Operation_Evpn{Evpn: &a.Evpn{Asn: n.Asn, Vrf: t.Name, Rd: rd, ImportRts: t.ImportRts, ExportRts: t.ExportRts}}})
			}
		}
		seenIDs := map[string]bool{}
		for _, o := range ops {
			if seenIDs[o.Id] {
				return nil, fmt.Errorf("duplicate generated object %s", o.Id)
			}
			seenIDs[o.Id] = true
		}
		result[n.NodeId] = ops
	}
	for _, nr := range c.NodeResources {
		if nr == nil || nr.NodeId == "" || len(nr.Objects) > 2048 {
			return nil, fmt.Errorf("invalid node resource collection")
		}
		if _, exists := result[nr.NodeId]; exists {
			return nil, fmt.Errorf("node %s has duplicate or legacy/resource ownership; explicitly import before conversion", nr.NodeId)
		}
		ops, e := resources.Compile(nr)
		if e != nil {
			return nil, fmt.Errorf("node %s: %w", nr.NodeId, e)
		}
		result[nr.NodeId] = ops
	}
	return result, nil
}
func l3Bridge(t *p.Tenant, x *p.TenantPlacement) string {
	if x.Bridge != "" {
		return x.Bridge
	}
	return "br" + strconv.Itoa(int(t.L3Vni))
}
func l3VX(t *p.Tenant, x *p.TenantPlacement) string {
	if x.Vxlan != "" {
		return x.Vxlan
	}
	return "vx" + strconv.Itoa(int(t.L3Vni))
}
func l2Bridge(t *p.Network, x *p.NetworkPlacement) string {
	if x.Bridge != "" {
		return x.Bridge
	}
	return "br" + strconv.Itoa(int(t.L2Vni))
}
func l2VX(t *p.Network, x *p.NetworkPlacement) string {
	if x.Vxlan != "" {
		return x.Vxlan
	}
	return "vx" + strconv.Itoa(int(t.L2Vni))
}

func Build(change *p.Change, db *p.Database) (*p.Plan, error) {
	all, e := Compile(change.Configuration)
	if e != nil {
		return nil, e
	}
	plan := &p.Plan{ChangeId: change.Id, BaseRevision: change.BaseRevision, EditRevision: change.EditRevision, Warnings: []string{"Agent transaction success does not establish protocol convergence; check topology and traffic separately.", "Cross-node failure policy is STOP; successful nodes are retained and reported as PARTIAL."}}
	reserved, e := reservations(db)
	if e != nil {
		return nil, e
	}
	for node, np := range reserved {
		if !sameTarget(np.TargetOperations, all[node]) {
			return nil, fmt.Errorf("node %s is reserved by an active job or failed rollback; retain its desired configuration until recovery", node)
		}
	}
	if len(reserved) > 0 {
		plan.Warnings = append(plan.Warnings, "Unchanged reserved nodes are excluded; their original jobs still determine completion. This plan covers only unreserved nodes.")
	}
	for id, old := range db.Applied {
		if _, exists := all[id]; len(old.Operations) > 0 && !exists {
			return nil, fmt.Errorf("removing a managed node profile is not supported; remove tenant/network placements first and retain node profile")
		}
	}
	ids := []string{}
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Underlay/RR first on initial provisioning, then deterministic node order.
	role := map[string]string{}
	for _, n := range change.Configuration.Nodes {
		role[n.NodeId] = n.Role
	}
	sort.SliceStable(ids, func(i, j int) bool { return role[ids[i]] == "spine" && role[ids[j]] != "spine" })
	for _, id := range ids {
		if reserved[id] != nil {
			continue
		}
		n := db.Nodes[id]
		if n == nil || !n.Adopted {
			return nil, fmt.Errorf("node %s must be registered and explicitly adopted", id)
		}
		if n.State != "RUNNING" {
			return nil, fmt.Errorf("node %s is awaiting confirmed Agent state for its current instance", id)
		}
		old := db.Applied[id]
		if old == nil {
			old = &p.Applied{AgentRevision: n.AgentRevision}
		}
		if n.AgentRevision != old.AgentRevision {
			return nil, fmt.Errorf("node %s agent revision drift; current %d, owned baseline %d", id, n.AgentRevision, old.AgentRevision)
		}
		checked, err := resources.CheckRemoved(old.Operations, all[id])
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", id, err)
		}
		if len(checked) != len(all[id]) {
			return nil, fmt.Errorf("node %s retains interface attributes for a removed virtual link", id)
		}
		resourceOwned := false
		for _, nr := range db.GetConfiguration().GetNodeResources() {
			if nr.NodeId == id {
				resourceOwned = true
			}
		}
		targetResource := false
		for _, nr := range change.Configuration.NodeResources {
			if nr.NodeId == id {
				targetResource = true
			}
		}
		if len(old.Operations) > 0 && resourceOwned != targetResource {
			return nil, fmt.Errorf("node %s ownership model cannot change through full JSON; use verified import, or remove owned resources first", id)
		}
		if old.AgentRevision > 0 && len(old.Operations) == 0 && len(all[id]) > 0 && !resourceOwned {
			return nil, fmt.Errorf("node %s has existing unmanaged configuration; ImportNodeResources must verify and establish ownership first", id)
		}
		ops, e := delta(old.Operations, all[id])
		if e != nil {
			return nil, fmt.Errorf("node %s: %w", id, e)
		}
		if len(ops) == 0 {
			continue
		}
		caps := map[a.OpCode]bool{}
		for _, cap := range n.GetCapabilities().GetOperations() {
			caps[cap.Code] = cap.Rollback
		}
		for _, o := range ops {
			if o.Code == a.OpCode_VXLAN && (o.Action == a.Action_SET || o.GetVxlan().GetEncapVrfId() != 0) {
				supported := false
				for _, feature := range n.GetCapabilities().GetFeatures() {
					if feature == "vxlan-encap-vrf/v1" {
						supported = true
					}
				}
				if !supported {
					return nil, fmt.Errorf("node %s lacks vxlan-encap-vrf/v1", id)
				}
			}
			if !caps[o.Code] {
				return nil, fmt.Errorf("node %s lacks rollback-capable %s", id, o.Code)
			}
		}
		plan.Nodes = append(plan.Nodes, &p.NodePlan{NodeId: id, Operations: ops, ExpectedAgentRevision: old.AgentRevision, TargetOperations: all[id]})
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(plan)
	h := sha256.Sum256(b)
	plan.Id = hex.EncodeToString(h[:16])
	return plan, nil
}
func delta(old, next []*a.Operation) ([]*a.Operation, error) {
	oldMap, newMap := map[string]*a.Operation{}, map[string]*a.Operation{}
	for _, o := range old {
		oldMap[o.Id] = o
	}
	for _, o := range next {
		newMap[o.Id] = o
	}
	// Removal runs before updates. Keep an old outer table until its surviving
	// tunnels have switched in a separate committed change.
	for _, o := range old {
		if o.Code != a.OpCode_TENANT || newMap[o.Id] != nil {
			continue
		}
		for _, vx := range old {
			if vx.Code == a.OpCode_VXLAN && vx.GetVxlan().GetEncapVrfId() == o.GetTenant().TableId && newMap[vx.Id] != nil {
				return nil, fmt.Errorf("switch VXLAN %s away from outer VRF %s in a separate change before deleting the VRF", vx.GetVxlan().Name, o.GetTenant().Name)
			}
		}
	}
	var out []*a.Operation
	for i := len(old) - 1; i >= 0; i-- {
		o := old[i]
		if newMap[o.Id] != nil {
			continue
		}
		if o.Code == a.OpCode_INTERFACE {
			continue
		}
		if o.Code == a.OpCode_NODE_NETWORK {
			return nil, fmt.Errorf("removing system settings is not supported")
		}
		x := proto.Clone(o).(*a.Operation)
		x.Id = "remove/" + o.Id
		x.DependsOn = nil
		x.Action = a.Action_DELETE
		if o.Action == a.Action_BIND {
			x.Action = a.Action_UNBIND
		}
		out = append(out, x)
	}
	// Deletion is not simply creation in reverse. In particular FRR refuses
	// removal of a tenant BGP instance while its VRF still has an L3VNI.
	var err error
	out, err = orderRemovals(out)
	if err != nil {
		return nil, err
	}
	for _, o := range next {
		prev := oldMap[o.Id]
		if prev != nil && proto.Equal(prev, o) {
			continue
		}
		x := proto.Clone(o).(*a.Operation)
		x.DependsOn = nil
		if prev != nil {
			switch o.Code {
			case a.OpCode_BGP_INSTANCE:
				if prev.GetBgp().Asn != o.GetBgp().Asn || prev.GetBgp().RouterId != o.GetBgp().RouterId {
					return nil, fmt.Errorf("changing ASN/router_id of a managed BGP instance is not supported")
				}
			case a.OpCode_OSPF_INSTANCE, a.OpCode_OSPF6_INSTANCE:
				if prev.GetOspf().RouterId != o.GetOspf().RouterId {
					return nil, fmt.Errorf("changing managed OSPF router_id is not supported")
				}
			case a.OpCode_EVPN_L2VNI, a.OpCode_EVPN_TENANT:
				if prev.GetEvpn().Vni != o.GetEvpn().Vni || prev.GetEvpn().Vrf != o.GetEvpn().Vrf {
					return nil, fmt.Errorf("changing EVPN identity requires explicit remove/create changes")
				}
				remove := proto.Clone(prev).(*a.Operation)
				remove.Id = "replace/" + prev.Id
				remove.Action = a.Action_DELETE
				remove.DependsOn = nil
				out = append(out, remove)
			case a.OpCode_VXLAN:
				before := proto.Clone(prev.GetVxlan()).(*a.Vxlan)
				after := proto.Clone(o.GetVxlan()).(*a.Vxlan)
				before.EncapVrfId, after.EncapVrfId = nil, nil
				before.Underlay, after.Underlay = "", ""
				if !proto.Equal(before, after) {
					return nil, fmt.Errorf("only VXLAN outer VRF/underlay may change in place")
				}
				x.Action = a.Action_SET
				x.Data = &a.Operation_Vxlan{Vxlan: &a.Vxlan{Name: o.GetVxlan().Name,
					EncapVrfId: proto.Uint32(o.GetVxlan().GetEncapVrfId()), Underlay: o.GetVxlan().Underlay}}
			case a.OpCode_TENANT, a.OpCode_BD_TENANT, a.OpCode_TENANT_L3VNI:
				return nil, fmt.Errorf("in-place identity change of %s requires explicit remove/create changes", o.Id)
			case a.OpCode_BD, a.OpCode_IPS_RULE, a.OpCode_IPS_INTERFACE:
				x.Action = a.Action_SET
			case a.OpCode_BD_PORT:
				if prev.GetBridgePort().Bridge != o.GetBridgePort().Bridge {
					return nil, fmt.Errorf("moving a port requires explicit detach/attach changes")
				}
			}
		}
		out = append(out, x)
	}
	for i, o := range out {
		if i > 0 {
			o.DependsOn = []string{out[i-1].Id}
		}
	}
	return out, nil
}

// orderRemovals adds semantic teardown constraints while preserving reverse
// creation order wherever those constraints do not require a different order.
func orderRemovals(ops []*a.Operation) ([]*a.Operation, error) {
	dependencies := make([][]int, len(ops))
	for i, o := range ops {
		for j, before := range ops {
			if i == j {
				continue
			}
			requires := false
			switch o.Code {
			case a.OpCode_TENANT_L3VNI:
				requires = (before.Code == a.OpCode_EVPN_TENANT && before.GetEvpn().Vrf == o.GetL3Vni().Tenant) || (before.Code == a.OpCode_EVPN_TYPE5 && before.GetBgpType5().Vrf == o.GetL3Vni().Tenant)
			case a.OpCode_BGP_INSTANCE:
				vrf := o.GetBgp().Vrf
				if vrf != "" {
					requires = (before.Code == a.OpCode_TENANT_L3VNI && before.GetL3Vni().Tenant == vrf) || (before.Code == a.OpCode_EVPN_TENANT && before.GetEvpn().Vrf == vrf)
				}
				if before.GetPeer() != nil && before.GetPeer().Vrf == vrf {
					requires = true
				}
				if before.GetEvpn() != nil && before.GetEvpn().Vrf == vrf {
					requires = true
				}
				if before.GetEvpnGlobal() != nil && vrf == "" {
					requires = true
				}
				if before.GetBgpNetwork() != nil && before.GetBgpNetwork().Vrf == vrf {
					requires = true
				}
				if before.GetBgpRedistribute() != nil && before.GetBgpRedistribute().Vrf == vrf {
					requires = true
				}
				if before.GetBgpType5() != nil && before.GetBgpType5().Vrf == vrf {
					requires = true
				}
			case a.OpCode_OSPF_INSTANCE:
				requires = (before.Code == a.OpCode_OSPF_NETWORK && before.GetOspfNetwork().Vrf == o.GetOspf().Vrf) || (before.Code == a.OpCode_OSPF_INTERFACE && before.GetOspfInterface().Vrf == o.GetOspf().Vrf)
			case a.OpCode_OSPF6_INSTANCE:
				requires = before.Code == a.OpCode_OSPF6_INTERFACE && before.GetOspf6Interface().Vrf == o.GetOspf().Vrf
			case a.OpCode_BGP_NEIGHBOR, a.OpCode_BGP_PEER_GROUP:
				requires = before.GetPeer() != nil && before.GetPeer().Vrf == o.GetPeer().Vrf && ((before.Code == a.OpCode_BGP_AF_PEER || before.Code == a.OpCode_BGP_EVPN_PEER) && before.GetPeer().Name == o.GetPeer().Name || before.Code == a.OpCode_BGP_NEIGHBOR && before.GetPeer().PeerGroup == o.GetPeer().Name)
			case a.OpCode_VXLAN:
				requires = before.Code == a.OpCode_BD_PORT && before.GetBridgePort().Port == o.GetVxlan().Name
			case a.OpCode_BD:
				name := o.GetBridge().Name
				requires = (before.Code == a.OpCode_BD_PORT && before.GetBridgePort().Bridge == name) || (before.Code == a.OpCode_BD_TENANT && before.GetBinding().Bridge == name) || (before.Code == a.OpCode_ADDRESS && before.GetAddress().Interface == name)
			case a.OpCode_TENANT:
				name := o.GetTenant().Name
				requires = (before.Code == a.OpCode_BGP_INSTANCE && before.GetBgp().Vrf == name) || (before.Code == a.OpCode_TENANT_L3VNI && before.GetL3Vni().Tenant == name) || (before.Code == a.OpCode_BD_TENANT && before.GetBinding().Tenant == name)
				requires = requires || (before.GetOspf() != nil && before.GetOspf().Vrf == name)
				requires = requires || (before.Code == a.OpCode_VXLAN && before.GetVxlan().GetEncapVrfId() == o.GetTenant().TableId)
			}
			if requires {
				dependencies[i] = append(dependencies[i], j)
			}
		}
	}
	result := make([]*a.Operation, 0, len(ops))
	emitted := make([]bool, len(ops))
	for len(result) < len(ops) {
		found := false
		for i, o := range ops {
			if emitted[i] {
				continue
			}
			ready := true
			for _, dep := range dependencies[i] {
				if !emitted[dep] {
					ready = false
					break
				}
			}
			if ready {
				result = append(result, o)
				emitted[i] = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("cycle in resource teardown dependencies")
		}
	}
	return result, nil
}
