package sshclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func identity(t *testing.T, pass string) (Identity, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "key")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", pass, "-f", file)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(file + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return Identity{PrivateKey: string(private), Passphrase: pass}, strings.TrimSpace(string(public))
}
func TestPrivateKeyValidationHandlesPassphrasesWithoutSecretArguments(t *testing.T) {
	for _, pass := range []string{"", "a-test-only-passphrase"} {
		t.Run(pass, func(t *testing.T) {
			id, pub := identity(t, pass)
			want, err := HostFingerprint(pub)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := Fingerprint(ctx, id, t.TempDir())
			if err != nil || got != want {
				t.Fatalf("fingerprint=%s err=%v", got, err)
			}
			if pass != "" {
				id.Passphrase = "incorrect"
				if _, err := Fingerprint(ctx, id, t.TempDir()); err == nil {
					t.Fatal("incorrect passphrase accepted")
				}
			}
		})
	}
}
func TestSessionPinsHostAndProtectsTemporaryIdentity(t *testing.T) {
	id, pub := identity(t, "")
	for _, host := range []string{"worker.example", "10.0.0.5", "::1"} {
		c := Config{Host: host, Port: 2222, User: "ubuntu", SecretID: "k", HostKey: pub, AgentPort: 9091}
		s, err := New(c, id, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(s.knownPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "["+host+"]:2222 ") {
			t.Fatalf("invalid known_hosts: %s", b)
		}
		info, _ := os.Stat(s.keyPath)
		if info.Mode().Perm() != 0600 {
			t.Fatal("identity permissions are unsafe")
		}
		args := strings.Join(s.args(), " ")
		for _, wanted := range []string{"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "PasswordAuthentication=no", "ForwardAgent=no", "-F /dev/null"} {
			if !strings.Contains(args, wanted) {
				t.Fatalf("missing %s", wanted)
			}
		}
		if strings.Contains(args, id.PrivateKey) {
			t.Fatal("private key in argv")
		}
		dir := s.dir
		s.Close()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("temporary identity not deleted")
		}
	}
}
func TestConfigRejectsUnpinnedHostsAndCommandInjection(t *testing.T) {
	_, pub := identity(t, "")
	for _, host := range []string{"https://host", "-oProxyCommand=evil", "host; rm -rf /", "host/path", "host@user", "host\nother"} {
		c := Config{Host: host, User: "root", SecretID: "k", HostKey: pub}
		if Validate(&c) == nil {
			t.Fatalf("unsafe host accepted: %s", host)
		}
	}
	c := Config{Host: "host", User: "root", SecretID: "k"}
	if Validate(&c) == nil {
		t.Fatal("unverified host allowed")
	}
	c.HostKey = pub
	c.User = "root;evil"
	if Validate(&c) == nil {
		t.Fatal("unsafe SSH user allowed")
	}
}
func TestSSHForwardedHTTPDoesNotTruncateResponse(t *testing.T) {
	id, pub := identity(t, "")
	dir := t.TempDir()
	script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in "$(printf '\r')"|"") break;; esac
done
printf 'HTTP/1.1 200 OK\r\nContent-Length: 131072\r\nConnection: close\r\n\r\n'
head -c 131072 /dev/zero | tr '\000' 'a'
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	s, err := New(Config{Host: "worker.test", User: "root", SecretID: "k", HostKey: pub}, id, root)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return s.Dial(ctx) }}}
	resp, err := client.Get("http://worker/info")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(data) != 131072 {
		t.Fatalf("response truncated: %d bytes %v", len(data), err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(s.dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("temporary SSH session did not exit")
}
func TestSSHChildEnvironmentDoesNotInheritControlPlaneSecrets(t *testing.T) {
	t.Setenv("ZELA_VAULT_KEY", "secret-master-key")
	t.Setenv("ZELA_ADMIN_TOKEN", "secret-admin-token")
	t.Setenv("SSH_AUTH_SOCK", "/private/socket")
	for _, v := range cleanEnv() {
		if strings.Contains(v, "secret-master-key") || strings.Contains(v, "secret-admin-token") || strings.HasPrefix(v, "SSH_AUTH_SOCK=") {
			t.Fatal("control-plane secret passed to SSH process")
		}
	}
}
