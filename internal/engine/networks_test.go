package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type networkTestTransport func(*http.Request) (*http.Response, error)

func (fn networkTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestDockerNetworkEnginePathsAndPayloads(t *testing.T) {
	for _, origin := range []string{"http://docker", "https://agent.example"} {
		t.Run(origin, func(t *testing.T) {
			prefix := ""
			if origin != "http://docker" {
				prefix = "/docker"
			}
			client := &Client{URL: origin, Token: "worker-secret", HTTP: &http.Client{Transport: networkTestTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.HasPrefix(r.URL.Path, prefix+"/networks") {
					t.Fatalf("incorrect network path %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer worker-secret" {
					t.Fatal("missing worker auth")
				}
				result := `{}`
				if r.Method == "GET" {
					result = `{"Id":"net1","Name":"app","Driver":"bridge","Scope":"local","IPAM":{"Config":[{"Subnet":"172.28.0.0/24","Gateway":"172.28.0.1"}]},"Containers":{"ct1":{"Name":"app-1","IPv4Address":"172.28.0.2/24"}}}`
					if r.URL.Path == prefix+"/networks" {
						result = "[" + result + "]"
					}
				}
				if r.Method == "POST" {
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Fatal("invalid request body")
					}
					if strings.HasSuffix(r.URL.Path, "/create") {
						if body["Driver"] != "bridge" || body["Name"] != "app" || body["Internal"] != true {
							t.Fatal(body)
						}
						result = `{"Id":"net1"}`
					} else if body["Container"] != "ct1" || body["Force"] == true {
						t.Fatal("unsafe network attachment payload")
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(result))}, nil
			})}}
			ctx := context.Background()
			nets, err := client.Networks(ctx)
			if err != nil || len(nets) != 1 || nets[0].ID != "net1" || nets[0].IPAM.Config[0].Subnet != "172.28.0.0/24" {
				t.Fatalf("decode networks: %+v %v", nets, err)
			}
			network, err := client.InspectNetwork(ctx, "net1")
			if err != nil || network.Containers["ct1"].IPv4Address != "172.28.0.2/24" {
				t.Fatal(network, err)
			}
			id, err := client.CreateNetwork(ctx, NetworkCreate{Name: "app", Internal: true})
			if err != nil || id != "net1" {
				t.Fatal(id, err)
			}
			if err = client.ConnectNetwork(ctx, "net1", "ct1", false); err != nil {
				t.Fatal(err)
			}
			if err = client.ConnectNetwork(ctx, "net1", "ct1", true); err != nil {
				t.Fatal(err)
			}
			if err = client.RemoveNetwork(ctx, "net1"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
