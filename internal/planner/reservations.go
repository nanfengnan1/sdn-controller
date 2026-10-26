package planner

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
)

// An admitted job owns its nodes until all of its dependent work stops. A
// terminal rollback failure quarantines only the affected node. A rollback
// retry never reacquires unrelated nodes from the original job.
func reservations(db *p.Database) (map[string]*p.NodePlan, error) {
	out := map[string]*p.NodePlan{}
	for _, job := range db.Jobs {
		recovering := false
		for _, n := range job.Nodes {
			recovering = recovering || n.RollbackPending
		}
		active := job.State != "SUCCEEDED" && job.State != "FAILED" && job.State != "PARTIAL" && job.State != "ROLLED_BACK" && job.State != "COMPENSATION_FAILED"
		for _, n := range job.Nodes {
			if !(n.RollbackPending || n.State == "ROLLBACK_FAILED" || active && !recovering) {
				continue
			}
			var plan *p.NodePlan
			for _, np := range job.GetPlan().GetNodes() {
				if np.NodeId == n.NodeId {
					plan = np
					break
				}
			}
			if plan == nil {
				return nil, fmt.Errorf("node %s reserved without a saved plan", n.NodeId)
			}
			if out[n.NodeId] != nil {
				return nil, fmt.Errorf("node %s has conflicting job reservations", n.NodeId)
			}
			out[n.NodeId] = plan
		}
	}
	return out, nil
}

func sameTarget(left, right []*a.Operation) bool {
	if len(left) != len(right) {
		return false
	}
	byID := make(map[string]*a.Operation, len(left))
	for _, op := range left {
		byID[op.Id] = op
	}
	for _, op := range right {
		if !proto.Equal(byID[op.Id], op) {
			return false
		}
	}
	return true
}
