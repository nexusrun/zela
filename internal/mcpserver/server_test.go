package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidID(t *testing.T) {
	for _, ok := range []string{"wrk_abc", "dep-1.2", "a"} {
		if validID(ok) != nil {
			t.Fatalf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "a?b", "-x", string(make([]byte, 200))} {
		if validID(bad) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := containerPath(containerArgs{"wrk_1", "../../api"}); err == nil {
		t.Fatal("container traversal accepted")
	}
}

func catalog(t *testing.T, readOnly bool) map[string]*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	server := New(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}, readOnly)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		out[tool.Name] = tool
	}
	return out
}

func TestCatalogSizeAndAnnotations(t *testing.T) {
	read, all := catalog(t, true), catalog(t, false)
	if len(read) != 7 || len(all) != 19 {
		t.Fatalf("catalog sizes: read %d, manage %d", len(read), len(all))
	}
	for name, tool := range read {
		if !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s in read catalog is not read-only", name)
		}
	}
	for _, name := range []string{"provision_worker", "deployment_action", "worker_action"} {
		if tool := all[name]; tool == nil || tool.Annotations.ReadOnlyHint || !*tool.Annotations.DestructiveHint {
			t.Fatalf("%s should be a destructive write tool", name)
		}
	}
	b, _ := json.Marshal(all["worker_action"].InputSchema)
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(b, &schema)
	if got := schema.Properties["action"].Enum; len(got) != 4 || got[0] != "pause" {
		t.Fatalf("worker_action enum: %v", got)
	}
}
