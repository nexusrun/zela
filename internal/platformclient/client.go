// Package platformclient connects to the control-plane HTTP API. It has no
// dependency on local cluster state, Docker sockets, or worker connections.
package platformclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const MaxResponseBytes = 32 << 20

type Config struct {
	URL     string
	Token   string
	CACert  string
	Timeout time.Duration
	// UserAgent defaults to "zela".
	UserAgent string
	// Transport is optional for embedding and tests. The caller owns its TLS policy.
	Transport http.RoundTripper
}
type Client struct {
	base      *url.URL
	token     string
	userAgent string
	http      *http.Client
}
type RequestError struct{ Message string }

func (e *RequestError) Error() string { return e.Message }

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("control plane returned HTTP %d: %s", e.Status, e.Message)
}

func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("endpoint must be an HTTP(S) origin, without credentials, path, query, or fragment")
	}
	u.Path = ""
	return u, nil
}
func New(cfg Config) (*Client, error) {
	u, err := ParseURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(cfg.Token, "\r\n\x00") {
		return nil, fmt.Errorf("invalid token")
	}
	// Bearer tokens must not cross the network in cleartext.
	if cfg.Token != "" && u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("refusing to send a token over plain HTTP to a non-loopback host; use https")
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "zela"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACert != "" {
		pem, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	var rt http.RoundTripper = transport
	if cfg.Transport != nil {
		if cfg.CACert != "" {
			return nil, fmt.Errorf("CA configuration cannot be combined with a custom transport")
		}
		rt = cfg.Transport
	}
	return &Client{base: u, token: cfg.Token, userAgent: cfg.UserAgent, http: &http.Client{Transport: rt, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Do does not retry mutations: a lost response can mean an operation was accepted.
// The returned JSON retains numbers exactly, including Docker nanosecond timestamps.
func (c *Client) Do(ctx context.Context, method, path string, body json.RawMessage) (json.RawMessage, error) {
	p, err := url.Parse(path)
	if err != nil || p == nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || p.IsAbs() || p.Host != "" || p.Fragment != "" {
		return nil, fmt.Errorf("API path must be relative to the cluster endpoint and start with /")
	}
	u := *c.base
	u.Path = p.Path
	u.RawPath = p.RawPath
	u.RawQuery = p.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid API request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("request failed: %s (requests are not automatically retried)", redact(err.Error(), append(secretValues(body), c.token)))}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("read response: %s", redact(err.Error(), append(secretValues(body), c.token)))}
	}
	if len(data) > MaxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", MaxResponseBytes)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := http.StatusText(res.StatusCode)
		var detail struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &detail) == nil {
			if detail.Error != "" {
				message = detail.Error
			} else if detail.Message != "" {
				message = detail.Message
			}
		}
		if res.StatusCode == 401 {
			message += "; check the configured token"
		}
		if res.StatusCode == 403 {
			message += "; this command may require the administrator token"
		}
		if res.StatusCode == 503 {
			message += "; active manager unavailable; check readiness before retrying"
		}
		return nil, &APIError{Status: res.StatusCode, Message: redact(message, append(secretValues(body), c.token))}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("expected a JSON response; check the cluster endpoint")
	}
	return data, nil
}
func secretValues(body []byte) []string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				switch strings.ToLower(k) {
				case "token", "privatekey", "passphrase", "password":
					if s, ok := x.(string); ok && s != "" {
						out = append(out, s)
					}
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}
func redact(s string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
