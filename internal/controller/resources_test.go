package controller

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/repository"
	"testing"
)

func resourceOp(t *testing.T, s string) *a.Operation {
	t.Helper()
	o := &a.Operation{}
	if e := protojson.Unmarshal([]byte(s), o); e != nil {
		t.Fatal(e)
	}
	return o
}
func resourceChange(t *testing.T, c *Controller, ss ...string) *p.Change {
	t.Helper()
	ctx := context.Background()
	d := c.snapshot()
	x, e := c.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: d.Revision, RequestId: id()})
	if e != nil {
		t.Fatal(e)
	}
	ops := []*a.Operation{}
	for _, s := range ss {
		ops = append(ops, resourceOp(t, s))
	}
	x, e = c.EditNodeResources(ctx, &p.EditNodeResourcesRequest{ChangeId: x.Id, NodeId: "node0", Operations: ops})
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func resourceCommit(t *testing.T, c *Controller, x *p.Change) *p.Job {
	t.Helper()
	ctx := context.Background()
	plan, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id})
	if e != nil {
		t.Fatal(e)
	}
	j, e := c.CommitChange(ctx, &p.CommitChangeRequest{ChangeId: x.Id, PlanId: plan.Id, RequestId: id()})
	if e != nil {
		t.Fatal(e)
	}
	c.advance(ctx, j.Id)
	j, e = c.GetJob(ctx, &p.JobRequest{JobId: j.Id})
	if e != nil || j.State != "SUCCEEDED" {
		t.Fatal(j, e)
	}
	return j
}
func TestResourceIncrementalCommitAndRestart(t *testing.T) {
	c, _, agents, path := fixture(t, 1)
	ctx := context.Background()
	first := resourceChange(t, c, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`)
	j := resourceCommit(t, c, first)
	if len(j.Plan.Nodes[0].Operations) != 1 {
		t.Fatal("standalone VRF emitted implicit resources")
	}
	next := resourceChange(t, c, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`, `{"code":"BD","action":"CREATE","bridge":{"name":"br10"}}`, `{"code":"BD_TENANT","action":"BIND","binding":{"bridge":"br10","tenant":"A"}}`)
	j = resourceCommit(t, c, next)
	if len(j.Plan.Nodes[0].Operations) != 2 {
		t.Fatal("existing VRF was recreated")
	}
	if agents[0].applies != 2 {
		t.Fatal("bad commits")
	}
	c.Close()
	c.store.Close()
	store, e := repository.OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	restarted, e := New(ctx, store, c.options)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	state, e := restarted.GetNodeResources(ctx, &p.NodeResourcesRequest{NodeId: "node0"})
	if e != nil || len(state.Resources.Objects) != 3 || state.Mode != "resources" {
		t.Fatal(state, e)
	}
}
func TestResourceDependencyPlanAndEditRevision(t *testing.T) {
	c, _, _, _ := fixture(t, 1)
	ctx := context.Background()
	x := resourceChange(t, c, `{"code":"BD_TENANT","action":"BIND","binding":{"bridge":"br10","tenant":"A"}}`)
	if _, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("missing refs planned", e)
	}
	q := &p.EditNodeResourcesRequest{ChangeId: x.Id, NodeId: "node0", EditRevision: 0, Operations: []*a.Operation{resourceOp(t, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`)}}
	if _, e := c.EditNodeResources(ctx, q); status.Code(e) != codes.Aborted {
		t.Fatal("stale edit accepted", e)
	}
	q.EditRevision = x.EditRevision
	q.Operations = append(q.Operations, resourceOp(t, `{"code":"BD","action":"CREATE","bridge":{"name":"br10"}}`))
	x, e := c.EditNodeResources(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	resourceCommit(t, c, x)
	x, e = c.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: c.snapshot().Revision, RequestId: id()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.EditNodeResources(ctx, &p.EditNodeResourcesRequest{ChangeId: x.Id, NodeId: "node0", Operations: []*a.Operation{resourceOp(t, `{"code":"TENANT","action":"DELETE","tenant":{"name":"A"}}`)}}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("referenced VRF deleted", e)
	}
}
func TestImportFoldsJournalAndRequiresReadback(t *testing.T) {
	c, _, agents, _ := fixture(t, 1)
	ctx := context.Background()
	f := agents[0]
	f.revision = 3
	f.runningJSON = `{"revision":3,"operations":[{"code":"TENANT","action":"CREATE","tenant":{"name":"OLD","tableId":1001}},{"code":"TENANT","action":"DELETE","tenant":{"name":"OLD","tableId":1001}},{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1002}}]}`
	c.mu.Lock()
	delete(c.db.Applied, "node0")
	c.db.Nodes["node0"].AgentRevision = 3
	c.db.Nodes["node0"].Adopted = false
	c.mu.Unlock()
	q := &p.ImportNodeResourcesRequest{NodeId: "node0", ExpectedAgentRevision: 3, RequestId: "import-test"}
	f.verifyReject = true
	if _, e := c.ImportNodeResources(ctx, q); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("failed readback imported", e)
	}
	if c.snapshot().Revision != 0 || c.snapshot().Nodes["node0"].Adopted {
		t.Fatal("failed import mutated ownership")
	}
	f.verifyReject = false
	got, e := c.ImportNodeResources(ctx, q)
	if e != nil || got.Mode != "resources" || !got.Adopted || len(got.Resources.Objects) != 1 || got.Resources.Objects[0].GetTenant().Name != "A" {
		t.Fatal(got, e)
	}
	if _, e = c.ImportNodeResources(ctx, q); e != nil || f.verifies != 2 {
		t.Fatal("receipt did not suppress repeated import", e)
	}
	x := resourceChange(t, c, `{"code":"BD","action":"CREATE","bridge":{"name":"br20"}}`)
	plan, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id})
	if e != nil || len(plan.Nodes) != 1 || len(plan.Nodes[0].Operations) != 1 || plan.Nodes[0].Operations[0].Code != a.OpCode_BD {
		t.Fatal("imported VRF recreated", plan, e)
	}
	q.RequestId = "mask-drift"
	if _, e = c.ImportNodeResources(ctx, q); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("managed reimport accepted", e)
	}
}
func TestResourceCapabilityAndQueryAdmission(t *testing.T) {
	c, _, _, _ := fixture(t, 1)
	ctx := context.Background()
	x := resourceChange(t, c, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`)
	c.mu.Lock()
	c.db.Nodes["node0"].Capabilities.Operations = nil
	c.db.Nodes["node0"].Capabilities.Queries = []*a.QueryCapability{{Kind: a.QueryKind_ROUTES, Families: []a.AddressFamily{a.AddressFamily_IPV4}}}
	c.mu.Unlock()
	if _, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("unsupported operation planned", e)
	}
	if _, e := c.QueryNode(ctx, &a.QueryRequest{NodeId: "node0", Kind: a.QueryKind_ROUTES, Family: a.AddressFamily_IPV6}); status.Code(e) != codes.Unimplemented {
		t.Fatal("unsupported query admitted", e)
	}
	if got, e := c.QueryNode(ctx, &a.QueryRequest{NodeId: "node0", Kind: a.QueryKind_ROUTES, Family: a.AddressFamily_IPV4}); e != nil || got.Source != "fake" {
		t.Fatal(got, e)
	}
}

func TestManagedEmptyNodeCanCreateAgain(t *testing.T) {
	c, _, _, _ := fixture(t, 1)
	resourceCommit(t, c, resourceChange(t, c, `{"code":"TENANT","action":"CREATE","tenant":{"name":"A","tableId":1001}}`))
	resourceCommit(t, c, resourceChange(t, c, `{"code":"TENANT","action":"DELETE","tenant":{"name":"A"}}`))
	j := resourceCommit(t, c, resourceChange(t, c, `{"code":"TENANT","action":"CREATE","tenant":{"name":"B","tableId":1002}}`))
	if len(j.Plan.Nodes[0].Operations) != 1 {
		t.Fatal("managed empty node was not reusable")
	}
}
