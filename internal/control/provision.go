package control

import (
	"context"
	"crypto/rand"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
)

type ProvisionRequest struct {
	Name   string           `json:"name"`
	Region string           `json:"region"`
	SSH    sshclient.Config `json:"ssh"`
	// NetworkEndpoint optionally overrides the cluster-network peer address.
	NetworkEndpoint string `json:"networkEndpoint,omitempty"`
}
type Provisioner interface {
	Install(context.Context, Worker, sshclient.Identity, func(string, string, string) error) error
}

// SSHProvisioner transfers locally built, static Linux binaries; no remote compiler or curl-to-shell installer is needed.
type SSHProvisioner struct {
	BinaryDir, TempRoot string
	// NetworkPort, when non-zero, prepares hosts for the cluster network:
	// WireGuard module, agent capabilities and the UDP port in the host firewall.
	NetworkPort int
	// EdgePorts are opened in the host firewall for the edge proxy.
	EdgePorts []int
}

func (c *Controller) workerEngine(w Worker) (engine.Engine, error) {
	if c.Store.database != nil && c.Store.database.ha {
		if err := validateHAWorker(w); err != nil {
			return nil, err
		}
	}
	if w.SSH == nil {
		return engine.New(w.Endpoint, w.Token)
	}
	client, err := engine.New("http://127.0.0.1:"+fmt.Sprint(w.SSH.AgentPort), w.Token)
	if err != nil {
		return nil, err
	}
	client.HTTP.Transport = &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		id, err := c.Store.Vault.Identity(w.SSH.SecretID)
		if err != nil {
			return nil, err
		}
		session, err := sshclient.New(*w.SSH, id, c.Store.Vault.tempRoot)
		if err != nil {
			return nil, err
		}
		return session.Dial(ctx)
	}}
	return client, nil
}
func (c *Controller) provisionStep(opID, name, status, detail string) error {
	return c.Store.update(func(s *State) error {
		for i := range s.Operations {
			if s.Operations[i].ID == opID {
				found := false
				for j := range s.Operations[i].Steps {
					if s.Operations[i].Steps[j].Name == name {
						s.Operations[i].Steps[j] = OperationStep{Name: name, Status: status, Detail: detail}
						found = true
						break
					}
				}
				if !found {
					s.Operations[i].Steps = append(s.Operations[i].Steps, OperationStep{Name: name, Status: status, Detail: detail})
				}
				s.Operations[i].Detail = name
				return nil
			}
		}
		return fmt.Errorf("provision operation not found")
	})
}
func (c *Controller) ProvisionWorker(req ProvisionRequest) (Operation, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !validName.MatchString(req.Name) || len(req.Region) > 80 {
		return Operation{}, fmt.Errorf("invalid worker name or region")
	}
	if err := sshclient.Validate(&req.SSH); err != nil {
		return Operation{}, err
	}
	if c.Store.database != nil && c.Store.database.ha {
		if err := validateHAWorker(Worker{SSH: &req.SSH}); err != nil {
			return Operation{}, err
		}
	}
	if req.SSH.User != "root" && !req.SSH.Sudo {
		return Operation{}, fmt.Errorf("non-root provisioning requires passwordless sudo")
	}
	if _, err := c.Store.Vault.Identity(req.SSH.SecretID); err != nil {
		return Operation{}, err
	}
	if c.Provisioner == nil && !c.Store.snapshot().Demo {
		return Operation{}, fmt.Errorf("SSH provisioning is not configured; set --worker-binaries and install OpenSSH client tools")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Operation{}, err
	}
	u := url.URL{Scheme: "ssh", Host: net.JoinHostPort(req.SSH.Host, fmt.Sprint(req.SSH.Port)), User: url.User(req.SSH.User)}
	if !validNetworkEndpoint(req.NetworkEndpoint) {
		return Operation{}, fmt.Errorf("networkEndpoint must be a hostname or IP address")
	}
	w := Worker{ID: newID("wrk_"), NetworkEndpoint: req.NetworkEndpoint, Name: req.Name, Region: req.Region, SSH: &req.SSH, Endpoint: u.String(), Token: hex.EncodeToString(b), Mode: "paused", Status: "provisioning", Containers: []engine.Container{}}
	return c.provisionHost(w, false)
}
func (c *Controller) RetryProvision(id string) (Operation, error) {
	w, ok := c.Store.snapshot().Workers[id]
	if !ok || w.SSH == nil || w.Status != "provision-failed" {
		return Operation{}, fmt.Errorf("only failed SSH provisioning jobs can be retried")
	}
	if c.Provisioner == nil && !c.Store.snapshot().Demo {
		return Operation{}, fmt.Errorf("SSH provisioning is not configured")
	}
	return c.provisionHost(w, true)
}
func (c *Controller) provisionHost(w Worker, retry bool) (Operation, error) {
	return c.submit("Provision "+w.Name, 30*time.Minute, func(s *State) error {
		if retry {
			old, ok := s.Workers[w.ID]
			if !ok || old.Status != "provision-failed" {
				return fmt.Errorf("worker is not eligible for a provisioning retry")
			}
			if len(old.Containers) > 0 {
				return fmt.Errorf("inspect existing containers before retrying provisioning")
			}
		}
		for _, x := range s.Workers {
			if x.ID != w.ID && (x.Name == w.Name || x.Endpoint == w.Endpoint || x.SSH != nil && x.SSH.Host == w.SSH.Host && x.SSH.Port == w.SSH.Port) {
				return fmt.Errorf("worker name or SSH host already registered")
			}
		}
		// A key deletion must not race the pending worker reference.
		if _, err := c.Store.Vault.Identity(w.SSH.SecretID); err != nil {
			return err
		}
		w.Status = "provisioning"
		w.Mode = "paused"
		w.Error = ""
		s.Workers[w.ID] = w
		return nil
	}, func(ctx context.Context, opID string) error {
		progress := func(name, status, detail string) error { return c.provisionStep(opID, name, status, detail) }
		finish := func(err error) error {
			saveErr := c.Store.update(func(s *State) error {
				x, ok := s.Workers[w.ID]
				if !ok {
					return nil // Removed while provisioning.
				}
				if err != nil {
					x.Status = "provision-failed"
					x.Mode = "paused"
					x.Error = err.Error()
				} else {
					x.Status = "online"
					x.Mode = "active"
					x.Provisioned = true
					x.Error = ""
					x.LastSeen = time.Now().UTC()
				}
				s.Workers[w.ID] = x
				return nil
			})
			if saveErr != nil {
				return fmt.Errorf("save provisioning result: %w", saveErr)
			}
			return err
		}
		id, err := c.Store.Vault.Identity(w.SSH.SecretID)
		if err != nil {
			return finish(err)
		}
		if c.Store.snapshot().Demo {
			for _, step := range []string{"Verify SSH host", "Install Docker", "Upload worker agent", "Configure worker service"} {
				if err := progress(step, "succeeded", "Simulated provisioning step; no host was contacted"); err != nil {
					return finish(err)
				}
			}
		} else {
			if err := c.Provisioner.Install(ctx, w, id, progress); err != nil {
				return finish(err)
			}
		}
		if err := progress("Verify worker connectivity", "running", "Checking Docker through the SSH tunnel"); err != nil {
			return finish(err)
		}
		e, err := c.Factory(w)
		var info engine.Info
		var containers []engine.Container
		if err == nil {
			probe, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			for {
				attempt, stop := context.WithTimeout(probe, 5*time.Second)
				info, err = e.Info(attempt)
				if err == nil {
					containers, err = e.List(attempt)
				}
				stop()
				if err == nil {
					break
				}
				select {
				case <-probe.Done():
					_ = progress("Verify worker connectivity", "failed", "Worker connection check timed out")
					return finish(fmt.Errorf("worker verification timed out; inspect zela-worker.service and SSH forwarding permission"))
				case <-time.After(time.Second):
				}
			}
		}
		if err != nil {
			_ = progress("Verify worker connectivity", "failed", "Worker connection check failed")
			return finish(fmt.Errorf("worker verification failed: %w", err))
		}
		if err := c.Store.update(func(s *State) error {
			x := s.Workers[w.ID]
			x.Info = info
			x.Containers = containers
			s.Workers[w.ID] = x
			return nil
		}); err != nil {
			return finish(err)
		}
		if err := progress("Verify worker connectivity", "succeeded", "Docker and worker agent are ready"); err != nil {
			return finish(err)
		}
		return finish(nil)
	})
}

const profileScript = `set -eu
[ "$(id -u)" -eq 0 ] || exit 70
[ "$(uname -s)" = Linux ] || exit 71
command -v systemctl >/dev/null
. /etc/os-release
case "$ID" in
  ubuntu|debian)
    command -v apt-get >/dev/null
    release="${UBUNTU_CODENAME:-${VERSION_CODENAME:-unknown}}"
    ;;
  rhel|rocky|almalinux|centos)
    command -v dnf >/dev/null
    command -v rpm >/dev/null
    release="${VERSION_ID:-unknown}"
    ;;
  *) exit 75 ;;
esac
printf 'ZELA_PROFILE %s %s %s\n' "$ID" "$release" "$(uname -m)"
`

var codenamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,30}$`)
var rpmReleasePattern = regexp.MustCompile(`^(8|9|10)(\.[0-9]+)*$`)

func parseProfile(b []byte) (distro, release, arch string, err error) {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == "ZELA_PROFILE" {
			distro, release, arch = f[1], f[2], f[3]
			break
		}
	}
	switch distro {
	case "ubuntu", "debian":
		if !codenamePattern.MatchString(release) || !(distro == "ubuntu" && (release == "jammy" || release == "noble" || release == "resolute") || distro == "debian" && (release == "bookworm" || release == "trixie")) {
			return "", "", "", fmt.Errorf("supported apt releases are Ubuntu 22.04/24.04/26.04 LTS and Debian 12/13")
		}
	case "rhel", "rocky", "almalinux", "centos":
		if !rpmReleasePattern.MatchString(release) {
			return "", "", "", fmt.Errorf("supported RPM releases are RHEL/Rocky/AlmaLinux 8/9/10 and CentOS Stream 9/10")
		}
		release, _, _ = strings.Cut(release, ".")
		if distro == "centos" && release == "8" {
			return "", "", "", fmt.Errorf("supported CentOS Stream releases are 9 and 10")
		}
	default:
		return "", "", "", fmt.Errorf("supported platforms are Ubuntu, Debian, RHEL, Rocky Linux, AlmaLinux, and CentOS Stream with systemd")
	}
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return "", "", "", fmt.Errorf("supported worker architectures are amd64 and arm64")
	}
	return
}
func (p *SSHProvisioner) Install(ctx context.Context, w Worker, id sshclient.Identity, progress func(string, string, string) error) error {
	if _, err := os.Stat(filepath.Join(p.BinaryDir, "zela-worker-linux-amd64")); err != nil {
		return fmt.Errorf("worker artifacts missing; run make workers and configure --worker-binaries")
	}
	session, err := sshclient.New(*w.SSH, id, p.TempRoot)
	if err != nil {
		return err
	}
	defer session.Close()
	rootCommand := "sh -s"
	if w.SSH.User != "root" {
		rootCommand = "sudo -n sh -s"
	}
	step := func(name string, fn func() error) error {
		if err := progress(name, "running", "Provisioning host over verified SSH"); err != nil {
			return err
		}
		if err := fn(); err != nil {
			err = bootstrapFailure(name, err)
			_ = progress(name, "failed", "Step failed; see the operation result and inspect the host")
			return fmt.Errorf("%s: %w", name, err)
		}
		return progress(name, "succeeded", "Completed")
	}
	var distro, release, arch string
	if err := step("Verify SSH host", func() error {
		b, err := session.Run(ctx, rootCommand, strings.NewReader(profileScript))
		if err != nil {
			return err
		}
		distro, release, arch, err = parseProfile(b)
		return err
	}); err != nil {
		return err
	}
	binaryPath := filepath.Join(p.BinaryDir, "zela-worker-linux-"+arch)
	f, err := elf.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("worker artifact is not a Linux ELF binary")
	}
	machine := f.Machine
	f.Close()
	if arch == "amd64" && machine != elf.EM_X86_64 || arch == "arm64" && machine != elf.EM_AARCH64 {
		return fmt.Errorf("worker binary architecture does not match host")
	}
	if err := step("Install Docker", func() error {
		_, err := session.Run(ctx, rootCommand, strings.NewReader(dockerInstallScript(distro, release, arch)))
		return err
	}); err != nil {
		return err
	}
	uploadPath := "/var/lib/zela-bootstrap/" + w.ID + ".worker"
	if err := step("Upload worker agent", func() error {
		_, err := session.Run(ctx, rootCommand, strings.NewReader("set -eu\ninstall -d -m 0700 /var/lib/zela-bootstrap\n"))
		if err != nil {
			return err
		}
		binary, err := os.Open(binaryPath)
		if err != nil {
			return err
		}
		defer binary.Close()
		command := "sh -c " + shellQuote("umask 077; cat > "+shellQuote(uploadPath))
		if w.SSH.User != "root" {
			command = "sudo -n " + command
		}
		_, err = session.Run(ctx, command, binary)
		return err
	}); err != nil {
		return err
	}
	if err := step("Configure worker service", func() error {
		_, err := session.Run(ctx, rootCommand, strings.NewReader(installScript(w, uploadPath, networkInstallScript(p.NetworkPort, p.EdgePorts...))))
		return err
	}); err != nil {
		return err
	}
	return nil
}

// Translate reserved script exit statuses without recording remotely supplied output.
func bootstrapFailure(step string, err error) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	if step == "Install Docker" {
		switch exit.ExitCode() {
		case 72:
			return fmt.Errorf("conflicting runtime packages detected; review Podman, old Docker, containerd, and runc packages before retrying")
		case 73:
			return fmt.Errorf("Docker did not become ready; inspect journalctl -u docker before retrying")
		case 76:
			return fmt.Errorf("Docker repository signing key did not match its trusted fingerprint; no key was imported")
		}
	}
	return err
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func dockerInstallScript(distro, release, arch string) string {
	if distro == "rhel" || distro == "rocky" || distro == "almalinux" || distro == "centos" {
		return dockerRPMInstallScript(distro, release)
	}
	return `set -eu
export DEBIAN_FRONTEND=noninteractive
if command -v docker >/dev/null 2>&1; then
  docker info >/dev/null
  systemctl enable docker >/dev/null
  exit 0
fi
# Do not remove conflicting packages or alter an existing Docker data directory.
for pkg in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
  if dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q 'install ok installed'; then
    printf 'Conflicting container runtime requires operator review\n' >&2
    exit 72
  fi
done
apt-get update
apt-get install -y ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl --proto '=https' --tlsv1.2 -fsSL https://download.docker.com/linux/` + distro + `/gpg -o /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<'ZELA_APT'
Types: deb
URIs: https://download.docker.com/linux/` + distro + `
Suites: ` + release + `
Components: stable
Architectures: ` + arch + `
Signed-By: /etc/apt/keyrings/docker.asc
ZELA_APT
install -d -m 0755 /etc/docker
if [ ! -e /etc/docker/daemon.json ]; then
  printf '%s\n' '{"log-driver":"local","log-opts":{"max-size":"20m","max-file":"3"}}' > /etc/docker/daemon.json
fi
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if docker info >/dev/null 2>&1; then exit 0; fi
  sleep 2
done
exit 73
`
}

// networkInstallScript runs before the agent restart in the same root shell.
// It adds a systemd drop-in, so the base unit and its flags stay untouched.
func networkInstallScript(port int, edgePorts ...int) string {
	if port == 0 {
		return ""
	}
	edge := ""
	for _, e := range edgePorts {
		if e > 0 {
			edge += fmt.Sprintf(`
if systemctl is-active --quiet firewalld 2>/dev/null; then
  firewall-cmd --quiet --add-port=%[1]d/tcp
  firewall-cmd --quiet --permanent --add-port=%[1]d/tcp
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow %[1]d/tcp >/dev/null
fi`, e)
		}
	}
	return `
if ! modprobe wireguard 2>/dev/null && ! [ -d /sys/module/wireguard ]; then
  echo "The WireGuard kernel module is unavailable; use a kernel with WireGuard (Linux 5.6+, RHEL 9+) or install it" >&2
  exit 75
fi
echo wireguard > /etc/modules-load.d/zela-wireguard.conf
for tool in ip iptables; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required for the cluster network" >&2; exit 76; }
done
install -d -m 0755 /etc/systemd/system/zela-worker.service.d
cat > /etc/systemd/system/zela-worker.service.d/cluster-network.conf <<'ZELA_NET'
[Service]
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
StateDirectory=zela-worker
StateDirectoryMode=0700
ReadWritePaths=/run
ZELA_NET
if command -v restorecon >/dev/null 2>&1; then
  restorecon -RF /etc/systemd/system/zela-worker.service.d
fi
if systemctl is-active --quiet firewalld 2>/dev/null; then
  firewall-cmd --quiet --add-port=` + fmt.Sprint(port) + `/udp
  firewall-cmd --quiet --permanent --add-port=` + fmt.Sprint(port) + `/udp
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow ` + fmt.Sprint(port) + `/udp >/dev/null
fi` + edge + `
`
}

func workerInstallScript(w Worker, uploadPath string) string { return installScript(w, uploadPath, "") }

// installScript inserts network preparation before the agent is (re)started.
func installScript(w Worker, uploadPath, network string) string {
	unitOpen, unitClose := "", ""
	if w.Provisioned {
		unitOpen = "if [ ! -f /etc/systemd/system/zela-worker.service ]; then\n"
		unitClose = "fi\n"
	}
	return `set -eu
umask 077
getent group docker >/dev/null
if ! id zela-worker >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin zela-worker
fi
usermod -a -G docker zela-worker
install -d -m 0750 -o root -g docker /etc/zela
install -m 0755 ` + shellQuote(uploadPath) + ` /usr/local/bin/zela-worker.next
mv /usr/local/bin/zela-worker.next /usr/local/bin/zela-worker
cat > /etc/zela/worker.env <<'ZELA_ENV'
ZELA_WORKER_TOKEN=` + w.Token + `
ZELA_ENV
chown root:docker /etc/zela/worker.env
chmod 0640 /etc/zela/worker.env
` + unitOpen + `cat > /etc/systemd/system/zela-worker.service <<'ZELA_UNIT'
[Unit]
Description=Zela Docker Management Worker (SSH transport)
After=docker.service network-online.target
Requires=docker.service
Wants=network-online.target
[Service]
User=zela-worker
Group=docker
EnvironmentFile=/etc/zela/worker.env
ExecStart=/usr/local/bin/zela-worker --listen 127.0.0.1:` + fmt.Sprint(w.SSH.AgentPort) + `
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
UMask=0077
[Install]
WantedBy=multi-user.target
ZELA_UNIT
` + unitClose + `if command -v restorecon >/dev/null 2>&1; then
  restorecon -F /usr/local/bin/zela-worker /etc/zela /etc/zela/worker.env /etc/systemd/system/zela-worker.service
fi
` + network + `systemctl daemon-reload
systemctl enable zela-worker.service
systemctl restart zela-worker.service
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet zela-worker.service; then
    rm -f ` + shellQuote(uploadPath) + `
    exit 0
  fi
  sleep 2
done
exit 74
`
}
