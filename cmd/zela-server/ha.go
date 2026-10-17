package main

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/zela-dev/zela/internal/control"
)

func runHA(cfg control.HAConfig, addr, cert, key string) error {
	manager, err := control.NewHAManager(cfg)
	if err != nil {
		return err
	}
	defer manager.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manager.Start(ctx)
	srv := &http.Server{Addr: addr, Handler: manager, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
		}
	}()
	scheme := "http"
	if cert != "" {
		scheme = "https"
	}
	fmt.Printf("Zela HA manager %s: %s://%s\n", cfg.ManagerID, scheme, addr)
	fmt.Println("Readiness: /api/ready (200 only on the active manager); liveness: /api/health")
	if cert != "" {
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	stop()
	manager.Close()
	<-shutdownDone
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
