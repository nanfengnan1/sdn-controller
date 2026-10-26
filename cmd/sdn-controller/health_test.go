package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	p "sdn-controller/api/controller/v1"
	"sdn-controller/internal/config"
	"sdn-controller/pkg/transport"
)

type healthAPI struct{ p.UnimplementedNorthServer }

func (healthAPI) GetConfiguration(context.Context, *p.Empty) (*p.ConfigurationState, error) {
	return &p.ConfigurationState{}, nil
}

func TestHealthUsesAuthenticationAndTLS(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		t.Run(map[bool]string{false: "plaintext", true: "tls-DNS-certificate"}[useTLS], func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Controller.AdminToken = "health-admin-test-credential"
			cfg.Controller.AgentToken = "health-agent-test-credential"
			cfg.Controller.Insecure = !useTLS
			if useTLS {
				cfg.Controller.TLSCert, cfg.Controller.TLSKey = makeCert(t)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Controller.Listen = listener.Addr().String()
			server, err := transport.Server(transport.Tokens{Admin: cfg.Controller.AdminToken, Agent: cfg.Controller.AgentToken}, cfg.Controller.TLSCert, cfg.Controller.TLSKey, cfg.Controller.Insecure)
			if err != nil {
				t.Fatal(err)
			}
			p.RegisterNorthServer(server, healthAPI{})
			go server.Serve(listener)
			defer server.Stop()
			if err = checkHealth(cfg); err != nil {
				t.Fatalf("healthy service failed check: %v", err)
			}
			cfg.Controller.AdminToken = "incorrect-admin-test-token"
			if err = checkHealth(cfg); err == nil {
				t.Fatal("health check ignored authentication")
			}
		})
	}
}

func makeCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "controller.example"}, DNSNames: []string{"controller.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "server.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: priv}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
