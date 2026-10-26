package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/planner"
	"sdn-controller/internal/resources"
)

func resourceCollection(cfg *p.Configuration, node string) *p.NodeResources {
	for _, r := range cfg.GetNodeResources() {
		if r.NodeId == node {
			return r
		}
	}
	return nil
}
func legacyNode(cfg *p.Configuration, node string) bool {
	for _, n := range cfg.GetNodes() {
		if n.NodeId == node {
			return true
		}
	}
	return false
}
func nodeBusy(d *p.Database, node string) bool {
	for _, j := range d.Jobs {
		for _, n := range j.Nodes {
			if n.NodeId == node && (!terminal(j.State) || n.RollbackPending || n.State == "ROLLBACK_FAILED") {
				return true
			}
		}
	}
	return false
}
func resourceMode(d *p.Database, cfg *p.Configuration, node string) string {
	if resourceCollection(cfg, node) != nil {
		return "resources"
	}
	if legacyNode(cfg, node) {
		return "legacy"
	}
	if n := d.Nodes[node]; n != nil && n.Adopted && n.AgentRevision == 0 {
		return "resources"
	}
	return "unmanaged"
}
func (c *Controller) GetNodeResources(ctx context.Context, q *p.NodeResourcesRequest) (*p.NodeResourcesState, error) {
	d := c.snapshot()
	n := d.Nodes[q.NodeId]
	if n == nil {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	cfg := d.Configuration
	var edit uint64
	if q.ChangeId != "" {
		x := d.Changes[q.ChangeId]
		if x == nil {
			return nil, status.Error(codes.NotFound, "change not found")
		}
		cfg = x.Configuration
		edit = x.EditRevision
	}
	r := resourceCollection(cfg, q.NodeId)
	if r == nil {
		r = &p.NodeResources{NodeId: q.NodeId}
	}
	if legacyNode(cfg, q.NodeId) {
		all, e := planner.Compile(cfg)
		if e != nil {
			return nil, status.Error(codes.FailedPrecondition, e.Error())
		}
		r.Objects = all[q.NodeId]
	}
	return &p.NodeResourcesState{Resources: r, DesiredRevision: d.Revision, EditRevision: edit, AgentRevision: n.AgentRevision, Adopted: n.Adopted, State: c.nodeView(n).State, Mode: resourceMode(d, cfg, q.NodeId)}, nil
}
func (c *Controller) EditNodeResources(ctx context.Context, q *p.EditNodeResourcesRequest) (out *p.Change, e error) {
	e = c.update(ctx, true, func(d *p.Database) error {
		x := d.Changes[q.ChangeId]
		if x == nil {
			return status.Error(codes.NotFound, "change not found")
		}
		if x.Aborted || x.Frozen {
			return status.Error(codes.FailedPrecondition, "change is frozen or aborted")
		}
		if x.BaseRevision != d.Revision || x.EditRevision != q.EditRevision {
			return status.Error(codes.Aborted, "candidate or desired revision conflict")
		}
		n := d.Nodes[q.NodeId]
		if n == nil {
			return status.Error(codes.NotFound, "node not found")
		}
		if !n.Adopted {
			return status.Error(codes.FailedPrecondition, "node must be explicitly adopted/imported")
		}
		if resourceMode(d, x.Configuration, q.NodeId) != "resources" {
			return status.Error(codes.FailedPrecondition, "existing configuration must be explicitly verified with ImportNodeResources before resource editing")
		}
		nr := resourceCollection(x.Configuration, q.NodeId)
		if nr == nil {
			nr = &p.NodeResources{NodeId: q.NodeId}
			x.Configuration.NodeResources = append(x.Configuration.NodeResources, nr)
		}
		next, err := resources.Apply(nr.Objects, q.Operations)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		next, err = resources.CheckRemoved(nr.Objects, next)
		if err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		policies, err := resources.MergePolicies(nr.BridgePolicies, q.BridgePolicies, next)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		nr.Objects = next
		nr.BridgePolicies = policies
		// A candidate may be staged in dependency order across multiple edits. Full
		// reference validation occurs at Plan/Commit; malformed operations never stage.
		x.EditRevision++
		for k, v := range d.Plans {
			if v.ChangeId == x.Id {
				delete(d.Plans, k)
			}
		}
		out = proto.Clone(x).(*p.Change)
		return nil
	})
	return
}

func (c *Controller) QueryNode(ctx context.Context, q *a.QueryRequest) (*a.QueryResponse, error) {
	if q == nil || q.Kind == a.QueryKind_QUERY_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "node_id and query kind required")
	}
	n, e := c.GetNode(ctx, &p.NodeRequest{NodeId: q.NodeId})
	if e != nil {
		return nil, e
	}
	if n.State != "RUNNING" {
		return nil, status.Error(codes.Unavailable, "node is not RUNNING")
	}
	supported := false
	for _, cap := range n.GetCapabilities().GetQueries() {
		if cap.Kind != q.Kind {
			continue
		}
		supported = true
		if q.Family != a.AddressFamily_AF_UNSPECIFIED && len(cap.Families) > 0 {
			ok := false
			for _, v := range cap.Families {
				ok = ok || v == q.Family
			}
			supported = supported && ok
		}
		if q.OspfVersion != 0 && len(cap.OspfVersions) > 0 {
			ok := false
			for _, v := range cap.OspfVersions {
				ok = ok || v == q.OspfVersion
			}
			supported = supported && ok
		}
	}
	if !supported {
		return nil, status.Error(codes.Unimplemented, "node does not advertise this query/family/version")
	}
	client, e := c.client(n)
	if e != nil {
		return nil, status.Error(codes.Unavailable, e.Error())
	}
	call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
	defer cancel()
	out, e := client.Query(call, q)
	if e != nil {
		return nil, e
	}
	if out.NodeId != q.NodeId || out.Kind != q.Kind {
		return nil, status.Error(codes.DataLoss, "query response identity mismatch")
	}
	return out, nil
}

// Import is a readback-confirmed ownership transition. It never executes any
// operation. Unknown/unverifiable history fails closed rather than claiming it.
func (c *Controller) ImportNodeResources(ctx context.Context, q *p.ImportNodeResourcesRequest) (*p.NodeResourcesState, error) {
	if e := requestID(q.RequestId); e != nil {
		return nil, e
	}
	d := c.snapshot()
	key := "import/" + q.RequestId
	h := hash(q)
	if found, e := receipt(d, key, h); e != nil {
		return nil, e
	} else if found != "" {
		return c.GetNodeResources(ctx, &p.NodeResourcesRequest{NodeId: found})
	}
	n := d.Nodes[q.NodeId]
	if n == nil {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	if c.nodeView(n).State != "RUNNING" || nodeBusy(d, q.NodeId) {
		return nil, status.Error(codes.FailedPrecondition, "node must be RUNNING without active tasks or failed rollback")
	}
	if resourceCollection(d.Configuration, q.NodeId) != nil {
		return nil, status.Error(codes.FailedPrecondition, "resource ownership already exists; import cannot hide drift")
	}
	old := d.Applied[q.NodeId]
	if old != nil && old.AgentRevision != q.ExpectedAgentRevision {
		return nil, status.Error(codes.Aborted, "managed baseline revision differs; import cannot hide drift")
	}
	client, e := c.client(n)
	if e != nil {
		return nil, e
	}
	call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
	defer cancel()
	state, e := client.GetState(call, &a.NodeRequest{NodeId: q.NodeId})
	if e != nil {
		return nil, e
	}
	if state.NodeId != n.NodeId || state.InstanceId != n.InstanceId || state.Revision != q.ExpectedAgentRevision {
		return nil, status.Error(codes.Aborted, "agent identity/revision changed")
	}
	var journal struct {
		Revision   uint64            `json:"revision"`
		Operations []json.RawMessage `json:"operations"`
	}
	if e = json.Unmarshal([]byte(state.RunningJson), &journal); e != nil || journal.Revision != state.Revision {
		return nil, status.Error(codes.FailedPrecondition, "agent running journal is invalid or inconsistent")
	}
	log := make([]*a.Operation, 0, len(journal.Operations))
	for _, raw := range journal.Operations {
		o := &a.Operation{}
		if e = protojson.Unmarshal(raw, o); e != nil {
			return nil, status.Error(codes.FailedPrecondition, "unknown Agent journal operation: "+e.Error())
		}
		log = append(log, o)
	}
	objects, e := resources.Replay(log)
	if e != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot materialize Agent journal: "+e.Error())
	}
	nr := &p.NodeResources{NodeId: q.NodeId, Objects: objects}
	compiled, e := resources.Compile(nr)
	if e != nil {
		return nil, status.Error(codes.FailedPrecondition, "journal cannot establish complete resource ownership: "+e.Error())
	}
	if old != nil && len(old.Operations) > 0 {
		baseline, err := resources.Apply(old.Operations, nil)
		if err != nil || !proto.Equal(&p.NodeResources{Objects: baseline}, &p.NodeResources{Objects: compiled}) {
			return nil, status.Error(codes.FailedPrecondition, "managed baseline differs from Agent journal; resolve drift before conversion")
		}
	}
	proof, e := client.VerifyResources(call, &a.VerifyResourcesRequest{NodeId: q.NodeId, ExpectedRevision: state.Revision, Operations: compiled})
	if e != nil {
		return nil, e
	}
	if proof.NodeId != q.NodeId || proof.Revision != state.Revision || !proof.Verified {
		return nil, status.Error(codes.FailedPrecondition, "actual-state verification failed: "+strings.Join(proof.Errors, "; "))
	}
	// Re-read the epoch after verification. Begin/Commit still compare revision,
	// protecting a subsequent Agent-local change after this read-only import.
	confirmed, e := client.GetState(call, &a.NodeRequest{NodeId: q.NodeId})
	if e != nil {
		return nil, e
	}
	if confirmed.InstanceId != n.InstanceId || confirmed.Revision != state.Revision {
		return nil, status.Error(codes.Aborted, "agent changed during verification")
	}
	e = c.update(ctx, true, func(now *p.Database) error {
		live := now.Nodes[q.NodeId]
		if live == nil || live.InstanceId != n.InstanceId || live.AgentRevision > state.Revision || now.Revision != d.Revision || !proto.Equal(now.Configuration, d.Configuration) || nodeBusy(now, q.NodeId) {
			return status.Error(codes.Aborted, "controller or node changed during import")
		}
		// Legacy placements may span nodes. Remove only this node's placements and
		// retain every other node's desired resources unchanged.
		profiles := now.Configuration.Nodes[:0]
		for _, v := range now.Configuration.Nodes {
			if v.NodeId != q.NodeId {
				profiles = append(profiles, v)
			}
		}
		now.Configuration.Nodes = profiles
		tenants := now.Configuration.Tenants[:0]
		for _, v := range now.Configuration.Tenants {
			places := v.Placements[:0]
			for _, place := range v.Placements {
				if place.NodeId != q.NodeId {
					places = append(places, place)
				}
			}
			v.Placements = places
			if len(places) > 0 {
				tenants = append(tenants, v)
			}
		}
		now.Configuration.Tenants = tenants
		networks := now.Configuration.Networks[:0]
		for _, v := range now.Configuration.Networks {
			places := v.Placements[:0]
			for _, place := range v.Placements {
				if place.NodeId != q.NodeId {
					places = append(places, place)
				}
			}
			v.Placements = places
			if len(places) > 0 {
				networks = append(networks, v)
			}
		}
		now.Configuration.Networks = networks
		now.Configuration.NodeResources = append(now.Configuration.NodeResources, nr)
		if _, err := planner.Compile(now.Configuration); err != nil {
			return status.Error(codes.FailedPrecondition, fmt.Sprintf("import would invalidate remaining configuration: %v", err))
		}
		now.Applied[q.NodeId] = &p.Applied{AgentRevision: state.Revision, Operations: compiled}
		live.Adopted = true
		live.AgentRevision = state.Revision
		live.LastSeenUnixMs = time.Now().UnixMilli()
		live.State = "RUNNING"
		live.ObservationError = ""
		now.Revision++
		now.Receipts[key] = &p.Receipt{Id: q.NodeId, Hash: h}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return c.GetNodeResources(ctx, &p.NodeResourcesRequest{NodeId: q.NodeId})
}
