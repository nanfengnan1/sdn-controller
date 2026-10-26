package controller

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/repository"
)

// fakeAgent models protocol outcomes, not network forwarding. Real forwarding is
// separately exercised by the Docker integration suite.
type fakeAgent struct {
	a.UnimplementedAgentServer
	mu                                             sync.Mutex
	node                                           string
	revision                                       uint64
	begins                                         map[string]*a.Transaction
	commits                                        map[string]*a.Task
	calls, applies                                 int
	loseAck, denyRetry, fail, pending, stageFail   bool
	rollbackFail, rollbackLoseAck, rollbackPending bool
	rollbackReceipts                               map[string]string
	undoCalls                                      int
	runningJSON                                    string
	verifyReject                                   bool
	verifies                                       int
}

func (f *fakeAgent) GetState(context.Context, *a.NodeRequest) (*a.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &a.State{NodeId: f.node, InstanceId: "instance-" + f.node, Revision: f.revision, NodeState: "RUNNING", RunningJson: f.runningJSON}, nil
}
func (f *fakeAgent) VerifyResources(ctx context.Context, q *a.VerifyResourcesRequest) (*a.VerifyResourcesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifies++
	return &a.VerifyResourcesResponse{NodeId: f.node, Revision: f.revision, Verified: !f.verifyReject && q.ExpectedRevision == f.revision, Errors: []string{"injected verification"}}, nil
}
func (f *fakeAgent) Query(ctx context.Context, q *a.QueryRequest) (*a.QueryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &a.QueryResponse{NodeId: f.node, Kind: q.Kind, Source: "fake", Revision: f.revision}, nil
}
func (f *fakeAgent) Begin(ctx context.Context, q *a.BeginRequest) (*a.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.begins[q.RequestId]; v != nil {
		return proto.Clone(v).(*a.Transaction), nil
	}
	if q.BaseRevision != f.revision {
		return nil, status.Error(codes.Aborted, "revision")
	}
	x := &a.Transaction{Id: id(), NodeId: f.node, BaseRevision: q.BaseRevision}
	f.begins[q.RequestId] = x
	return proto.Clone(x).(*a.Transaction), nil
}
func (f *fakeAgent) Stage(ctx context.Context, q *a.StageRequest) (*a.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stageFail {
		return nil, status.Error(codes.InvalidArgument, "invalid staged operation")
	}
	for _, x := range f.begins {
		if x.Id == q.TransactionId {
			x.Operations = cloneOps(q.Operations)
			return proto.Clone(x).(*a.Transaction), nil
		}
	}
	return nil, status.Error(codes.NotFound, "transaction")
}
func (f *fakeAgent) Commit(ctx context.Context, q *a.CommitRequest) (*a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.denyRetry && f.calls == 2 {
		return nil, status.Error(codes.PermissionDenied, "credential rotation")
	}
	if v := f.commits[q.RequestId]; v != nil {
		return proto.Clone(v).(*a.Task), nil
	}
	state := "SUCCEEDED"
	if f.fail {
		state = "FAILED_ROLLED_BACK"
		if f.rollbackFail {
			state = "ROLLBACK_FAILED"
		}
	} else if f.pending {
		state = "APPLYING"
	} else {
		f.revision++
	}
	f.applies++
	x := &a.Task{Id: id(), NodeId: f.node, TransactionId: q.TransactionId, State: state, Revision: f.revision, Sequence: 3}
	if f.fail {
		x.Error = "injected backend failure"
	}
	f.commits[q.RequestId] = x
	if f.loseAck {
		return nil, status.Error(codes.Unavailable, "lost acknowledgement after persist")
	}
	return proto.Clone(x).(*a.Task), nil
}
func (f *fakeAgent) GetTask(ctx context.Context, q *a.TaskRequest) (*a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.commits {
		if x.Id == q.TaskId {
			return proto.Clone(x).(*a.Task), nil
		}
	}
	return nil, status.Error(codes.NotFound, "task")
}
func (f *fakeAgent) Abort(context.Context, *a.AbortRequest) (*a.Empty, error) { return &a.Empty{}, nil }
func startAgent(t *testing.T, node string) *fakeAgent {
	t.Helper()
	f := &fakeAgent{node: node, begins: map[string]*a.Transaction{}, commits: map[string]*a.Task{}}
	return f
}
func fixture(t *testing.T, count int) (*Controller, *p.Configuration, []*fakeAgent, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.pb")
	s, e := repository.OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := New(context.Background(), s, Options{Insecure: true, AgentToken: "test-agent-token-123", RPCTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	cfg := &p.Configuration{}
	agents := []*fakeAgent{}
	for i := 0; i < count; i++ {
		node := fmt.Sprintf("node%d", i)
		f := startAgent(t, node)
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		server := grpc.NewServer()
		a.RegisterAgentServer(server, f)
		go server.Serve(l)
		t.Cleanup(server.Stop)
		caps := &a.Capabilities{ProtocolVersion: 1}
		for code := range a.OpCode_name {
			if code != 0 {
				caps.Operations = append(caps.Operations, &a.Capability{Code: a.OpCode(code), Backends: []string{"fake"}, Rollback: true})
			}
		}
		_, e = c.Register(context.Background(), &a.Registration{ProposedNodeId: node, RegistrationId: "reg-" + node, InstanceId: "instance-" + node, Endpoint: l.Addr().String(), Capabilities: caps})
		if e != nil {
			t.Fatal(e)
		}
		_, e = c.AdoptNode(context.Background(), &p.AdoptNodeRequest{NodeId: node})
		if e != nil {
			t.Fatal(e)
		}
		cfg.Nodes = append(cfg.Nodes, &p.NodeProfile{NodeId: node, Name: node, Role: "leaf", FabricId: "fabric", RouterId: fmt.Sprintf("10.255.0.%d", i+1), VtepIp: fmt.Sprintf("10.255.0.%d", i+1), Asn: 65000, Interfaces: []*p.Interface{{Name: "lo", Addresses: []string{fmt.Sprintf("10.255.0.%d/32", i+1)}, Ospf: true, Passive: true}}, Ospf: true})
		agents = append(agents, f)
	}
	return c, cfg, agents, path
}
func submit(t *testing.T, c *Controller, cfg *p.Configuration, key string) (*p.Job, *p.CommitChangeRequest) {
	t.Helper()
	ctx := context.Background()
	state, _ := c.GetConfiguration(ctx, &p.Empty{})
	change, e := c.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: state.Revision, RequestId: key + "/begin"})
	if e != nil {
		t.Fatal(e)
	}
	change, e = c.EditChange(ctx, &p.EditChangeRequest{ChangeId: change.Id, Configuration: cfg})
	if e != nil {
		t.Fatal(e)
	}
	plan, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: change.Id})
	if e != nil {
		t.Fatal(e)
	}
	q := &p.CommitChangeRequest{ChangeId: change.Id, RequestId: key + "/commit", PlanId: plan.Id}
	j, e := c.CommitChange(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	return j, q
}
func TestCandidateCommitIdempotencyAndApplied(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 1)
	j, q := submit(t, c, cfg, "one")
	if agents[0].applies != 0 {
		t.Fatal("staging changed agent")
	}
	again, e := c.CommitChange(context.Background(), q)
	if e != nil || again.Id != j.Id {
		t.Fatal("commit not idempotent", e)
	}
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "SUCCEEDED" || agents[0].applies != 1 {
		t.Fatalf("%+v", got)
	}
	if c.snapshot().Applied["node0"].AgentRevision != 1 {
		t.Fatal("Applied not advanced")
	}
	q.PlanId = "different"
	if _, e = c.CommitChange(context.Background(), q); status.Code(e) != codes.AlreadyExists {
		t.Fatalf("same key/different request accepted: %v", e)
	}
}
func TestConcurrentCandidateConflict(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	ctx := context.Background()
	x, e := c.BeginChange(ctx, &p.BeginChangeRequest{RequestId: "loser"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.EditChange(ctx, &p.EditChangeRequest{ChangeId: x.Id, Configuration: cfg})
	if e != nil {
		t.Fatal(e)
	}
	plan, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id})
	if e != nil {
		t.Fatal(e)
	}
	_, _ = submit(t, c, cfg, "winner")
	_, e = c.CommitChange(ctx, &p.CommitChangeRequest{ChangeId: x.Id, PlanId: plan.Id, RequestId: "loser-commit"})
	if status.Code(e) != codes.Aborted {
		t.Fatalf("expected conflict, got %v", e)
	}
}
func TestStageAllBeforeAnyCommit(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 2)
	agents[1].stageFail = true
	j, _ := submit(t, c, cfg, "stage-fail")
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "FAILED" || agents[0].applies != 0 {
		t.Fatal("stage failure caused config writes", got)
	}
}
func TestPartialFailurePreservesSuccessfulBaseline(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 2)
	agents[1].fail = true
	j, _ := submit(t, c, cfg, "partial")
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "PARTIAL" || got.Nodes[0].State != "SUCCEEDED" || c.snapshot().Applied["node1"].AgentRevision != 0 {
		t.Fatal(got)
	}
}
func TestRestartResolvesLostCommitAcknowledgement(t *testing.T) {
	c, cfg, agents, path := fixture(t, 1)
	agents[0].loseAck = true
	j, _ := submit(t, c, cfg, "restart")
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "UNKNOWN" {
		t.Fatal(got)
	}
	c.Close()
	c.store.Close()
	s, e := repository.OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	restarted, e := New(context.Background(), s, c.options)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	restarted.advance(context.Background(), j.Id)
	got, _ = restarted.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "SUCCEEDED" || agents[0].applies != 1 || agents[0].calls != 2 {
		t.Fatal(got, agents[0].applies, agents[0].calls)
	}
}
func TestPermissionErrorAfterLostAckRemainsUnknown(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 1)
	agents[0].loseAck = true
	agents[0].denyRetry = true
	j, _ := submit(t, c, cfg, "uncertain")
	c.advance(context.Background(), j.Id)
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "UNKNOWN" || agents[0].applies != 1 {
		t.Fatal(got)
	}
	c.advance(context.Background(), j.Id)
	got, _ = c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "SUCCEEDED" || agents[0].applies != 1 {
		t.Fatal(got)
	}
}
func TestNoopConfigurationDoesNotReapply(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 1)
	j, _ := submit(t, c, cfg, "first")
	c.advance(context.Background(), j.Id)
	j, _ = submit(t, c, cfg, "second")
	if len(j.Plan.Nodes) != 0 {
		t.Fatal("unexpected nonempty diff")
	}
	c.advance(context.Background(), j.Id)
	if agents[0].applies != 1 {
		t.Fatal("duplicate operation application")
	}
}
func TestRevisionDriftFailsBeforeBegin(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 1)
	j, _ := submit(t, c, cfg, "drift")
	agents[0].revision = 42
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "FAILED" || agents[0].applies != 0 {
		t.Fatal(got)
	}
}
func TestFrozenCandidateAndEditRevision(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	ctx := context.Background()
	x, _ := c.BeginChange(ctx, &p.BeginChangeRequest{RequestId: "edit"})
	_, e := c.EditChange(ctx, &p.EditChangeRequest{ChangeId: x.Id, Configuration: cfg, EditRevision: 3})
	if status.Code(e) != codes.Aborted {
		t.Fatal(e)
	}
	j, _ := submit(t, c, cfg, "freeze")
	_, e = c.EditChange(ctx, &p.EditChangeRequest{ChangeId: j.ChangeId, Configuration: cfg})
	if status.Code(e) != codes.FailedPrecondition {
		t.Fatal(e)
	}
}
func TestIdentityConflictAndSupersededHeartbeat(t *testing.T) {
	c, _, _, _ := fixture(t, 1)
	n := c.snapshot().Nodes["node0"]
	q := &a.Registration{ProposedNodeId: n.NodeId, RegistrationId: "different", InstanceId: "second", Endpoint: n.Endpoint, Capabilities: n.Capabilities}
	r, e := c.Register(context.Background(), q)
	if e != nil || r.NodeId == n.NodeId {
		t.Fatal(r, e)
	}
	_, e = c.Heartbeat(context.Background(), &a.Heartbeat{NodeId: n.NodeId, InstanceId: "old", Endpoint: n.Endpoint})
	if status.Code(e) != codes.FailedPrecondition {
		t.Fatal(e)
	}
}
func TestParseEVPNPreservesRDAndContext(t *testing.T) {
	raw := `{"10.0.0.1:10":{"rd":"10.0.0.1:10","[2]:[0]:[48]:[aa:bb:cc:dd:ee:ff]:[32]:[192.0.2.1]":{"paths":[{"mac":"aa:bb:cc:dd:ee:ff","ip":"192.0.2.1","extendedCommunity":{"string":"RT:65000:1 RT:65000:10010"},"nexthops":[{"ip":"10.0.0.1"}]}]}},"numPrefix":1}`
	r, e := ParseEVPN("spine", raw)
	if e != nil || len(r) != 1 || r[0].Rd != "10.0.0.1:10" || r[0].Ip != "192.0.2.1" || len(r[0].RouteTargets) != 2 {
		t.Fatal(r, e)
	}
}

type captureStream struct {
	ctx    context.Context
	events []*p.JobEvent
}

func (s *captureStream) Context() context.Context { return s.ctx }
func (s *captureStream) Send(x *p.JobEvent) error {
	s.events = append(s.events, proto.Clone(x).(*p.JobEvent))
	return nil
}
func (s *captureStream) SetHeader(metadata.MD) error  { return nil }
func (s *captureStream) SendHeader(metadata.MD) error { return nil }
func (s *captureStream) SetTrailer(metadata.MD)       {}
func (s *captureStream) SendMsg(any) error            { return nil }
func (s *captureStream) RecvMsg(any) error            { return nil }
func TestWatchJobReplayAndCursorValidation(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	j, _ := submit(t, c, cfg, "events")
	c.advance(context.Background(), j.Id)
	s := &captureStream{ctx: context.Background()}
	if e := c.WatchJob(&p.JobRequest{JobId: j.Id, AfterSequence: 1}, s); e != nil {
		t.Fatal(e)
	}
	if len(s.events) == 0 || s.events[0].Sequence != 2 || s.events[len(s.events)-1].State != "SUCCEEDED" {
		t.Fatal(s.events)
	}
	if e := c.WatchJob(&p.JobRequest{JobId: j.Id, AfterSequence: 99999}, s); status.Code(e) != codes.OutOfRange {
		t.Fatal(e)
	}
}
func TestNearCapacityAcceptedJobCanFinish(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	if e := c.update(context.Background(), false, func(d *p.Database) error {
		d.Receipts["historical"] = &p.Receipt{Hash: strings.Repeat("x", 690000), Id: "historical"}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	j, _ := submit(t, c, cfg, "large")
	c.advance(context.Background(), j.Id)
	got, _ := c.GetJob(context.Background(), &p.JobRequest{JobId: j.Id})
	if got.State != "SUCCEEDED" {
		t.Fatal(got)
	}
	if proto.Size(c.snapshot()) < 690000 {
		t.Fatal("test did not exercise database occupancy")
	}
}
func TestUnsupportedEVPNSchemaIsNotEmptyTopology(t *testing.T) {
	if _, e := ParseEVPN("node", "{\"unexpected\": []}"); e == nil {
		t.Fatal("unknown schema treated as empty table")
	}
	if r, e := ParseEVPN("node", "{\"numPrefix\":0}"); e != nil || len(r) != 0 {
		t.Fatal(r, e)
	}
}

func TestDelayedHeartbeatCannotUndoPublishedRevision(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	ctx := context.Background()
	j, _ := submit(t, c, cfg, "heartbeat-order")
	c.advance(ctx, j.Id)
	n := c.snapshot().Nodes["node0"]
	if n.AgentRevision != 1 {
		t.Fatal("success did not publish revision", n)
	}
	// Re-registration on a transient connection failure uses the same process
	// instance and must not reopen the monotonic revision boundary.
	_, e := c.Register(ctx, &a.Registration{ProposedNodeId: n.NodeId, RegistrationId: n.RegistrationId, InstanceId: n.InstanceId, Endpoint: n.Endpoint, Capabilities: n.Capabilities})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Heartbeat(ctx, &a.Heartbeat{NodeId: n.NodeId, InstanceId: n.InstanceId, Endpoint: n.Endpoint, Revision: 0})
	if e != nil {
		t.Fatal(e)
	}
	n = c.snapshot().Nodes["node0"]
	if n.AgentRevision != 1 || n.State != "RUNNING" || n.ObservationError != "" {
		t.Fatal("late heartbeat regressed observation", n)
	}
	x, e := c.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: 1, RequestId: "after-delayed-heartbeat"})
	if e != nil {
		t.Fatal(e)
	}
	plan, e := c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id})
	if e != nil || len(plan.GetNodes()) != 0 {
		t.Fatal("late heartbeat caused false plan drift", plan, e)
	}
}

func TestNewInstanceRevisionRegressionRemainsVisible(t *testing.T) {
	c, cfg, _, _ := fixture(t, 1)
	ctx := context.Background()
	j, _ := submit(t, c, cfg, "instance-regression")
	c.advance(ctx, j.Id)
	old := c.snapshot().Nodes["node0"]
	_, e := c.Register(ctx, &a.Registration{ProposedNodeId: old.NodeId, RegistrationId: old.RegistrationId, InstanceId: "new-instance", Endpoint: old.Endpoint, Capabilities: old.Capabilities})
	if e != nil {
		t.Fatal(e)
	}
	n := c.snapshot().Nodes["node0"]
	if n.State != "INIT" || n.AgentRevision != 0 {
		t.Fatal("new instance reused old revision", n)
	}
	x, e := c.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: 1, RequestId: "after-restart"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("unconfirmed new instance was planned", e)
	}
	_, e = c.Heartbeat(ctx, &a.Heartbeat{NodeId: n.NodeId, InstanceId: "new-instance", Endpoint: n.Endpoint, Revision: 0})
	if e != nil {
		t.Fatal(e)
	}
	n = c.snapshot().Nodes["node0"]
	if n.State != "RUNNING" || n.AgentRevision != 0 || !strings.Contains(n.ObservationError, "revision drift") || c.snapshot().Applied[n.NodeId].AgentRevision != 1 {
		t.Fatal("new instance state loss was hidden", n)
	}
	if _, e = c.PlanChange(ctx, &p.ChangeRequest{ChangeId: x.Id}); status.Code(e) != codes.FailedPrecondition || !strings.Contains(e.Error(), "revision drift") {
		t.Fatal("real revision drift was not blocked", e)
	}
	_, e = c.Heartbeat(ctx, &a.Heartbeat{NodeId: n.NodeId, InstanceId: old.InstanceId, Endpoint: n.Endpoint, Revision: 1})
	if status.Code(e) != codes.FailedPrecondition {
		t.Fatal("superseded instance repaired new observation", e)
	}
}
