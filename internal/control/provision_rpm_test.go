package control

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRPMProfilesNormalizeMinorReleases(t *testing.T) {
	for _, distro := range []string{"rhel", "rocky", "almalinux", "centos"} {
		for _, release := range []string{"8.10", "9.6", "10.1"} {
			if distro == "centos" && release == "8.10" {
				continue
			}
			for _, machine := range []string{"x86_64", "aarch64"} {
				t.Run(distro+release+machine, func(t *testing.T) {
					d, r, a, err := parseProfile([]byte("ZELA_PROFILE " + distro + " " + release + " " + machine))
					major, _, _ := strings.Cut(release, ".")
					if err != nil || d != distro || r != major || (machine == "x86_64" && a != "amd64") || (machine == "aarch64" && a != "arm64") {
						t.Fatalf("invalid profile %q %q %q: %v", d, r, a, err)
					}
					cmd := exec.Command("sh", "-n")
					cmd.Stdin = strings.NewReader(dockerInstallScript(d, r, a))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("invalid installer: %s %v", out, err)
					}
				})
			}
		}
	}
	for _, profile := range []string{"rhel 7.9 x86_64", "rhel 11 x86_64", "rhel 9;evil x86_64", "rhel 9.bad x86_64", "centos 8 x86_64", "fedora 44 x86_64", "rhel 9 s390x"} {
		if _, _, _, err := parseProfile([]byte("ZELA_PROFILE " + profile)); err == nil {
			t.Fatalf("unsupported profile accepted: %s", profile)
		}
	}
}

// Execute the generated installer with isolated commands and redirected /etc paths.
// No real package manager, network command, service, or host configuration is touched.
type rpmHarness struct {
	root, bin string
	env       []string
}

func newRPMHarness(t *testing.T) *rpmHarness {
	t.Helper()
	h := &rpmHarness{root: t.TempDir()}
	h.bin = filepath.Join(h.root, "bin")
	if err := os.Mkdir(h.bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mktemp", "rm", "install", "cat", "awk", "chmod", "sleep"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(h.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	h.env = []string{"PATH=" + h.bin, "TEST_BIN=" + h.bin, "TEST_ROOT=" + h.root, "TMPDIR=" + h.root, "TEST_FINGERPRINT=060A61C51B558A7F742B77AAC52FEB6B621E9F35"}
	h.stub(t, "rpm", `if [ "$1" = -q ]; then [ "$2" = "${TEST_CONFLICT:-}" ]; else exit 0; fi`)
	h.stub(t, "curl", `while [ "$1" != -o ]; do shift; done; printf 'test-key\n' > "$2"`)
	h.stub(t, "gpg", `printf 'fpr:::::::::%s:\n' "$TEST_FINGERPRINT"`)
	h.stub(t, "restorecon", "exit 0")
	h.stub(t, "systemctl", "exit 0")
	h.stub(t, "dnf", `case " $* " in
  *" docker-ce "*)
    cat > "$TEST_BIN/docker" <<'DOCKER'
#!/bin/sh
printf 'docker %s\n' "$*" >> "$TEST_ROOT/commands"
exit 0
DOCKER
    chmod +x "$TEST_BIN/docker"
    ;;
esac`)
	return h
}

func (h *rpmHarness) stub(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.bin, name), []byte("#!/bin/sh\nset -eu\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$TEST_ROOT/commands\"\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
}
func (h *rpmHarness) run(t *testing.T, distro string) ([]byte, error) {
	t.Helper()
	script := strings.ReplaceAll(dockerInstallScript(distro, "9", "amd64"), "/etc/", h.root+"/etc/")
	cmd := exec.Command("sh")
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(script)
	return cmd.CombinedOutput()
}
func (h *rpmHarness) commands(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRPMInstallerUsesSignedMajorVersionRepositoryAndStartsDocker(t *testing.T) {
	for _, distro := range []string{"rhel", "rocky", "almalinux", "centos"} {
		t.Run(distro, func(t *testing.T) {
			h := newRPMHarness(t)
			if out, err := h.run(t, distro); err != nil {
				t.Fatalf("installer failed: %s %v", out, err)
			}
			repo, err := os.ReadFile(filepath.Join(h.root, "etc/yum.repos.d/docker-ce.repo"))
			if err != nil {
				t.Fatal(err)
			}
			family := "rhel"
			if distro == "centos" {
				family = "centos"
			}
			if !strings.Contains(string(repo), "https://download.docker.com/linux/"+family+"/9/$basearch/stable") || !strings.Contains(string(repo), "gpgcheck=1") {
				t.Fatalf("incorrect or unsigned repository: %s", repo)
			}
			b, err := os.ReadFile(filepath.Join(h.root, "etc/docker/daemon.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(b, &config); err != nil || config["selinux-enabled"] != true || config["log-driver"] != "local" {
				t.Fatalf("missing SELinux/log defaults: %s", b)
			}
			commands := h.commands(t)
			if !strings.Contains(commands, "install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin") || !strings.Contains(commands, "systemctl enable --now docker") || !strings.Contains(commands, "docker info") || !strings.Contains(commands, "restorecon -F") {
				t.Fatalf("incomplete install: %s", commands)
			}
			if strings.Contains(commands, "remove ") || strings.Contains(commands, "--allowerasing") || strings.Contains(commands, "setenforce") {
				t.Fatalf("unsafe install: %s", commands)
			}
		})
	}
}

func TestRPMInstallerRejectsConflictingRuntimeBeforeChanges(t *testing.T) {
	for _, pkg := range []string{"podman", "podman-docker", "runc", "containerd", "docker-engine"} {
		t.Run(pkg, func(t *testing.T) {
			h := newRPMHarness(t)
			h.env = append(h.env, "TEST_CONFLICT="+pkg)
			h.stub(t, "docker", "exit 0") // Reject a Podman shim even when it answers docker info.
			out, err := h.run(t, "rhel")
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 72 {
				t.Fatalf("conflict did not fail early: %s %v", out, err)
			}
			if detail := bootstrapFailure("Install Docker", err).Error(); !strings.Contains(detail, "conflicting runtime packages") {
				t.Fatalf("operator does not receive a useful conflict error: %s", detail)
			}
			if commands := h.commands(t); strings.Contains(commands, "dnf ") || strings.Contains(commands, "docker info") {
				t.Fatalf("conflicting host modified: %s", commands)
			}
			if _, err := os.Stat(filepath.Join(h.root, "etc")); !os.IsNotExist(err) {
				t.Fatal("configuration changed on conflicting host")
			}
		})
	}
}

func TestRPMInstallerReusesHealthyDockerAndPreservesDaemonConfig(t *testing.T) {
	h := newRPMHarness(t)
	h.stub(t, "docker", "exit 0")
	if out, err := h.run(t, "rhel"); err != nil {
		t.Fatalf("existing Docker failed: %s %v", out, err)
	}
	if commands := h.commands(t); strings.Contains(commands, "dnf ") || strings.Contains(commands, "--now") || !strings.Contains(commands, "docker info") {
		t.Fatalf("existing Docker changed: %s", commands)
	}
	// A fresh Docker install must also preserve an existing administrator configuration.
	h = newRPMHarness(t)
	dir := filepath.Join(h.root, "etc/docker")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	want := `{"log-driver":"journald","selinux-enabled":true}`
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := h.run(t, "rhel"); err != nil {
		t.Fatalf("fresh Docker failed: %s %v", out, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if string(b) != want {
		t.Fatal("administrator daemon configuration overwritten")
	}
}

func TestRPMInstallerRejectsUnexpectedSigningKey(t *testing.T) {
	h := newRPMHarness(t)
	h.env = append(h.env, "TEST_FINGERPRINT=BADKEY")
	out, err := h.run(t, "rhel")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 76 {
		t.Fatalf("unexpected key accepted: %s %v", out, err)
	}
	if detail := bootstrapFailure("Install Docker", err).Error(); !strings.Contains(detail, "signing key") {
		t.Fatalf("operator does not receive a useful signing error: %s", detail)
	}
	if commands := h.commands(t); strings.Contains(commands, "rpm --import") || strings.Contains(commands, "install docker-ce ") {
		t.Fatalf("untrusted key installed: %s", commands)
	}
}

func TestRPMProfileProbeDoesNotRequireApt(t *testing.T) {
	h := newRPMHarness(t)
	h.stub(t, "id", "printf '0\\n'")
	h.stub(t, "uname", `if [ "$1" = -s ]; then printf 'Linux\n'; else printf 'aarch64\n'; fi`)
	osRelease := filepath.Join(h.root, "os-release")
	if err := os.WriteFile(osRelease, []byte("ID=rhel\nVERSION_ID=9.6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh")
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(profileScript, "/etc/os-release", shellQuote(osRelease)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("RPM detection incorrectly needs apt: %s %v", out, err)
	}
	d, r, a, err := parseProfile(out)
	if err != nil || d != "rhel" || r != "9" || a != "arm64" {
		t.Fatalf("RPM host misidentified: %s %v", out, err)
	}
}
