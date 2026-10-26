package controller

import p "sdn-controller/api/controller/v1"

// Protobuf Clone canonicalizes empty maps to nil.
func initializeMaps(d *p.Database) {
	if d.Nodes == nil {
		d.Nodes = map[string]*p.Node{}
	}
	if d.Changes == nil {
		d.Changes = map[string]*p.Change{}
	}
	if d.Plans == nil {
		d.Plans = map[string]*p.Plan{}
	}
	if d.Jobs == nil {
		d.Jobs = map[string]*p.Job{}
	}
	if d.Receipts == nil {
		d.Receipts = map[string]*p.Receipt{}
	}
	if d.Applied == nil {
		d.Applied = map[string]*p.Applied{}
	}
}
