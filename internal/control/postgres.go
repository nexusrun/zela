package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// One dedicated session owns both the lock and every database operation. Never
// reconnect this session: losing it loses ownership of the cluster.
type postgresStore struct {
	mu          databaseMutex
	ha          bool
	lost        chan struct{}
	lostOnce    sync.Once
	conn        postgresConnection
	initial     map[string][]byte
	initialized bool
	// epoch increases on every HA leadership acquisition; workers reject lower ones.
	epoch int64
}

// A health probe must not wait indefinitely behind a state write.
type databaseMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *databaseMutex) init() {
	m.once.Do(func() { m.token = make(chan struct{}, 1); m.token <- struct{}{} })
}
func (m *databaseMutex) Lock() { m.init(); <-m.token }
func (m *databaseMutex) LockContext(ctx context.Context) error {
	m.init()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		return nil
	}
}
func (m *databaseMutex) Unlock() { m.token <- struct{}{} }

var ErrClusterOwned = errors.New("PostgreSQL database is already in use by another control plane")

func (p *postgresStore) enableHA() { p.ha = true; p.lost = make(chan struct{}) }
func (p *postgresStore) fence() {
	if p.ha {
		p.lostOnce.Do(func() { close(p.lost) })
	}
}
func (p *postgresStore) fenced() bool {
	if !p.ha {
		return false
	}
	select {
	case <-p.lost:
		return true
	default:
		return false
	}
}

type postgresConnection interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Ping(context.Context) error
	Close(context.Context) error
}

const databaseTimeout = 10 * time.Second

func databaseError(action string, err error) error {
	// Driver errors may include the DSN, passwords, or query parameter values.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("PostgreSQL %s failed (SQLSTATE %s)", action, pgErr.Code)
	}
	return fmt.Errorf("PostgreSQL %s failed; check connectivity, credentials, TLS, and database permissions", action)
}

func openPostgres(url string) (*postgresStore, error) {
	return openPostgresContext(context.Background(), url)
}

func openPostgresContext(parent context.Context, url string) (*postgresStore, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid ZELA_DATABASE_URL")
	}
	return openPostgresConfigContext(parent, cfg)
}

func openPostgresHAContext(parent context.Context, url string) (*postgresStore, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid ZELA_DATABASE_URL")
	}
	// Bound how long the server retains a dead TCP session and its advisory lock.
	cfg.RuntimeParams["tcp_keepalives_idle"] = "10"
	cfg.RuntimeParams["tcp_keepalives_interval"] = "5"
	cfg.RuntimeParams["tcp_keepalives_count"] = "3"
	cfg.RuntimeParams["tcp_user_timeout"] = "15000"
	p, err := openPostgresConfigContext(parent, cfg)
	if err != nil {
		return p, err
	}
	p.enableHA()
	ctx, cancel := context.WithTimeout(parent, databaseTimeout)
	defer cancel()
	// Taken while holding the cluster lock, so epochs are strictly increasing.
	if err := p.conn.QueryRow(ctx, "UPDATE zela.leadership SET epoch = epoch + 1 WHERE singleton = true RETURNING epoch").Scan(&p.epoch); err != nil {
		_ = p.close()
		return nil, databaseError("leadership epoch", err)
	}
	return p, nil
}

func openPostgresConfig(cfg *pgx.ConnConfig) (*postgresStore, error) {
	return openPostgresConfigContext(context.Background(), cfg)
}
func openPostgresConfigContext(parent context.Context, cfg *pgx.ConnConfig) (*postgresStore, error) {
	if cfg.Database == "" {
		return nil, fmt.Errorf("ZELA_DATABASE_URL must specify an existing database")
	}
	cfg.ConnectTimeout = databaseTimeout
	cfg.RuntimeParams["application_name"] = "zela"
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, databaseError("connection", err)
	}
	p := &postgresStore{conn: conn, initial: map[string][]byte{}}
	ok := false
	defer func() {
		if !ok {
			_ = p.close()
		}
	}()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_lock(1129598032, 1)").Scan(&locked); err != nil {
		return nil, databaseError("cluster lock", err)
	}
	if !locked {
		return nil, ErrClusterOwned
	}
	if err := p.migrate(ctx); err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, "SELECT name, payload FROM zela.state")
	if err != nil {
		return nil, databaseError("state load", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var payload []byte
		if err := rows.Scan(&name, &payload); err != nil {
			return nil, databaseError("state load", err)
		}
		p.initial[name] = payload
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("state load", err)
	}
	if len(p.initial) != 0 {
		for _, name := range []string{"cluster", "vault", "monitoring"} {
			if _, ok := p.initial[name]; !ok {
				return nil, fmt.Errorf("PostgreSQL state is incomplete: missing %s; restore a complete backup", name)
			}
		}
		p.initialized = true
	}
	ok = true
	return p, nil
}

func (p *postgresStore) migrate(ctx context.Context) error {
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return databaseError("migration", err)
	}
	defer tx.Rollback(ctx)
	for _, query := range []string{
		"CREATE SCHEMA IF NOT EXISTS zela",
		"CREATE TABLE IF NOT EXISTS zela.schema_version (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), version integer NOT NULL)",
		"INSERT INTO zela.schema_version(singleton, version) VALUES (true, 0) ON CONFLICT DO NOTHING",
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return databaseError("migration", err)
		}
	}
	var version int
	if err := tx.QueryRow(ctx, "SELECT version FROM zela.schema_version WHERE singleton = true FOR UPDATE").Scan(&version); err != nil {
		return databaseError("migration", err)
	}
	if version > 2 || version < 0 {
		return fmt.Errorf("unsupported PostgreSQL schema version %d; use a compatible control-plane binary", version)
	}
	if version == 0 {
		if _, err := tx.Exec(ctx, `CREATE TABLE zela.state (
			name text PRIMARY KEY CHECK (name IN ('cluster', 'vault', 'monitoring')),
			payload jsonb NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return databaseError("migration", err)
		}
		if _, err := tx.Exec(ctx, "UPDATE zela.schema_version SET version = 1 WHERE singleton = true"); err != nil {
			return databaseError("migration", err)
		}
		version = 1
	}
	if version == 1 {
		for _, query := range []string{
			"CREATE TABLE zela.leadership (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), epoch bigint NOT NULL)",
			"INSERT INTO zela.leadership(singleton, epoch) VALUES (true, 0)",
			"UPDATE zela.schema_version SET version = 2 WHERE singleton = true",
		} {
			if _, err := tx.Exec(ctx, query); err != nil {
				return databaseError("migration", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("migration commit", err)
	}
	return nil
}

func readState(p *postgresStore, name, path string) ([]byte, error) {
	if p != nil && p.initialized {
		return p.initial[name], nil
	}
	return os.ReadFile(path)
}

// Commit all imported state together, after vault decryption and validation.
func (p *postgresStore) initialize(state State, records []secretRecord, monitoring monitorData) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.initialized {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return databaseError("initialization", err)
	}
	defer tx.Rollback(ctx)
	for _, item := range []struct {
		name  string
		value any
	}{{"cluster", state}, {"vault", records}, {"monitoring", monitoring}} {
		b, err := json.Marshal(item.value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO zela.state(name, payload) VALUES ($1, $2::jsonb)", item.name, string(b)); err != nil {
			return databaseError("initialization", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("initialization commit", err)
	}
	p.initialized = true
	return nil
}

func (p *postgresStore) save(name string, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	if p.fenced() {
		return fmt.Errorf("PostgreSQL leadership is fenced")
	}
	result, err := p.conn.Exec(ctx, "UPDATE zela.state SET payload = $2::jsonb, updated_at = now() WHERE name = $1", name, string(b))
	if err != nil {
		p.fence()
		return databaseError("state write", err)
	}
	if result.RowsAffected() != 1 {
		p.fence()
		return fmt.Errorf("PostgreSQL state record %s is missing; restore a complete backup", name)
	}
	return nil
}

func (p *postgresStore) check() error {
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	return p.checkContext(ctx)
}
func (p *postgresStore) checkContext(ctx context.Context) error {
	if err := p.mu.LockContext(ctx); err != nil {
		// A slow state write holds the session; that is not lost ownership. The
		// heartbeat fences if verification stays stale past the readiness window.
		return databaseError("health check", err)
	}
	defer p.mu.Unlock()
	if p.fenced() {
		return fmt.Errorf("PostgreSQL leadership is fenced")
	}
	if p.ha {
		var owned bool
		err := p.conn.QueryRow(ctx, `SELECT NOT pg_catalog.pg_is_in_recovery()
 AND pg_catalog.current_setting('transaction_read_only') = 'off'
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_locks
 WHERE locktype = 'advisory' AND pid = pg_catalog.pg_backend_pid()
 AND classid = 1129598032::oid AND objid = 1::oid AND objsubid = 2 AND granted
 AND database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = pg_catalog.current_database()))`).Scan(&owned)
		if err != nil {
			p.fence()
			return databaseError("leadership check", err)
		}
		if !owned {
			p.fence()
			return fmt.Errorf("PostgreSQL session no longer owns writable cluster leadership")
		}
	} else if err := p.conn.Ping(ctx); err != nil {
		return databaseError("health check", err)
	}
	return nil
}

func (p *postgresStore) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.conn.Close(ctx)
}
