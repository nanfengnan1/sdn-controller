package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

// Run gives every admitted, node-disjoint job a turn. Durable intents make a
// retry use the original request ID even if its RPC response was lost.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	c.notify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.store.Lost():
			return
		case <-ticker.C:
		case <-c.kick:
		}
		d := c.snapshot()
		jobs := []*p.Job{}
		for _, j := range d.Jobs {
			if !terminal(j.State) {
				jobs = append(jobs, j)
			}
		}
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedUnixMs < jobs[j].CreatedUnixMs })
		for _, j := range jobs {
			if ctx.Err() != nil {
				return
			}
			c.advance(ctx, j.Id)
		}
	}
}
func (c *Controller) updateJob(ctx context.Context, id string, fn func(*p.Database, *p.Job) error) error {
	return c.update(ctx, false, func(d *p.Database) error {
		j := d.Jobs[id]
		if j == nil {
			return status.Error(codes.NotFound, "job disappeared")
		}
		return fn(d, j)
	})
}
func (c *Controller) unknown(ctx context.Context, id, node string, e error) {
	_ = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
		msg := safeError(e)
		if j.State != "UNKNOWN" || j.Error != msg {
			j.State = "UNKNOWN"
			j.Error = msg
			event(j, node, "result unknown; retrying original RPC/query: "+msg)
		}
		return nil
	})
}
func (c *Controller) fail(ctx context.Context, id string, index int, state string, e error) {
	_ = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
		j.Nodes[index].State = state
		j.Nodes[index].Error = safeError(e)
		j.Error = safeError(e)
		j.State = "FAILED"
		for _, n := range j.Nodes {
			if n.State == "SUCCEEDED" || n.State == "ROLLBACK_FAILED" {
				j.State = "PARTIAL"
			}
		}
		event(j, j.Nodes[index].NodeId, "execution stopped: "+safeError(e))
		return nil
	})
	// Abort only candidates known never to have had Commit sent.
	j := c.snapshot().Jobs[id]
	for _, n := range j.Nodes {
		if n.TransactionId == "" || n.CommitIntent {
			continue
		}
		record := c.snapshot().Nodes[n.NodeId]
		if record == nil {
			continue
		}
		client, e := c.client(record)
		if e != nil {
			continue
		}
		call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
		_, _ = client.Abort(call, &a.AbortRequest{NodeId: n.NodeId, TransactionId: n.TransactionId})
		cancel()
	}
}
func (c *Controller) advance(ctx context.Context, id string) {
	d := c.snapshot()
	job := d.Jobs[id]
	if job == nil || terminal(job.State) {
		return
	}
	for i, n := range job.Nodes {
		if n.RollbackPending {
			c.advanceRollback(ctx, job, i)
			return
		}
	}
	if job.State == "ACCEPTED" {
		if e := c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
			j.State = "STAGING"
			event(j, "", "preparing node candidates")
			return nil
		}); e != nil {
			return
		}
	}
	// Phase 1 stages all node candidates. This is validation, not distributed 2PC.
	for i := range job.Nodes {
		if ctx.Err() != nil {
			return
		}
		d = c.snapshot()
		j := d.Jobs[id]
		n := j.Nodes[i]
		if n.State == "STAGED" || n.CommitIntent || n.State == "SUCCEEDED" {
			continue
		}
		record := d.Nodes[n.NodeId]
		if record == nil {
			c.fail(ctx, id, i, "FAILED", fmt.Errorf("node is no longer registered"))
			return
		}
		client, e := c.client(record)
		if e != nil {
			c.unknown(ctx, id, n.NodeId, e)
			return
		}
		if !n.BeginIntent {
			call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
			state, e := client.GetState(call, &a.NodeRequest{NodeId: n.NodeId})
			cancel()
			if e != nil {
				c.unknown(ctx, id, n.NodeId, e)
				return
			}
			if state.Revision != n.BaseRevision {
				c.fail(ctx, id, i, "FAILED", status.Error(codes.Aborted, "Agent revision changed after plan"))
				return
			}
			if state.InstanceId != "" && state.InstanceId != record.InstanceId {
				c.unknown(ctx, id, n.NodeId, fmt.Errorf("agent instance differs from registered identity"))
				return
			}
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
				j.Nodes[i].BeginIntent = true
				j.Nodes[i].State = "BEGINNING"
				event(j, n.NodeId, "Begin intent saved")
				return nil
			}); e != nil {
				return
			}
		}
		if n.TransactionId == "" {
			call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
			tx, e := client.Begin(call, &a.BeginRequest{NodeId: n.NodeId, BaseRevision: n.BaseRevision, RequestId: n.BeginRequestId})
			cancel()
			if e != nil {
				if uncertain(e) {
					c.unknown(ctx, id, n.NodeId, e)
				} else {
					c.fail(ctx, id, i, "FAILED", e)
				}
				return
			}
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error { j.Nodes[i].TransactionId = tx.Id; return nil }); e != nil {
				return
			}
			n.TransactionId = tx.Id
		}
		call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
		_, e = client.Stage(call, &a.StageRequest{NodeId: n.NodeId, TransactionId: n.TransactionId, Operations: j.Plan.Nodes[i].Operations})
		cancel()
		if e != nil {
			if uncertain(e) {
				c.unknown(ctx, id, n.NodeId, e)
			} else {
				c.fail(ctx, id, i, "FAILED", e)
			}
			return
		}
		if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
			j.Nodes[i].State = "STAGED"
			event(j, n.NodeId, "candidate validated and staged")
			return nil
		}); e != nil {
			return
		}
	}
	// Phase 2 submits in the deterministic node plan order. A pending node task
	// leaves the worker so shutdown and lease loss remain immediately cancellable.
	for i := range job.Nodes {
		if ctx.Err() != nil {
			return
		}
		d = c.snapshot()
		j := d.Jobs[id]
		n := j.Nodes[i]
		if n.State == "SUCCEEDED" {
			continue
		}
		record := d.Nodes[n.NodeId]
		client, e := c.client(record)
		if e != nil {
			c.unknown(ctx, id, n.NodeId, e)
			return
		}
		if !n.CommitIntent {
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
				j.State = "APPLYING"
				j.Error = ""
				j.Nodes[i].CommitIntent = true
				j.Nodes[i].State = "COMMITTING"
				event(j, n.NodeId, "Commit intent saved")
				return nil
			}); e != nil {
				return
			}
		}
		var task *a.Task
		if n.TaskId == "" {
			call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
			task, e = client.Commit(call, &a.CommitRequest{NodeId: n.NodeId, TransactionId: n.TransactionId, RequestId: n.CommitRequestId})
			cancel()
			// After a durable Commit intent any response error may follow a lost older
			// response. Only an explicit Agent Task terminal state proves the outcome.
			if e != nil {
				c.unknown(ctx, id, n.NodeId, e)
				return
			}
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
				j.Nodes[i].TaskId = task.Id
				j.Nodes[i].State = "APPLYING"
				j.State = "APPLYING"
				j.Error = ""
				event(j, n.NodeId, "Agent accepted task "+task.Id)
				return nil
			}); e != nil {
				return
			}
		} else {
			call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
			task, e = client.GetTask(call, &a.TaskRequest{NodeId: n.NodeId, TaskId: n.TaskId})
			cancel()
			if e != nil {
				c.unknown(ctx, id, n.NodeId, e)
				return
			}
		}
		switch task.State {
		case "SUCCEEDED":
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
				x := j.Nodes[i]
				x.State = "SUCCEEDED"
				x.AgentRevision = task.Revision
				x.AgentSequence = task.Sequence
				x.Error = ""
				d.Applied[x.NodeId] = &p.Applied{AgentRevision: task.Revision, Operations: cloneOps(j.Plan.Nodes[i].TargetOperations)}
				observed := d.Nodes[x.NodeId]
				if observed.InstanceId == record.InstanceId && observed.State == "RUNNING" && task.Revision > observed.AgentRevision {
					observed.AgentRevision = task.Revision
					observed.ObservationError = ""
				}
				j.State = "APPLYING"
				j.Error = ""
				event(j, x.NodeId, "Agent configuration transaction succeeded")
				return nil
			}); e != nil {
				return
			}
		case "FAILED_NO_CHANGE", "FAILED_ROLLED_BACK", "ROLLBACK_FAILED":
			if e = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
				j.Nodes[i].AgentSequence = task.Sequence
				return nil
			}); e != nil {
				return
			}
			c.fail(ctx, id, i, task.State, fmt.Errorf("%s: %s", task.ErrorCode, task.Error))
			return
		case "ACCEPTED", "APPLYING", "ROLLING_BACK":
			if n.AgentSequence != task.Sequence || j.State == "UNKNOWN" {
				_ = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
					j.State = "APPLYING"
					j.Error = ""
					j.Nodes[i].AgentSequence = task.Sequence
					return nil
				})
			}
			return
		default:
			c.unknown(ctx, id, n.NodeId, fmt.Errorf("unsupported Agent task state %q", task.State))
			return
		}
	}
	_ = c.updateJob(ctx, id, func(d *p.Database, j *p.Job) error {
		j.State = "SUCCEEDED"
		j.Error = ""
		event(j, "", "all node configuration transactions succeeded; protocol convergence is a separate check")
		return nil
	})
}
func cloneOps(ops []*a.Operation) []*a.Operation {
	out := make([]*a.Operation, len(ops))
	for i, o := range ops {
		out[i] = proto.Clone(o).(*a.Operation)
	}
	return out
}
