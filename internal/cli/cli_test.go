package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/control"
)

type handlerTransport struct{ h http.Handler }

func (tr handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	tr.h.ServeHTTP(w, r)
	res := w.Result()
	res.Request = r
	return res, nil
}
func runner(t *testing.T, h http.Handler) (*Runner, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.json")
	r := &Runner{In: strings.NewReader(""), Out: &out, Err: &stderr, Env: func(k string) string {
		if k == "ZELA_CONFIG" {
			return path
		}
		return ""
	}, transport: handlerTransport{h}, pollInterval: time.Millisecond}
	return r, &out, &stderr, path
}

const adminToken = "cli-test-administrator-token-at-least-32-characters"

func demoHandler(t *testing.T) (*control.Controller, http.Handler) {
	t.Helper()
	s, err := control.OpenStore(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := control.DemoFactory(s)
	if err != nil {
		t.Fatal(err)
	}
	c := control.New(s, factory)
	t.Cleanup(c.Close)
	c.Refresh(context.Background())
	return c, control.Handler(c, control.HTTPConfig{Token: adminToken, AllowHTTPWorkers: true})
}
func privateFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func runOK(t *testing.T, r *Runner, out *bytes.Buffer, args ...string) json.RawMessage {
	t.Helper()
	out.Reset()
	if err := r.Run(context.Background(), args); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return append([]byte(nil), out.Bytes()...)
}
func TestCLIPlatformLifecycleAndScopedPermissions(t *testing.T) {
	_, h := demoHandler(t)
	r, out, stderr, path := runner(t, h)
	tokenFile := privateFile(t, "admin", adminToken)
	runOK(t, r, out, "login", "--url", "https://cluster.example", "--token-file", tokenFile, "--profile", "prod", "--json")
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal("profile is not private")
	}
	runOK(t, r, out, "config", "list", "--json")
	if bytes.Contains(out.Bytes(), []byte(adminToken)) {
		t.Fatal("profile list leaked token")
	}
	b := runOK(t, r, out, "workers", "list", "--json")
	var workers []control.InfrastructureWorker
	if json.Unmarshal(b, &workers) != nil || len(workers) != 3 {
		t.Fatal(string(b))
	}
	b = runOK(t, r, out, "workers", "pause", workers[0].ID, "--wait", "--json")
	var op control.Operation
	if json.Unmarshal(b, &op) != nil || op.Status != "succeeded" {
		t.Fatal(string(b))
	}
	b = runOK(t, r, out, "workers", "get", workers[0].ID, "--json")
	var w control.InfrastructureWorker
	json.Unmarshal(b, &w)
	if w.Mode != "paused" {
		t.Fatal("worker was not paused")
	}
	runOK(t, r, out, "workers", "activate", workers[0].ID, "--wait", "--json")
	tokenOut := filepath.Join(t.TempDir(), "nexus-ai-token")
	b = runOK(t, r, out, "tokens", "create", "--name", "nexus-ai", "--scope", "infrastructure", "--expires-days", "7", "--out", tokenOut, "--json")
	var creation struct{ Metadata struct{ ID string } }
	if json.Unmarshal(b, &creation) != nil || creation.Metadata.ID == "" {
		t.Fatal(string(b))
	}
	raw, err := os.ReadFile(tokenOut)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), bytes.TrimSpace(raw)) {
		t.Fatal("saved token printed")
	}
	st, _ = os.Stat(tokenOut)
	if st.Mode().Perm() != 0600 {
		t.Fatal("token output permissions")
	}
	runOK(t, r, out, "status", "--token-file", tokenOut, "--json")
	if err := r.Run(context.Background(), []string{"workers", "pause", workers[0].ID, "--token-file", tokenOut}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("scoped token could administer", err)
	}
	runOK(t, r, out, "tokens", "revoke", creation.Metadata.ID, "--json")
	if err := r.Run(context.Background(), []string{"workers", "list", "--token-file", tokenOut}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("revoked token accepted", err)
	}
	for _, args := range [][]string{{"operations", "list", "--json"}, {"operations", "wait", op.ID, "--json"}, {"workers", "metrics", workers[0].ID, "--window", "6h", "--json"}, {"notifications", "list", "--json"}, {"notifications", "read", "--all", "--json"}, {"monitoring", "rules", "--json"}, {"alerts", "list", "--json"}, {"containers", "list", "--json"}, {"deployments", "list", "--json"}, {"refresh", "--wait", "--json"}} {
		runOK(t, r, out, args...)
	}
	b = runOK(t, r, out, "containers", "list", "--worker", workers[0].ID, "--json")
	if !bytes.Contains(b, []byte(`"Id"`)) || bytes.Contains(b, []byte("sensitive")) {
		t.Fatal(string(b))
	}
	runOK(t, r, out, "logout", "--profile", "prod", "--json")
	cfg, err := loadConfig(path)
	if err != nil || cfg.Profiles["prod"].Token != "" {
		t.Fatal("logout retained token", err)
	}
	if strings.Contains(stderr.String(), adminToken) {
		t.Fatal("stderr leaked token")
	}
}
func TestCLIWorkerProvisioningAndVault(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("OpenSSH is required for vault integration")
	}
	c, h := demoHandler(t)
	r, out, _, _ := runner(t, h)
	token := privateFile(t, "admin", adminToken)
	runOK(t, r, out, "login", "--url", "https://cluster.example", "--token-file", token)
	key := filepath.Join(t.TempDir(), "id_ed25519")
	if err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).Run(); err != nil {
		t.Fatal(err)
	}
	b := runOK(t, r, out, "secrets", "create", "--name", "rpm-workers", "--key-file", key, "--json")
	var meta control.SecretMetadata
	if json.Unmarshal(b, &meta) != nil || meta.ID == "" {
		t.Fatal(string(b))
	}
	keyData, _ := os.ReadFile(key)
	if bytes.Contains(b, keyData) {
		t.Fatal("private key in output")
	}
	runOK(t, r, out, "workers", "provision", "--name", "rhel-worker", "--host", "192.0.2.20", "--secret", meta.ID, "--host-key-file", key+".pub", "--user", "root", "--wait", "--json")
	runOK(t, r, out, "secrets", "list", "--json")
	if len(c.Store.Vault.List()) != 1 {
		t.Fatal("key not in control-plane vault")
	}
	b = runOK(t, r, out, "workers", "list", "--json")
	var ws []control.InfrastructureWorker
	json.Unmarshal(b, &ws)
	var id string
	for _, w := range ws {
		if w.Name == "rhel-worker" {
			id = w.ID
		}
	}
	if id == "" {
		t.Fatal("provisioned worker missing")
	}
	runOK(t, r, out, "workers", "update-agent", id, "--wait", "--json")
	runOK(t, r, out, "workers", "remove", id, "--wait", "--json")
	runOK(t, r, out, "secrets", "remove", meta.ID, "--json")
	if len(c.Store.Vault.List()) != 0 {
		t.Fatal("secret not removed")
	}
}
func TestCLIProfilePrivacyAndOriginChanges(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host == "other.example" && r.Header.Get("Authorization") != "" {
			t.Fatal("profile credential forwarded to a changed endpoint")
		}
		fmt.Fprint(w, `{"workers":[]}`)
	})
	r, out, _, path := runner(t, h)
	token := privateFile(t, "token", adminToken)
	runOK(t, r, out, "config", "set", "prod", "--url", "https://cluster.example", "--token-file", token, "--json")
	if calls != 0 {
		t.Fatal("offline configuration used network")
	}
	if err := r.Run(context.Background(), []string{"status", "--url", "https://other.example"}); err == nil {
		t.Fatal("changed origin inherited token")
	}
	runOK(t, r, out, "health", "--url", "https://other.example", "--json")
	runOK(t, r, out, "config", "set", "prod", "--url", "https://other.example", "--json")
	cfg, _ := loadConfig(path)
	if cfg.Profiles["prod"].Token != "" {
		t.Fatal("changed profile retained old endpoint token")
	}
	os.Chmod(path, 0644)
	if err := r.Run(context.Background(), []string{"config", "list"}); err == nil {
		t.Fatal("accepted exposed credentials file")
	}
	os.Chmod(path, 0600)
	os.Chmod(token, 0644)
	if err := r.Run(context.Background(), []string{"login", "--url", "https://cluster.example", "--token-file", token}); err == nil {
		t.Fatal("accepted exposed token input")
	}
}
func TestCLIWaitFailureTimeoutAndTakeover(t *testing.T) {
	for _, scenario := range []string{"failure", "timeout", "takeover"} {
		t.Run(scenario, func(t *testing.T) {
			mutations, polls := 0, 0
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					mutations++
					w.WriteHeader(202)
					fmt.Fprint(w, `{"id":"op_test","status":"running"}`)
					return
				}
				polls++
				if scenario == "takeover" && polls == 1 {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":"standby"}`)
					return
				}
				status := "running"
				if scenario == "failure" {
					status = "failed"
				}
				if scenario == "takeover" {
					status = "succeeded"
				}
				fmt.Fprintf(w, `{"id":"op_test","status":%q,"detail":"job details"}`, status)
			})
			r, out, _, _ := runner(t, h)
			r.Env = func(k string) string {
				switch k {
				case "ZELA_URL":
					return "https://cluster.example"
				case "ZELA_TOKEN":
					return adminToken
				case "ZELA_CONFIG":
					return filepath.Join(t.TempDir(), "config.json")
				}
				return ""
			}
			err := r.Run(context.Background(), []string{"refresh", "--wait", "--wait-timeout", "10ms", "--json"})
			if mutations != 1 {
				t.Fatal("mutation repeated")
			}
			if scenario == "takeover" {
				if err != nil || polls < 2 {
					t.Fatal(err, polls)
				}
			} else if err == nil {
				t.Fatal("failure had successful exit")
			}
			if scenario == "failure" && !strings.Contains(out.String(), `"failed"`) {
				t.Fatal("failed operation not printed")
			}
			if scenario == "timeout" && !strings.Contains(err.Error(), "op_test") {
				t.Fatal("timeout omitted accepted operation ID")
			}
		})
	}
}
func TestCLIInputValidationAndJSONOutput(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":"op_test","size":9223372036854775807}`)
	})
	r, out, _, _ := runner(t, h)
	r.Env = func(k string) string {
		if k == "ZELA_URL" {
			return "https://cluster.example"
		}
		if k == "ZELA_TOKEN" {
			return adminToken
		}
		if k == "ZELA_CONFIG" {
			return filepath.Join(t.TempDir(), "config.json")
		}
		return ""
	}
	for _, a := range [][]string{{"workers", "pause"}, {"workers", "pause", "x", "--unknown"}, {"workers", "metrics", "x", "--window", "2h"}, {"tokens", "create", "--name", "x", "--scope", "admin"}, {"workers", "provision", "--file", "x", "--host", "y"}, {"status", "--timeout", "0s"}, {"api", "GET", "https://other.example/api"}} {
		if err := r.Run(context.Background(), a); err == nil {
			t.Errorf("accepted %v", a)
		}
	}
	if calls != 0 {
		t.Fatal("invalid command reached server")
	}
	runOK(t, r, out, "api", "GET", "/api/state", "--json")
	if !strings.Contains(out.String(), "9223372036854775807") {
		t.Fatal("integer precision lost")
	}
	if err := r.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
}
func TestCLIReadPollingRetriesTransportLoss(t *testing.T) {
	count := 0
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		count++
		if count == 1 {
			return nil, io.EOF
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"op_test","status":"succeeded"}`))}, nil
	})
	r, out, _, _ := runner(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r.transport = transport
	r.Env = func(k string) string {
		if k == "ZELA_URL" {
			return "https://cluster.example"
		}
		if k == "ZELA_TOKEN" {
			return adminToken
		}
		if k == "ZELA_CONFIG" {
			return filepath.Join(t.TempDir(), "config.json")
		}
		return ""
	}
	// The initial get is deliberately successful; only polling may retry lost responses.
	transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		count++
		switch count {
		case 1:
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"op_test","status":"running"}`))}, nil
		case 2:
			return nil, errors.New("connection lost")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"op_test","status":"succeeded"}`))}, nil
	})
	r.transport = transport
	runOK(t, r, out, "operations", "wait", "op_test", "--wait-timeout", "1s", "--json")
	if count != 3 {
		t.Fatal(count)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCLIHelpDoesNotRequireConnection(t *testing.T) {
	r, out, stderr, _ := runner(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("help contacted a server") }))
	// Even an unreadable configuration must not prevent local help.
	r.Env = func(k string) string {
		if k == "ZELA_CONFIG" {
			return "/no-such-directory/config.json"
		}
		return ""
	}
	for _, args := range [][]string{{"workers", "provision", "--help"}, {"tokens", "create", "-h"}, {"workers", "--help"}, {"config", "set", "prod", "--help"}} {
		out.Reset()
		stderr.Reset()
		if err := r.Run(context.Background(), args); err != nil {
			t.Fatal(args, err)
		}
		if out.Len()+stderr.Len() == 0 {
			t.Fatal("help is empty")
		}
	}
	if err := r.Run(context.Background(), []string{"workers", "add", "--name", "--help", "--endpoint", "https://worker"}); err == nil {
		t.Fatal("help supplied as a name should fail validation")
	}
}
