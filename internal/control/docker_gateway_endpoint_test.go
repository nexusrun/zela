package control

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var dockerAPIVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// This test is a client of an already running cluster. It never constructs a
// control plane or worker, accesses Docker sockets, or changes worker settings.
func TestDockerGatewayLiveEndpoint(t *testing.T) {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("ZELA_TEST_CLUSTER_URL")), "/")
	if endpoint == "" {
		t.Skip("set ZELA_TEST_CLUSTER_URL, ZELA_TEST_ADMIN_TOKEN_FILE and ZELA_TEST_DOCKER_IMAGE for live endpoint testing; ZELA_TEST_WORKER_ID is optional")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("ZELA_TEST_CLUSTER_URL must be an HTTP(S) origin, without credentials, path, query, or fragment")
	}
	tokenFile, worker, image := os.Getenv("ZELA_TEST_ADMIN_TOKEN_FILE"), os.Getenv("ZELA_TEST_WORKER_ID"), os.Getenv("ZELA_TEST_DOCKER_IMAGE")
	if tokenFile == "" || image == "" {
		t.Fatal("set ZELA_TEST_ADMIN_TOKEN_FILE and ZELA_TEST_DOCKER_IMAGE (a Linux image with sh and sleep)")
	}
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal("cannot read ZELA_TEST_ADMIN_TOKEN_FILE")
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		t.Fatal("invalid control-plane admin token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	stateJSON := gatewayEndpointCall(t, client, endpoint, token, "", "GET", "/api/state", nil, 200)
	var state State
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		t.Fatal("invalid cluster state response")
	}
	if state.Demo {
		t.Fatal("the endpoint is running in demo mode; a real cluster is required")
	}
	if _, ok := state.Workers[worker]; worker != "" && !ok {
		t.Fatal("ZELA_TEST_WORKER_ID is not registered in this cluster")
	}
	runLiveGatewayLifecycle(t, client, endpoint, token, worker, image)
}
