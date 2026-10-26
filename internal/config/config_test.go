package config

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const admin = "config-admin-credential-012345"
const agent = "config-agent-credential-012345"

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}
func validEnv() func(string) (string, bool) {
	return env(map[string]string{"SDN_ADMIN_TOKEN": admin, "SDN_AGENT_TOKEN": agent})
}
func file(t *testing.T, data string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "controller.conf")
	if err := os.WriteFile(name, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLegacyCLIAndHelp(t *testing.T) {
	c, mode, err := Parse([]string{"-insecure"}, validEnv(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Controller.Listen != ":19090" || c.Controller.Etcd != "127.0.0.1:2379" || c.Etcd.Enabled || mode.Check {
		t.Fatalf("legacy defaults changed: %#v", c.Plan())
	}
	_, _, err = Parse([]string{"-h"}, env(nil), io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help should not require credentials: %v", err)
	}
}

func TestFileEnvironmentAndExplicitFlagPrecedence(t *testing.T) {
	path := file(t, `{"controller":{"listen":":19190","etcdPrefix":"/configured","insecure":true,"adminToken":"`+admin+`","agentToken":"`+agent+`"},"etcd":{"enabled":true,"clientPort":12379,"peerPort":12380}}`)
	c, _, err := Parse([]string{"-config", path}, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Controller.Etcd != "http://127.0.0.1:12379" || c.Controller.AdminToken != admin {
		t.Fatal("file-only configuration was not applied")
	}
	replacement := "environment-admin-credential-012345"
	c, _, err = Parse([]string{"-config", path, "-listen", ":29190", "-etcd-prefix", "/override"}, env(map[string]string{"SDN_ADMIN_TOKEN": replacement}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Controller.Listen != ":29190" || c.Controller.EtcdPrefix != "/override" || c.Controller.AdminToken != replacement || c.Controller.AgentToken != agent {
		t.Fatal("precedence incorrect")
	}
	c, _, err = Parse([]string{"-config", path, "-insecure=false"}, env(nil), io.Discard)
	if err == nil || c.Controller.Insecure {
		t.Fatal("explicit false must override JSON true and require TLS")
	}
	_, _, err = Parse([]string{"-config", path}, env(map[string]string{"SDN_ADMIN_TOKEN": ""}), io.Discard)
	if err == nil {
		t.Fatal("empty explicit env must clear file token")
	}
}

func TestExternalEtcdAndFileModes(t *testing.T) {
	for _, body := range []string{
		`{"controller":{"etcd":"http://etcd.example:2379","insecure":true},"etcd":{"enabled":false}}`,
		`{"controller":{"stateFile":"state.pb","insecure":true},"etcd":{"enabled":false}}`,
	} {
		_, _, err := Parse([]string{"-config", file(t, body)}, validEnv(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectInvalidConfigurationBeforeRuntime(t *testing.T) {
	cases := map[string]string{
		"unknown root":             `{"redis":{}}`,
		"unknown field":            `{"controller":{"adminTokne":"secret"}}`,
		"wrong type":               `{"controller":{"insecure":"yes"}}`,
		"duplicate":                `{"controller":{"listen":":1","listen":":2"}}`,
		"case duplicate":           `{"controller":{"listen":":1","Listen":":2"}}`,
		"case endpoint conflict":   `{"controller":{"Etcd":"http://remote:2379"},"etcd":{"enabled":true}}`,
		"null root":                `null`,
		"null object":              `{"controller":null}`,
		"null scalar":              `{"controller":{"listen":null}}`,
		"two documents":            `{ } { }`,
		"array":                    `[]`,
		"bad listen":               `{"controller":{"listen":"missing-port"}}`,
		"bad listen port":          `{"controller":{"listen":":999999"}}`,
		"endpoint auth":            `{"controller":{"etcd":"http://user:password@example:2379"}}`,
		"file conflict":            `{"controller":{"stateFile":"state.pb"},"etcd":{"enabled":true}}`,
		"endpoint conflict":        `{"controller":{"etcd":"http://remote:2379"},"etcd":{"enabled":true}}`,
		"endpoint port conflict":   `{"etcd":{"enabled":true,"clientPort":2380}}`,
		"controller port conflict": `{"controller":{"listen":":2379"},"etcd":{"enabled":true}}`,
		"data root":                `{"etcd":{"enabled":true,"dataDir":"/"}}`,
		"data relative":            `{"etcd":{"enabled":true,"dataDir":"etcd"}}`,
		"negative timeout":         `{"etcd":{"startupTimeoutSeconds":-1}}`,
		"name separators":          `{"etcd":{"enabled":true,"name":"bad=name"}}`,
		"partial TLS":              `{"controller":{"tlsCert":"missing.pem"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Parse([]string{"-config", file(t, body), "-insecure"}, validEnv(), io.Discard)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestDiagnosticsDoNotExposeTokens(t *testing.T) {
	c, mode, err := Parse([]string{"-runtime-plan", "-insecure"}, validEnv(), io.Discard)
	if err != nil || !mode.Plan {
		t.Fatal(err)
	}
	data, err := json.Marshal(c.Plan())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), admin) || strings.Contains(string(data), agent) || strings.Contains(string(data), "Token") {
		t.Fatal("runtime plan leaked token fields")
	}
	_, _, err = Parse([]string{"-insecure"}, env(map[string]string{"SDN_ADMIN_TOKEN": "secret-short", "SDN_AGENT_TOKEN": agent}), io.Discard)
	if err == nil || strings.Contains(err.Error(), "secret-short") {
		t.Fatal("invalid token error leaked its value")
	}
	_, _, err = Parse([]string{"-insecure", "-runtime-plan", "-check-config"}, validEnv(), io.Discard)
	if err == nil {
		t.Fatal("ambiguous diagnostic modes accepted")
	}
}

func TestBoundedFileAndUnexpectedArguments(t *testing.T) {
	_, _, err := Parse([]string{"-config", file(t, strings.Repeat(" ", (1<<20)+1)), "-insecure"}, validEnv(), io.Discard)
	if err == nil {
		t.Fatal("oversized file accepted")
	}
	_, _, err = Parse([]string{"-insecure", "unexpected"}, validEnv(), io.Discard)
	if err == nil {
		t.Fatal("positional argument accepted")
	}
}
