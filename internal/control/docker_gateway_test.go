package control

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
)

type gatewayRoundTripper func(*http.Request) (*http.Response, error)

func (f gatewayRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gatewayFixture(t *testing.T, rt http.RoundTripper) (*Controller, http.Handler) {
	t.Helper()
	c, _ := fixture(t)
	if err := c.Store.update(func(s *State) error {
		s.Demo = false
		s.Workers = map[string]Worker{
			"w1": {ID: "w1", Endpoint: "https://one:9091", Token: strings.Repeat("1", 32), Status: "online", Mode: "active"},
			"w2": {ID: "w2", Endpoint: "ssh://two", Token: strings.Repeat("2", 32), Status: "online", Mode: "paused", SSH: &sshclient.Config{Host: "two", AgentPort: 9091}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Factory = func(w Worker) (engine.Engine, error) {
		return &engine.Client{URL: "http://" + w.ID + ":9091", Token: w.Token, HTTP: &http.Client{Transport: rt}}, nil
	}
	return c, Handler(c, HTTPConfig{Token: testToken})
}

func gatewayResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestDockerGatewayRoutesAuthenticatesAndPreservesDockerProtocol(t *testing.T) {
	calls := 0
	_, h := gatewayFixture(t, gatewayRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "w2:9091" || r.URL.EscapedPath() != "/docker/v1.51/images/registry%2Frepository/json" || r.URL.RawQuery != "filters=%7B%7D" {
			t.Errorf("incorrect routing: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("2", 32) || r.Header.Get("Cookie") != "" || r.Header.Get("X-Zela-Worker") != "" || r.Header.Get("X-Registry-Auth") != "registry-credential" {
			t.Error("credentials or registry auth forwarded incorrectly")
		}
		resp := gatewayResponse(404, `{"message":"image not found"}`)
		resp.Header.Set("API-Version", "1.51")
		resp.Header.Set("Set-Cookie", "worker-cookie=private")
		return resp, nil
	}))
	for _, target := range []string{"/docker/workers/w2/v1.51/images/registry%2Frepository/json?filters=%7B%7D", "/v1.51/images/registry%2Frepository/json?filters=%7B%7D"} {
		r := httptest.NewRequest("GET", "http://control"+target, nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("X-Zela-Worker", "w2")
		r.Header.Set("Cookie", "zela_session=private")
		r.Header.Set("X-Registry-Auth", "registry-credential")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || w.Body.String() != `{"message":"image not found"}` || w.Header().Get("API-Version") != "1.51" || w.Header().Get("X-Zela-Worker") != "w2" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("Docker response altered: %d %s %v", w.Code, w.Body, w.Header())
		}
	}
	if calls != 2 {
		t.Fatal("requests not forwarded")
	}
}

func TestDockerGatewayRejectsMissingAmbiguousAndUnauthorizedTargets(t *testing.T) {
	c, cHandler := gatewayFixture(t, gatewayRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("invalid request reached worker")
		return gatewayResponse(200, "OK"), nil
	}))
	for _, v := range []struct {
		path, token, worker, origin string
		want                        int
	}{
		{"/_ping", "", "w1", "", 401},
		{"/networks/create", testToken, "", "", 501},
		{"/version", testToken, "unknown", "", 404},
		{"/docker/workers/w1/info", testToken, "w2", "", 400},
		{"/docker/workers/w1/containers/../info", testToken, "", "", 404},
		{"/containers//create", testToken, "w1", "", 404},
		{"/v1.51/unknown", testToken, "w1", "", 404},
		{"/docker/workers/%77%31/info", testToken, "", "", 400},
		{"/docker/workers/w1/info", testToken, "", "http://evil", 403},
		{"/docker/workers/w1/zela/metrics", testToken, "", "", 404},
	} {
		r := httptest.NewRequest("GET", "http://control"+v.path, nil)
		if v.token != "" {
			r.Header.Set("Authorization", "Bearer "+v.token)
		}
		if v.worker != "" {
			r.Header.Set("X-Zela-Worker", v.worker)
		}
		if v.origin != "" {
			r.Header.Set("Origin", v.origin)
		}
		w := httptest.NewRecorder()
		cHandler.ServeHTTP(w, r)
		if w.Code != v.want || !strings.Contains(w.Body.String(), `"message"`) {
			t.Errorf("%s got %d %s want %d", v.path, w.Code, w.Body, v.want)
		}
	}
	_ = c.Store.update(func(s *State) error { w := s.Workers["w1"]; w.Status = "provisioning"; s.Workers[w.ID] = w; return nil })
	r := httptest.NewRequest("GET", "http://control/docker/workers/w1/info", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	cHandler.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("provisioning worker used")
	}
	_ = c.Store.update(func(s *State) error { s.Demo = true; return nil })
	w = httptest.NewRecorder()
	cHandler.ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatal("demo gateway was not isolated")
	}
}

func TestDockerGatewayRequiresBearerAndSingleWorkerHeader(t *testing.T) {
	c, h := gatewayFixture(t, gatewayRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("invalid authentication reached worker")
		return gatewayResponse(200, "OK"), nil
	}))
	login := httptest.NewRequest("POST", "http://control/api/login", strings.NewReader(`{"token":"`+testToken+`"}`))
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, login)
	if lw.Code != 200 {
		t.Fatal("login fixture failed")
	}
	r := httptest.NewRequest("GET", "http://control/docker/workers/w1/_ping", nil)
	r.AddCookie(lw.Result().Cookies()[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("browser session granted full Docker access")
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Add("X-Zela-Worker", "w1")
	r.Header.Add("X-Zela-Worker", "w2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("ambiguous worker selectors accepted")
	}
	public := c.Store.public()
	if public.Workers["w1"].DockerAPI != "/docker/workers/w1" || public.Workers["w1"].Token != "" {
		t.Fatal("gateway discovery missing or credentials leaked")
	}
}

func TestDockerGatewayForwardsBodiesStreamingAndUnixSocketPaths(t *testing.T) {
	body := strings.Repeat("build-data", 200000)
	c, h := gatewayFixture(t, gatewayRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1.51/build" || r.Header.Get("Content-Type") != "application/x-tar" {
			t.Error("Unix Docker route or content type altered")
		}
		b, err := io.ReadAll(r.Body)
		if err != nil || string(b) != body {
			t.Error("build body truncated")
		}
		resp := gatewayResponse(200, "pull-progress\nnext-progress\n")
		resp.Header.Set("Content-Type", "application/json")
		resp.ContentLength = -1
		return resp, nil
	}))
	_ = c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Endpoint = "unix:///test/docker.sock"
		w.Token = ""
		s.Workers[w.ID] = w
		return nil
	})
	r := httptest.NewRequest("POST", "http://control/docker/workers/w1/v1.51/build", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/x-tar")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !w.Flushed || w.Body.String() != "pull-progress\nnext-progress\n" {
		t.Fatal("Docker stream buffered or lost")
	}
}

func TestDockerGatewayCancelsOnShutdownAndRedactsTransportErrors(t *testing.T) {
	var c *Controller
	c, h := gatewayFixture(t, gatewayRoundTripper(func(r *http.Request) (*http.Response, error) {
		c.cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			t.Error("gateway not canceled by manager shutdown")
		}
		return nil, errors.New("private-worker-token with internal transport details")
	}))
	r := httptest.NewRequest("GET", "http://control/docker/workers/w1/info", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 502 || strings.Contains(w.Body.String(), "private-worker-token") {
		t.Fatal("unsafe gateway error")
	}
}

type gatewayHijacker struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *gatewayHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestDockerGatewayBidirectionalExecUpgrade(t *testing.T) {
	backend, engineSide := net.Pipe()
	defer backend.Close()
	defer engineSide.Close()
	clientSide, gatewaySide := net.Pipe()
	defer clientSide.Close()
	defer gatewaySide.Close()
	_ = clientSide.SetDeadline(time.Now().Add(3 * time.Second))
	_ = engineSide.SetDeadline(time.Now().Add(3 * time.Second))
	_, h := gatewayFixture(t, gatewayRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/docker/v1.51/exec/exec-id/start" || r.Header.Get("Upgrade") != "tcp" {
			t.Error("upgrade request altered")
		}
		return &http.Response{StatusCode: 101, Status: "101 Switching Protocols", Header: http.Header{"Connection": []string{"Upgrade"}, "Upgrade": []string{"tcp"}}, Body: backend}, nil
	}))
	r := httptest.NewRequest("POST", "http://control/docker/workers/w1/v1.51/exec/exec-id/start", strings.NewReader(`{"Detach":false,"Tty":true}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "tcp")
	w := &gatewayHijacker{httptest.NewRecorder(), gatewaySide}
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, r) }()
	resp, err := http.ReadResponse(bufio.NewReader(clientSide), r)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade failed: %v", err)
	}
	echo := make(chan error, 1)
	go func() {
		b := make([]byte, 4)
		_, err := io.ReadFull(engineSide, b)
		if err == nil && string(b) != "ping" {
			err = errors.New("stdin corrupted")
		}
		if err == nil {
			_, err = engineSide.Write([]byte("pong"))
		}
		echo <- err
	}()
	if _, err := clientSide.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(clientSide, b); err != nil || string(b) != "pong" {
		t.Fatalf("stdout not proxied: %s %v", b, err)
	}
	if err := <-echo; err != nil {
		t.Fatal(err)
	}
	clientSide.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exec proxy did not close after disconnect")
	}
}
