package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

func (c *Controller) GetTopology(ctx context.Context, q *p.TopologyRequest) (*p.Topology, error) {
	d := c.snapshot()
	selected := map[string]bool{}
	if len(q.NodeIds) == 0 {
		for id := range d.Nodes {
			selected[id] = true
		}
	} else {
		for _, id := range q.NodeIds {
			if d.Nodes[id] == nil {
				return nil, status.Error(codes.NotFound, "unknown topology node")
			}
			selected[id] = true
		}
	}
	if q.Refresh {
		if !c.collectMu.TryLock() {
			return nil, status.Error(codes.ResourceExhausted, "a topology refresh is already in progress")
		}
		defer c.collectMu.Unlock()
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for id := range selected {
			n := d.Nodes[id]
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-call.Done():
					c.observationFailure(n.NodeId, call.Err())
					return
				}
				client, e := c.client(n)
				if e != nil {
					c.observationFailure(n.NodeId, e)
					return
				}
				child, stop := context.WithTimeout(call, 5*time.Second)
				defer stop()
				s, e := client.GetState(child, &a.NodeRequest{NodeId: n.NodeId})
				if e != nil {
					c.observationFailure(n.NodeId, e)
					return
				}
				o := &p.Observation{NodeId: n.NodeId, CollectedUnixMs: time.Now().UnixMilli(), Data: s.Observations, AgentRevision: s.Revision, Error: s.ObservationError, Stale: s.ObservationError != ""}
				c.mu.Lock()
				if old := c.observations[n.NodeId]; old != nil && s.ObservationError != "" {
					merged := map[string]string{}
					for k, v := range old.Data {
						merged[k] = v
					}
					for k, v := range o.Data {
						merged[k] = v
					}
					o.Data = merged
				}
				c.observations[n.NodeId] = o
				c.mu.Unlock()
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	out := &p.Topology{DesiredRevision: d.Revision, Warnings: []string{"Physical links are not inferred from EVPN; LLDP ingestion is not implemented.", "Desired configuration links do not prove data-plane reachability."}}
	ids := []string{}
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := d.Nodes[id]
		label := n.Name
		if label == "" {
			label = id
		}
		out.Nodes = append(out.Nodes, &p.TopologyNode{Id: "node/" + id, Label: label, Kind: "node", NodeId: id, Source: "inventory"})
		c.mu.Lock()
		o := c.observations[id]
		if o != nil {
			o = proto.Clone(o).(*p.Observation)
		}
		c.mu.Unlock()
		if o == nil {
			o = &p.Observation{NodeId: id, Stale: true, Error: "not collected"}
		}
		if time.Since(time.UnixMilli(o.CollectedUnixMs)) > 30*time.Second || c.nodeView(n).State == "DOWN" {
			o.Stale = true
		}
		out.Observations = append(out.Observations, o)
		if raw := o.Data["frr/evpn"]; raw != "" {
			routes, e := ParseEVPN(id, raw)
			if e != nil {
				out.Warnings = append(out.Warnings, id+": "+e.Error())
			} else {
				out.EvpnRoutes = append(out.EvpnRoutes, routes...)
			}
		}
	}
	for _, t := range d.Configuration.Tenants {
		tid := "tenant/" + t.Id
		out.Nodes = append(out.Nodes, &p.TopologyNode{Id: tid, Label: t.Name, Kind: "tenant", TenantId: t.Id, Source: "desired"})
		for _, x := range t.Placements {
			if selected[x.NodeId] {
				out.Links = append(out.Links, &p.TopologyLink{Source: "node/" + x.NodeId, Target: tid, Kind: "tenant-placement", Origin: "desired"})
			}
		}
	}
	for _, nw := range d.Configuration.Networks {
		nid := "network/" + nw.Id
		out.Nodes = append(out.Nodes, &p.TopologyNode{Id: nid, Label: nw.Id, Kind: "network", TenantId: nw.TenantId, NetworkId: nw.Id, Source: "desired"})
		out.Links = append(out.Links, &p.TopologyLink{Source: "tenant/" + nw.TenantId, Target: nid, Kind: "tenant-network", Origin: "desired"})
		for _, x := range nw.Placements {
			if selected[x.NodeId] {
				out.Links = append(out.Links, &p.TopologyLink{Source: "node/" + x.NodeId, Target: nid, Kind: "network-placement", Origin: "desired"})
			}
		}
	}
	// Resource identities are local to a node. Empty VRFs and unbound BDs/VXLANs
	// remain visible, and only explicit desired relationships create links.
	for _, nr := range d.Configuration.NodeResources {
		if !selected[nr.NodeId] {
			continue
		}
		prefix := "resource/" + nr.NodeId + "/"
		for _, op := range nr.Objects {
			var name, kind string
			switch op.Code {
			case a.OpCode_TENANT:
				name, kind = op.GetTenant().Name, "tenant"
			case a.OpCode_BD:
				name, kind = op.GetBridge().Name, "bridge"
			case a.OpCode_VXLAN:
				name, kind = op.GetVxlan().Name, "vxlan"
			}
			if name != "" {
				out.Nodes = append(out.Nodes, &p.TopologyNode{Id: prefix + name, NodeId: nr.NodeId, Label: name, Kind: kind, Source: "desired"})
				out.Links = append(out.Links, &p.TopologyLink{Source: "node/" + nr.NodeId, Target: prefix + name, Kind: "resource-placement", Origin: "desired"})
			}
			switch op.Code {
			case a.OpCode_BD_TENANT:
				x := op.GetBinding()
				out.Links = append(out.Links, &p.TopologyLink{Source: prefix + x.Tenant, Target: prefix + x.Bridge, Kind: "tenant-bridge", Origin: "desired"})
			case a.OpCode_BD_PORT:
				x := op.GetBridgePort()
				found := false
				for _, v := range nr.Objects {
					if v.Code == a.OpCode_VXLAN && v.GetVxlan().Name == x.Port {
						found = true
					}
				}
				if found {
					out.Links = append(out.Links, &p.TopologyLink{Source: prefix + x.Bridge, Target: prefix + x.Port, Kind: "bridge-vxlan", Origin: "desired"})
				}
			case a.OpCode_TENANT_L3VNI:
				x := op.GetL3Vni()
				for _, v := range nr.Objects {
					if v.Code == a.OpCode_VXLAN && v.GetVxlan().Vni == x.Vni {
						out.Links = append(out.Links, &p.TopologyLink{Source: prefix + x.Tenant, Target: prefix + v.GetVxlan().Name, Kind: "tenant-l3vni", Origin: "desired"})
					}
				}
			}
		}
	}
	if len(out.EvpnRoutes) > 2000 {
		out.EvpnRoutes = out.EvpnRoutes[:2000]
		out.Warnings = append(out.Warnings, "EVPN route output truncated to 2000; query a narrower node scope")
	}
	addObservedGraph(out, d.Configuration, d.Nodes)
	// Raw snapshots stay in the cache; cap the public raw-data portion so a
	// dashboard query is not an unbounded dump of every node's forwarding table.
	rawBudget := 512 * 1024
	for _, o := range out.Observations {
		keys := []string{}
		for k := range o.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(o.Data[k]) > rawBudget {
				delete(o.Data, k)
				out.Warnings = append(out.Warnings, o.NodeId+" raw observation "+k+" omitted from response size budget")
			} else {
				rawBudget -= len(o.Data[k])
			}
		}
	}
	return out, nil
}
func (c *Controller) observationFailure(id string, e error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.observations[id]
	if o == nil {
		o = &p.Observation{NodeId: id}
		c.observations[id] = o
	}
	o.Error = safeError(e)
	o.Stale = true
}

// ParseEVPN preserves source+RD+NLRI and all path metadata. RT is not interpreted
// as a VNI or tenant ID. Unknown FRR shapes return an explicit error.
func ParseEVPN(node, raw string) ([]*p.EvpnRoute, error) {
	var root map[string]json.RawMessage
	if e := json.Unmarshal([]byte(raw), &root); e != nil {
		return nil, fmt.Errorf("invalid FRR EVPN JSON: %w", e)
	}
	out := []*p.EvpnRoute{}
	foundRD := false
	for _, v := range root {
		var bucket map[string]json.RawMessage
		if json.Unmarshal(v, &bucket) != nil {
			continue
		}
		var rd string
		_ = json.Unmarshal(bucket["rd"], &rd)
		if rd == "" {
			continue
		}
		foundRD = true
		for nlri, data := range bucket {
			typ := uint32(0)
			if strings.HasPrefix(nlri, "[2]:") {
				typ = 2
			} else if strings.HasPrefix(nlri, "[3]:") {
				typ = 3
			} else {
				continue
			}
			var record struct {
				Paths []struct {
					Mac, IP           string
					NextHops          []struct{ IP string }   `json:"nexthops"`
					ExtendedCommunity struct{ String string } `json:"extendedCommunity"`
				} `json:"paths"`
			}
			if e := json.Unmarshal(data, &record); e != nil {
				return nil, fmt.Errorf("invalid route %s", nlri)
			}
			r := &p.EvpnRoute{SourceNode: node, Rd: rd, Type: typ, Nlri: nlri, RawJson: string(data)}
			hops, rts := map[string]bool{}, map[string]bool{}
			for _, path := range record.Paths {
				if r.Mac == "" {
					r.Mac = path.Mac
				}
				if r.Ip == "" {
					r.Ip = path.IP
				}
				for _, hop := range path.NextHops {
					if hop.IP != "" {
						hops[hop.IP] = true
					}
				}
				for _, word := range strings.Fields(path.ExtendedCommunity.String) {
					if strings.HasPrefix(word, "RT:") {
						rts[strings.TrimPrefix(word, "RT:")] = true
					}
				}
			}
			for x := range hops {
				r.NextHops = append(r.NextHops, x)
			}
			for x := range rts {
				r.RouteTargets = append(r.RouteTargets, x)
			}
			sort.Strings(r.NextHops)
			sort.Strings(r.RouteTargets)
			out = append(out, r)
		}
	}
	if !foundRD {
		var total int
		_ = json.Unmarshal(root["numPrefix"], &total)
		_, hasCount := root["numPrefix"]
		if total > 0 || !hasCount {
			return nil, fmt.Errorf("unsupported FRR EVPN schema; raw observation retained")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rd+out[i].Nlri < out[j].Rd+out[j].Nlri })
	return out, nil
}

// Match L2 network RT policy explicitly. Shared tenant RTs never alone select a
// network. Ambiguous or unrecognized mappings remain visible without invention.
func addObservedGraph(out *p.Topology, cfg *p.Configuration, inventory map[string]*p.Node) {
	profiles := map[string]*p.NodeProfile{}
	for _, n := range cfg.Nodes {
		profiles[n.NodeId] = n
	}
	seenNodes, seenLinks := map[string]bool{}, map[string]bool{}
	addNode := func(n *p.TopologyNode) {
		if !seenNodes[n.Id] {
			seenNodes[n.Id] = true
			out.Nodes = append(out.Nodes, n)
		}
	}
	link := func(source, target, kind string) {
		key := source + "/" + target + "/" + kind
		if !seenLinks[key] {
			seenLinks[key] = true
			out.Links = append(out.Links, &p.TopologyLink{Source: source, Target: target, Kind: kind, Origin: "observed"})
		}
	}
	for _, r := range out.EvpnRoutes {
		record := inventory[r.SourceNode]
		if record == nil {
			continue
		}
		var matches []*p.Network
		for _, nw := range cfg.Networks {
			applicable := false
			policy := append(append([]string{}, nw.ImportRts...), nw.ExportRts...)
			for _, pl := range nw.Placements {
				n := profiles[pl.NodeId]
				if n == nil {
					continue
				}
				if n.FabricId == record.FabricId {
					applicable = true
				}
				if len(nw.ImportRts) == 0 || len(nw.ExportRts) == 0 {
					policy = append(policy, fmt.Sprintf("%d:%d", n.Asn, nw.L2Vni))
				}
			}
			if !applicable {
				continue
			}
			matched := false
			for _, rt := range r.RouteTargets {
				for _, wanted := range policy {
					if rt == wanted {
						matched = true
					}
				}
			}
			if matched {
				matches = append(matches, nw)
			}
		}
		var network *p.Network
		if len(matches) == 1 {
			network = matches[0]
		}
		if network == nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s RD %s has unknown/ambiguous L2 network mapping; route context retained", r.SourceNode, r.Rd))
		}
		hops := r.NextHops
		if r.Type == 3 && r.Ip != "" {
			hops = []string{r.Ip}
		}
		vteps := []string{}
		for _, ip := range hops {
			if ip == "" || ip == "0.0.0.0" || ip == "::" {
				continue
			}
			v := fmt.Sprintf("vtep/%s/%s", record.FabricId, ip)
			vteps = append(vteps, v)
			addNode(&p.TopologyNode{Id: v, Label: ip, Kind: "vtep", Source: "observed"})
			if network != nil {
				link(v, "network/"+network.Id, "evpn-membership")
			}
		}
		if r.Type == 2 {
			host := fmt.Sprintf("host/%s/%s/%s/%s", r.SourceNode, r.Rd, r.Mac, r.Ip)
			label := r.Mac
			if r.Ip != "" {
				label += " / " + r.Ip
			}
			n := &p.TopologyNode{Id: host, Label: label, Kind: "host", NodeId: r.SourceNode, Source: "observed"}
			if network != nil {
				n.NetworkId = network.Id
				n.TenantId = network.TenantId
			}
			addNode(n)
			for _, v := range vteps {
				link(v, host, "evpn-reachability")
			}
			if network != nil {
				link("network/"+network.Id, host, "evpn-host")
			}
		}
	}
}
