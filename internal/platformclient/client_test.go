package platformclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestEndpointValidation(t *testing.T) {
	for _, u := range []string{"unix:///var/run/docker.sock", "ssh://host", "https://user:secret@host", "https://host/api", "https://host?token=secret", "https://host#token", "https://", "//host"} {
		if _, err := New(Config{URL: u}); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	for _, u := range []string{"https://cluster.example:8443", "http://127.0.0.1:8080/", "https://[2001:db8::1]:8443"} {
		c, err := New(Config{URL: u})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	c, err := New(Config{URL: "https://cluster.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tr := c.http.Transport.(*http.Transport)
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
}
func TestClientAuthenticationAndJSONNumbers(t *testing.T) {
	const token = "a-private-token-not-in-the-url"
	calls := 0
	c, err := New(Config{URL: "https://cluster.example", Token: token, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://cluster.example/api/workers" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("wrong remote request")
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"name":"worker"}` {
			t.Fatal(string(b))
		}
		return response(202, `{"id":"op_a","size":9223372036854775807}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, err := c.Do(context.Background(), "POST", "/api/workers", json.RawMessage(`{"name":"worker"}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(b), "9223372036854775807") {
		t.Fatal("request repeated or number changed")
	}
	for _, p := range []string{"https://other.example/api", "//other.example/api", "api/state", "/api#fragment"} {
		if _, err := c.Do(context.Background(), "GET", p, nil); err == nil {
			t.Errorf("accepted %s", p)
		}
	}
	if calls != 1 {
		t.Fatal("invalid path reached transport")
	}
}
func TestClientRedirectAndErrorRedaction(t *testing.T) {
	calls := 0
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		res := response(307, "")
		res.Header.Set("Location", "https://untrusted.example/api")
		return res, nil
	})
	c, _ := New(Config{URL: "https://cluster.example", Token: "sensitive-admin", Transport: tr})
	defer c.Close()
	_, err := c.Do(context.Background(), "POST", "/api/workers", nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 307 || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(400, `{"error":"sensitive-admin private-key-material passphrase"}`), nil
	})
	_, err = c.Do(context.Background(), "POST", "/api/secrets", json.RawMessage(`{"privateKey":"private-key-material","passphrase":"passphrase"}`))
	if err == nil || strings.Contains(err.Error(), "sensitive-admin") || strings.Contains(err.Error(), "private-key-material") || strings.Contains(err.Error(), "passphrase") {
		t.Fatal("secret leaked", err)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(503, `{"error":"standby"}`), nil })
	_, err = c.Do(context.Background(), "GET", "/api/state", nil)
	if !errors.As(err, &api) || api.Status != 503 {
		t.Fatal(err)
	}
}
func TestClientTimeoutNeverRepeatsMutation(t *testing.T) {
	calls := 0
	c, _ := New(Config{URL: "https://cluster.example", Timeout: 5 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	defer c.Close()
	_, err := c.Do(context.Background(), "POST", "/api/workers", json.RawMessage(`{}`))
	var req *RequestError
	if !errors.As(err, &req) || calls != 1 {
		t.Fatal("timeout or mutation retried", err, calls)
	}
}
