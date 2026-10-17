package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zela-dev/zela/internal/platformclient"
)

func TestPlainHTTPTokenOnlyToLoopback(t *testing.T) {
	if _, err := platformclient.New(platformclient.Config{URL: "http://cluster.example", Token: "t"}); err == nil {
		t.Fatal("expected token over remote plain HTTP to be refused")
	}
	for _, u := range []string{"http://127.0.0.1:8080", "http://localhost", "http://[::1]:1"} {
		if _, err := platformclient.New(platformclient.Config{URL: u, Token: "t"}); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if _, err := platformclient.New(platformclient.Config{URL: "http://cluster.example"}); err != nil {
		t.Fatalf("tokenless health checks should still work: %v", err)
	}
}

func TestConfigSetIgnoresAmbientToken(t *testing.T) {
	r, out, _, path := runner(t, http.NotFoundHandler())
	r.Env = func(k string) string {
		switch k {
		case "ZELA_CONFIG":
			return path
		case "ZELA_TOKEN":
			return "ambient-token-must-not-be-saved"
		}
		return ""
	}
	runOK(t, r, out, "config", "set", "prod", "--url", "https://cluster.example")
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "ambient-token") {
		t.Fatalf("config set persisted ZELA_TOKEN: %s", b)
	}
}

func TestCommandFlagValuesAreNotGlobalOrHelp(t *testing.T) {
	var body map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"id":"x"}`))
	})
	r, out, _, _ := runner(t, h)
	r.Env = func(k string) string {
		switch k {
		case "ZELA_URL":
			return "https://cluster.example"
		case "ZELA_TOKEN":
			return adminToken
		case "ZELA_CONFIG":
			return filepath.Join(t.TempDir(), "c.json")
		}
		return ""
	}
	if err := r.Run(context.Background(), []string{"secrets", "create", "--name", "--json", "--key-file", "-"}); err != nil && !strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	out.Reset()
	runOK(t, r, out, "workers", "add", "--name", "-h", "--endpoint", "https://w.example")
	if body["name"] != "-h" {
		t.Fatalf("name = %v", body["name"])
	}
	out.Reset()
	runOK(t, r, out, "deployments", "stop", "d1")
	if _, ok := body["target"]; ok {
		t.Fatalf("stop sent target: %v", body)
	}
	if _, ok := body["replicas"]; ok {
		t.Fatalf("stop sent replicas: %v", body)
	}
}

func TestContainersListSortedByWorkerAndID(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte(`{"workers":{"w2":{"Name":"b","Containers":[{"Id":"c"}]},"w1":{"Name":"a","Containers":[{"Id":"z"},{"Id":"a"}]}}}`))
	})
	r, out, _, _ := runner(t, h)
	r.Env = func(k string) string {
		switch k {
		case "ZELA_URL":
			return "https://cluster.example"
		case "ZELA_TOKEN":
			return adminToken
		case "ZELA_CONFIG":
			return filepath.Join(t.TempDir(), "c.json")
		}
		return ""
	}
	for i := 0; i < 20; i++ {
		out.Reset()
		if err := r.Run(context.Background(), []string{"containers", "list", "--json"}); err != nil {
			t.Fatal(err)
		}
		var rows []struct{ Id string }
		json.Unmarshal(out.Bytes(), &rows)
		if len(rows) != 3 || rows[0].Id != "a" || rows[1].Id != "z" || rows[2].Id != "c" {
			t.Fatalf("order: %s", out)
		}
	}
}

func TestNotificationsReadAllEmpty(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte(`{"notifications":[{"id":"n1","readAt":"2026-01-01T00:00:00Z"}]}`))
	})
	r, out, _, _ := runner(t, h)
	r.Env = func(k string) string {
		switch k {
		case "ZELA_URL":
			return "https://cluster.example"
		case "ZELA_TOKEN":
			return adminToken
		case "ZELA_CONFIG":
			return filepath.Join(t.TempDir(), "c.json")
		}
		return ""
	}
	runOK(t, r, out, "notifications", "read", "--all")
	if !strings.Contains(out.String(), `"read": 0`) {
		t.Fatalf("got %s", out)
	}
}
