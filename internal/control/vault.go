package control

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/sshclient"
)

type SecretMetadata struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}
type secretRecord struct {
	SecretMetadata
	Ciphertext string `json:"ciphertext"`
}
type Vault struct {
	database       *postgresStore
	mu             sync.Mutex
	path, tempRoot string
	aead           cipher.AEAD
	records        []secretRecord
}

func OpenVault(dir, encodedKey string) (*Vault, error) {
	return openVault(dir, encodedKey, nil)
}
func openVault(dir, encodedKey string, database *postgresStore) (*Vault, error) {
	path := filepath.Join(dir, "vault.json")
	records := []secretRecord{}
	b, err := readState(database, "vault", path)
	if err == nil {
		if err := json.Unmarshal(b, &records); err != nil {
			return nil, fmt.Errorf("invalid secrets vault")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	keyPath := filepath.Join(dir, "vault-key")
	var key []byte
	if encodedKey != "" {
		key, err = base64.StdEncoding.DecodeString(encodedKey)
	} else {
		var data []byte
		data, err = os.ReadFile(keyPath)
		if os.IsNotExist(err) {
			if len(records) > 0 {
				return nil, fmt.Errorf("vault master key is missing; restore vault-key or ZELA_VAULT_KEY")
			}
			key = make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return nil, err
			}
			if err = os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0600); err != nil {
				return nil, err
			}
		} else if err == nil {
			key, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		}
	}
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("vault key must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{database: database, path: path, tempRoot: filepath.Join(dir, "ssh-tmp"), aead: aead, records: records}
	for _, r := range records {
		if _, err := v.decrypt(r); err != nil {
			return nil, fmt.Errorf("vault key cannot decrypt stored secrets; restore the original master key")
		}
	}
	// The manager's state-directory lock is held. Clear identities left by an interrupted process.
	if err := os.RemoveAll(v.tempRoot); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(v.tempRoot, 0700); err != nil {
		return nil, err
	}
	return v, nil
}
func (v *Vault) decrypt(r secretRecord) (sshclient.Identity, error) {
	var id sshclient.Identity
	b, err := base64.StdEncoding.DecodeString(r.Ciphertext)
	if err != nil || len(b) < v.aead.NonceSize() {
		return id, fmt.Errorf("invalid encrypted secret")
	}
	plain, err := v.aead.Open(nil, b[:v.aead.NonceSize()], b[v.aead.NonceSize():], []byte(r.ID))
	if err != nil {
		return id, err
	}
	defer clear(plain)
	err = json.Unmarshal(plain, &id)
	return id, err
}
func (v *Vault) List() []SecretMetadata {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []SecretMetadata{}
	for _, r := range v.records {
		out = append(out, r.SecretMetadata)
	}
	return out
}
func (v *Vault) Identity(id string) (sshclient.Identity, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.records {
		if r.ID == id {
			return v.decrypt(r)
		}
	}
	return sshclient.Identity{}, fmt.Errorf("SSH key not found in the secrets vault")
}
func (v *Vault) Create(ctx context.Context, name string, id sshclient.Identity) (SecretMetadata, error) {
	if !validName.MatchString(name) {
		return SecretMetadata{}, fmt.Errorf("invalid secret name")
	}
	id.PrivateKey = strings.ReplaceAll(strings.TrimSpace(id.PrivateKey), "\r\n", "\n") + "\n"
	fingerprint, err := sshclient.Fingerprint(ctx, id, v.tempRoot)
	if err != nil {
		return SecretMetadata{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.records {
		if r.Name == name {
			return SecretMetadata{}, fmt.Errorf("secret name already exists")
		}
	}
	meta := SecretMetadata{ID: newID("key_"), Name: name, Kind: "ssh-key", Fingerprint: fingerprint, CreatedAt: time.Now().UTC()}
	plain, err := json.Marshal(id)
	if err != nil {
		return SecretMetadata{}, err
	}
	defer clear(plain)
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return SecretMetadata{}, err
	}
	sealed := v.aead.Seal(nonce, nonce, plain, []byte(meta.ID))
	record := secretRecord{SecretMetadata: meta, Ciphertext: base64.StdEncoding.EncodeToString(sealed)}
	next := append(append([]secretRecord{}, v.records...), record)
	if err := v.save(next); err != nil {
		return SecretMetadata{}, err
	}
	v.records = next
	return meta, nil
}
func (v *Vault) Delete(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	next := []secretRecord{}
	found := false
	for _, r := range v.records {
		if r.ID == id {
			found = true
		} else {
			next = append(next, r)
		}
	}
	if !found {
		return fmt.Errorf("secret not found")
	}
	if err := v.save(next); err != nil {
		return err
	}
	v.records = next
	return nil
}
func (v *Vault) save(records []secretRecord) error {
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if v.database != nil {
		return v.database.save("vault", b)
	}
	f, err := os.CreateTemp(filepath.Dir(v.path), ".vault-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), v.path)
}
func (c *Controller) CreateSSHSecret(ctx context.Context, name string, id sshclient.Identity) (SecretMetadata, error) {
	if !c.acquireOperationGate() {
		return SecretMetadata{}, fmt.Errorf("another cluster operation is running; wait for it to finish")
	}
	defer func() { <-c.gate }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.Store.Vault.Create(ctx, name, id)
}
func (c *Controller) DeleteSSHSecret(id string) error {
	if !c.acquireOperationGate() {
		return fmt.Errorf("another cluster operation is running; wait for it to finish")
	}
	defer func() { <-c.gate }()
	for _, w := range c.Store.snapshot().Workers {
		if w.SSH != nil && w.SSH.SecretID == id {
			return fmt.Errorf("key is used by worker %s; unregister that worker before deleting its key", w.Name)
		}
	}
	return c.Store.Vault.Delete(id)
}

// Seal encrypts plain with the vault master key. purpose is bound as
// additional data, so a sealed value cannot be replayed for another purpose.
func (v *Vault) Seal(purpose string, plain []byte) (string, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(v.aead.Seal(nonce, nonce, plain, []byte(purpose))), nil
}

// Unseal reverses Seal for the same purpose.
func (v *Vault) Unseal(purpose, sealed string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(b) < v.aead.NonceSize() {
		return nil, fmt.Errorf("invalid sealed value")
	}
	plain, err := v.aead.Open(nil, b[:v.aead.NonceSize()], b[v.aead.NonceSize():], []byte(purpose))
	if err != nil {
		return nil, fmt.Errorf("sealed value cannot be decrypted with this vault key")
	}
	return plain, nil
}
