package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/planner"
	"sdn-controller/internal/repository"
	"sdn-controller/pkg/transport"
)

type Options struct {
	AgentToken, AgentCA          string
	Insecure                     bool
	HeartbeatTimeout, RPCTimeout time.Duration
}
type Controller struct {
	p.UnimplementedNorthServer
	a.UnimplementedControllerServer
	store        repository.Store
	options      Options
	mu           sync.Mutex
	db           *p.Database
	connections  map[string]*grpc.ClientConn
	observations map[string]*p.Observation
	collectMu    sync.Mutex
	kick         chan struct{}
}

func New(ctx context.Context, store repository.Store, o Options) (*Controller, error) {
	b, e := store.Load(ctx)
	if e != nil {
		return nil, e
	}
	db := &p.Database{}
	if len(b) > 0 {
		if len(b) > repository.MaxSnapshotBytes {
			return nil, fmt.Errorf("stored database exceeds limit")
		}
		if e = proto.Unmarshal(b, db); e != nil {
			return nil, fmt.Errorf("invalid database: %w", e)
		}
		if db.SchemaVersion != 1 {
			return nil, fmt.Errorf("unsupported database schema %d", db.SchemaVersion)
		}
	} else {
		db.SchemaVersion = 1
	}
	if db.Configuration == nil {
		db.Configuration = &p.Configuration{}
	}
	if db.Nodes == nil {
		db.Nodes = map[string]*p.Node{}
	}
	if db.Changes == nil {
		db.Changes = map[string]*p.Change{}
	}
	if db.Plans == nil {
		db.Plans = map[string]*p.Plan{}
	}
	if db.Jobs == nil {
		db.Jobs = map[string]*p.Job{}
	}
	if db.Receipts == nil {
		db.Receipts = map[string]*p.Receipt{}
	}
	if db.Applied == nil {
		db.Applied = map[string]*p.Applied{}
	}
	if o.HeartbeatTimeout == 0 {
		o.HeartbeatTimeout = 8 * time.Second
	}
	if o.RPCTimeout == 0 {
		o.RPCTimeout = 10 * time.Second
	}
	return &Controller{store: store, options: o, db: db, connections: map[string]*grpc.ClientConn{}, observations: map[string]*p.Observation{}, kick: make(chan struct{}, 1)}, nil
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func hash(m proto.Message) string {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (c *Controller) snapshot() *p.Database {
	c.mu.Lock()
	defer c.mu.Unlock()
	return proto.Clone(c.db).(*p.Database)
}
func (c *Controller) update(ctx context.Context, reserve bool, fn func(*p.Database) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.store.Lost():
		return status.Error(codes.Unavailable, "writer lease lost or storage outcome unresolved; restart from persisted state")
	default:
	}
	d := proto.Clone(c.db).(*p.Database)
	initializeMaps(d)
	if e := fn(d); e != nil {
		return e
	}
	if proto.Equal(d, c.db) {
		return nil
	}
	if reserve {
		// Acknowledgement reserves the eventual Applied baseline, not just the
		// currently stored plan. Existing tasks may finish after admission closes.
		projected := proto.Clone(d).(*p.Database)
		initializeMaps(projected)
		for _, j := range projected.Jobs {
			if !terminal(j.State) && j.Plan != nil {
				for _, np := range j.Plan.Nodes {
					projected.Applied[np.NodeId] = &p.Applied{AgentRevision: np.ExpectedAgentRevision + 1, Operations: cloneOps(np.TargetOperations)}
				}
			}
		}
		if proto.Size(projected) > repository.MaxSnapshotBytes-256*1024 {
			return status.Error(codes.ResourceExhausted, "insufficient space for final Applied baseline and task recovery reserve")
		}
	}
	b, e := proto.MarshalOptions{Deterministic: true}.Marshal(d)
	if e != nil {
		return status.Error(codes.Internal, e.Error())
	}
	limit := repository.MaxSnapshotBytes
	if reserve {
		limit -= 256 * 1024
	}
	if len(b) > limit {
		return status.Error(codes.ResourceExhausted, "bounded controller database full; new writes reserve 256 KiB for in-flight task progress")
	}
	if e = c.store.Save(ctx, b); e != nil {
		return status.Error(codes.Unavailable, "persist controller database: "+e.Error())
	}
	c.db = d
	return nil
}
func receipt(d *p.Database, key, h string) (string, error) {
	if x := d.Receipts[key]; x != nil {
		if x.Hash != h {
			return "", status.Error(codes.AlreadyExists, "idempotency key reused with different request")
		}
		return x.Id, nil
	}
	return "", nil
}
func requestID(s string) error {
	if s == "" || len(s) > 128 {
		return status.Error(codes.InvalidArgument, "request_id required, max 128 bytes")
	}
	return nil
}
func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func terminal(s string) bool {
	switch s {
	case "SUCCEEDED", "FAILED", "PARTIAL", "ROLLED_BACK", "COMPENSATION_FAILED":
		return true
	}
	return false
}
func event(j *p.Job, node, msg string) {
	j.Sequence++
	j.Events = append(j.Events, &p.JobEvent{JobId: j.Id, Sequence: j.Sequence, State: j.State, NodeId: node, Message: trim(msg, 512), TimeUnixMs: time.Now().UnixMilli()})
	if len(j.Events) > 128 {
		j.Events = j.Events[len(j.Events)-128:]
	}
}
func (c *Controller) notify() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.connections {
		conn.Close()
	}
}
func (c *Controller) client(n *p.Node) (a.AgentClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn := c.connections[n.Endpoint]
	if conn == nil {
		var e error
		conn, e = transport.Dial(n.Endpoint, c.options.AgentToken, c.options.AgentCA, c.options.Insecure)
		if e != nil {
			return nil, e
		}
		c.connections[n.Endpoint] = conn
	}
	return a.NewAgentClient(conn), nil
}
func (c *Controller) Register(ctx context.Context, q *a.Registration) (out *a.Registered, e error) {
	if q == nil || q.ProposedNodeId == "" || q.RegistrationId == "" || q.InstanceId == "" || len(q.RegistrationId) > 128 || len(q.InstanceId) > 128 || len(q.ProposedNodeId) > 128 {
		return nil, status.Error(codes.InvalidArgument, "identity fields required and <=128 bytes")
	}
	if _, _, e = net.SplitHostPort(q.Endpoint); e != nil {
		return nil, status.Error(codes.InvalidArgument, "endpoint must be host:port")
	}
	if q.Capabilities == nil || q.Capabilities.ProtocolVersion != 1 {
		return nil, status.Error(codes.FailedPrecondition, "agent protocol version 1 required")
	}
	e = c.update(ctx, true, func(d *p.Database) error {
		var n *p.Node
		for _, v := range d.Nodes {
			if v.RegistrationId == q.RegistrationId {
				n = v
				break
			}
		}
		if n == nil {
			if len(d.Nodes) >= 64 {
				return status.Error(codes.ResourceExhausted, "v1 supports at most 64 nodes")
			}
			nodeID := q.ProposedNodeId
			for d.Nodes[nodeID] != nil {
				nodeID = id()
			}
			n = &p.Node{NodeId: nodeID, RegistrationId: q.RegistrationId}
			d.Nodes[nodeID] = n
		}
		if n.InstanceId != q.InstanceId {
			// A new process may have retained or lost its journal. Do not carry
			// the previous instance's observed revision into the new epoch.
			n.AgentRevision = 0
			n.State = "INIT"
			n.ObservationError = "awaiting revision confirmation from new Agent instance"
		}
		n.InstanceId = q.InstanceId
		n.Endpoint = q.Endpoint
		n.Capabilities = proto.Clone(q.Capabilities).(*a.Capabilities)
		n.Capabilities.NodeId = n.NodeId
		n.LastSeenUnixMs = time.Now().UnixMilli()
		out = &a.Registered{NodeId: n.NodeId}
		return nil
	})
	return
}
func (c *Controller) Heartbeat(ctx context.Context, q *a.Heartbeat) (*a.Empty, error) {
	e := c.update(ctx, false, func(d *p.Database) error {
		n := d.Nodes[q.NodeId]
		if n == nil {
			return status.Error(codes.NotFound, "unregistered node")
		}
		if n.InstanceId != q.InstanceId {
			return status.Error(codes.FailedPrecondition, "superseded instance")
		}
		if _, _, e := net.SplitHostPort(q.Endpoint); e != nil {
			return status.Error(codes.InvalidArgument, "invalid endpoint")
		}
		n.LastSeenUnixMs = time.Now().UnixMilli()
		// Heartbeat RPCs can arrive after a newer task result has been
		// published. Within one instance the durable revision is monotonic.
		// INIT marks a new instance whose first revision may legitimately be
		// lower than the previously applied baseline.
		if n.State == "INIT" || q.Revision > n.AgentRevision {
			n.AgentRevision = q.Revision
		}
		n.Endpoint = q.Endpoint
		n.State = "RUNNING"
		n.ObservationError = ""
		if applied := d.Applied[n.NodeId]; applied != nil && n.AgentRevision != applied.AgentRevision {
			n.ObservationError = fmt.Sprintf("agent revision drift: observed %d, applied %d", n.AgentRevision, applied.AgentRevision)
		}
		return nil
	})
	return &a.Empty{}, e
}
func (c *Controller) nodeView(n *p.Node) *p.Node {
	x := proto.Clone(n).(*p.Node)
	if time.Since(time.UnixMilli(x.LastSeenUnixMs)) > c.options.HeartbeatTimeout {
		x.State = "DOWN"
	}
	return x
}
func page(q *p.ListRequest, total int) (int, int, error) {
	size := int(q.PageSize)
	if size == 0 {
		size = 100
	}
	if size > 500 {
		return 0, 0, status.Error(codes.InvalidArgument, "page_size exceeds 500")
	}
	start := 0
	if q.PageToken != "" {
		v, e := strconv.Atoi(q.PageToken)
		if e != nil || v < 0 {
			return 0, 0, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		start = v
	}
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return start, end, nil
}
func (c *Controller) ListNodes(ctx context.Context, q *p.ListRequest) (*p.Nodes, error) {
	d := c.snapshot()
	keys := []string{}
	for k := range d.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	start, end, e := page(q, len(keys))
	if e != nil {
		return nil, e
	}
	out := &p.Nodes{}
	for _, k := range keys[start:end] {
		out.Items = append(out.Items, c.nodeView(d.Nodes[k]))
	}
	if end < len(keys) {
		out.NextPageToken = strconv.Itoa(end)
	}
	return out, nil
}
func (c *Controller) GetNode(ctx context.Context, q *p.NodeRequest) (*p.Node, error) {
	n := c.snapshot().Nodes[q.NodeId]
	if n == nil {
		return nil, status.Error(codes.NotFound, "node not found")
	}
	return c.nodeView(n), nil
}
func (c *Controller) AdoptNode(ctx context.Context, q *p.AdoptNodeRequest) (*p.Node, error) {
	n, e := c.GetNode(ctx, &p.NodeRequest{NodeId: q.NodeId})
	if e != nil {
		return nil, e
	}
	client, e := c.client(n)
	if e != nil {
		return nil, status.Error(codes.Unavailable, e.Error())
	}
	call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
	defer cancel()
	s, e := client.GetState(call, &a.NodeRequest{NodeId: n.NodeId})
	if e != nil {
		return nil, e
	}
	if s.Revision != q.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "agent revision conflict")
	}
	if s.NodeId != n.NodeId || s.InstanceId != n.InstanceId {
		return nil, status.Error(codes.Aborted, "GetState identity differs from registration")
	}
	if s.Revision != 0 && !q.AllowExisting {
		return nil, status.Error(codes.FailedPrecondition, "existing configuration requires explicit allow_existing; it is not imported or deleted")
	}
	e = c.update(ctx, true, func(d *p.Database) error {
		x := d.Nodes[q.NodeId]
		if x == nil || x.InstanceId != n.InstanceId {
			return status.Error(codes.Aborted, "node instance changed")
		}
		if x.State == "RUNNING" && x.AgentRevision > s.Revision {
			return status.Error(codes.Aborted, "GetState revision superseded by a newer observation")
		}
		if baseline := d.Applied[q.NodeId]; baseline != nil && len(baseline.Operations) > 0 {
			return status.Error(codes.FailedPrecondition, "managed baseline already exists; re-adoption cannot hide configuration drift")
		}
		x.Adopted = true
		x.AgentRevision = s.Revision
		x.State = "RUNNING"
		x.LastSeenUnixMs = time.Now().UnixMilli()
		x.ObservationError = ""
		d.Applied[q.NodeId] = &p.Applied{AgentRevision: s.Revision}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return c.GetNode(ctx, &p.NodeRequest{NodeId: q.NodeId})
}
func (c *Controller) GetConfiguration(context.Context, *p.Empty) (*p.ConfigurationState, error) {
	d := c.snapshot()
	return &p.ConfigurationState{Revision: d.Revision, Configuration: d.Configuration}, nil
}
func (c *Controller) BeginChange(ctx context.Context, q *p.BeginChangeRequest) (out *p.Change, e error) {
	if e = requestID(q.RequestId); e != nil {
		return
	}
	e = c.update(ctx, true, func(d *p.Database) error {
		key := "begin/" + q.RequestId
		found, e := receipt(d, key, hash(q))
		if e != nil {
			return e
		}
		if found != "" {
			out = proto.Clone(d.Changes[found]).(*p.Change)
			return nil
		}
		if q.BaseRevision != d.Revision {
			return status.Error(codes.Aborted, "desired revision conflict")
		}
		out = &p.Change{Id: id(), BaseRevision: d.Revision, Configuration: proto.Clone(d.Configuration).(*p.Configuration)}
		d.Changes[out.Id] = out
		d.Receipts[key] = &p.Receipt{Id: out.Id, Hash: hash(q)}
		return nil
	})
	return
}
func (c *Controller) EditChange(ctx context.Context, q *p.EditChangeRequest) (out *p.Change, e error) {
	if _, e = planner.Compile(q.Configuration); e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	e = c.update(ctx, true, func(d *p.Database) error {
		x := d.Changes[q.ChangeId]
		if x == nil {
			return status.Error(codes.NotFound, "change not found")
		}
		if x.Frozen || x.Aborted {
			return status.Error(codes.FailedPrecondition, "change is frozen or aborted")
		}
		if x.EditRevision != q.EditRevision {
			return status.Error(codes.Aborted, "candidate edit revision conflict")
		}
		x.Configuration = proto.Clone(q.Configuration).(*p.Configuration)
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
func (c *Controller) GetChange(ctx context.Context, q *p.ChangeRequest) (*p.Change, error) {
	x := c.snapshot().Changes[q.ChangeId]
	if x == nil {
		return nil, status.Error(codes.NotFound, "change not found")
	}
	return x, nil
}
func (c *Controller) PlanChange(ctx context.Context, q *p.ChangeRequest) (out *p.Plan, e error) {
	e = c.update(ctx, true, func(d *p.Database) error {
		x := d.Changes[q.ChangeId]
		if x == nil {
			return status.Error(codes.NotFound, "change not found")
		}
		if x.Frozen || x.Aborted {
			return status.Error(codes.FailedPrecondition, "change unavailable")
		}
		if x.BaseRevision != d.Revision {
			return status.Error(codes.Aborted, "desired revision conflict")
		}
		var e error
		out, e = planner.Build(x, d)
		if e != nil {
			return status.Error(codes.FailedPrecondition, e.Error())
		}
		d.Plans[out.Id] = out
		return nil
	})
	return
}
func (c *Controller) CommitChange(ctx context.Context, q *p.CommitChangeRequest) (out *p.Job, e error) {
	if e = requestID(q.RequestId); e != nil {
		return
	}
	e = c.update(ctx, true, func(d *p.Database) error {
		key := "commit/" + q.RequestId
		found, e := receipt(d, key, hash(q))
		if e != nil {
			return e
		}
		if found != "" {
			out = proto.Clone(d.Jobs[found]).(*p.Job)
			return nil
		}
		x := d.Changes[q.ChangeId]
		if x == nil {
			return status.Error(codes.NotFound, "change not found")
		}
		if x.Aborted {
			return status.Error(codes.FailedPrecondition, "change aborted")
		}
		if x.Frozen {
			return status.Error(codes.FailedPrecondition, "change already committed; reuse original request_id")
		}
		if x.BaseRevision != d.Revision {
			return status.Error(codes.Aborted, "desired revision conflict")
		}
		plan, e := planner.Build(x, d)
		if e != nil {
			return status.Error(codes.FailedPrecondition, e.Error())
		}
		if q.PlanId == "" || d.Plans[q.PlanId] == nil || q.PlanId != plan.Id {
			return status.Error(codes.Aborted, "plan changed; preview the current plan again")
		}
		d.Revision++
		d.Configuration = proto.Clone(x.Configuration).(*p.Configuration)
		x.Frozen = true
		out = &p.Job{Id: id(), ChangeId: x.Id, State: "ACCEPTED", DesiredRevision: d.Revision, CreatedUnixMs: time.Now().UnixMilli(), Plan: plan}
		x.JobId = out.Id
		for _, np := range plan.Nodes {
			out.Nodes = append(out.Nodes, &p.NodeJob{NodeId: np.NodeId, State: "PENDING", BaseRevision: np.ExpectedAgentRevision, BeginRequestId: id(), CommitRequestId: id()})
		}
		event(out, "", "configuration and execution plan durably accepted")
		d.Jobs[out.Id] = out
		d.Receipts[key] = &p.Receipt{Id: out.Id, Hash: hash(q)}
		delete(d.Plans, q.PlanId)
		for _, profile := range d.Configuration.Nodes {
			n := d.Nodes[profile.NodeId]
			n.Name = profile.Name
			n.Role = profile.Role
			n.FabricId = profile.FabricId
		}
		return nil
	})
	if e == nil {
		c.notify()
	}
	return
}
func (c *Controller) AbortChange(ctx context.Context, q *p.ChangeRequest) (*p.Empty, error) {
	e := c.update(ctx, false, func(d *p.Database) error {
		x := d.Changes[q.ChangeId]
		if x == nil {
			return status.Error(codes.NotFound, "change not found")
		}
		if x.Frozen {
			return status.Error(codes.FailedPrecondition, "committed change cannot be aborted")
		}
		x.Aborted = true
		return nil
	})
	return &p.Empty{}, e
}
func (c *Controller) GetJob(ctx context.Context, q *p.JobRequest) (*p.Job, error) {
	j := c.snapshot().Jobs[q.JobId]
	if j == nil {
		return nil, status.Error(codes.NotFound, "job not found")
	}
	return j, nil
}
func (c *Controller) ListJobs(ctx context.Context, q *p.ListRequest) (*p.Jobs, error) {
	d := c.snapshot()
	items := []*p.Job{}
	for _, j := range d.Jobs {
		items = append(items, j)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedUnixMs == items[j].CreatedUnixMs {
			return items[i].Id < items[j].Id
		}
		return items[i].CreatedUnixMs > items[j].CreatedUnixMs
	})
	start, end, e := page(q, len(items))
	if e != nil {
		return nil, e
	}
	out := &p.Jobs{Items: items[start:end]}
	for _, j := range out.Items {
		j.Events = nil
		j.Plan = nil
	}
	if end < len(items) {
		out.NextPageToken = strconv.Itoa(end)
	}
	return out, nil
}
func (c *Controller) WatchJob(q *p.JobRequest, stream grpc.ServerStreamingServer[p.JobEvent]) error {
	seq := q.AfterSequence
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		j, e := c.GetJob(stream.Context(), q)
		if e != nil {
			return e
		}
		if seq > j.Sequence {
			return status.Error(codes.OutOfRange, "event cursor is ahead of current sequence")
		}
		if len(j.Events) > 0 && seq+1 < j.Events[0].Sequence {
			return status.Error(codes.OutOfRange, "event history expired; query GetJob and resume at its sequence")
		}
		for _, ev := range j.Events {
			if ev.Sequence > seq {
				if e = stream.Send(ev); e != nil {
					return e
				}
				seq = ev.Sequence
			}
		}
		if terminal(j.State) {
			return nil
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-t.C:
		}
	}
}
func uncertain(e error) bool {
	switch status.Code(e) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal:
		return true
	}
	return false
}
func safeError(e error) string {
	if e == nil {
		return ""
	}
	return trim(strings.ReplaceAll(e.Error(), "\n", " "), 1024)
}
