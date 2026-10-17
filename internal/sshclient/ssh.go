// Package sshclient uses the system OpenSSH client with explicit identities and pinned host keys.
package sshclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	SecretID  string `json:"secretID"`
	HostKey   string `json:"hostKey"`
	Sudo      bool   `json:"sudo"`
	AgentPort int    `json:"agentPort"`
}
type Identity struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase,omitempty"`
}

var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`)
var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]{0,252}$`)

func Validate(c *Config) error {
	c.Host = strings.TrimSpace(c.Host)
	c.HostKey = strings.TrimSpace(c.HostKey)
	if net.ParseIP(c.Host) == nil && (!hostPattern.MatchString(c.Host) || strings.Contains(c.Host, "..") || strings.Contains(c.Host, ":")) {
		return fmt.Errorf("enter a DNS name or IP address without a scheme or port")
	}
	if !userPattern.MatchString(c.User) {
		return fmt.Errorf("invalid SSH user")
	}
	if c.Port == 0 {
		c.Port = 22
	}
	if c.AgentPort == 0 {
		c.AgentPort = 9091
	}
	if c.Port < 1 || c.Port > 65535 || c.AgentPort < 1024 || c.AgentPort > 65535 {
		return fmt.Errorf("SSH port must be 1–65535 and agent port 1024–65535")
	}
	if c.SecretID == "" {
		return fmt.Errorf("select an SSH key from the secrets vault")
	}
	_, err := HostFingerprint(c.HostKey)
	return err
}
func HostFingerprint(key string) (string, error) {
	f := strings.Fields(key)
	if len(f) < 2 || len(f) > 3 || strings.ContainsAny(key, "\n\r\x00") {
		return "", fmt.Errorf("supply the host public key as 'ssh-ed25519 AAAA…' (not a fingerprint or private key)")
	}
	switch f[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
	default:
		return "", fmt.Errorf("unsupported SSH host key type")
	}
	b, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(b) < 8 || len(b) > 16384 {
		return "", fmt.Errorf("invalid SSH host public key")
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n > len(b)-4 || string(b[4:4+n]) != f[0] {
		return "", fmt.Errorf("invalid SSH host key encoding")
	}
	digest := sha256.Sum256(b)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

type Session struct {
	Config             Config
	dir                string
	env                []string
	keyPath, knownPath string
}

func New(c Config, id Identity, tempRoot string) (*Session, error) {
	if err := Validate(&c); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tempRoot, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tempRoot, "session-")
	if err != nil {
		return nil, err
	}
	s := &Session{Config: c, dir: dir, keyPath: filepath.Join(dir, "identity"), knownPath: filepath.Join(dir, "known_hosts")}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	if err := os.WriteFile(s.keyPath, []byte(id.PrivateKey), 0600); err != nil {
		return nil, err
	}
	host := c.Host
	if c.Port != 22 {
		host = fmt.Sprintf("[%s]:%d", c.Host, c.Port)
	}
	fields := strings.Fields(c.HostKey)
	if err := os.WriteFile(s.knownPath, []byte(host+" "+fields[0]+" "+fields[1]+"\n"), 0600); err != nil {
		return nil, err
	}
	s.env = askpassEnv(dir, id.Passphrase)
	if id.Passphrase != "" {
		if err := writeAskpass(dir); err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}
func (s *Session) Close() { _ = os.RemoveAll(s.dir) }
func cleanEnv() []string {
	out := []string{}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key == "PATH" || key == "HOME" || key == "USER" || key == "LOGNAME" || key == "LANG" || key == "TMPDIR" || strings.HasPrefix(key, "LC_") {
			out = append(out, v)
		}
	}
	return out
}
func askpassEnv(dir, pass string) []string {
	env := cleanEnv()
	if pass != "" {
		env = append(env, "SSH_ASKPASS="+filepath.Join(dir, "askpass"), "SSH_ASKPASS_REQUIRE=force", "DISPLAY=ctl:0", "ZELA_SSH_PASSPHRASE="+pass)
	}
	return env
}
func writeAskpass(dir string) error {
	return os.WriteFile(filepath.Join(dir, "askpass"), []byte("#!/bin/sh\nprintf '%s\\n' \"$ZELA_SSH_PASSPHRASE\"\n"), 0700)
}
func (s *Session) args() []string {
	batch := "yes"
	for _, v := range s.env {
		if strings.HasPrefix(v, "ZELA_SSH_PASSPHRASE=") {
			batch = "no"
		}
	}
	return []string{"-F", "/dev/null", "-T", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + strconv.Quote(s.knownPath), "-o", "GlobalKnownHostsFile=/dev/null", "-o", "IdentitiesOnly=yes", "-o", "BatchMode=" + batch, "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "NumberOfPasswordPrompts=1", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ControlMaster=no", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-i", s.keyPath, "-p", fmt.Sprint(s.Config.Port), "-l", s.Config.User}
}

// Run does not return stderr: a remote shell can echo secrets read from stdin.
func (s *Session) Run(ctx context.Context, command string, input io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", append(s.args(), "--", s.Config.Host, command)...)
	cmd.Env = s.env
	cmd.Stdin = input
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SSH command failed (%w); check the pinned host key, SSH access, sudo privileges, and host service logs", err)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := 64*1024 - b.Len()
	if left > 0 {
		if left < len(p) {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

// Fingerprint validates an imported private key using OpenSSH without putting its passphrase in argv.
func Fingerprint(ctx context.Context, id Identity, tempRoot string) (string, error) {
	if len(id.PrivateKey) > 65536 || len(id.Passphrase) > 4096 || strings.ContainsAny(id.Passphrase, "\n\r\x00") || !strings.Contains(id.PrivateKey, "PRIVATE KEY-----") {
		return "", fmt.Errorf("invalid SSH private key or passphrase")
	}
	if err := os.MkdirAll(tempRoot, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(tempRoot, "session-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	key := filepath.Join(dir, "identity")
	if err := os.WriteFile(key, []byte(id.PrivateKey), 0600); err != nil {
		return "", err
	}
	if id.Passphrase != "" {
		if err := writeAskpass(dir); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-y", "-f", key)
	cmd.Env = askpassEnv(dir, id.Passphrase)
	cmd.Stdin = strings.NewReader("")
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cannot read SSH private key; check its format and passphrase")
	}
	return HostFingerprint(strings.TrimSpace(out.String()))
}

// Dial opens an SSH-encrypted stdio forwarding connection to the loopback agent.
func (s *Session) Dial(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
	args := append(s.args(), "-W", net.JoinHostPort("127.0.0.1", fmt.Sprint(s.Config.AgentPort)), "--", s.Config.Host)
	cmd := exec.Command("ssh", args...)
	cmd.Env = s.env
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	out, writer := io.Pipe()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		writer.Close()
		s.Close()
		return nil, err
	}
	conn := &stdioConn{in: in, out: out, cmd: cmd, session: s, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		if err != nil {
			err = fmt.Errorf("SSH tunnel failed; verify the pinned host key, saved identity, and SSH forwarding permission")
		}
		_ = writer.CloseWithError(err)
		s.Close()
		close(conn.done)
	}()
	return conn, nil
}

type stdioConn struct {
	in                          io.WriteCloser
	out                         io.ReadCloser
	cmd                         *exec.Cmd
	session                     *Session
	once                        sync.Once
	done                        chan struct{}
	mu                          sync.Mutex
	timer                       *time.Timer
	readDeadline, writeDeadline time.Time
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *stdioConn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		if c.timer != nil {
			c.timer.Stop()
		}
		c.mu.Unlock()
		c.in.Close()
		c.out.Close()
		_ = c.cmd.Process.Kill()
		c.session.Close()
	})
	return nil
}

type addr string

func (a addr) Network() string            { return "ssh" }
func (a addr) String() string             { return string(a) }
func (c *stdioConn) LocalAddr() net.Addr  { return addr("control-plane") }
func (c *stdioConn) RemoteAddr() net.Addr { return addr(c.session.Config.Host) }
func (c *stdioConn) deadline() {
	if c.timer != nil {
		c.timer.Stop()
	}
	d := c.readDeadline
	if d.IsZero() || !c.writeDeadline.IsZero() && c.writeDeadline.Before(d) {
		d = c.writeDeadline
	}
	if !d.IsZero() {
		c.timer = time.AfterFunc(time.Until(d), func() { c.Close() })
	}
}
func (c *stdioConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.writeDeadline = t
	c.deadline()
	return nil
}
func (c *stdioConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.deadline()
	return nil
}
func (c *stdioConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	c.deadline()
	return nil
}
