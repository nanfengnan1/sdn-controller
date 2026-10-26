package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	a "sdn-agent/api/agent/v1"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/config"
	"sdn-controller/internal/controller"
	"sdn-controller/internal/repository"
	"sdn-controller/pkg/transport"
)

func main() {
	cfg, mode, err := config.Parse(os.Args[1:], os.LookupEnv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	if mode.Check {
		log.Print("configuration valid")
		return
	}
	if mode.Plan {
		if err := json.NewEncoder(os.Stdout).Encode(cfg.Plan()); err != nil {
			log.Fatal("encode runtime plan")
		}
		return
	}
	if mode.Health {
		if err := checkHealth(cfg); err != nil {
			log.Fatal("Controller health check failed")
		}
		return
	}
	app := cfg.Controller
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	admin, viewer, agent := app.AdminToken, app.ViewerToken, app.AgentToken
	server, e := transport.Server(transport.Tokens{Admin: admin, Viewer: viewer, Agent: agent}, app.TLSCert, app.TLSKey, app.Insecure)
	if e != nil {
		log.Fatal(e)
	}
	var store repository.Store
	// Leave room in the supervisor's readiness deadline for loading the snapshot
	// and the authenticated health probe after a stale lease has expired.
	startupWait := cfg.Etcd.StartupTimeoutSeconds - 5
	if startupWait < 1 {
		startupWait = 1
	}
	startup, cancel := context.WithTimeout(ctx, time.Duration(startupWait)*time.Second)
	if app.StateFile != "" {
		store, e = repository.OpenFile(app.StateFile)
	} else {
		log.Printf("acquiring exclusive writer lease; startup wait limited to %ds", startupWait)
		store, e = repository.OpenEtcdWait(startup, strings.Split(app.Etcd, ","), app.EtcdPrefix)
	}
	cancel()
	if e != nil {
		if ctx.Err() != nil {
			log.Print("Controller startup canceled")
			return
		}
		log.Fatal(e)
	}
	defer store.Close()
	c, e := controller.New(ctx, store, controller.Options{AgentToken: agent, AgentCA: app.AgentCA, Insecure: app.Insecure})
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	p.RegisterNorthServer(server, c)
	a.RegisterControllerServer(server, c)
	listener, e := net.Listen("tcp", app.Listen)
	if e != nil {
		log.Fatal(e)
	}
	go c.Run(ctx)
	go func() {
		select {
		case <-store.Lost():
			log.Print("writer lease lost or storage outcome unresolved; stopping all RPCs and task execution; reopen persisted state before resuming")
			stop()
		case <-ctx.Done():
		}
		server.Stop()
	}()
	log.Printf("sdn-controller listening at %s; single writer, schema v1", listener.Addr())
	if e = server.Serve(listener); e != nil && ctx.Err() == nil {
		log.Fatal(e)
	}
}
