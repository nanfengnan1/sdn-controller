// Package resources materializes node-local desired objects from typed edits.
// It does not execute commands or import objects merely because names match.
package resources

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

var payloads = map[a.OpCode]string{
	a.OpCode_IPS_RULE: "ips_rule", a.OpCode_IPS_INTERFACE: "ips_interface",
	a.OpCode_NEIGHBOR_KEEPALIVE: "neighbor_keepalive",
	a.OpCode_NODE_NETWORK:       "network", a.OpCode_INTERFACE: "interface", a.OpCode_ADDRESS: "address",
	a.OpCode_TENANT: "tenant", a.OpCode_BD: "bridge", a.OpCode_BD_PORT: "bridge_port", a.OpCode_BD_TENANT: "binding",
	a.OpCode_VXLAN: "vxlan", a.OpCode_STATIC_ROUTE: "route", a.OpCode_OSPF_INSTANCE: "ospf", a.OpCode_OSPF_NETWORK: "ospf_network",
	a.OpCode_OSPF_INTERFACE: "ospf_interface", a.OpCode_OSPF6_INSTANCE: "ospf", a.OpCode_OSPF6_INTERFACE: "ospf6_interface",
	a.OpCode_BGP_INSTANCE: "bgp", a.OpCode_BGP_PEER_GROUP: "peer", a.OpCode_BGP_NEIGHBOR: "peer", a.OpCode_BGP_EVPN_PEER: "peer",
	a.OpCode_BGP_EVPN_GLOBAL: "evpn_global", a.OpCode_EVPN_L2VNI: "evpn", a.OpCode_EVPN_TENANT: "evpn", a.OpCode_TENANT_L3VNI: "l3vni",
	a.OpCode_BGP_AF_PEER: "peer", a.OpCode_BGP_NETWORK: "bgp_network", a.OpCode_BGP_REDISTRIBUTE: "bgp_redistribute", a.OpCode_EVPN_TYPE5: "bgp_type5",
}

func payload(o *a.Operation) (protoreflect.Message, error) {
	if o == nil {
		return nil, fmt.Errorf("operation required")
	}
	want, ok := payloads[o.Code]
	if !ok {
		return nil, fmt.Errorf("unsupported operation %s", o.Code)
	}
	m := o.ProtoReflect()
	field := m.WhichOneof(m.Descriptor().Oneofs().ByName("data"))
	if field == nil || string(field.Name()) != want {
		return nil, fmt.Errorf("%s requires %s data", o.Code, want)
	}
	value := m.Get(field).Message()
	if !value.IsValid() {
		return nil, fmt.Errorf("%s data is nil", o.Code)
	}
	return value, nil
}
func str(m protoreflect.Message, name string) string {
	f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if f == nil {
		return ""
	}
	v := m.Get(f)
	switch f.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		return strconv.FormatUint(v.Uint(), 10)
	case protoreflect.EnumKind:
		return strconv.Itoa(int(v.Enum()))
	}
	return ""
}
func Key(o *a.Operation) (string, error) {
	m, e := payload(o)
	if e != nil {
		return "", e
	}
	var fields []string
	switch o.Code {
	case a.OpCode_NODE_NETWORK, a.OpCode_NEIGHBOR_KEEPALIVE:
		fields = nil
	case a.OpCode_TENANT, a.OpCode_BD, a.OpCode_INTERFACE, a.OpCode_VXLAN:
		fields = []string{"name"}
	case a.OpCode_IPS_RULE:
		fields = []string{"sid"}
	case a.OpCode_IPS_INTERFACE:
		fields = []string{"name"}
	case a.OpCode_ADDRESS:
		fields = []string{"interface", "prefix"}
	case a.OpCode_BD_PORT:
		fields = []string{"port"}
	case a.OpCode_BD_TENANT:
		fields = []string{"bridge"}
	case a.OpCode_TENANT_L3VNI:
		fields = []string{"tenant"}
	case a.OpCode_STATIC_ROUTE:
		fields = []string{"table", "prefix"}
	case a.OpCode_OSPF_INSTANCE, a.OpCode_OSPF6_INSTANCE:
		fields = []string{"vrf"}
	case a.OpCode_OSPF_NETWORK:
		fields = []string{"vrf", "prefix"}
	case a.OpCode_OSPF_INTERFACE, a.OpCode_OSPF6_INTERFACE:
		fields = []string{"vrf", "name"}
	case a.OpCode_BGP_INSTANCE:
		fields = []string{"vrf"}
	case a.OpCode_BGP_PEER_GROUP, a.OpCode_BGP_NEIGHBOR, a.OpCode_BGP_EVPN_PEER:
		fields = []string{"vrf", "name"}
	case a.OpCode_BGP_AF_PEER:
		fields = []string{"vrf", "name", "family"}
	case a.OpCode_BGP_EVPN_GLOBAL:
		fields = nil
	case a.OpCode_EVPN_L2VNI:
		fields = []string{"vrf", "vni"}
	case a.OpCode_EVPN_TENANT:
		fields = []string{"vrf"}
	case a.OpCode_BGP_NETWORK:
		fields = []string{"vrf", "family", "prefix"}
	case a.OpCode_BGP_REDISTRIBUTE:
		fields = []string{"vrf", "family", "protocol"}
	case a.OpCode_EVPN_TYPE5:
		fields = []string{"vrf", "family"}
	}
	parts := []string{"resource", strings.ToLower(o.Code.String())}
	for _, f := range fields {
		s := str(m, f)
		if s == "" && f != "vrf" {
			return "", fmt.Errorf("%s identity requires %s", o.Code, f)
		}
		parts = append(parts, url.PathEscape(s))
	}
	return strings.Join(parts, "/"), nil
}
func canonicalAction(c a.OpCode) a.Action {
	switch c {
	case a.OpCode_BD_PORT, a.OpCode_BD_TENANT, a.OpCode_TENANT_L3VNI:
		return a.Action_BIND
	case a.OpCode_NEIGHBOR_KEEPALIVE, a.OpCode_NODE_NETWORK, a.OpCode_INTERFACE, a.OpCode_OSPF_INTERFACE, a.OpCode_OSPF6_INTERFACE, a.OpCode_BGP_EVPN_PEER, a.OpCode_BGP_EVPN_GLOBAL, a.OpCode_EVPN_L2VNI, a.OpCode_EVPN_TENANT, a.OpCode_BGP_AF_PEER, a.OpCode_EVPN_TYPE5:
		return a.Action_SET
	default:
		return a.Action_CREATE
	}
}
func canonical(o *a.Operation) (*a.Operation, error) {
	if o == nil {
		return nil, fmt.Errorf("nil resource object")
	}
	x := proto.Clone(o).(*a.Operation)
	m, e := payload(x)
	if e != nil {
		return nil, e
	}
	for _, name := range []protoreflect.Name{"local", "router_id", "gateway", "update_source"} {
		if f := m.Descriptor().Fields().ByName(name); f != nil {
			if ip, err := netip.ParseAddr(m.Get(f).String()); err == nil {
				m.Set(f, protoreflect.ValueOfString(ip.String()))
			}
		}
	}
	if x.GetPeer() != nil {
		if ip, err := netip.ParseAddr(x.GetPeer().Name); err == nil {
			x.GetPeer().Name = ip.String()
		}
	}
	if f := m.Descriptor().Fields().ByName("mac"); f != nil && m.Has(f) {
		if mac, err := net.ParseMAC(m.Get(f).String()); err == nil {
			m.Set(f, protoreflect.ValueOfString(mac.String()))
		}
	}
	if x.Code == a.OpCode_NODE_NETWORK {
		seen := map[string]bool{}
		for _, s := range x.GetNetwork().Settings {
			if s == nil || s.Key == "" || seen[s.Key] {
				return nil, fmt.Errorf("invalid or duplicate sysctl setting")
			}
			seen[s.Key] = true
		}
		sort.Slice(x.GetNetwork().Settings, func(i, j int) bool { return x.GetNetwork().Settings[i].Key < x.GetNetwork().Settings[j].Key })
	}
	for _, name := range []string{"prefix"} {
		if f := m.Descriptor().Fields().ByName(protoreflect.Name(name)); f != nil && m.Get(f).String() != "" {
			v, e := netip.ParsePrefix(m.Get(f).String())
			if e != nil {
				return nil, fmt.Errorf("invalid prefix")
			}
			if x.Code != a.OpCode_ADDRESS {
				v = v.Masked()
			}
			m.Set(f, protoreflect.ValueOfString(v.String()))
		}
	}
	if x.Code == a.OpCode_VXLAN && x.Action != a.Action_SET && x.GetVxlan().Port == 0 {
		x.GetVxlan().Port = 4789
	}
	if x.Code == a.OpCode_STATIC_ROUTE && x.GetRoute().Table == 0 {
		x.GetRoute().Table = 254
	}
	x.Id, e = Key(x)
	x.DependsOn = nil
	x.Action = canonicalAction(x.Code)
	return x, e
}
func allowed(o *a.Operation) bool {
	if o.Code == a.OpCode_VXLAN && o.Action == a.Action_SET {
		x := o.GetVxlan()
		return x != nil && x.EncapVrfId != nil && x.Vni == 0 && x.Local == "" && x.Port == 0 && !x.Learning
	}

	if o.Code == a.OpCode_NEIGHBOR_KEEPALIVE {
		return o.Action == a.Action_SET && o.GetNeighborKeepalive() != nil && o.GetNeighborKeepalive().Enabled != nil
	}
	switch o.Action {
	case a.Action_DELETE:
		return o.Code != a.OpCode_NEIGHBOR_KEEPALIVE && o.Code != a.OpCode_INTERFACE && o.Code != a.OpCode_NODE_NETWORK && o.Code != a.OpCode_BD_PORT && o.Code != a.OpCode_BD_TENANT && o.Code != a.OpCode_TENANT_L3VNI
	case a.Action_UNBIND:
		return o.Code == a.OpCode_BD_PORT || o.Code == a.OpCode_BD_TENANT || o.Code == a.OpCode_TENANT_L3VNI
	case a.Action_BIND:
		return canonicalAction(o.Code) == a.Action_BIND
	case a.Action_CREATE:
		return canonicalAction(o.Code) == a.Action_CREATE
	case a.Action_SET:
		return o.Code != a.OpCode_TENANT && o.Code != a.OpCode_VXLAN && o.Code != a.OpCode_ADDRESS && o.Code != a.OpCode_STATIC_ROUTE
	}
	return false
}

// merge replaces explicitly present repeated fields, and preserves optional
// false/zero. Proto3 scalar defaults without presence are not destructive edits.
func merge(dst, src protoreflect.Message) {
	src.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.IsList() {
			target := dst.Mutable(f).List()
			target.Truncate(0)
			for i := 0; i < v.List().Len(); i++ {
				target.Append(v.List().Get(i))
			}
		} else {
			dst.Set(f, v)
		}
		return true
	})
}

// Apply uses natural identities, so callers cannot change ownership by picking IDs.
func Apply(base []*a.Operation, edits []*a.Operation) ([]*a.Operation, error) {
	objects := map[string]*a.Operation{}
	for _, o := range base {
		x, e := canonical(o)
		if e != nil {
			return nil, e
		}
		if o.Action == a.Action_DELETE || o.Action == a.Action_UNBIND {
			return nil, fmt.Errorf("desired objects cannot contain removal commands")
		}
		if objects[x.Id] != nil {
			return nil, fmt.Errorf("duplicate resource %s", x.Id)
		}
		objects[x.Id] = x
	}
	for _, edit := range edits {
		if edit == nil || !allowed(edit) {
			return nil, fmt.Errorf("unsupported resource action")
		}
		x, e := canonical(edit)
		if e != nil {
			return nil, e
		}
		old := objects[x.Id]
		if edit.Action == a.Action_DELETE || edit.Action == a.Action_UNBIND {
			delete(objects, x.Id)
			continue
		}
		if edit.Action == a.Action_SET && old == nil && (edit.Code == a.OpCode_BD || edit.Code == a.OpCode_VXLAN || edit.Code == a.OpCode_IPS_RULE || edit.Code == a.OpCode_IPS_INTERFACE) {
			return nil, fmt.Errorf("%s does not exist; create it first", edit.Code)
		}
		if edit.Action == a.Action_SET && old != nil {
			if edit.Code == a.OpCode_NODE_NETWORK {
				settings := map[string]*a.Sysctl{}
				for _, s := range old.GetNetwork().Settings {
					settings[s.Key] = s
				}
				for _, s := range x.GetNetwork().Settings {
					settings[s.Key] = s
				}
				keys := make([]string, 0, len(settings))
				for k := range settings {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				old.GetNetwork().Settings = nil
				for _, k := range keys {
					old.GetNetwork().Settings = append(old.GetNetwork().Settings, settings[k])
				}
				continue
			}
			dm, _ := payload(old)
			sm, _ := payload(x)
			merge(dm, sm)
			syncLinkAttributes(objects, x)
			continue
		}
		if old != nil && !proto.Equal(old, x) {
			return nil, fmt.Errorf("resource %s already exists with different settings; use SET or explicit detach/remove", x.Id)
		}
		objects[x.Id] = x
		syncLinkAttributes(objects, x)
	}
	out := make([]*a.Operation, 0, len(objects))
	for _, o := range objects {
		out = append(out, o)
	}
	Sort(out)
	return out, nil
}

// Bridge and interface APIs both expose MTU/MAC. An explicit edit updates the
// shared link property in both representations; unrelated optional fields stay
// untouched. Full desired documents with conflicting copies are rejected below.
func syncLinkAttributes(objects map[string]*a.Operation, edit *a.Operation) {
	if edit.Code != a.OpCode_BD && edit.Code != a.OpCode_INTERFACE {
		return
	}
	source, _ := payload(edit)
	name := str(source, "name")
	for _, other := range objects {
		if other.Code == edit.Code || (other.Code != a.OpCode_BD && other.Code != a.OpCode_INTERFACE) {
			continue
		}
		target, _ := payload(other)
		if str(target, "name") != name {
			continue
		}
		for _, field := range []protoreflect.Name{"mtu", "mac"} {
			sf := source.Descriptor().Fields().ByName(field)
			tf := target.Descriptor().Fields().ByName(field)
			if source.Has(sf) {
				target.Set(tf, source.Get(sf))
			}
		}
	}
}

// Replay folds an Agent's successful operation journal, including removals.
func Replay(journal []*a.Operation) ([]*a.Operation, error) {
	var out []*a.Operation
	for _, o := range journal {
		if o == nil {
			return nil, fmt.Errorf("nil journal operation")
		}
		// Agent logs may contain repeated CREATE from imported historic targets.
		edit := proto.Clone(o).(*a.Operation)
		if edit.Action == a.Action_CREATE && edit.Code >= a.OpCode_OSPF_INSTANCE {
			edit.Action = a.Action_SET
		}
		var e error
		out, e = Apply(out, []*a.Operation{edit})
		if e != nil {
			return nil, e
		}
		// Link deletion also removes its interface attributes in Linux. They are
		// observations of that link, not ownership of an independent object.
		if o.Action == a.Action_DELETE && (o.Code == a.OpCode_TENANT || o.Code == a.OpCode_BD || o.Code == a.OpCode_VXLAN) {
			m, _ := payload(o)
			name := str(m, "name")
			filtered := out[:0]
			for _, v := range out {
				if v.Code != a.OpCode_INTERFACE || v.GetInterface().Name != name {
					filtered = append(filtered, v)
				}
			}
			out = filtered
		}
	}
	return out, nil
}
func rank(c a.OpCode) int {
	switch c {
	case a.OpCode_IPS_RULE:
		return 120
	case a.OpCode_IPS_INTERFACE:
		return 130
	case a.OpCode_NODE_NETWORK, a.OpCode_NEIGHBOR_KEEPALIVE:
		return 0
	case a.OpCode_TENANT:
		return 10
	case a.OpCode_BD:
		return 20
	case a.OpCode_VXLAN:
		return 25
	case a.OpCode_INTERFACE:
		return 30
	case a.OpCode_BD_TENANT:
		return 40
	case a.OpCode_BD_PORT:
		return 50
	case a.OpCode_ADDRESS:
		return 60
	case a.OpCode_STATIC_ROUTE:
		return 65
	case a.OpCode_TENANT_L3VNI:
		return 70
	case a.OpCode_OSPF_INSTANCE, a.OpCode_OSPF6_INSTANCE, a.OpCode_BGP_INSTANCE:
		return 80
	case a.OpCode_BGP_PEER_GROUP:
		return 90
	case a.OpCode_BGP_NEIGHBOR:
		return 100
	default:
		return 110
	}
}
func Sort(ops []*a.Operation) {
	sort.Slice(ops, func(i, j int) bool {
		a, b := rank(ops[i].Code), rank(ops[j].Code)
		if a == b {
			return ops[i].Id < ops[j].Id
		}
		return a < b
	})
}
func validName(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.-", c)) {
			return false
		}
	}
	return true
}
func fieldName(m protoreflect.Message, f string) error {
	s := str(m, f)
	if s != "" && !validName(s) {
		return fmt.Errorf("invalid interface/VRF %s", s)
	}
	return nil
}

// Validate checks object references against the whole final node target. Physical
// interfaces can be external; owned virtual interfaces and protocol references
// cannot disappear while objects still refer to them.
func Validate(ops []*a.Operation) error {
	tenants := map[string]*a.Operation{}
	bridges := map[string]bool{}
	vxlan := map[string]*a.Operation{}
	ifaces := map[string]bool{}
	names := map[string]string{}
	tables := map[uint32]string{}
	vnis := map[uint32]string{}
	bgp := map[string]uint32{}
	peers := map[string]bool{}
	groups := map[string]bool{}
	bindings := map[string]string{}
	ports := map[string]string{}
	l3 := map[string]uint32{}
	ospf := map[string]bool{}
	ospf6 := map[string]bool{}
	for _, o := range ops {
		m, e := payload(o)
		if e != nil {
			return e
		}
		for _, f := range []string{"vrf", "tenant", "bridge", "interface", "underlay"} {
			if e = fieldName(m, f); e != nil {
				return e
			}
		}
		var name, kind string
		switch o.Code {
		case a.OpCode_IPS_RULE:
			x := o.GetIpsRule()
			if x.Sid == 0 || len(x.Rule) == 0 || len(x.Rule) > 16384 || strings.ContainsAny(x.Rule, "\x00\r\n") {
				return fmt.Errorf("IPS rule requires nonzero SID and a single rule line")
			}
		case a.OpCode_IPS_INTERFACE:
			x := o.GetIpsInterface()
			if !regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$").MatchString(x.Name) ||
				(x.Direction != "input" && x.Direction != "output" && x.Direction != "inout") {
				return fmt.Errorf("invalid IPS native interface or direction")
			}
		case a.OpCode_TENANT:
			x := o.GetTenant()
			name, kind = x.Name, "VRF"
			if x.TableId == 0 || x.TableId >= 253 && x.TableId <= 255 {
				return fmt.Errorf("invalid VRF table")
			}
			if tables[x.TableId] != "" {
				return fmt.Errorf("VRF table %d is already used", x.TableId)
			}
			tables[x.TableId] = name
			tenants[name] = o
		case a.OpCode_BD:
			name, kind = o.GetBridge().Name, "BD"
			bridges[name] = true
		case a.OpCode_VXLAN:
			x := o.GetVxlan()
			name, kind = x.Name, "VXLAN"
			if x.Vni == 0 || x.Vni > 16777215 || x.Port > 65535 {
				return fmt.Errorf("invalid VXLAN VNI/port")
			}
			ip, e := netip.ParseAddr(x.Local)
			if e != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
				return fmt.Errorf("VXLAN local VTEP requires unicast IPv4/IPv6 without a zone")
			}
			if vnis[x.Vni] != "" {
				return fmt.Errorf("VNI %d is already used", x.Vni)
			}
			vnis[x.Vni] = name
			if x.GetEncapVrfId() >= 4294967295 || (x.GetEncapVrfId() >= 253 && x.GetEncapVrfId() <= 255) {
				return fmt.Errorf("invalid/reserved VXLAN outer VRF table")
			}
			vxlan[name] = o
		case a.OpCode_INTERFACE:
			x := o.GetInterface()
			if !validName(x.Name) {
				return fmt.Errorf("invalid interface")
			}
			ifaces[x.Name] = true
			if x.Mtu != nil && (*x.Mtu < 68 || *x.Mtu > 65535) {
				return fmt.Errorf("invalid MTU")
			}
		case a.OpCode_BD_TENANT:
			x := o.GetBinding()
			bindings[x.Bridge] = x.Tenant
		case a.OpCode_BD_PORT:
			x := o.GetBridgePort()
			ports[x.Port] = x.Bridge
		case a.OpCode_TENANT_L3VNI:
			x := o.GetL3Vni()
			l3[x.Tenant] = x.Vni
		case a.OpCode_BGP_INSTANCE:
			x := o.GetBgp()
			if x.Asn == 0 {
				return fmt.Errorf("BGP ASN required")
			}
			bgp[x.Vrf] = x.Asn
		case a.OpCode_BGP_NEIGHBOR:
			x := o.GetPeer()
			peers[x.Vrf+"/"+x.Name] = true
		case a.OpCode_BGP_PEER_GROUP:
			x := o.GetPeer()
			groups[x.Vrf+"/"+x.Name] = true
		case a.OpCode_OSPF_INSTANCE:
			ospf[o.GetOspf().Vrf] = true
		case a.OpCode_OSPF6_INSTANCE:
			ospf6[o.GetOspf().Vrf] = true
		}
		if name != "" {
			if !validName(name) {
				return fmt.Errorf("invalid %s name", kind)
			}
			if names[name] != "" {
				return fmt.Errorf("interface name %s is used by %s and %s", name, names[name], kind)
			}
			names[name] = kind
		}
		if f := m.Descriptor().Fields().ByName("mac"); f != nil && m.Has(f) {
			s := m.Get(f).String()
			if s != "" {
				mac, e := net.ParseMAC(s)
				if e != nil || len(mac) != 6 || mac[0]&1 != 0 {
					return fmt.Errorf("invalid unicast MAC")
				}
			}
		}
	}
	for _, o := range ops {
		if o.Code != a.OpCode_BD {
			continue
		}
		for _, i := range ops {
			if i.Code != a.OpCode_INTERFACE || i.GetInterface().Name != o.GetBridge().Name {
				continue
			}
			b, iface := o.GetBridge(), i.GetInterface()
			if b.Mtu != nil && iface.Mtu != nil && *b.Mtu != *iface.Mtu || b.Mac != nil && iface.Mac != nil && !strings.EqualFold(*b.Mac, *iface.Mac) {
				return fmt.Errorf("conflicting BD/interface MTU or MAC for %s", b.Name)
			}
		}
	}
	for bridge, tenant := range bindings {
		if !bridges[bridge] || tenants[tenant] == nil {
			return fmt.Errorf("BD %s binding requires existing BD and VRF %s", bridge, tenant)
		}
	}
	for port, bridge := range ports {
		if !bridges[bridge] || !validName(port) || port == "lo" || bridges[port] || tenants[port] != nil {
			return fmt.Errorf("invalid bridge member %s -> %s", port, bridge)
		}
	}
	for tenant, vni := range l3 {
		if tenants[tenant] == nil {
			return fmt.Errorf("L3VNI binding requires VRF %s", tenant)
		}
		found := false
		for name, v := range vxlan {
			if v.GetVxlan().Vni == vni && ports[name] != "" && bindings[ports[name]] == tenant {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("L3VNI %d requires a VXLAN member of a BD bound to VRF %s", vni, tenant)
		}
	}
	for _, o := range ops {
		m, _ := payload(o)
		vrf := str(m, "vrf")
		if vrf != "" && tenants[vrf] == nil {
			return fmt.Errorf("%s requires VRF %s", o.Code, vrf)
		}
		if o.Code >= a.OpCode_BGP_INSTANCE && o.Code != a.OpCode_TENANT_L3VNI && o.Code != a.OpCode_BGP_INSTANCE {
			asn, _ := strconv.ParseUint(str(m, "asn"), 10, 32)
			if bgp[vrf] == 0 || uint32(asn) != bgp[vrf] {
				return fmt.Errorf("%s requires matching BGP instance", o.Code)
			}
		}
		switch o.Code {
		case a.OpCode_ADDRESS:
			x := o.GetAddress()
			if !validName(x.Interface) {
				return fmt.Errorf("invalid address interface")
			}
			if _, e := netip.ParsePrefix(x.Prefix); e != nil {
				return e
			}
			if ports[x.Interface] != "" {
				return fmt.Errorf("bridge member cannot have an L3 address")
			}
		case a.OpCode_STATIC_ROUTE:
			x := o.GetRoute()
			if _, e := netip.ParsePrefix(x.Prefix); e != nil {
				return e
			}
			if _, e := netip.ParseAddr(x.Gateway); e != nil {
				return e
			}
		case a.OpCode_OSPF_NETWORK, a.OpCode_OSPF_INTERFACE:
			if !ospf[vrf] {
				return fmt.Errorf("OSPFv2 instance required")
			}
		case a.OpCode_OSPF6_INTERFACE:
			if !ospf6[vrf] {
				return fmt.Errorf("OSPFv3 instance required")
			}
		case a.OpCode_BGP_NEIGHBOR:
			x := o.GetPeer()
			if _, e := netip.ParseAddr(x.Name); e != nil {
				return fmt.Errorf("BGP neighbor address required")
			}
			if x.PeerGroup != "" {
				if !groups[vrf+"/"+x.PeerGroup] {
					return fmt.Errorf("peer group is absent")
				}
			} else if x.RemoteAs == 0 {
				return fmt.Errorf("remote ASN or peer group required")
			}
		case a.OpCode_BGP_EVPN_PEER, a.OpCode_BGP_AF_PEER:
			if !peers[vrf+"/"+o.GetPeer().Name] && !groups[vrf+"/"+o.GetPeer().Name] {
				return fmt.Errorf("BGP neighbor/peer group required")
			}
		case a.OpCode_EVPN_L2VNI:
			x := o.GetEvpn()
			if vnis[x.Vni] == "" || ports[vnis[x.Vni]] == "" {
				return fmt.Errorf("L2VNI %d requires an existing VXLAN/BD association", x.Vni)
			}
		case a.OpCode_EVPN_TYPE5:
			if vrf == "" || l3[vrf] == 0 {
				return fmt.Errorf("Type-5 export requires tenant BGP and L3VNI")
			}
		}
	}
	for _, o := range vxlan {
		if table := o.GetVxlan().GetEncapVrfId(); table != 0 && tables[table] == "" {
			return fmt.Errorf("VXLAN outer VRF table %d requires a managed VRF resource", table)
		}
	}
	return nil
}
func Compile(n *p.NodeResources) ([]*a.Operation, error) {
	if n == nil || n.NodeId == "" {
		return nil, fmt.Errorf("node resources require node_id")
	}
	ops, e := Apply(n.Objects, nil)
	if e != nil {
		return nil, e
	}
	if e = Validate(ops); e != nil {
		return nil, e
	}
	bridges := map[string]bool{}
	vxlan := map[string]bool{}
	policies := map[string]bool{}
	for _, o := range ops {
		if o.Code == a.OpCode_BD {
			bridges[o.GetBridge().Name] = true
		}
		if o.Code == a.OpCode_VXLAN {
			vxlan[o.GetVxlan().Name] = true
		}
	}
	for _, p := range n.BridgePolicies {
		if p == nil || !bridges[p.Bridge] || p.NeighSuppress == nil {
			return nil, fmt.Errorf("bridge policy requires an existing BD and neigh_suppress")
		}
		if _, ok := policies[p.Bridge]; ok {
			return nil, fmt.Errorf("duplicate BD policy")
		}
		policies[p.Bridge] = *p.NeighSuppress
	}
	for _, o := range ops {
		if o.Code == a.OpCode_BD_PORT {
			port := o.GetBridgePort()
			if value, ok := policies[port.Bridge]; ok && vxlan[port.Port] {
				v := value
				port.NeighSuppress = &v
			}
		}
	}
	return ops, nil
}

// CheckRemoved prevents a removed virtual link from being reinterpreted as an
// external physical port. Interface attributes disappear with the owned link.
func CheckRemoved(before, after []*a.Operation) ([]*a.Operation, error) {
	removed := map[string]bool{}
	current := map[string]bool{}
	for _, o := range after {
		if o.Code == a.OpCode_TENANT || o.Code == a.OpCode_BD || o.Code == a.OpCode_VXLAN {
			m, _ := payload(o)
			current[str(m, "name")] = true
		}
	}
	for _, o := range before {
		if o.Code == a.OpCode_TENANT || o.Code == a.OpCode_BD || o.Code == a.OpCode_VXLAN {
			m, _ := payload(o)
			name := str(m, "name")
			if !current[name] {
				removed[name] = true
			}
		}
	}
	out := make([]*a.Operation, 0, len(after))
	for _, o := range after {
		if o.Code == a.OpCode_INTERFACE && removed[o.GetInterface().Name] {
			continue
		}
		m, _ := payload(o)
		for _, f := range []string{"interface", "port", "bridge", "tenant", "underlay", "vrf"} {
			if removed[str(m, f)] {
				return nil, fmt.Errorf("cannot delete referenced virtual link %s (%s)", str(m, f), o.Code)
			}
		}
		if (o.Code == a.OpCode_OSPF_INTERFACE || o.Code == a.OpCode_OSPF6_INTERFACE) && removed[str(m, "name")] {
			return nil, fmt.Errorf("interface still has OSPF configuration")
		}
		out = append(out, o)
	}
	return out, nil
}
func MergePolicies(base []*p.BridgePolicy, edits []*p.BridgePolicy, objects []*a.Operation) ([]*p.BridgePolicy, error) {
	exists := map[string]bool{}
	for _, o := range objects {
		if o.Code == a.OpCode_BD {
			exists[o.GetBridge().Name] = true
		}
	}
	by := map[string]*p.BridgePolicy{}
	for _, v := range base {
		if v != nil && exists[v.Bridge] {
			by[v.Bridge] = proto.Clone(v).(*p.BridgePolicy)
		}
	}
	for _, v := range edits {
		if v == nil || !exists[v.Bridge] || v.NeighSuppress == nil {
			return nil, fmt.Errorf("policy requires existing BD and explicit neigh_suppress")
		}
		by[v.Bridge] = proto.Clone(v).(*p.BridgePolicy)
	}
	out := []*p.BridgePolicy{}
	for _, v := range by {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bridge < out[j].Bridge })
	return out, nil
}
