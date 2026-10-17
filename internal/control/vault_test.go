package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/sshclient"
)

func testSSHIdentity(t *testing.T) (sshclient.Identity, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "identity")
	if err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "vault-test-passphrase", "-f", p).Run(); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(p + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return sshclient.Identity{PrivateKey: string(key), Passphrase: "vault-test-passphrase"}, strings.TrimSpace(string(pub))
}
func saveTestSecret(t *testing.T, c *Controller) (SecretMetadata, sshclient.Identity, string) {
	t.Helper()
	id, pub := testSSHIdentity(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := c.CreateSSHSecret(ctx, "workers", id)
	if err != nil {
		t.Fatal(err)
	}
	return m, id, pub
}
func TestVaultEncryptsAndReloadsSSHSecrets(t *testing.T) {
	c, _ := fixture(t)
	m, id, _ := saveTestSecret(t, c)
	b, err := os.ReadFile(c.Store.Vault.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE KEY") || strings.Contains(string(b), id.Passphrase) {
		t.Fatal("vault contains plaintext credentials")
	}
	mode, _ := os.Stat(c.Store.Vault.path)
	if mode.Mode().Perm() != 0600 {
		t.Fatal("vault mode must be 0600")
	}
	v, err := OpenVault(filepath.Dir(c.Store.path), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Identity(m.ID)
	if err != nil || got.PrivateKey != id.PrivateKey || got.Passphrase != id.Passphrase {
		t.Fatal("secret did not survive encrypted persistence")
	}
	public, _ := json.Marshal(c.Store.public())
	if strings.Contains(string(public), "PRIVATE KEY") || strings.Contains(string(public), id.Passphrase) || strings.Contains(string(public), "ciphertext") {
		t.Fatal("API state exports secret material")
	}
}
func TestVaultRefusesWrongMissingAndTamperedMasterKey(t *testing.T) {
	c, _ := fixture(t)
	saveTestSecret(t, c)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if _, err := OpenVault(filepath.Dir(c.Store.path), base64.StdEncoding.EncodeToString(key)); err == nil {
		t.Fatal("incorrect key accepted")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(c.Store.path), "vault-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(filepath.Dir(c.Store.path), ""); err == nil {
		t.Fatal("vault regenerated a missing master key")
	}
}
func TestVaultCiphertextCannotBeMovedToAnotherSecretID(t *testing.T) {
	c, _ := fixture(t)
	saveTestSecret(t, c)
	r := c.Store.Vault.records[0]
	r.ID = "different-id"
	if _, err := c.Store.Vault.decrypt(r); err == nil {
		t.Fatal("unauthenticated record swap accepted")
	}
	data, _ := base64.StdEncoding.DecodeString(r.Ciphertext)
	data[len(data)-1] ^= 0xff
	r.Ciphertext = base64.StdEncoding.EncodeToString(data)
	if _, err := c.Store.Vault.decrypt(r); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
func TestVaultDoesNotReturnPrivateKeyOrPassphraseFromCreateAPI(t *testing.T) {
	c, _ := fixture(t)
	id, _ := testSSHIdentity(t)
	body, _ := json.Marshal(map[string]string{"name": "api-key", "privateKey": id.PrivateKey, "passphrase": id.Passphrase})
	req := httptest.NewRequest("POST", "http://control.local/api/secrets", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	Handler(c, HTTPConfig{Token: testToken}).ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), id.Passphrase) {
		t.Fatal("secret leaked in import response")
	}
	var meta SecretMetadata
	_ = json.Unmarshal(w.Body.Bytes(), &meta)
	if meta.ID == "" || meta.Fingerprint == "" {
		t.Fatal("metadata missing")
	}
}
func TestVaultRejectsInvalidPrivateKeysWithoutEchoingInput(t *testing.T) {
	c, _ := fixture(t)
	raw := "-----BEGIN OPENSSH PRIVATE KEY-----\nsensitive-invalid-secret\n-----END OPENSSH PRIVATE KEY-----"
	_, err := c.CreateSSHSecret(context.Background(), "bad", sshclient.Identity{PrivateKey: raw})
	if err == nil || strings.Contains(err.Error(), "sensitive-invalid-secret") {
		t.Fatalf("unsafe validation: %v", err)
	}
	if len(c.Store.Vault.List()) != 0 {
		t.Fatal("invalid secret persisted")
	}
}
