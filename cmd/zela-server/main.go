package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zela-dev/zela/internal/control"
	"github.com/zela-dev/zela/internal/version"
)

func main() {
	ha := flag.Bool("ha", false, "enable PostgreSQL-coordinated active/standby managers")
	managerID := flag.String("manager-id", os.Getenv("ZELA_MANAGER_ID"), "unique HA manager name (defaults to hostname)")
	takeoverDelay := flag.Duration("ha-takeover-delay", 10*time.Second, "settling time after acquiring HA leadership (5s–1m)")
	addr := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	data := flag.String("data", "./data", "private state directory")
	demo := flag.Bool("demo", false, "use simulated Docker workers")
	allowHTTP := flag.Bool("allow-http-workers", false, "permit HTTP agents on a trusted private network")
	cert := flag.String("tls-cert", "", "PEM certificate for HTTPS")
	key := flag.String("tls-key", "", "PEM private key for HTTPS")
	workerBinaries := flag.String("worker-binaries", "./bin", "directory containing zela-worker-linux-amd64 and zela-worker-linux-arm64")
	clusterNetwork := flag.String("cluster-network", os.Getenv("ZELA_CLUSTER_NETWORK"), "enable the encrypted cluster network on this private IPv4 /16, e.g. 10.200.0.0/16 (empty disables it)")
	edgeHTTP := flag.Int("edge-http-port", envInt("ZELA_EDGE_HTTP_PORT"), "edge proxy HTTP port on every worker (requires --cluster-network; 0 disables)")
	edgeHTTPS := flag.Int("edge-https-port", envInt("ZELA_EDGE_HTTPS_PORT"), "edge proxy HTTPS port on every worker (workers need --edge-tls-cert/--edge-tls-key; 0 disables)")
	edgeRedirect := flag.Bool("edge-redirect-https", os.Getenv("ZELA_EDGE_REDIRECT_HTTPS") == "true", "redirect edge HTTP to HTTPS for hosts that have a certificate")
	acmeEmail := flag.String("acme-email", os.Getenv("ZELA_ACME_EMAIL"), "enable automatic edge certificates (ACME HTTP-01) with this account email; needs --edge-http-port 80")
	acmeDirectory := flag.String("acme-directory", envOr("ZELA_ACME_DIRECTORY", control.LetsEncryptDirectory), "ACME directory URL (default Let's Encrypt production)")
	clusterNetworkPort := flag.Int("cluster-network-port", 51820, "UDP port for cluster-network WireGuard traffic on workers")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("zela-server " + version.Version)
		return
	}
	log.Printf("Zela %s", version.Version)
	networkingExplicit := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "cluster-network", "cluster-network-port", "edge-http-port", "edge-https-port", "edge-redirect-https":
			networkingExplicit = true
		}
	})
	for _, name := range []string{"ZELA_CLUSTER_NETWORK", "ZELA_EDGE_HTTP_PORT", "ZELA_EDGE_HTTPS_PORT", "ZELA_EDGE_REDIRECT_HTTPS"} {
		if os.Getenv(name) != "" {
			networkingExplicit = true
		}
	}
	networkStartup := control.NetworkConfiguration{Enabled: *clusterNetwork != "", CIDR: *clusterNetwork, Port: *clusterNetworkPort, EdgeHTTPPort: *edgeHTTP, EdgeHTTPSPort: *edgeHTTPS, RedirectHTTPS: *edgeRedirect}
	httpConfig := control.HTTPConfig{Token: strings.TrimSpace(os.Getenv("ZELA_ADMIN_TOKEN")), AllowHTTPWorkers: *allowHTTP, MCPPublicURL: strings.TrimSpace(os.Getenv("ZELA_MCP_PUBLIC_URL"))}
	list := func(name string) []string {
		var out []string
		for _, v := range strings.Split(os.Getenv(name), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	httpConfig.MCPRedirectURIs = list("ZELA_MCP_REDIRECT_URIS")
	httpConfig.MCPProvisionHosts = list("ZELA_MCP_PROVISION_HOSTS")
	httpConfig.MCPTrustedProxies = list("ZELA_MCP_TRUSTED_PROXIES")
	if err := control.ValidateMCPConfig(httpConfig); err != nil {
		log.Fatal(err)
	}
	if *demo && *data == "./data" {
		*data = "./data-demo"
	}
	databaseURL := strings.TrimSpace(os.Getenv("ZELA_DATABASE_URL"))
	if *ha {
		if *demo {
			log.Fatal("HA is available only for real PostgreSQL-backed clusters")
		}
		if *managerID == "" {
			*managerID, _ = os.Hostname()
		}
		if err := runHA(control.HAConfig{DataDir: *data, DatabaseURL: databaseURL, ManagerID: *managerID, TakeoverDelay: *takeoverDelay,
			HTTP: httpConfig,
			Configure: func(c *control.Controller) error {
				configureProvisioner(c, *workerBinaries, *data, *clusterNetwork, *clusterNetworkPort, *edgeHTTP, *edgeHTTPS)
				if err := c.ApplyStartupNetwork(networkStartup, networkingExplicit); err != nil {
					return err
				}
				return c.SetACME(control.ACMEConfig{Email: *acmeEmail, Directory: *acmeDirectory})
			}}, *addr, *cert, *key); err != nil {
			log.Fatal(err)
		}
		return
	}
	store, err := control.OpenStoreWithDatabase(*data, *demo, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	token := strings.TrimSpace(os.Getenv("ZELA_ADMIN_TOKEN"))
	tokenPath := filepath.Join(*data, "admin-token")
	if token == "" {
		b, err := os.ReadFile(tokenPath)
		if os.IsNotExist(err) {
			var secret [32]byte
			if _, err := rand.Read(secret[:]); err != nil {
				log.Fatal(err)
			}
			token = hex.EncodeToString(secret[:])
			if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
				log.Fatal(err)
			}
		} else if err != nil {
			log.Fatal(err)
		} else {
			token = strings.TrimSpace(string(b))
		}
		absolute, _ := filepath.Abs(tokenPath)
		log.Printf("Sign-in token is stored in %s (mode 0600)", absolute)
	}
	if len(token) < 32 {
		log.Fatal("ZELA_ADMIN_TOKEN must contain at least 32 characters")
	}
	var factory control.Factory
	if *demo {
		factory, err = control.DemoFactory(store)
		if err != nil {
			log.Fatal(err)
		}
	}
	c := control.New(store, factory)
	configureProvisioner(c, *workerBinaries, *data, *clusterNetwork, *clusterNetworkPort, *edgeHTTP, *edgeHTTPS)
	if !*demo || *clusterNetwork != "" {
		if err := c.ApplyStartupNetwork(networkStartup, networkingExplicit); err != nil {
			log.Fatal(err)
		}
		if err := c.SetACME(control.ACMEConfig{Email: *acmeEmail, Directory: *acmeDirectory}); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("State persistence: %s", func() string {
		if databaseURL != "" {
			return "postgresql"
		}
		return "json"
	}())
	c.StartPolling()
	httpConfig.Token = token
	srv := &http.Server{Addr: *addr, Handler: control.Handler(c, httpConfig), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	databaseFailure := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := store.CheckPersistence(); err != nil {
					databaseFailure <- err
					stop()
					return
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	scheme := "http"
	if *cert != "" {
		scheme = "https"
	}
	fmt.Printf("Zela cluster control: %s://%s\n", scheme, *addr)
	if *demo {
		fmt.Println("Demo mode: all Docker operations are simulated.")
	}
	if *cert != "" {
		err = srv.ListenAndServeTLS(*cert, *key)
	} else {
		err = srv.ListenAndServe()
	}
	c.Close()
	select {
	case failure := <-databaseFailure:
		log.Fatal(failure)
	default:
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

func configureProvisioner(c *control.Controller, binaries, data, network string, port, edgeHTTP, edgeHTTPS int) {
	_, sshErr := exec.LookPath("ssh")
	_, keygenErr := exec.LookPath("ssh-keygen")
	if sshErr == nil && keygenErr == nil {
		p := &control.SSHProvisioner{BinaryDir: binaries, TempRoot: filepath.Join(data, "ssh-tmp")}
		if network != "" {
			p.NetworkPort = port
			p.EdgePorts = []int{edgeHTTP, edgeHTTPS}
		}
		c.Provisioner = p
	} else {
		log.Print("SSH provisioning requires the OpenSSH client (ssh and ssh-keygen)")
	}
}
