package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
)

type fakeProvisioner struct {
	fail  bool
	calls int
}

func (p *fakeProvisioner) Install(ctx context.Context, w Worker, id sshclient.Identity, progress func(string, string, string) error) error {
	p.calls++
	if id.PrivateKey == "" {
		return errors.New("missing identity")
	}
	for _, name := range []string{"Verify SSH host", "Install Docker", "Upload worker agent", "Configure worker service"} {
		if err := progress(name, "running", "Started"); err != nil {
			return err
		}
		if p.fail && name == "Install Docker" {
			_ = progress(name, "failed", "Install failed")
			return errors.New("Docker installation failed")
		}
		if err := progress(name, "succeeded", "Completed"); err != nil {
			return err
		}
	}
	return nil
}
func provisionFixture(t *testing.T) (*Controller, *fakeProvisioner, ProvisionRequest) {
	t.Helper()
	c, _ := fixture(t)
	m, _, pub := saveTestSecret(t, c)
	p := &fakeProvisioner{}
	c.Provisioner = p
	fake := &DemoEngine{name: "provisioned", containers: map[string]engine.Container{}, specs: map[string]engine.Spec{}}
	c.Factory = func(w Worker) (engine.Engine, error) { return fake, nil }
	req := ProvisionRequest{Name: "new-worker", Region: "west", SSH: sshclient.Config{Host: "10.0.0.3", Port: 22, User: "ubuntu", SecretID: m.ID, HostKey: pub, Sudo: true, AgentPort: 9091}}
	return c, p, req
}
func TestProvisioningRegistersHostOnlyAfterVerification(t *testing.T) {
	c, p, req := provisionFixture(t)
	op, err := c.ProvisionWorker(req)
	succeeded(t, c, op, err)
	if p.calls != 1 {
		t.Fatal("installer not called")
	}
	var worker Worker
	for _, w := range c.Store.snapshot().Workers {
		if w.Name == req.Name {
			worker = w
		}
	}
	if worker.Status != "online" || worker.Mode != "active" || !worker.Provisioned || worker.SSH == nil {
		t.Fatalf("worker not ready: %+v", worker)
	}
	if !strings.HasPrefix(worker.Endpoint, "ssh://") || len(worker.Token) != 64 {
		t.Fatal("missing secure worker configuration")
	}
	o := c.Store.snapshot().Operations[0]
	if len(o.Steps) != 5 {
		t.Fatalf("missing progress: %+v", o.Steps)
	}
	if err := c.DeleteSSHSecret(req.SSH.SecretID); err == nil {
		t.Fatal("deleted key needed by active worker")
	}
}
func TestProvisioningFailureCanRetryWithoutDuplicateWorkersOrCredentialRotation(t *testing.T) {
	c, p, req := provisionFixture(t)
	p.fail = true
	op, err := c.ProvisionWorker(req)
	o := waitOp(t, c, op, err)
	if o.Status != "failed" {
		t.Fatal("failure not recorded")
	}
	var worker Worker
	for _, w := range c.Store.snapshot().Workers {
		if w.Name == req.Name {
			worker = w
		}
	}
	if worker.Status != "provision-failed" || worker.Mode != "paused" {
		t.Fatal("failed host became schedulable")
	}
	p.fail = false
	op, err = c.RetryProvision(worker.ID)
	succeeded(t, c, op, err)
	got := c.Store.snapshot().Workers[worker.ID]
	if got.Token != worker.Token || len(c.Store.snapshot().Workers) != 3 {
		t.Fatal("retry changed identity or duplicated registry entries")
	}
}
func TestWorkerVerificationFailureDoesNotActivateHost(t *testing.T) {
	c, _, req := provisionFixture(t)
	c.Factory = func(Worker) (engine.Engine, error) { return nil, errors.New("unreachable") }
	op, err := c.ProvisionWorker(req)
	o := waitOp(t, c, op, err)
	if o.Status != "failed" {
		t.Fatal("failed verification reported success")
	}
	for _, w := range c.Store.snapshot().Workers {
		if w.Name == req.Name && w.Mode != "paused" {
			t.Fatal("unverified host activated")
		}
	}
}
func TestAgentUpdatePreservesWorkerCredentialsPlacementAndContainers(t *testing.T) {
	c, p, req := provisionFixture(t)
	op, err := c.ProvisionWorker(req)
	succeeded(t, c, op, err)
	var worker Worker
	for _, w := range c.Store.snapshot().Workers {
		if w.Name == req.Name {
			worker = w
		}
	}
	e, _ := c.Factory(worker)
	id, err := e.Create(context.Background(), engine.Spec{Name: "existing", Image: "nginx:alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.update(func(s *State) error { w := s.Workers[worker.ID]; w.Mode = "paused"; s.Workers[w.ID] = w; return nil }); err != nil {
		t.Fatal(err)
	}
	op, err = c.UpgradeWorker(worker.ID)
	succeeded(t, c, op, err)
	c.Refresh(context.Background())
	w := c.Store.snapshot().Workers[worker.ID]
	if w.Token != worker.Token || w.Mode != "paused" || len(w.Containers) != 1 || w.Containers[0].ID != id || w.Metrics == nil || p.calls != 2 {
		t.Fatalf("agent update changed existing worker: %+v", w)
	}
}
func TestProvisioningInputRequiresPinnedIdentityRootOrSudoAndNoInjection(t *testing.T) {
	c, _, req := provisionFixture(t)
	for _, edit := range []func(*ProvisionRequest){func(r *ProvisionRequest) { r.SSH.Host = "host;touch /tmp/evil" }, func(r *ProvisionRequest) { r.SSH.HostKey = "" }, func(r *ProvisionRequest) { r.SSH.Sudo = false }, func(r *ProvisionRequest) { r.SSH.SecretID = "missing" }, func(r *ProvisionRequest) { r.Name = "bad\nname" }} {
		r := req
		edit(&r)
		if _, err := c.ProvisionWorker(r); err == nil {
			t.Fatal("invalid provisioning configuration accepted")
		}
	}
}
func TestProvisioningAPIAndProgressReturnNoCredentialMaterial(t *testing.T) {
	c, _, req := provisionFixture(t)
	b, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "http://control.local/api/workers/provision", strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	Handler(c, HTTPConfig{Token: testToken}).ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var op Operation
	_ = json.Unmarshal(w.Body.Bytes(), &op)
	succeeded(t, c, op, nil)
	b, _ = json.Marshal(c.Store.public())
	id, _ := c.Store.Vault.Identity(req.SSH.SecretID)
	if strings.Contains(string(b), id.PrivateKey) || strings.Contains(string(b), id.Passphrase) {
		t.Fatal("provisioning result leaks SSH key")
	}
}
func TestBootstrapScriptsAreValidShellAndUsePrivateAgentTransport(t *testing.T) {
	w := Worker{ID: "wrk_123", Token: strings.Repeat("a", 64), SSH: &sshclient.Config{AgentPort: 9091}}
	updated := w
	updated.Provisioned = true
	scripts := []string{profileScript, dockerInstallScript("ubuntu", "noble", "amd64"), workerInstallScript(w, "/var/lib/zela-bootstrap/wrk_123.worker"), workerInstallScript(updated, "/var/lib/zela-bootstrap/wrk_123.worker")}
	for i, script := range scripts {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script %d: %s %v", i, out, err)
		}
	}
	script := scripts[2]
	if !strings.Contains(script, "--listen 127.0.0.1:9091") || strings.Contains(script, "--allow-http") || strings.Contains(script, "0.0.0.0") {
		t.Fatal("agent exposed beyond loopback")
	}
	if strings.Contains(scripts[1], "get.docker.com") || strings.Contains(scripts[1], "apt-get remove") {
		t.Fatal("bootstrap performs unreviewed runtime replacement")
	}
	if !strings.Contains(scripts[3], "if [ ! -f /etc/systemd/system/zela-worker.service ]; then") {
		t.Fatal("agent update overwrites existing service flags")
	}
}
func TestProfileRejectsUnsupportedOperatingSystemsAndInjection(t *testing.T) {
	for _, profile := range []string{"ZELA_PROFILE alpine edge x86_64", "ZELA_PROFILE ubuntu noble;evil x86_64", "ZELA_PROFILE debian bookworm armv7l", "ZELA_PROFILE debian buster x86_64", "ZELA_PROFILE ubuntu focal x86_64", "unstructured output"} {
		if _, _, _, err := parseProfile([]byte(profile)); err == nil {
			t.Fatalf("unsupported profile accepted: %s", profile)
		}
	}
	d, c, a, err := parseProfile([]byte("message\nZELA_PROFILE ubuntu noble aarch64\n"))
	if err != nil || fmt.Sprint(d, c, a) != "ubuntunoblearm64" {
		t.Fatalf("profile parse failed %q %q %q %v", d, c, a, err)
	}
}
