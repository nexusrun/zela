package control

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/zela-dev/zela/internal/clusternet"
)

// The control plane runs ACME for the whole cluster: the CA may validate
// HTTP-01 against any worker, so challenges are pushed to every worker and the
// CA is asked to validate only after all online workers applied them. One
// certificate per host name is shared by all workers (keeping CA rate limits).
// Private keys are sealed with the vault key at rest and travel to workers
// over the authenticated agent channel.

const (
	LetsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	acmeRenewBefore      = 30 * 24 * time.Hour
	acmeRetryAfter       = time.Hour
	acmeKeyPurpose       = "acme-key:"
)

type ACMEConfig struct {
	Email     string
	Directory string
}

type acmeState struct {
	AccountKey string              `json:"accountKey,omitempty"` // sealed PEM
	AccountURL string              `json:"accountURL,omitempty"`
	Certs      map[string]acmeCert `json:"certs,omitempty"`
	Challenges map[string]string   `json:"challenges,omitempty"`
}
type acmeCert struct {
	CertPEM     string    `json:"certPEM,omitempty"`
	Key         string    `json:"key,omitempty"` // sealed PEM
	NotAfter    time.Time `json:"notAfter,omitempty"`
	Error       string    `json:"error,omitempty"`
	NextAttempt time.Time `json:"nextAttempt,omitempty"`
	Failures    int       `json:"failures,omitempty"`
}

// acmeBackoff waits 1, 3, 9, 27 minutes, then hourly, after failures.
func acmeBackoff(failures int) time.Duration {
	d := time.Minute
	for i := 1; i < failures && d < acmeRetryAfter; i++ {
		d *= 3
	}
	return min(d, acmeRetryAfter)
}

// ACMECertificateStatus is the public view; it never includes key material.
type ACMECertificateStatus struct {
	Host        string    `json:"host"`
	NotAfter    time.Time `json:"notAfter,omitempty"`
	Error       string    `json:"error,omitempty"`
	NextAttempt time.Time `json:"nextAttempt,omitempty"`
}

func acmePublic(a *acmeState) []ACMECertificateStatus {
	if a == nil {
		return nil
	}
	out := []ACMECertificateStatus{}
	for host, c := range a.Certs {
		out = append(out, ACMECertificateStatus{Host: host, NotAfter: c.NotAfter, Error: c.Error, NextAttempt: c.NextAttempt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// SetACME enables automatic certificates. The CA connects to port 80, so the
// edge HTTP port must be 80, and an HTTPS port is needed to use the result.
func (c *Controller) SetACME(cfg ACMEConfig) error {
	if cfg.Email == "" {
		c.acme = nil
		return nil
	}
	if !strings.Contains(cfg.Email, "@") || strings.ContainsAny(cfg.Email, " \t\r\n<>") {
		return fmt.Errorf("--acme-email must be an email address")
	}
	if cfg.Directory == "" {
		cfg.Directory = LetsEncryptDirectory
	}
	if !strings.HasPrefix(cfg.Directory, "https://") {
		return fmt.Errorf("--acme-directory must be an https URL")
	}
	s := c.Store.snapshot()
	if !networkRequired(s) || s.Network.EdgeHTTPPort != 80 || s.Network.EdgeHTTPSPort == 0 {
		return fmt.Errorf("ACME needs the cluster network with --edge-http-port 80 and an --edge-https-port")
	}
	c.acme = &cfg
	return nil
}

// acmeHosts returns exact (non-wildcard) route hosts of deployments with VIPs.
// Wildcards need DNS-01, which is not supported.
func acmeHosts(s State) []string {
	seen := map[string]bool{}
	for id, d := range s.Deployments {
		if _, ok := s.Network.Services[id]; !ok {
			continue
		}
		for _, r := range d.Routes {
			if r.Host != "" && !strings.HasPrefix(r.Host, "*.") {
				seen[r.Host] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// acmeLoop issues and renews certificates. It is cheap when nothing is due.
func (c *Controller) acmeLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		c.acmeRound(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Controller) acmeRound(ctx context.Context) {
	cfg := c.acme
	s := c.Store.snapshot()
	if cfg == nil || !networkRequired(s) {
		return
	}
	now := time.Now()
	for _, host := range acmeHosts(s) {
		var cur acmeCert
		if s.ACME != nil {
			cur = s.ACME.Certs[host]
		}
		if (cur.CertPEM != "" && cur.NotAfter.Sub(now) > acmeRenewBefore) || now.Before(cur.NextAttempt) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		octx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := c.obtainCertificate(octx, *cfg, host)
		cancel()
		if err != nil {
			log.Printf("acme %s: %v", host, err)
			_ = c.Store.update(func(s *State) error {
				a := acmeInit(s)
				x := a.Certs[host]
				x.Failures++
				x.Error, x.NextAttempt = err.Error(), time.Now().Add(acmeBackoff(x.Failures))
				a.Certs[host] = x
				return nil
			})
		} else {
			log.Printf("acme: issued certificate for %s", host)
		}
	}
}

func acmeInit(s *State) *acmeState {
	if s.ACME == nil {
		s.ACME = &acmeState{}
	}
	if s.ACME.Certs == nil {
		s.ACME.Certs = map[string]acmeCert{}
	}
	if s.ACME.Challenges == nil {
		s.ACME.Challenges = map[string]string{}
	}
	return s.ACME
}

func (c *Controller) acmeAccount(ctx context.Context, cfg ACMEConfig) (*acme.Client, error) {
	s := c.Store.snapshot()
	var key crypto.Signer
	if s.ACME != nil && s.ACME.AccountKey != "" {
		b, err := c.Store.Vault.Unseal("acme-account", s.ACME.AccountKey)
		if err != nil {
			return nil, err
		}
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("invalid ACME account key")
		}
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key = k
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, _ := x509.MarshalECPrivateKey(k)
		sealed, err := c.Store.Vault.Seal("acme-account", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
		if err != nil {
			return nil, err
		}
		if err := c.Store.update(func(s *State) error { acmeInit(s).AccountKey = sealed; return nil }); err != nil {
			return nil, err
		}
		key = k
	}
	client := &acme.Client{Key: key, DirectoryURL: cfg.Directory, UserAgent: "zela"}
	if s.ACME == nil || s.ACME.AccountURL == "" {
		acct, err := client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + cfg.Email}}, acme.AcceptTOS)
		if errors.Is(err, acme.ErrAccountAlreadyExists) {
			acct, err = client.GetReg(ctx, "")
		}
		if err != nil {
			return nil, fmt.Errorf("ACME account registration: %w", err)
		}
		if err := c.Store.update(func(s *State) error { acmeInit(s).AccountURL = acct.URI; return nil }); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (c *Controller) obtainCertificate(ctx context.Context, cfg ACMEConfig, host string) error {
	client, err := c.acmeAccount(ctx, cfg)
	if err != nil {
		return err
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(host))
	if err != nil {
		return fmt.Errorf("create order: %w", err)
	}
	// Only the new-order response is guaranteed to carry the order URL
	// (Location); later responses from this client may not repeat it.
	orderURI := order.URI
	var tokens []string
	defer func() {
		// Challenges are removed whether validation succeeded or not.
		if len(tokens) > 0 {
			_ = c.Store.update(func(s *State) error {
				for _, t := range tokens {
					delete(acmeInit(s).Challenges, t)
				}
				return nil
			})
			c.pushNetwork(context.Background())
		}
	}()
	for _, u := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "http-01" {
				chal = ch
			}
		}
		if chal == nil {
			return errors.New("CA offered no http-01 challenge")
		}
		auth, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return err
		}
		if err := c.Store.update(func(s *State) error { acmeInit(s).Challenges[chal.Token] = auth; return nil }); err != nil {
			return err
		}
		tokens = append(tokens, chal.Token)
		if err := c.awaitNetworkApplied(ctx); err != nil {
			return err
		}
		if _, err := client.Accept(ctx, chal); err != nil {
			return fmt.Errorf("accept challenge: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
			return fmt.Errorf("validation of http://%s/.well-known/acme-challenge/ failed (does DNS point at the workers and is port 80 reachable?): %w", host, err)
		}
	}
	// Keep the finalize URL from the new-order response: an order fetched
	// back from some CAs (and this client) may not repeat it.
	finalize := order.FinalizeURL
	if order, err = client.WaitOrder(ctx, orderURI); err != nil {
		return fmt.Errorf("order: %w", err)
	}
	if order.FinalizeURL != "" {
		finalize = order.FinalizeURL
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{host}}, key)
	if err != nil {
		return err
	}
	chain, _, err := client.CreateOrderCert(ctx, finalize, csr, true)
	if err != nil {
		// x/crypto waits on the order URL from the finalize response, which a
		// CA may omit (RFC 8555 makes it optional). The order may still have
		// been issued: wait on the known order URL and fetch the certificate.
		issued, werr := client.WaitOrder(ctx, orderURI)
		if werr != nil || issued.CertURL == "" {
			return fmt.Errorf("finalize: %w", err)
		}
		if chain, err = client.FetchCert(ctx, issued.CertURL, true); err != nil {
			return fmt.Errorf("fetch certificate: %w", err)
		}
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return err
	}
	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	der, _ := x509.MarshalECPrivateKey(key)
	sealed, err := c.Store.Vault.Seal(acmeKeyPurpose+host, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	if err != nil {
		return err
	}
	if err := c.Store.update(func(s *State) error {
		acmeInit(s).Certs[host] = acmeCert{CertPEM: string(certPEM), Key: sealed, NotAfter: leaf.NotAfter}
		return nil
	}); err != nil {
		return err
	}
	c.pushNetwork(ctx)
	return nil
}

// awaitNetworkApplied pushes configuration and waits until every online
// member applied the current one, so the CA can reach the challenge anywhere.
func (c *Controller) awaitNetworkApplied(ctx context.Context) error {
	for attempt := 0; attempt < 10; attempt++ {
		c.pushNetwork(ctx)
		s := c.Store.snapshot()
		pending := []string{}
		for id, m := range s.Network.Members {
			w, ok := s.Workers[id]
			if !ok || w.Status != "online" {
				continue
			}
			cfg, err := c.networkConfig(s, id)
			if err != nil || m.Applied != cfg.Hash() {
				pending = append(pending, w.Name)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if attempt == 9 {
			return fmt.Errorf("workers did not apply the ACME challenge: %s", strings.Join(pending, ", "))
		}
	}
	return nil
}

// acmeEdge adds challenges and issued certificates to the edge config.
func (c *Controller) acmeEdge(s State, e *clusternet.Edge) {
	if s.ACME == nil {
		return
	}
	if len(s.ACME.Challenges) > 0 {
		e.Challenges = s.ACME.Challenges
	}
	hosts := make([]string, 0, len(s.ACME.Certs))
	for h := range s.ACME.Certs {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		cert := s.ACME.Certs[h]
		if cert.CertPEM == "" || time.Now().After(cert.NotAfter) {
			continue
		}
		key, err := c.Store.Vault.Unseal(acmeKeyPurpose+h, cert.Key)
		if err != nil {
			log.Printf("acme %s: %v", h, err)
			continue
		}
		e.Certificates = append(e.Certificates, clusternet.EdgeCertificate{CertPEM: cert.CertPEM, KeyPEM: string(key)})
	}
}
