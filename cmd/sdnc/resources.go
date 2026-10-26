package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

func resourceCommand(s string) bool {
	switch s {
	case "tenant", "bd", "vxlan", "interface", "route", "bgp", "ospf", "resources", "change", "system":
		return true
	}
	return false
}

type resourceCLI struct {
	op     *a.Operation
	policy *p.BridgePolicy
	query  *a.QueryRequest
	flags  map[string]string
	words  []string
}

func parseFlags(args []string) ([]string, map[string]string, error) {
	words := []string{}
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		s := args[i]
		if !strings.HasPrefix(s, "--") {
			words = append(words, s)
			continue
		}
		key, value, has := strings.Cut(strings.TrimPrefix(s, "--"), "=")
		if !has {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, nil, fmt.Errorf("--%s needs a value (booleans use true/false)", key)
			}
			i++
			value = args[i]
		}
		if _, ok := flags[key]; ok {
			return nil, nil, fmt.Errorf("duplicate flag --%s", key)
		}
		flags[key] = value
	}
	return words, flags, nil
}
func cliFamily(s string) (a.AddressFamily, error) {
	switch strings.ToLower(s) {
	case "", "ipv4", "4":
		return a.AddressFamily_IPV4, nil
	case "ipv6", "6":
		return a.AddressFamily_IPV6, nil
	case "evpn", "l2vpn_evpn":
		return a.AddressFamily_L2VPN_EVPN, nil
	}
	return 0, fmt.Errorf("family must be ipv4, ipv6 or evpn")
}
func parseResourceCLI(args []string) (*resourceCLI, error) {
	words, flags, e := parseFlags(args)
	if e != nil {
		return nil, e
	}
	r := &resourceCLI{flags: flags, words: words}
	if len(words) < 2 {
		return nil, fmt.Errorf("usage: tenant|bd|vxlan|interface|route|bgp|ospf <action> [name] [--field value]")
	}
	group, action := words[0], words[1]
	name := ""
	if len(words) > 2 {
		name = words[2]
	}
	if group == "resources" || group == "change" {
		return r, nil
	}
	// Query names are deliberately distinct from configuration commands.
	queries := map[string]map[string]a.QueryKind{
		"tenant": {"show": a.QueryKind_TENANTS}, "bd": {"show": a.QueryKind_BRIDGES, "ports": a.QueryKind_PORTS, "fdb": a.QueryKind_FDB, "mac": a.QueryKind_FDB, "neighbors": a.QueryKind_NEIGHBORS, "fib": a.QueryKind_ROUTES, "addresses": a.QueryKind_ADDRESSES},
		"vxlan": {"show": a.QueryKind_VXLAN_TUNNELS}, "interface": {"show": a.QueryKind_INTERFACES, "addresses": a.QueryKind_ADDRESSES}, "route": {"show": a.QueryKind_ROUTES},
		"bgp":  {"show": a.QueryKind_BGP_SUMMARY, "summary": a.QueryKind_BGP_SUMMARY, "neighbors": a.QueryKind_BGP_NEIGHBORS, "routes": a.QueryKind_BGP_ROUTES, "evpn-routes": a.QueryKind_EVPN_ROUTES, "vni": a.QueryKind_EVPN_VNI},
		"ospf": {"show": a.QueryKind_OSPF_SUMMARY, "summary": a.QueryKind_OSPF_SUMMARY, "interfaces": a.QueryKind_OSPF_INTERFACES, "neighbors": a.QueryKind_OSPF_NEIGHBORS, "database": a.QueryKind_OSPF_DATABASE, "routes": a.QueryKind_OSPF_ROUTES}}
	if kind := queries[group][action]; kind != 0 {
		q := &a.QueryRequest{Kind: kind, Vrf: flags["vrf"], Bridge: flags["bridge"], Interface: flags["interface"], Prefix: flags["prefix"], Neighbor: flags["neighbor"], PageToken: flags["page-token"]}
		if name != "" {
			switch group {
			case "tenant":
				q.Vrf = name
			case "bd":
				if kind == a.QueryKind_ROUTES {
					q.Interface = name
				} else {
					q.Bridge = name
				}
			case "interface", "vxlan":
				q.Interface = name
			default:
				return nil, fmt.Errorf("unexpected query name")
			}
		}
		if flags["family"] != "" {
			q.Family, e = cliFamily(flags["family"])
			if e != nil {
				return nil, e
			}
		}
		for key, dest := range map[string]*uint32{"version": &q.OspfVersion, "page-size": &q.PageSize, "route-type": &q.RouteType} {
			if flags[key] != "" {
				v, err := strconv.ParseUint(flags[key], 10, 32)
				if err != nil {
					return nil, err
				}
				*dest = uint32(v)
			}
		}
		allowed := map[string]bool{}
		for _, k := range []string{"node", "vrf", "bridge", "interface", "prefix", "neighbor", "page-token", "family", "version", "page-size", "route-type"} {
			allowed[k] = true
		}
		for k := range flags {
			if !allowed[k] {
				return nil, fmt.Errorf("unsupported query flag --%s", k)
			}
		}
		r.query = q
		return r, nil
	}
	if group == "bd" && action == "suppress" {
		if name == "" {
			return nil, fmt.Errorf("bd suppress NAME --enabled true|false")
		}
		v, e := strconv.ParseBool(flags["enabled"])
		if e != nil {
			return nil, e
		}
		r.policy = &p.BridgePolicy{Bridge: name, NeighSuppress: &v}
		return r, nil
	}
	var code a.OpCode
	var dataName, fields string
	data := map[string]any{}
	switch group {
	case "tenant":
		switch action {
		case "create", "delete":
			code = a.OpCode_TENANT
			dataName = "tenant"
			fields = "table-id"
			data["name"] = name
		case "bind-vni", "unbind-vni":
			code = a.OpCode_TENANT_L3VNI
			dataName = "l3vni"
			fields = "vni"
			data["tenant"] = name
		}
	case "bd":
		switch action {
		case "create", "set", "delete":
			code = a.OpCode_BD
			dataName = "bridge"
			fields = "stp mtu mac ageing-time"
			data["name"] = name
		case "bind-tenant", "unbind-tenant":
			code = a.OpCode_BD_TENANT
			dataName = "binding"
			fields = "tenant"
			data["bridge"] = name
		case "port-add", "port-delete", "port-set":
			code = a.OpCode_BD_PORT
			dataName = "bridgePort"
			fields = "port learning neigh-suppress"
			data["bridge"] = name
		case "address-add", "address-delete":
			code = a.OpCode_ADDRESS
			dataName = "address"
			fields = "prefix"
			data["interface"] = name
		}
	case "vxlan":
		switch action {
		case "create", "delete":
			code = a.OpCode_VXLAN
			dataName = "vxlan"
			fields = "vni local port learning underlay"
			data["name"] = name
		case "bind", "unbind":
			code = a.OpCode_BD_PORT
			dataName = "bridgePort"
			fields = "bridge learning neigh-suppress"
			data["port"] = name
		}
	case "interface":
		switch action {
		case "set":
			code = a.OpCode_INTERFACE
			dataName = "interface"
			fields = "mtu mac up addrgenmode"
			data["name"] = name
		case "address-add", "address-delete":
			code = a.OpCode_ADDRESS
			dataName = "address"
			fields = "prefix"
			data["interface"] = name
		}
	case "route":
		switch action {
		case "create", "delete":
			code = a.OpCode_STATIC_ROUTE
			dataName = "route"
			fields = "gateway interface table"
			data["prefix"] = name
		}
	case "system":
		if action == "set" {
			code = a.OpCode_NODE_NETWORK
			dataName = "network"
			fields = ""
			if name == "" || flags["value"] == "" {
				return nil, fmt.Errorf("system set SYSCTL --value VALUE")
			}
			data["settings"] = []any{map[string]any{"key": name, "value": flags["value"]}}
			delete(flags, "value")
		}
	case "bgp":
		if len(words) < 3 {
			return nil, fmt.Errorf("bgp instance|neighbor|peer-group|af-peer|evpn|l2vni|tenant|network|redistribute|type5 create|set|delete [name]")
		}
		kind := action
		action = words[2]
		name = ""
		if len(words) > 3 {
			name = words[3]
		}
		fields = "asn vrf"
		switch kind {
		case "instance":
			code = a.OpCode_BGP_INSTANCE
			dataName = "bgp"
			fields += " router-id default-ipv4-unicast"
		case "neighbor", "peer-group", "af-peer", "evpn-peer":
			code = map[string]a.OpCode{"neighbor": a.OpCode_BGP_NEIGHBOR, "peer-group": a.OpCode_BGP_PEER_GROUP, "af-peer": a.OpCode_BGP_AF_PEER, "evpn-peer": a.OpCode_BGP_EVPN_PEER}[kind]
			dataName = "peer"
			fields += " remote-as update-source peer-group active rr-client family"
			data["name"] = name
		case "evpn":
			code = a.OpCode_BGP_EVPN_GLOBAL
			dataName = "evpnGlobal"
			fields = "asn advertise-all-vni advertise-svi-ip"
		case "l2vni", "tenant":
			code = a.OpCode_EVPN_TENANT
			if kind == "l2vni" {
				code = a.OpCode_EVPN_L2VNI
			}
			dataName = "evpn"
			fields += " vni rd import-rts export-rts"
		case "network":
			code = a.OpCode_BGP_NETWORK
			dataName = "bgpNetwork"
			fields += " family prefix"
			if name != "" {
				data["prefix"] = name
			}
		case "redistribute":
			code = a.OpCode_BGP_REDISTRIBUTE
			dataName = "bgpRedistribute"
			fields += " family protocol"
			if name != "" {
				data["protocol"] = name
			}
		case "type5":
			code = a.OpCode_EVPN_TYPE5
			dataName = "bgpType5"
			fields += " family enabled"
		}
	case "ospf":
		if len(words) < 3 {
			return nil, fmt.Errorf("ospf instance|interface|network create|set|delete [name] --version 2|3")
		}
		kind := action
		action = words[2]
		name = ""
		if len(words) > 3 {
			name = words[3]
		}
		version := flags["version"]
		if version == "" {
			version = "2"
		}
		if version != "2" && version != "3" {
			return nil, fmt.Errorf("OSPF version must be 2 or 3")
		}
		delete(flags, "version")
		fields = "vrf"
		switch kind {
		case "instance":
			code = a.OpCode_OSPF_INSTANCE
			if version == "3" {
				code = a.OpCode_OSPF6_INSTANCE
			}
			dataName = "ospf"
			fields += " router-id"
		case "interface":
			code = a.OpCode_OSPF_INTERFACE
			dataName = "ospfInterface"
			if version == "3" {
				code = a.OpCode_OSPF6_INTERFACE
				dataName = "ospf6Interface"
			}
			fields += " area passive network-type cost hello-interval dead-interval"
			data["name"] = name
		case "network":
			if version != "2" {
				return nil, fmt.Errorf("OSPFv3 uses interface area configuration")
			}
			code = a.OpCode_OSPF_NETWORK
			dataName = "ospfNetwork"
			fields += " prefix area"
			if name != "" {
				data["prefix"] = name
			}
		}
	}
	if code == 0 {
		return nil, fmt.Errorf("unknown resource command %s %s", group, action)
	}
	act := map[string]a.Action{"create": a.Action_CREATE, "set": a.Action_SET, "delete": a.Action_DELETE, "bind": a.Action_BIND, "unbind": a.Action_UNBIND, "bind-vni": a.Action_BIND, "unbind-vni": a.Action_UNBIND, "bind-tenant": a.Action_BIND, "unbind-tenant": a.Action_UNBIND, "port-add": a.Action_BIND, "port-delete": a.Action_UNBIND, "port-set": a.Action_SET, "address-add": a.Action_CREATE, "address-delete": a.Action_DELETE}[action]
	if act == 0 {
		return nil, fmt.Errorf("unsupported action %s", action)
	}
	allowed := map[string]bool{"node": true, "change": true, "request-id": true}
	for _, f := range strings.Fields(fields) {
		allowed[f] = true
	}
	for k, v := range flags {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported --%s for this command", k)
		}
		if k == "node" || k == "change" || k == "request-id" {
			continue
		}
		key := k
		for strings.Contains(key, "-") {
			i := strings.IndexByte(key, '-')
			key = key[:i] + strings.ToUpper(key[i+1:i+2]) + key[i+2:]
		}
		switch k {
		case "stp", "learning", "neigh-suppress", "up", "default-ipv4-unicast", "active", "rr-client", "advertise-all-vni", "advertise-svi-ip", "passive", "enabled":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, err
			}
			data[key] = b
		case "table-id", "table", "vni", "port", "mtu", "ageing-time", "asn", "remote-as", "cost", "hello-interval", "dead-interval":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return nil, err
			}
			data[key] = uint32(n)
		case "family":
			f, err := cliFamily(v)
			if err != nil {
				return nil, err
			}
			data[key] = f.String()
		case "import-rts", "export-rts":
			if v == "" {
				data[key] = []string{}
			} else {
				data[key] = strings.Split(v, ",")
			}
		default:
			data[key] = v
		}
	}
	raw, _ := json.Marshal(map[string]any{"code": code.String(), "action": act.String(), dataName: data})
	r.op = &a.Operation{}
	if e = protojson.Unmarshal(raw, r.op); e != nil {
		return nil, e
	}
	return r, nil
}
func cliID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func runResources(ctx context.Context, client p.NorthClient, args []string, node, request string, wait bool, revision uint64, emit func(proto.Message) error) error {
	r, e := parseResourceCLI(args)
	if e != nil {
		return e
	}
	if r.flags["node"] != "" {
		node = r.flags["node"]
	}
	if r.flags["request-id"] != "" {
		request = r.flags["request-id"]
	}
	if request == "" {
		request = cliID()
	}
	group, action := r.words[0], r.words[1]
	if group == "change" {
		changeID := r.flags["change"]
		if len(r.words) > 2 {
			changeID = r.words[2]
		}
		switch action {
		case "begin":
			state, e := client.GetConfiguration(ctx, &p.Empty{})
			if e != nil {
				return e
			}
			x, e := client.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: state.Revision, RequestId: request + "/begin"})
			if e != nil {
				return e
			}
			return emit(x)
		case "show":
			x, e := client.GetChange(ctx, &p.ChangeRequest{ChangeId: changeID})
			if e != nil {
				return e
			}
			return emit(x)
		case "plan":
			x, e := client.PlanChange(ctx, &p.ChangeRequest{ChangeId: changeID})
			if e != nil {
				return e
			}
			return emit(x)
		case "abort":
			x, e := client.AbortChange(ctx, &p.ChangeRequest{ChangeId: changeID})
			if e != nil {
				return e
			}
			return emit(x)
		case "commit":
			return commitResourceCLI(ctx, client, changeID, request, wait, emit)
		}
		return fmt.Errorf("change begin|show|plan|commit|abort [CHANGE_ID]")
	}
	if node == "" {
		return fmt.Errorf("--node is required")
	}
	if group == "resources" {
		switch action {
		case "show":
			x, e := client.GetNodeResources(ctx, &p.NodeResourcesRequest{NodeId: node, ChangeId: r.flags["change"]})
			if e != nil {
				return e
			}
			return emit(x)
		case "import":
			if v := r.flags["revision"]; v != "" {
				revision, e = strconv.ParseUint(v, 10, 64)
				if e != nil {
					return e
				}
			}
			x, e := client.ImportNodeResources(ctx, &p.ImportNodeResourcesRequest{NodeId: node, ExpectedAgentRevision: revision, RequestId: request})
			if e != nil {
				return e
			}
			return emit(x)
		}
		return fmt.Errorf("resources show|import")
	}
	if r.query != nil {
		r.query.NodeId = node
		x, e := client.QueryNode(ctx, r.query)
		if e != nil {
			return e
		}
		return emit(x)
	}
	var change *p.Change
	if cid := r.flags["change"]; cid != "" {
		change, e = client.GetChange(ctx, &p.ChangeRequest{ChangeId: cid})
	} else {
		state, err := client.GetConfiguration(ctx, &p.Empty{})
		if err != nil {
			return err
		}
		change, e = client.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: state.Revision, RequestId: request + "/begin"})
	}
	if e != nil {
		return e
	}
	if change.Frozen {
		x, e := client.GetJob(ctx, &p.JobRequest{JobId: change.JobId})
		if e != nil {
			return e
		}
		return emit(x)
	}
	req := &p.EditNodeResourcesRequest{NodeId: node, ChangeId: change.Id, EditRevision: change.EditRevision}
	if r.op != nil {
		req.Operations = []*a.Operation{r.op}
	}
	if r.policy != nil {
		req.BridgePolicies = []*p.BridgePolicy{r.policy}
	}
	change, e = client.EditNodeResources(ctx, req)
	if e != nil {
		return e
	}
	if r.flags["change"] != "" {
		return emit(change)
	}
	return commitResourceCLI(ctx, client, change.Id, request, wait, emit)
}
func commitResourceCLI(ctx context.Context, client p.NorthClient, change, request string, wait bool, emit func(proto.Message) error) error {
	plan, e := client.PlanChange(ctx, &p.ChangeRequest{ChangeId: change})
	if e != nil {
		return e
	}
	job, e := client.CommitChange(ctx, &p.CommitChangeRequest{ChangeId: change, PlanId: plan.Id, RequestId: request + "/commit"})
	if e != nil {
		return e
	}
	if !wait {
		return emit(job)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch job.State {
		case "SUCCEEDED":
			return emit(job)
		case "FAILED", "PARTIAL", "ROLLED_BACK", "COMPENSATION_FAILED":
			_ = emit(job)
			return fmt.Errorf("job %s ended %s: %s", job.Id, job.State, job.Error)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w; query job %s (execution continues)", ctx.Err(), job.Id)
		case <-ticker.C:
		}
		job, e = client.GetJob(ctx, &p.JobRequest{JobId: job.Id})
		if e != nil {
			return e
		}
	}
}
