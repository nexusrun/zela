// worker exposes authenticated Docker Engine access and host monitoring.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/zela-dev/zela/internal/storageprobe"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/telemetry"
	"github.com/zela-dev/zela/internal/version"
	"github.com/zela-dev/zela/internal/workerproxy"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "storage-probe" {
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		readOnly, err := strconv.ParseBool(os.Args[3])
		if err != nil {
			os.Exit(2)
		}
		if err := storageprobe.Run("/zela-volume", os.Args[2], readOnly); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	addr := flag.String("listen", "127.0.0.1:9091", "private interface to listen on")
	socket := flag.String("socket", "/var/run/docker.sock", "Docker Unix socket")
	cert := flag.String("tls-cert", "", "PEM certificate")
	key := flag.String("tls-key", "", "PEM private key")
	allowHTTP := flag.Bool("allow-http", false, "allow plaintext transport on a trusted private network")
	edgeCert := flag.String("edge-tls-cert", "", "PEM certificate for the cluster edge proxy's HTTPS listener (may be a wildcard)")
	edgeKey := flag.String("edge-tls-key", "", "PEM private key for --edge-tls-cert")
	stateDir := flag.String("state-dir", "/var/lib/zela-worker", "private directory for the cluster-network key and config")
	diskPaths := flag.String("metrics-disk-paths", "/,/var/lib", "comma-separated filesystem paths to monitor on this Linux host")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("zela-worker " + version.Version)
		return
	}
	token := os.Getenv("ZELA_WORKER_TOKEN")
	if len(token) < 32 {
		log.Fatal("ZELA_WORKER_TOKEN must contain at least 32 characters")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if *cert == "" && !*allowHTTP && (ip == nil || !ip.IsLoopback()) {
		log.Fatal("remote workers need TLS (--tls-cert/--tls-key), or explicit --allow-http on a trusted network")
	}
	proxy := workerproxy.DockerProxy(&http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
	}})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	paths := []string{}
	for _, p := range strings.Split(*diskPaths, ",") {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "/") {
			log.Fatal("metrics disk paths must be absolute")
		}
		paths = append(paths, p)
	}
	collector := telemetry.NewCollector(&http.Client{Transport: proxy.Transport, Timeout: 3 * time.Second}, "http://docker", paths)
	sam := &telemetry.Sampler{Collector: collector}
	go sam.Run(ctx)
	// The cluster network is configured by the control plane and re-applied
	// locally, so it keeps working while managers are unavailable.
	network := clusternet.NewAgent(*stateDir, &http.Client{Transport: proxy.Transport, Timeout: 30 * time.Second})
	network.EdgeCertFile, network.EdgeKeyFile = *edgeCert, *edgeKey
	go network.Run(ctx, 30*time.Second)
	routes := http.NewServeMux()
	routes.Handle("GET /zela/metrics", sam)
	routes.Handle("POST /zela/storage/verify", &storageprobe.Handler{Docker: &http.Client{Transport: proxy.Transport, Timeout: 20 * time.Second}})
	routes.Handle("GET /zela/network", network)
	routes.Handle("PUT /zela/network", network)
	routes.Handle("/", proxy)
	handler := workerproxy.Handler(token, routes, proxy)
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 10 * time.Minute}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	fmt.Printf("Docker worker listening on %s\n", *addr)
	if *cert != "" {
		err = srv.ListenAndServeTLS(*cert, *key)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
