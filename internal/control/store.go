package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
	"github.com/zela-dev/zela/internal/telemetry"
)

type Worker struct {
	DockerAPI    string             `json:"dockerAPI,omitempty"`
	Metrics      *telemetry.Sample  `json:"metrics,omitempty"`
	MetricsError string             `json:"metricsError,omitempty"`
	SSH          *sshclient.Config  `json:"ssh,omitempty"`
	Provisioned  bool               `json:"provisioned,omitempty"`
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Endpoint     string             `json:"endpoint"`
	Token        string             `json:"token,omitempty"`
	Region       string             `json:"region"`
	Mode         string             `json:"mode"`
	Status       string             `json:"status"`
	Error        string             `json:"error,omitempty"`
	Info         engine.Info        `json:"info"`
	Containers   []engine.Container `json:"containers"`
	LastSeen     time.Time          `json:"lastSeen"`
	// NetworkEndpoint overrides the address peers use for the cluster network,
	// e.g. a public IP when the management address is private or NATed.
	NetworkEndpoint string `json:"networkEndpoint,omitempty"`
	// FailedProbes counts consecutive failed inventory probes.
	FailedProbes int `json:"failedProbes,omitempty"`
}

// Route sends external requests for Host (empty: any host) under Path to the
// deployment's container Port, through the edge proxy on every worker.
type Route struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
	Port int    `json:"port"`
}
type Replica struct {
	ID       string `json:"id"`
	WorkerID string `json:"workerID"`
	Name     string `json:"name"`
	Image    string `json:"image,omitempty"`
}
type Deployment struct {
	AccessTokenID string        `json:"accessTokenID,omitempty"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Image         string        `json:"image"`
	Replicas      int           `json:"replicas"`
	WorkerIDs     []string      `json:"workerIDs"`
	Env           []string      `json:"env,omitempty"`
	Command       []string      `json:"command,omitempty"`
	Ports         []engine.Port `json:"ports"`
	MemoryMB      int64         `json:"memoryMB"`
	CPUs          float64       `json:"cpus"`
	DesiredState  string        `json:"desiredState"`
	Status        string        `json:"status"`
	Error         string        `json:"error,omitempty"`
	Tasks         []Replica     `json:"tasks"`
	// Routes publish the deployment through the cluster edge proxy.
	Routes []Route `json:"routes,omitempty"`
	// Evicted replicas were replaced after their worker went offline. They are
	// re-adopted or removed once that worker is reachable again.
	Evicted   []Replica `json:"evicted,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}
type OperationStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type Operation struct {
	AccessTokenID string          `json:"accessTokenID,omitempty"`
	Steps         []OperationStep `json:"steps,omitempty"`
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Status        string          `json:"status"`
	Detail        string          `json:"detail"`
	StartedAt     time.Time       `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt,omitempty"`
}
type State struct {
	MCP     *MCPStatus    `json:"mcp,omitempty"`
	OAuth   *oauthState   `json:"oauth,omitempty"`
	Network *NetworkState `json:"network,omitempty"`
	ACME    *acmeState    `json:"acme,omitempty"`
	// ACMECertificates is the public certificate status in API views only.
	ACMECertificates      []ACMECertificateStatus      `json:"acmeCertificates,omitempty"`
	HAConfigurationHash   string                       `json:"haConfigurationHash,omitempty"`
	HighAvailability      *HAStatus                    `json:"highAvailability,omitempty"`
	Notifications         []Notification               `json:"notifications"`
	UnreadNotifications   int                          `json:"unreadNotifications"`
	AccessTokens          map[string]accessTokenRecord `json:"accessTokens,omitempty"`
	Tokens                []AccessTokenMetadata        `json:"tokens,omitempty"`
	DockerContainers      map[string]DockerContainer   `json:"dockerContainers,omitempty"`
	DockerExecs           map[string]DockerExec        `json:"dockerExecs,omitempty"`
	Persistence           string                       `json:"persistence"`
	MonitoringError       string                       `json:"monitoringError,omitempty"`
	Alerts                []Alert                      `json:"alerts"`
	MonitoringRules       AlertRules                   `json:"monitoringRules"`
	Secrets               []SecretMetadata             `json:"secrets,omitempty"`
	ProvisioningAvailable bool                         `json:"provisioningAvailable"`
	Workers               map[string]Worker            `json:"workers"`
	Deployments           map[string]Deployment        `json:"deployments"`
	Operations            []Operation                  `json:"operations"`
	Demo                  bool                         `json:"demo"`
}

// Docker ownership and reservations survive restarts in either state backend.
// Container configuration and registry credentials are never stored here.
type DockerContainer struct {
	AccessTokenID string        `json:"accessTokenID,omitempty"`
	ID            string        `json:"id"`
	Name          string        `json:"name,omitempty"`
	WorkerID      string        `json:"workerID"`
	MemoryBytes   int64         `json:"memoryBytes"`
	NanoCPUs      int64         `json:"nanoCPUs"`
	Ports         []engine.Port `json:"ports,omitempty"`
}

type DockerExec struct {
	AccessTokenID string `json:"accessTokenID,omitempty"`
	WorkerID      string `json:"workerID"`
	ContainerID   string `json:"containerID"`
}
type Store struct {
	database *postgresStore
	haStatus func() HAStatus
	Monitor  *MonitorStore
	Vault    *Vault
	mu       sync.RWMutex
	path     string
	state    State
	lock     *os.File
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func copyState(s State) State {
	b, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}

func OpenStore(dir string, demo bool) (*Store, error) {
	return OpenStoreWithDatabase(dir, demo, "")
}

// OpenStoreWithDatabase initializes PostgreSQL before exposing any cluster operations.
// An empty URL retains the local JSON backend.
func OpenStoreWithDatabase(dir string, demo bool, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return openStore(dir, demo, nil)
	}
	return openStore(dir, demo, func() (*postgresStore, error) { return openPostgres(databaseURL) })
}

func openStore(dir string, demo bool, openDatabase func() (*postgresStore, error), validators ...func(*State) error) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("state directory is already in use by another control plane: %w", err)
	}
	var database *postgresStore
	ok := false
	defer func() {
		if !ok {
			if database != nil {
				_ = database.close()
			}
			lock.Close()
		}
	}()
	if openDatabase != nil {
		database, err = openDatabase()
		if err != nil {
			return nil, err
		}
	}
	s := &Store{database: database, path: filepath.Join(dir, "cluster.json"), state: State{Workers: map[string]Worker{}, Deployments: map[string]Deployment{}, Operations: []Operation{}, Demo: demo}}
	s.lock = lock
	b, err := readState(database, "cluster", s.path)
	if err == nil {
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("invalid cluster state: %w", err)
		}
		if s.state.Demo != demo {
			return nil, fmt.Errorf("use separate databases and data directories for real and demo clusters")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.state.Workers == nil {
		s.state.Workers = map[string]Worker{}
	}
	if s.state.Deployments == nil {
		s.state.Deployments = map[string]Deployment{}
	}
	if s.state.Operations == nil {
		s.state.Operations = []Operation{}
	}
	if s.state.HAConfigurationHash != "" && (database == nil || !database.ha) {
		return nil, fmt.Errorf("HA-configured cluster requires --ha and shared credentials")
	}
	for _, validate := range validators {
		if err := validate(&s.state); err != nil {
			return nil, err
		}
	}
	// Interrupted jobs cannot safely be replayed: Docker may have accepted a request.
	for i := range s.state.Operations {
		if s.state.Operations[i].Status == "running" {
			now := time.Now().UTC()
			s.state.Operations[i].Status = "failed"
			s.state.Operations[i].Detail = "Control plane restarted. Inspect recorded containers before retrying."
			s.state.Operations[i].FinishedAt = &now
			for j := range s.state.Operations[i].Steps {
				if s.state.Operations[i].Steps[j].Status == "running" {
					s.state.Operations[i].Steps[j].Status = "failed"
					s.state.Operations[i].Steps[j].Detail = "Interrupted by control-plane restart; inspect the host before retrying"
				}
			}
		}
	}
	for id, w := range s.state.Workers {
		if w.Status == "provisioning" {
			w.Status = "provision-failed"
			w.Mode = "paused"
			w.Error = "Provisioning interrupted by manager restart; inspect host and retry"
			s.state.Workers[id] = w
		}
	}
	for id, d := range s.state.Deployments {
		if d.Status == "deploying" || d.Status == "moving" || d.Status == "updating" {
			d.Status = "degraded"
			d.Error = "Operation interrupted; inspect replicas before retrying"
			s.state.Deployments[id] = d
		}
	}
	if s.Vault, err = openVault(dir, os.Getenv("ZELA_VAULT_KEY"), database); err != nil {
		return nil, err
	}
	if s.Monitor, err = openMonitorStore(dir, database); err != nil {
		return nil, err
	}
	s.state.Persistence = "json"
	if database != nil {
		s.state.Persistence = "postgresql"
		if err := database.initialize(s.state, s.Vault.records, s.Monitor.data); err != nil {
			return nil, err
		}
		database.initial = nil // Startup snapshot may contain credentials; do not retain duplicate copies.
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	if err := s.Monitor.InitializeNotifications(s.state, time.Now().UTC()); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	var dbErr error
	if s.database != nil {
		dbErr = s.database.close()
	}
	err := s.lock.Close()
	s.lock = nil
	if dbErr != nil {
		return dbErr
	}
	return err
}
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if s.database != nil {
		return s.database.save("cluster", b)
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".cluster-*")
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
	return os.Rename(f.Name(), s.path)
}
func (s *Store) update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := copyState(s.state)
	if err := fn(&s.state); err != nil {
		s.state = before
		return err
	}
	if err := s.save(); err != nil {
		s.state = before
		return fmt.Errorf("persist cluster state: %w", err)
	}
	return nil
}
func (s *Store) snapshot() State { s.mu.RLock(); defer s.mu.RUnlock(); return copyState(s.state) }
func (s *Store) public() State {
	v := s.snapshot()
	v.HAConfigurationHash = ""
	if s.haStatus != nil {
		status := s.haStatus()
		v.HighAvailability = &status
	}
	v.Tokens = tokenMetadata(v.AccessTokens)
	v.AccessTokens = nil
	v.OAuth = nil
	v.ACMECertificates = acmePublic(v.ACME)
	v.ACME = nil // account and certificate keys never leave the store
	v.Alerts, v.MonitoringRules = s.Monitor.Public()
	v.Notifications, v.UnreadNotifications = s.Monitor.Notifications()
	v.MonitoringError = s.Monitor.Error()
	v.Secrets = s.Vault.List()
	for id, w := range v.Workers {
		w.Token = ""
		w.DockerAPI = ""
		if !v.Demo {
			w.DockerAPI = "/docker/workers/" + w.ID
		}
		v.Workers[id] = w
	}
	return v
}

// CheckPersistence validates the original lock-owning session without reconnecting.
func (s *Store) CheckPersistence() error {
	if s.database == nil {
		return nil
	}
	return s.database.check()
}
