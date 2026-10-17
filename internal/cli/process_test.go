package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in executable-level smoke test. It starts an isolated demo manager, never
// reads real cluster credentials, and never accesses Docker sockets or SSH hosts.
func TestCLIProcessesOverHTTP(t *testing.T) {
	manager, cli := os.Getenv("ZELA_TEST_MANAGER_BINARY"), os.Getenv("ZELA_TEST_CLI_BINARY")
	if manager == "" || cli == "" {
		t.Skip("set ZELA_TEST_MANAGER_BINARY and ZELA_TEST_CLI_BINARY to built binaries for an isolated process smoke test")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	root := t.TempDir()
	config := filepath.Join(root, "client", "config.json")
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret[:])
	tokenFile := filepath.Join(root, "admin-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	cmd := exec.Command(manager, "--demo", "--listen", addr, "--data", filepath.Join(root, "manager"))
	cmd.Env = append(cleanCLIProcessEnv(), "ZELA_ADMIN_TOKEN="+token)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("manager failed to stop")
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	env := append(cleanCLIProcessEnv(), "ZELA_CONFIG="+config)
	run := func(args ...string) ([]byte, error) {
		p := exec.Command(cli, args...)
		p.Env = env
		return p.CombinedOutput()
	}
	url := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := run("health", "--url", url, "--json"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager never became healthy", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	mustRun := func(args ...string) []byte {
		t.Helper()
		b, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, b)
		}
		return b
	}
	mustRun("workers", "provision", "--help")
	mustRun("login", "--profile", "isolated", "--url", url, "--token-file", tokenFile, "--json")
	b := mustRun("workers", "list", "--json")
	var workers []struct{ ID string }
	if json.Unmarshal(b, &workers) != nil || len(workers) != 3 {
		t.Fatal("wrong workers", string(b))
	}
	mustRun("workers", "pause", workers[0].ID, "--wait", "--json")
	mustRun("workers", "activate", workers[0].ID, "--wait", "--json")
	mustRun("workers", "metrics", workers[0].ID, "--window", "1h", "--json")
	scopedFile := filepath.Join(root, "integration-token")
	b = mustRun("tokens", "create", "--name", "process-test", "--scope", "infrastructure", "--out", scopedFile, "--json")
	var creation struct{ Metadata struct{ ID string } }
	if json.Unmarshal(b, &creation) != nil || creation.Metadata.ID == "" {
		t.Fatal("wrong token metadata", string(b))
	}
	raw, _ := os.ReadFile(scopedFile)
	if bytes.Contains(b, bytes.TrimSpace(raw)) {
		t.Fatal("token file option leaked token")
	}
	mustRun("workers", "list", "--token-file", scopedFile, "--json")
	if b, err := run("workers", "pause", workers[0].ID, "--token-file", scopedFile); err == nil || !bytes.Contains(b, []byte("403")) {
		t.Fatal("scoped CLI administration succeeded", err, string(b))
	}
	mustRun("tokens", "revoke", creation.Metadata.ID, "--json")
	if b, err := run("status", "--token-file", scopedFile); err == nil || !bytes.Contains(b, []byte("401")) {
		t.Fatal("revoked CLI credential succeeded", err, string(b))
	}
	mustRun("notifications", "list", "--json")
	mustRun("notifications", "read", "--all", "--json")
	mustRun("refresh", "--wait", "--json")
	mustRun("containers", "list", "--json")
	mustRun("operations", "list", "--json")
	if b, err := run("workers", "remove", workers[0].ID, "--wait", "--json"); err == nil || !bytes.Contains(b, []byte(`"failed"`)) {
		t.Fatal("failed asynchronous job did not produce a failing process exit", err, string(b))
	}
	b = mustRun("config", "list", "--json")
	if bytes.Contains(b, []byte(token)) {
		t.Fatal("profile list exposed admin token")
	}
	t.Logf("Verified compiled CLI against isolated manager over HTTP at %s: profiles, workers, jobs, scoped token denial/revocation, notifications and failed-job exit codes", url)
}
func cleanCLIProcessEnv() []string {
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "ZELA_") || strings.HasPrefix(key, "PG") || key == "ZELA_CONFIG" {
			continue
		}
		env = append(env, v)
	}
	return env
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return fmt.Sprint(&b.buf) }
