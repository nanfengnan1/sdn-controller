package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/config"
)

// checkHealth verifies a real authenticated North RPC, including TLS when enabled.
// The configured server certificate is the local health check's trust anchor.
func checkHealth(cfg config.Config) error {
	a := cfg.Controller
	host, port, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if a.TLSCert != "" {
		pair, err := tls.LoadX509KeyPair(a.TLSCert, a.TLSKey)
		if err != nil {
			return err
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return err
		}
		serverName := host
		if len(leaf.DNSNames) > 0 {
			serverName = leaf.DNSNames[0]
			if strings.HasPrefix(serverName, "*.") {
				serverName = "health." + strings.TrimPrefix(serverName, "*.")
			}
		} else if len(leaf.IPAddresses) > 0 {
			serverName = leaf.IPAddresses[0].String()
		}
		pem, err := os.ReadFile(a.TLSCert)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("invalid server certificate")
		}
		creds = credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(net.JoinHostPort(host, port), grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+a.AdminToken))
	_, err = p.NewNorthClient(conn).GetConfiguration(ctx, &p.Empty{})
	return err
}
