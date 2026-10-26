package controller

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

// RetryRollback publishes a durable retry intent. Only an Agent-confirmed Undo
// result can release the node; successful forward steps are never replayed.
func (c *Controller) RetryRollback(ctx context.Context, q *p.RetryRollbackRequest) (*p.Job, error) {
	if q.RequestId == "" || q.JobId == "" || q.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id, node_id and request_id required")
	}
	var out *p.Job
	e := c.update(ctx, true, func(d *p.Database) error {
		key := "rollback/" + q.RequestId
		found, e := receipt(d, key, hash(q))
		if e != nil {
			return e
		}
		if found != "" {
			out = proto.Clone(d.Jobs[found]).(*p.Job)
			return nil
		}
		j := d.Jobs[q.JobId]
		if j == nil {
			return status.Error(codes.NotFound, "job not found")
		}
		if !terminal(j.State) || j.Sequence != q.ExpectedJobSequence {
			return status.Error(codes.FailedPrecondition, "expected terminal job with current sequence")
		}
		var node *p.NodeJob
		for _, n := range j.Nodes {
			if n.NodeId == q.NodeId {
				node = n
				break
			}
		}
		if node == nil || node.State != "ROLLBACK_FAILED" || node.TaskId == "" {
			return status.Error(codes.FailedPrecondition, "node has no recoverable rollback failure in this job")
		}
		node.RollbackRequestId = id()
		node.RollbackSequence = node.AgentSequence // Older journals may be zero; worker reads the task first.
		node.RollbackPending = true
		j.State = "RECOVERING"
		event(j, node.NodeId, "rollback retry intent saved; node remains reserved until Agent verifies Undo")
		d.Receipts[key] = &p.Receipt{Id: j.Id, Hash: hash(q)}
		out = proto.Clone(j).(*p.Job)
		return nil
	})
	if e == nil {
		c.notify()
	}
	return out, e
}

func (c *Controller) advanceRollback(ctx context.Context, job *p.Job, index int) {
	n := job.Nodes[index]
	record := c.snapshot().Nodes[n.NodeId]
	if record == nil {
		c.unknown(ctx, job.Id, n.NodeId, fmt.Errorf("node no longer registered"))
		return
	}
	client, e := c.client(record)
	if e != nil {
		c.unknown(ctx, job.Id, n.NodeId, e)
		return
	}
	if n.RollbackSequence == 0 {
		call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
		task, err := client.GetTask(call, &a.TaskRequest{NodeId: n.NodeId, TaskId: n.TaskId})
		cancel()
		if err != nil {
			c.unknown(ctx, job.Id, n.NodeId, err)
			return
		}
		if task.State != "ROLLBACK_FAILED" || task.Sequence == 0 || task.Revision != n.BaseRevision {
			c.unknown(ctx, job.Id, n.NodeId, fmt.Errorf("saved task differs from rollback baseline; reconcile explicitly"))
			return
		}
		if e = c.updateJob(ctx, job.Id, func(_ *p.Database, j *p.Job) error {
			j.Nodes[index].RollbackSequence = task.Sequence
			return nil
		}); e != nil {
			return
		}
		n.RollbackSequence = task.Sequence
	}
	call, cancel := context.WithTimeout(ctx, c.options.RPCTimeout)
	task, e := client.RetryRollback(call, &a.RetryRollbackRequest{
		NodeId: n.NodeId, TaskId: n.TaskId, RequestId: n.RollbackRequestId,
		ExpectedSequence: n.RollbackSequence, ExpectedRevision: n.BaseRevision,
	})
	cancel()
	if e != nil {
		c.unknown(ctx, job.Id, n.NodeId, e)
		return
	}
	if task.Id != n.TaskId || task.NodeId != n.NodeId || task.Revision != n.BaseRevision {
		c.unknown(ctx, job.Id, n.NodeId, fmt.Errorf("Agent rollback identity or baseline mismatch"))
		return
	}
	switch task.State {
	case "ROLLING_BACK":
		_ = c.updateJob(ctx, job.Id, func(_ *p.Database, j *p.Job) error {
			if j.State != "RECOVERING" || j.Nodes[index].AgentSequence != task.Sequence {
				j.State = "RECOVERING"
				j.Nodes[index].AgentSequence = task.Sequence
				event(j, n.NodeId, "Agent is retrying saved Undo")
			}
			return nil
		})
	case "FAILED_ROLLED_BACK", "ROLLBACK_FAILED":
		_ = c.updateJob(ctx, job.Id, func(_ *p.Database, j *p.Job) error {
			x := j.Nodes[index]
			x.State = task.State
			x.AgentSequence = task.Sequence
			x.RollbackPending = false
			x.Error = task.Error
			j.State = "FAILED"
			for _, node := range j.Nodes {
				if node.State == "SUCCEEDED" || node.State == "ROLLBACK_FAILED" {
					j.State = "PARTIAL"
				}
			}
			event(j, n.NodeId, "rollback retry ended "+task.State+"; original forward job was not resumed")
			return nil
		})
	default:
		c.unknown(ctx, job.Id, n.NodeId, fmt.Errorf("unexpected rollback task state %q", task.State))
	}
}
