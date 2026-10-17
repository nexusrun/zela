package dockerapi

import "testing"

func TestEnginePathsAndGatewayDispatch(t *testing.T) {
	for _, path := range []string{"/_ping", "/version", "/v1.51/info", "/v1.44/containers/create", "/exec/id/start", "/images/registry/repository:tag/json", "/networks", "/volumes/create", "/build", "/session", "/events", "/system/df", "/swarm/init"} {
		if _, ok := EnginePath(path); !ok {
			t.Errorf("Docker path rejected: %s", path)
		}
		if !IsGatewayRequest(path) {
			t.Errorf("Docker path bypasses gateway: %s", path)
		}
	}
	for _, path := range []string{"/api/state", "/zela/metrics", "/app.js", "/containers/../info", "/v1.51/containers/../../api/state", "/containers//create", "/containers/one\\two", "/unknown", "/v1.51/not-a-route"} {
		if _, ok := EnginePath(path); ok {
			t.Errorf("invalid path accepted: %s", path)
		}
	}
	for _, path := range []string{"/docker/workers/w1/info", "/containers/../api/state", "/v1.51/not-a-route", "/containers//create"} {
		if !IsGatewayRequest(path) {
			t.Errorf("unsafe path escaped authentication: %s", path)
		}
	}
	if p, ok := EnginePath("/v1.51/containers/id/json"); !ok || p != "/containers/id/json" {
		t.Fatal("version validation changed route")
	}
}
