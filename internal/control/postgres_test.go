package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zela-dev/zela/internal/sshclient"
)

type testDatabase struct {
	postgresConnection
	documents map[string][]byte
	fail      string
	version   int
	closed    bool
	commits   int
}

type testTransaction struct {
	pgx.Tx
	db      *testDatabase
	pending map[string][]byte
	version int
}

type testRow struct{ version int }

func (r testRow) Scan(dest ...any) error { *dest[0].(*int) = r.version; return nil }
func (d *testDatabase) Begin(context.Context) (pgx.Tx, error) {
	return &testTransaction{db: d, pending: map[string][]byte{}, version: d.version}, nil
}
func (d *testDatabase) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if d.closed || d.fail == args[0].(string) {
		return pgconn.CommandTag{}, errors.New("private database password")
	}
	name := args[0].(string)
	if _, ok := d.documents[name]; !ok {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	d.documents[name] = []byte(args[1].(string))
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (d *testDatabase) Ping(context.Context) error {
	if d.closed {
		return errors.New("lost connection")
	}
	return nil
}
func (d *testDatabase) Close(context.Context) error { d.closed = true; return nil }
func (tx *testTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(query, "INSERT INTO zela.state(") {
		name := args[0].(string)
		if name == tx.db.fail {
			return pgconn.CommandTag{}, errors.New("secret SQL parameter")
		}
		tx.pending[name] = []byte(args[1].(string))
	}
	if strings.HasPrefix(query, "UPDATE zela.schema_version") {
		_, v, _ := strings.Cut(query, "SET version = ")
		tx.version, _ = strconv.Atoi(strings.Fields(v)[0])
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (tx *testTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	return testRow{tx.version}
}
func (tx *testTransaction) Commit(context.Context) error {
	for name, b := range tx.pending {
		tx.db.documents[name] = b
	}
	tx.db.version = tx.version
	tx.db.commits++
	return nil
}
func (tx *testTransaction) Rollback(context.Context) error { return nil }

func testBackend(d *testDatabase) *postgresStore {
	initial := map[string][]byte{}
	for k, v := range d.documents {
		initial[k] = append([]byte{}, v...)
	}
	return &postgresStore{conn: d, initial: initial, initialized: len(initial) > 0}
}

func seedEncryptedVault(t *testing.T, s *Store) {
	t.Helper()
	id := sshclient.Identity{PrivateKey: "private-key-value", Passphrase: "private-passphrase"}
	b, _ := json.Marshal(id)
	nonce := make([]byte, s.Vault.aead.NonceSize())
	r := secretRecord{SecretMetadata: SecretMetadata{ID: "key_test", Name: "test", Kind: "ssh-key"}, Ciphertext: base64.StdEncoding.EncodeToString(s.Vault.aead.Seal(nonce, nonce, b, []byte("key_test")))}
	if err := s.Vault.save([]secretRecord{r}); err != nil {
		t.Fatal(err)
	}
	s.Vault.records = []secretRecord{r}
}

func TestPostgresImportRestartAndFailedWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	seedEncryptedVault(t, s)
	if err := s.update(func(v *State) error {
		v.Workers["wrk_test"] = Worker{ID: "wrk_test", Name: "imported", Token: "worker-private-token"}
		v.DockerContainers = map[string]DockerContainer{"raw_test": {ID: "raw_test", Name: "raw", WorkerID: "wrk_test", MemoryBytes: 128 << 20, NanoCPUs: 250000000}}
		v.DockerExecs = map[string]DockerExec{"exec_test": {WorkerID: "wrk_test", ContainerID: "raw_test"}}
		v.Operations = []Operation{{ID: "op_test", Status: "running"}}
		v.Deployments["dep_test"] = Deployment{ID: "dep_test", Name: "app", Image: "nginx", Status: "updating", Env: []string{"APP_MODE=production"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rules := defaultAlertRules()
	rules.CPUPercent = 81
	if err := s.Monitor.SetRules(rules); err != nil {
		t.Fatal(err)
	}
	if err := s.Monitor.mutate(func() error {
		s.Monitor.data.Alerts = []Alert{{ID: "alert_test", Active: true, Acknowledged: true}}
		s.Monitor.data.History["wrk_test"] = []HistoryPoint{{Time: time.Now().UTC()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	d := &testDatabase{documents: map[string][]byte{}}
	open := func() (*postgresStore, error) { d.closed = false; return testBackend(d), nil }
	s, err = openStore(dir, false, open)
	if err != nil {
		t.Fatal(err)
	}
	if d.commits != 1 || len(d.documents) != 3 {
		t.Fatal("import was not committed together")
	}
	if s.public().Persistence != "postgresql" || s.snapshot().Operations[0].Status != "failed" {
		t.Fatal("missing database mode or restart recovery")
	}
	if strings.Contains(string(d.documents["vault"]), "private-key-value") {
		t.Fatal("vault plaintext persisted")
	}
	if err := s.update(func(v *State) error {
		w := v.Workers["wrk_test"]
		w.Name = "database-current"
		v.Workers[w.ID] = w
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openStore(dir, false, open)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.snapshot().Workers["wrk_test"].Name != "database-current" {
		t.Fatal("local files overrode database")
	}
	if s.snapshot().DockerContainers["raw_test"].MemoryBytes != 128<<20 || s.snapshot().DockerExecs["exec_test"].ContainerID != "raw_test" {
		t.Fatal("Docker ownership and reservations did not survive PostgreSQL import/restart")
	}
	id, err := s.Vault.Identity("key_test")
	if err != nil || id.Passphrase != "private-passphrase" {
		t.Fatal("encrypted secret did not survive restart")
	}
	alerts, gotRules := s.Monitor.Public()
	if len(alerts) != 1 || !alerts[0].Acknowledged || len(s.Monitor.History("wrk_test", time.Time{})) != 1 {
		t.Fatal("monitoring history or acknowledgement lost on restart")
	}
	if s.snapshot().Deployments["dep_test"].Status != "degraded" {
		t.Fatal("interrupted deployment was not recovered")
	}
	if gotRules != rules {
		t.Fatal("monitor rules did not survive restart")
	}
	for _, name := range []string{"cluster", "monitoring", "vault"} {
		d.fail = name
		var err error
		switch name {
		case "cluster":
			err = s.update(func(v *State) error { delete(v.Workers, "wrk_test"); return nil })
		case "monitoring":
			next := rules
			next.CPUPercent = 50
			err = s.Monitor.SetRules(next)
		case "vault":
			err = s.Vault.Delete("key_test")
		}
		if err == nil || strings.Contains(err.Error(), "private database password") {
			t.Fatalf("invalid failure handling for %s: %v", name, err)
		}
	}
	_, gotRules = s.Monitor.Public()
	if len(s.snapshot().Workers) != 1 || len(s.Vault.List()) != 1 || gotRules != rules {
		t.Fatal("failed database write changed memory")
	}
	d.closed = true
	if s.CheckPersistence() == nil {
		t.Fatal("lost database session was not detected")
	}
}

func TestPostgresInitializationFailureAndSchemaVersion(t *testing.T) {
	d := &testDatabase{documents: map[string][]byte{}, fail: "monitoring"}
	p := testBackend(d)
	if err := p.initialize(State{}, nil, monitorData{}); err == nil {
		t.Fatal("initialization succeeded after failed insert")
	}
	if len(d.documents) != 0 || p.initialized {
		t.Fatal("partial import committed")
	}
	d.fail = ""
	if err := p.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.version != 2 {
		t.Fatal("schema not initialized")
	}
	d.version = 3
	if err := p.migrate(context.Background()); err == nil {
		t.Fatal("future schema accepted")
	}
	if d.version != 3 {
		t.Fatal("future schema modified")
	}
}

func TestPostgresImportRejectsWrongVaultKey(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	seedEncryptedVault(t, s)
	s.Close()
	if err := os.WriteFile(filepath.Join(dir, "vault-key"), []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	d := &testDatabase{documents: map[string][]byte{}}
	_, err = openStore(dir, false, func() (*postgresStore, error) { return testBackend(d), nil })
	if err == nil || len(d.documents) != 0 || !d.closed {
		t.Fatal("wrong-key import did not fail atomically and release database")
	}
}

func TestPostgresIntegration(t *testing.T) {
	url := os.Getenv("ZELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ZELA_TEST_DATABASE_URL for isolated PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(databaseError("test connection", err))
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = admin.Close(cleanup)
	})
	name := newID("zela_test_")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(databaseError("test database creation", err))
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(databaseError("test database cleanup", err))
		}
	})
	cfg.Database = name
	open := func() (*postgresStore, error) { return openPostgresConfig(cfg.Copy()) }
	dir := t.TempDir()
	s, err := openStore(dir, false, open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	if _, err := openStore(t.TempDir(), false, open); err == nil {
		t.Fatal("second manager acquired database lock")
	}
	seedEncryptedVault(t, s)
	if err := s.update(func(v *State) error { v.Workers["wrk_test"] = Worker{ID: "wrk_test", Name: "persisted"}; return nil }); err != nil {
		t.Fatal(err)
	}
	rules := defaultAlertRules()
	rules.MemoryPercent = 70
	if err := s.Monitor.SetRules(rules); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openStore(dir, false, open)
	if err != nil {
		t.Fatal(err)
	}
	if s.snapshot().Workers["wrk_test"].Name != "persisted" {
		t.Fatal("worker lost after restart")
	}
	if _, err := s.Vault.Identity("key_test"); err != nil {
		t.Fatal(err)
	}
	_, actual := s.Monitor.Public()
	if actual != rules {
		t.Fatal("rules lost after restart")
	}
	if _, err := openStore(t.TempDir(), true, open); err == nil {
		t.Fatal("demo manager acquired live database")
	}
	if err := s.database.close(); err != nil {
		t.Fatal(err)
	}
	if s.CheckPersistence() == nil {
		t.Fatal("lost session undetected")
	}
	if err := s.update(func(v *State) error { delete(v.Workers, "wrk_test"); return nil }); err == nil {
		t.Fatal("write reconnected without cluster ownership")
	}
}

func TestPostgresMissingRecordAndErrorRedaction(t *testing.T) {
	d := &testDatabase{documents: map[string][]byte{}}
	p := testBackend(d)
	if err := p.save("cluster", []byte(`{}`)); err == nil {
		t.Fatal("missing state row silently recreated")
	}
	err := databaseError("write", &pgconn.PgError{Code: "42501", Message: "private-password-value", Detail: "private-query-value"})
	if strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "42501") {
		t.Fatal("unsafe database error")
	}
	if _, err := openPostgres("postgres://%invalid-secret"); err == nil || strings.Contains(err.Error(), "invalid-secret") {
		t.Fatal("invalid connection string exposed")
	}
}
