package transport

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"testing"
)

type north struct{ p.UnimplementedNorthServer }

func (n *north) GetConfiguration(ctx context.Context, q *p.Empty) (*p.ConfigurationState, error) {
	return &p.ConfigurationState{}, nil
}
func (n *north) BeginChange(ctx context.Context, q *p.BeginChangeRequest) (*p.Change, error) {
	return &p.Change{Id: Role(ctx)}, nil
}
func (n *north) RetryRollback(ctx context.Context, q *p.RetryRollbackRequest) (*p.Job, error) {
	return &p.Job{Id: Role(ctx)}, nil
}
func (n *north) GetNodeResources(context.Context, *p.NodeResourcesRequest) (*p.NodeResourcesState, error) {
	return &p.NodeResourcesState{}, nil
}
func (n *north) QueryNode(context.Context, *a.QueryRequest) (*a.QueryResponse, error) {
	return &a.QueryResponse{}, nil
}
func (n *north) EditNodeResources(context.Context, *p.EditNodeResourcesRequest) (*p.Change, error) {
	return &p.Change{}, nil
}
func (n *north) ImportNodeResources(context.Context, *p.ImportNodeResourcesRequest) (*p.NodeResourcesState, error) {
	return &p.NodeResourcesState{}, nil
}

type registration struct {
	a.UnimplementedControllerServer
}

func (r *registration) Register(ctx context.Context, q *a.Registration) (*a.Registered, error) {
	return &a.Registered{NodeId: Role(ctx)}, nil
}
func TestRoleEnforcementAcrossServices(t *testing.T) {
	tokens := Tokens{Admin: "admin-token-for-test", Viewer: "viewer-token-for-test", Agent: "agent-token-for-test"}
	s, e := Server(tokens, "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	p.RegisterNorthServer(s, &north{})
	a.RegisterControllerServer(s, &registration{})
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go s.Serve(l)
	defer s.Stop()
	for _, tt := range []struct {
		name, token      string
		read, write, reg codes.Code
	}{{"admin", tokens.Admin, codes.OK, codes.OK, codes.PermissionDenied}, {"viewer", tokens.Viewer, codes.OK, codes.PermissionDenied, codes.PermissionDenied}, {"agent", tokens.Agent, codes.PermissionDenied, codes.PermissionDenied, codes.OK}, {"invalid", "invalid", codes.Unauthenticated, codes.Unauthenticated, codes.Unauthenticated}} {
		t.Run(tt.name, func(t *testing.T) {
			conn, e := Dial(l.Addr().String(), tt.token, "", true)
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close()
			c := p.NewNorthClient(conn)
			_, e = c.GetConfiguration(context.Background(), &p.Empty{})
			if status.Code(e) != tt.read {
				t.Fatal("read", e)
			}
			_, e = c.GetNodeResources(context.Background(), &p.NodeResourcesRequest{})
			if status.Code(e) != tt.read {
				t.Fatal("resource read", e)
			}
			_, e = c.QueryNode(context.Background(), &a.QueryRequest{})
			if status.Code(e) != tt.read {
				t.Fatal("query permission", e)
			}
			_, e = c.EditNodeResources(context.Background(), &p.EditNodeResourcesRequest{})
			if status.Code(e) != tt.write {
				t.Fatal("resource edit permission", e)
			}
			_, e = c.ImportNodeResources(context.Background(), &p.ImportNodeResourcesRequest{})
			if status.Code(e) != tt.write {
				t.Fatal("resource import permission", e)
			}
			_, e = c.BeginChange(context.Background(), &p.BeginChangeRequest{})
			if status.Code(e) != tt.write {
				t.Fatal("write", e)
			}
			_, e = c.RetryRollback(context.Background(), &p.RetryRollbackRequest{})
			if status.Code(e) != tt.write {
				t.Fatal("rollback recovery permission", e)
			}
			_, e = a.NewControllerClient(conn).Register(context.Background(), &a.Registration{})
			if status.Code(e) != tt.reg {
				t.Fatal("register", e)
			}
		})
	}
}
