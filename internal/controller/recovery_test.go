package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/repository"
)

func (f *fakeAgent) RetryRollback(ctx context.Context, q *a.RetryRollbackRequest) (*a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var task *a.Task
	for _, x := range f.commits {
		if x.Id == q.TaskId {
			task = x
			break
		}
	}
	if task == nil {
		return nil, status.Error(codes.NotFound, "task")
	}
	if f.rollbackReceipts == nil {
		f.rollbackReceipts = map[string]string{}
	}
	if receipt := f.rollbackReceipts[q.RequestId]; receipt != "" {
		if receipt != hash(q) {
			return nil, status.Error(codes.AlreadyExists, "reused request")
		}
		return proto.Clone(task).(*a.Task), nil
	}
	if task.State != "ROLLBACK_FAILED" || task.Sequence != q.ExpectedSequence || f.revision != q.ExpectedRevision {
		return nil, status.Error(codes.FailedPrecondition, "rollback baseline")
	}
	f.rollbackReceipts[q.RequestId] = hash(q)
	f.undoCalls++
	task.Sequence++
	task.State = "FAILED_ROLLED_BACK"
	if f.rollbackPending {
		task.State = "ROLLING_BACK"
	}
	if f.rollbackLoseAck {
		return nil, status.Error(codes.Unavailable, "lost Undo retry response")
	}
	return proto.Clone(task).(*a.Task), nil
}

func TestUnknownJobDoesNotBlockUnrelatedNodes(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 2)
	agents[0].pending = true
	first := &p.Configuration{Nodes: []*p.NodeProfile{cfg.Nodes[0]}}
	job, _ := submit(t, c, first, "blocked-node")
	c.advance(context.Background(), job.Id)
	c.unknown(context.Background(), job.Id, "node0", fmt.Errorf("offline"))
	other, _ := submit(t, c, cfg, "other-node")
	if len(other.Nodes) != 1 || other.Nodes[0].NodeId != "node1" {
		t.Fatal("reserved node was rescheduled", other)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	// File-backed commits fsync each state transition. Keep the fairness check
	// bounded, but allow race builds sharing a host with Docker image builds.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := c.GetJob(ctx, &p.JobRequest{JobId: other.Id})
		if got.State == "SUCCEEDED" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("oldest pending job starved unrelated admitted job")
}

func TestReservedNodeCannotBeChangedOrRemoved(t *testing.T) {
	c, cfg, agents, _ := fixture(t, 1)
	agents[0].pending = true
	_, _ = submit(t, c, cfg, "first")
	for _, remove := range []bool{false, true} {
		state, _ := c.GetConfiguration(context.Background(), &p.Empty{})
		x, e := c.BeginChange(context.Background(), &p.BeginChangeRequest{BaseRevision: state.Revision, RequestId: fmt.Sprint("next", remove)})
		if e != nil {
			t.Fatal(e)
		}
		changed := proto.Clone(cfg).(*p.Configuration)
		if remove {
			changed.Nodes = nil
		} else {
			changed.Nodes[0].Interfaces[0].Mtu = 1400
		}
		if _, e = c.EditChange(context.Background(), &p.EditChangeRequest{ChangeId: x.Id, Configuration: changed}); e != nil {
			t.Fatal(e)
		}
		if _, e = c.PlanChange(context.Background(), &p.ChangeRequest{ChangeId: x.Id}); status.Code(e) != codes.FailedPrecondition {
			t.Fatalf("reserved node mutation accepted: %v", e)
		}
	}
}

func TestRollbackRecoveryLostReplyAndControllerRestart(t *testing.T) {
	ctx := context.Background()
	c, cfg, agents, path := fixture(t, 2)
	agents[0].fail, agents[0].rollbackFail, agents[0].rollbackLoseAck = true, true, true
	first := &p.Configuration{Nodes: []*p.NodeProfile{cfg.Nodes[0]}}
	job, _ := submit(t, c, first, "fails")
	c.advance(ctx, job.Id)
	failed, _ := c.GetJob(ctx, &p.JobRequest{JobId: job.Id})
	if failed.Nodes[0].State != "ROLLBACK_FAILED" {
		t.Fatal(failed)
	}
	other, _ := submit(t, c, cfg, "unrelated")
	c.advance(ctx, other.Id)
	q := &p.RetryRollbackRequest{JobId: job.Id, NodeId: "node0", RequestId: "retry-undo", ExpectedJobSequence: failed.Sequence}
	accepted, e := c.RetryRollback(ctx, q)
	if e != nil || accepted.State != "RECOVERING" {
		t.Fatal(accepted, e)
	}
	c.advance(ctx, job.Id)
	unknown, _ := c.GetJob(ctx, &p.JobRequest{JobId: job.Id})
	if unknown.State != "UNKNOWN" {
		t.Fatal(unknown)
	}
	c.Close()
	c.store.Close()
	s, e := repository.OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	restarted, e := New(ctx, s, c.options)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	restarted.advance(ctx, job.Id)
	recovered, e := restarted.GetJob(ctx, &p.JobRequest{JobId: job.Id})
	if e != nil || recovered.Nodes[0].State != "FAILED_ROLLED_BACK" || recovered.Nodes[0].RollbackPending {
		t.Fatal(recovered, e)
	}
	if agents[0].undoCalls != 1 || agents[0].applies != 1 {
		t.Fatal("repeated Undo or forward replay")
	}
	if len(recovered.Events) <= len(failed.Events) {
		t.Fatal("failure audit was replaced")
	}
	again, e := restarted.RetryRollback(ctx, q)
	if e != nil || again.Sequence != recovered.Sequence {
		t.Fatal("retry receipt lost", e)
	}
	agents[0].mu.Lock()
	agents[0].fail = false
	agents[0].mu.Unlock()
	fixed, _ := submit(t, restarted, cfg, "retry-forward-explicitly")
	if len(fixed.Nodes) != 1 || fixed.Nodes[0].NodeId != "node0" {
		t.Fatal(fixed)
	}
	restarted.advance(ctx, fixed.Id)
	got, _ := restarted.GetJob(ctx, &p.JobRequest{JobId: fixed.Id})
	if got.State != "SUCCEEDED" {
		t.Fatal("recovered node remained quarantined", got)
	}
}
