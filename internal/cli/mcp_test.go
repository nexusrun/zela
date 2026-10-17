package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStdioUsesProfileAndKeepsProtocolClean(t *testing.T) {
	_, h := demoHandler(t)
	r, _, stderr, path := runner(t, h)
	// Save a normal profile first: the MCP process reuses the same credentials.
	tokenFile := privateFile(t, "token", adminToken)
	if err := r.Run(context.Background(), []string{"config", "set", "mcp", "--url", "http://127.0.0.1:8080", "--token-file", tokenFile}); err != nil {
		t.Fatal(err)
	}
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	r.In = toServer
	r.Out = fromServer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx, []string{"--config", path, "--profile", "mcp", "mcp", "serve", "--read-only"})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: toClient, Writer: fromClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 7 {
		t.Fatalf("stdio catalog: %v %+v", err, list)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cluster_overview", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("stdio read failed: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), "workers") {
		t.Fatal("missing worker data")
	}
	_ = session.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stdio server did not exit on disconnect")
	}
	if strings.Contains(stderr.String(), adminToken) {
		t.Fatal("token leaked in diagnostics")
	}
}

func TestMCPHelpAndStdinCredentialRejectionAreLocal(t *testing.T) {
	r, out, stderr, _ := runner(t, http.NotFoundHandler())
	r.Env = func(k string) string {
		if k == "ZELA_CONFIG" {
			return filepath.Join(t.TempDir(), "missing.json")
		}
		return ""
	}
	if err := r.Run(context.Background(), []string{"mcp", "serve", "--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "read-only") {
		t.Fatal("missing MCP help")
	}
	if err := r.Run(context.Background(), []string{"mcp", "serve", "--token-file", "-"}); err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatal("MCP consumed stdin as a token")
	}
	if out.Len() != 0 {
		t.Fatal("protocol stdout was polluted by setup")
	}
}
