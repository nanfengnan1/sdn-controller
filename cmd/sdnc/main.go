// sdnc supports typed convenience commands and every unary North RPC.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/pkg/transport"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:19090", "Controller address")
	ca := flag.String("ca", "", "TLS CA")
	insecure := flag.Bool("insecure", false, "lab plaintext")
	file := flag.String("file", "", "configuration or RPC request JSON file")
	node := flag.String("node", "", "node ID")
	jobID := flag.String("job", "", "job ID")
	method := flag.String("method", "", "North unary RPC method for rpc command")
	revision := flag.Uint64("revision", 0, "expected agent revision for adopt")
	allow := flag.Bool("allow-existing", false, "acknowledge existing Agent-owned configuration during adoption")
	refresh := flag.Bool("refresh", false, "refresh topology")
	wait := flag.Bool("wait", true, "wait for submitted job terminal state")
	request := flag.String("request-id", "", "stable submit request prefix; keep on retry")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall request timeout")
	flag.Parse()
	if flag.NArg() < 1 {
		return fmt.Errorf("usage: sdnc [flags] nodes|configuration|adopt|plan|submit|job|watch|topology|rpc")
	}
	token := os.Getenv("SDN_ADMIN_TOKEN")
	if token == "" {
		token = os.Getenv("SDN_VIEWER_TOKEN")
	}
	conn, e := transport.Dial(*addr, token, *ca, *insecure)
	if e != nil {
		return e
	}
	defer conn.Close()
	client := p.NewNorthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	emit := func(m proto.Message) error {
		b, e := protojson.MarshalOptions{Multiline: true, Indent: "  ", EmitUnpopulated: true}.Marshal(m)
		if e == nil {
			fmt.Println(string(b))
		}
		return e
	}
	if resourceCommand(flag.Arg(0)) {
		return runResources(ctx, client, flag.Args(), *node, *request, *wait, *revision, emit)
	}
	if flag.NArg() != 1 {
		return fmt.Errorf("unexpected command arguments")
	}
	switch flag.Arg(0) {
	case "nodes":
		x, e := client.ListNodes(ctx, &p.ListRequest{PageSize: 500})
		if e != nil {
			return e
		}
		return emit(x)
	case "configuration":
		x, e := client.GetConfiguration(ctx, &p.Empty{})
		if e != nil {
			return e
		}
		return emit(x)
	case "adopt":
		x, e := client.AdoptNode(ctx, &p.AdoptNodeRequest{NodeId: *node, ExpectedRevision: *revision, AllowExisting: *allow})
		if e != nil {
			return e
		}
		return emit(x)
	case "job":
		x, e := client.GetJob(ctx, &p.JobRequest{JobId: *jobID})
		if e != nil {
			return e
		}
		return emit(x)
	case "topology":
		x, e := client.GetTopology(ctx, &p.TopologyRequest{Refresh: *refresh})
		if e != nil {
			return e
		}
		return emit(x)
	case "watch":
		stream, e := client.WatchJob(ctx, &p.JobRequest{JobId: *jobID})
		if e != nil {
			return e
		}
		for {
			ev, e := stream.Recv()
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
			if e = emit(ev); e != nil {
				return e
			}
		}
	case "rpc":
		svc := p.File_api_controller_v1_controller_proto.Services().ByName("North")
		m := svc.Methods().ByName(protoreflect.Name(*method))
		if m == nil || m.IsStreamingServer() {
			return fmt.Errorf("unknown/unary-only method")
		}
		q := dynamicpb.NewMessage(m.Input())
		if *file != "" {
			b, e := os.ReadFile(*file)
			if e != nil {
				return e
			}
			if e = protojson.Unmarshal(b, q); e != nil {
				return e
			}
		}
		out := dynamicpb.NewMessage(m.Output())
		if e = conn.Invoke(ctx, "/sdn.controller.v1.North/"+*method, q, out); e != nil {
			return e
		}
		return emit(out)
	case "plan", "submit":
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		cfg := &p.Configuration{}
		if e = protojson.Unmarshal(b, cfg); e != nil {
			return e
		}
		state, e := client.GetConfiguration(ctx, &p.Empty{})
		if e != nil {
			return e
		}
		rid := *request
		if rid == "" {
			b := make([]byte, 16)
			if _, e = rand.Read(b); e != nil {
				return e
			}
			rid = hex.EncodeToString(b)
		}
		change, e := client.BeginChange(ctx, &p.BeginChangeRequest{BaseRevision: state.Revision, RequestId: rid + "/begin"})
		if e != nil {
			return e
		}
		if change.Frozen {
			j, e := client.GetJob(ctx, &p.JobRequest{JobId: change.JobId})
			if e != nil {
				return e
			}
			return emit(j)
		}
		change, e = client.EditChange(ctx, &p.EditChangeRequest{ChangeId: change.Id, EditRevision: change.EditRevision, Configuration: cfg})
		if e != nil {
			return e
		}
		plan, e := client.PlanChange(ctx, &p.ChangeRequest{ChangeId: change.Id})
		if e != nil {
			return e
		}
		if flag.Arg(0) == "plan" {
			return emit(plan)
		}
		job, e := client.CommitChange(ctx, &p.CommitChangeRequest{ChangeId: change.Id, RequestId: rid + "/commit", PlanId: plan.Id})
		if e != nil {
			return e
		}
		if !*wait {
			return emit(job)
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			switch job.State {
			case "SUCCEEDED":
				return emit(job)
			case "FAILED", "PARTIAL", "ROLLED_BACK", "COMPENSATION_FAILED":
				_ = emit(job)
				return fmt.Errorf("job %s ended %s: %s", job.Id, job.State, job.Error)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w; query job %s (execution continues)", ctx.Err(), job.Id)
			case <-ticker.C:
			}
			job, e = client.GetJob(ctx, &p.JobRequest{JobId: job.Id})
			if e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("unknown command")
	}
}
