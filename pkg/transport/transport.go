package transport

import (
	"context"
	"crypto/subtle"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
)

type roleKey struct{}

func Role(ctx context.Context) string { v, _ := ctx.Value(roleKey{}).(string); return v }
func WithRole(ctx context.Context, r string) context.Context {
	return context.WithValue(ctx, roleKey{}, r)
}

type Tokens struct{ Admin, Viewer, Agent string }

func (t Tokens) role(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	v := md.Get("authorization")
	if len(v) != 1 {
		return "", status.Error(codes.Unauthenticated, "bearer token required")
	}
	for _, x := range []struct{ token, role string }{{t.Admin, "admin"}, {t.Viewer, "viewer"}, {t.Agent, "agent"}} {
		if x.token != "" && subtle.ConstantTimeCompare([]byte(v[0]), []byte("Bearer "+x.token)) == 1 {
			return x.role, nil
		}
	}
	return "", status.Error(codes.Unauthenticated, "invalid bearer token")
}
func authorized(role, method string) bool {
	if strings.HasPrefix(method, "/sdn.agent.v1.Controller/") {
		return role == "agent"
	}
	if role == "admin" {
		return true
	}
	if role != "viewer" {
		return false
	}
	switch method[strings.LastIndex(method, "/")+1:] {
	case "ListNodes", "GetNode", "GetConfiguration", "GetChange", "ListJobs", "GetJob", "WatchJob", "GetTopology", "GetNodeResources", "QueryNode":
		return true
	}
	return false
}
func Server(t Tokens, cert, key string, allowInsecure bool) (*grpc.Server, error) {
	if len(t.Admin) < 16 || len(t.Agent) < 16 || t.Admin == t.Agent || t.Viewer != "" && (len(t.Viewer) < 16 || t.Viewer == t.Admin || t.Viewer == t.Agent) {
		return nil, fmt.Errorf("admin/agent tokens must be distinct and >=16 bytes; optional viewer must also be distinct and >=16")
	}
	auth := func(ctx context.Context, method string) (context.Context, error) {
		r, e := t.role(ctx)
		if e != nil {
			return nil, e
		}
		if !authorized(r, method) {
			return nil, status.Error(codes.PermissionDenied, "role cannot invoke this method")
		}
		return WithRole(ctx, r), nil
	}
	opts := []grpc.ServerOption{grpc.MaxRecvMsgSize(2 << 20), grpc.UnaryInterceptor(func(ctx context.Context, q any, i *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		ctx, e := auth(ctx, i.FullMethod)
		if e != nil {
			return nil, e
		}
		return h(ctx, q)
	}), grpc.StreamInterceptor(func(s any, stream grpc.ServerStream, i *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		ctx, e := auth(stream.Context(), i.FullMethod)
		if e != nil {
			return e
		}
		return h(s, &serverStream{ServerStream: stream, ctx: ctx})
	})}
	if cert != "" && key != "" {
		c, e := credentials.NewServerTLSFromFile(cert, key)
		if e != nil {
			return nil, e
		}
		opts = append(opts, grpc.Creds(c))
	} else if !allowInsecure {
		return nil, fmt.Errorf("TLS certificate/key required; -insecure is for isolated labs")
	}
	return grpc.NewServer(opts...), nil
}

type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverStream) Context() context.Context { return s.ctx }
func Dial(addr, token, ca string, allowInsecure bool) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if ca != "" {
		c, e := credentials.NewClientTLSFromFile(ca, "")
		if e != nil {
			return nil, e
		}
		creds = c
	} else if allowInsecure {
		creds = insecure.NewCredentials()
	} else {
		return nil, fmt.Errorf("CA required unless -insecure")
	}
	auth := func(ctx context.Context) context.Context {
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(creds), grpc.WithUnaryInterceptor(func(ctx context.Context, m string, q, r any, c *grpc.ClientConn, invoke grpc.UnaryInvoker, o ...grpc.CallOption) error {
		return invoke(auth(ctx), m, q, r, c, o...)
	}), grpc.WithStreamInterceptor(func(ctx context.Context, d *grpc.StreamDesc, c *grpc.ClientConn, m string, f grpc.Streamer, o ...grpc.CallOption) (grpc.ClientStream, error) {
		return f(auth(ctx), d, c, m, o...)
	}))
}
