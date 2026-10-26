// Package config owns Controller startup configuration and the bundled etcd contract.
package config

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Controller struct {
	Listen      string `json:"listen"`
	Etcd        string `json:"etcd"`
	EtcdPrefix  string `json:"etcdPrefix"`
	StateFile   string `json:"stateFile"`
	TLSCert     string `json:"tlsCert"`
	TLSKey      string `json:"tlsKey"`
	AgentCA     string `json:"agentCa"`
	Insecure    bool   `json:"insecure"`
	AdminToken  string `json:"adminToken"`
	ViewerToken string `json:"viewerToken"`
	AgentToken  string `json:"agentToken"`
}

type Etcd struct {
	Enabled                bool   `json:"enabled"`
	Name                   string `json:"name"`
	ClientPort             int    `json:"clientPort"`
	PeerPort               int    `json:"peerPort"`
	DataDir                string `json:"dataDir"`
	StartupTimeoutSeconds  int    `json:"startupTimeoutSeconds"`
	ShutdownTimeoutSeconds int    `json:"shutdownTimeoutSeconds"`
}

type Config struct {
	Controller Controller `json:"controller"`
	Etcd       Etcd       `json:"etcd"`
}

type Mode struct{ Check, Plan, Health bool }

type RuntimePlan struct {
	Etcd   Etcd   `json:"etcd"`
	Listen string `json:"listen"`
}

func Defaults() Config {
	return Config{
		Controller: Controller{Listen: ":19090", Etcd: "127.0.0.1:2379", EtcdPrefix: "/sdn/controller/v1"},
		Etcd:       Etcd{Name: "sdn-docker", ClientPort: 2379, PeerPort: 2380, DataDir: "/etcd-data", StartupTimeoutSeconds: 30, ShutdownTimeoutSeconds: 20},
	}
}

// Parse preserves legacy CLI defaults if -config is omitted. An explicitly empty
// token environment variable clears a configured token rather than silently falling back.
func Parse(args []string, lookup func(string) (string, bool), output io.Writer) (Config, Mode, error) {
	cfg, flags := Defaults(), Defaults()
	var path string
	var mode Mode
	fs := flag.NewFlagSet("sdn-controller", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&path, "config", "", "strict JSON controller.conf (optional for legacy CLI)")
	fs.StringVar(&flags.Controller.Listen, "listen", flags.Controller.Listen, "gRPC listen address")
	fs.StringVar(&flags.Controller.Etcd, "etcd", flags.Controller.Etcd, "comma-separated etcd endpoints")
	fs.StringVar(&flags.Controller.EtcdPrefix, "etcd-prefix", flags.Controller.EtcdPrefix, "isolated database prefix")
	fs.StringVar(&flags.Controller.StateFile, "state-file", "", "development file store instead of etcd")
	fs.StringVar(&flags.Controller.TLSCert, "tls-cert", "", "server certificate")
	fs.StringVar(&flags.Controller.TLSKey, "tls-key", "", "server key")
	fs.StringVar(&flags.Controller.AgentCA, "agent-ca", "", "CA for outbound Agent TLS")
	fs.BoolVar(&flags.Controller.Insecure, "insecure", false, "explicit isolated-lab plaintext transport")
	fs.BoolVar(&mode.Check, "check-config", false, "validate startup configuration without starting services")
	fs.BoolVar(&mode.Plan, "runtime-plan", false, "emit non-secret bundled service settings for the supervisor")
	fs.BoolVar(&mode.Health, "healthcheck", false, "query this Controller using configured authentication")
	if err := fs.Parse(args); err != nil {
		return cfg, mode, err
	}
	if fs.NArg() != 0 {
		return cfg, mode, errors.New("unexpected positional arguments")
	}
	if boolInt(mode.Check)+boolInt(mode.Plan)+boolInt(mode.Health) > 1 {
		return cfg, mode, errors.New("choose only one diagnostic mode")
	}
	endpointSet := false
	if path != "" {
		var err error
		cfg, endpointSet, err = Load(path)
		if err != nil {
			return cfg, mode, err
		}
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.Controller.Listen = flags.Controller.Listen
		case "etcd":
			cfg.Controller.Etcd = flags.Controller.Etcd
			endpointSet = true
		case "etcd-prefix":
			cfg.Controller.EtcdPrefix = flags.Controller.EtcdPrefix
		case "state-file":
			cfg.Controller.StateFile = flags.Controller.StateFile
		case "tls-cert":
			cfg.Controller.TLSCert = flags.Controller.TLSCert
		case "tls-key":
			cfg.Controller.TLSKey = flags.Controller.TLSKey
		case "agent-ca":
			cfg.Controller.AgentCA = flags.Controller.AgentCA
		case "insecure":
			cfg.Controller.Insecure = flags.Controller.Insecure
		}
	})
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"SDN_ADMIN_TOKEN", &cfg.Controller.AdminToken}, {"SDN_VIEWER_TOKEN", &cfg.Controller.ViewerToken}, {"SDN_AGENT_TOKEN", &cfg.Controller.AgentToken},
	} {
		if value, ok := lookup(item.name); ok {
			*item.target = value
		}
	}
	if cfg.Etcd.Enabled && !endpointSet {
		cfg.Controller.Etcd = cfg.Etcd.ClientURL()
	}
	return cfg, mode, cfg.Validate()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func (e Etcd) ClientURL() string { return fmt.Sprintf("http://127.0.0.1:%d", e.ClientPort) }

func Load(path string) (Config, bool, error) {
	cfg := Defaults()
	f, err := os.Open(path)
	if err != nil {
		return cfg, false, fmt.Errorf("read controller configuration: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return cfg, false, fmt.Errorf("read controller configuration: %w", err)
	}
	if len(data) > 1<<20 {
		return cfg, false, errors.New("controller configuration exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err = checkValue(decoder); err != nil {
		return cfg, false, fmt.Errorf("invalid controller configuration: %w", err)
	}
	if _, err = decoder.Token(); err != io.EOF {
		return cfg, false, errors.New("configuration must contain one JSON object")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return cfg, false, fmt.Errorf("decode controller configuration: %w", err)
	}
	var raw struct {
		Controller map[string]json.RawMessage `json:"controller"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return cfg, false, errors.New("configuration must be a JSON object")
	}
	explicit := false
	for name := range raw.Controller {
		if strings.EqualFold(name, "etcd") {
			explicit = true
		}
	}
	return cfg, explicit, nil
}

// Reject ambiguous duplicate keys, nulls, and malformed JSON before typed decoding.
func checkValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return errors.New("null configuration values are not supported")
	}
	if open, ok := t.(json.Delim); ok {
		switch open {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("expected object key")
				}
				// encoding/json matches struct fields case-insensitively; reject
				// differently-cased duplicates as the same effective field.
				name = strings.ToLower(name)
				if keys[name] {
					return errors.New("duplicate configuration key")
				}
				keys[name] = true
				if err = checkValue(d); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := checkValue(d); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, err = d.Token()
	}
	return err
}

func (c Config) Validate() error {
	a, e := c.Controller, c.Etcd
	_, listenPort, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return errors.New("controller.listen must be a host:port address")
	}
	if _, err := net.LookupPort("tcp", listenPort); err != nil {
		return errors.New("invalid controller.listen port")
	}
	if e.StartupTimeoutSeconds < 1 || e.StartupTimeoutSeconds > 300 || e.ShutdownTimeoutSeconds < 1 || e.ShutdownTimeoutSeconds > 300 {
		return errors.New("etcd timeouts must be within 1..300 seconds")
	}
	if len(a.AdminToken) < 16 || len(a.AgentToken) < 16 || a.AdminToken == a.AgentToken || a.ViewerToken != "" && (len(a.ViewerToken) < 16 || a.ViewerToken == a.AdminToken || a.ViewerToken == a.AgentToken) {
		return errors.New("admin/agent tokens must be distinct and at least 16 bytes; optional viewer must also be distinct and at least 16 bytes")
	}
	if (a.TLSCert == "") != (a.TLSKey == "") {
		return errors.New("tlsCert and tlsKey must be provided together")
	}
	if a.TLSCert == "" && !a.Insecure {
		return errors.New("TLS certificate/key required unless insecure is explicitly enabled")
	}
	if a.TLSCert != "" {
		if _, err := tls.LoadX509KeyPair(a.TLSCert, a.TLSKey); err != nil {
			return errors.New("cannot load Controller TLS certificate/key")
		}
	}
	if a.AgentCA != "" {
		pem, err := os.ReadFile(a.AgentCA)
		if err != nil || !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return errors.New("cannot load Agent CA certificate")
		}
	}
	if a.StateFile == "" {
		if a.EtcdPrefix == "" || !strings.HasPrefix(a.EtcdPrefix, "/") {
			return errors.New("etcdPrefix must start with /")
		}
		if err := validateEndpoints(a.Etcd); err != nil {
			return err
		}
	}
	if e.Enabled {
		if a.StateFile != "" {
			return errors.New("bundled etcd cannot be enabled with controller.stateFile")
		}
		if e.ClientPort < 1 || e.ClientPort > 65535 || e.PeerPort < 1 || e.PeerPort > 65535 || e.ClientPort == e.PeerPort {
			return errors.New("bundled etcd client/peer ports must be distinct and within 1..65535")
		}
		if !filepath.IsAbs(e.DataDir) || filepath.Clean(e.DataDir) == "/" {
			return errors.New("etcd.dataDir must be a dedicated absolute directory")
		}
		if e.Name == "" || strings.ContainsAny(e.Name, "=, \t\r\n") {
			return errors.New("etcd.name must be nonempty and contain no separator or whitespace")
		}
		if a.Etcd != e.ClientURL() && a.Etcd != "127.0.0.1:"+strconv.Itoa(e.ClientPort) {
			return errors.New("controller.etcd must match the bundled etcd loopback client port; disable bundled etcd to use external storage")
		}
		_, port, _ := net.SplitHostPort(a.Listen)
		if port == strconv.Itoa(e.ClientPort) || port == strconv.Itoa(e.PeerPort) {
			return errors.New("Controller and bundled etcd listening ports must differ")
		}
	}
	return nil
}

func validateEndpoints(endpoints string) error {
	if endpoints == "" {
		return errors.New("at least one etcd endpoint is required")
	}
	for _, endpoint := range strings.Split(endpoints, ",") {
		if !strings.Contains(endpoint, "://") {
			endpoint = "http://" + endpoint
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid etcd endpoint; use http(s)://host:port without credentials or a path")
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("invalid etcd endpoint port")
		}
	}
	return nil
}

func (c Config) Plan() RuntimePlan { return RuntimePlan{Etcd: c.Etcd, Listen: c.Controller.Listen} }
