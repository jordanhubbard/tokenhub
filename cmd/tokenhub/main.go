package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/app"
)

// version is set at build time via -ldflags.
var version = "dev"

// runHealthCheck performs an HTTP health check against the given address.
// addr should be in the form ":port" or "host:port".
func runHealthCheck(addr string) error {
	resp, err := http.Get(fmt.Sprintf("http://localhost%s/healthz", addr))
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}
	return nil
}

func main() {
	// Built-in health check mode for Docker HEALTHCHECK (distroless has no curl).
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		addr := os.Getenv("TOKENHUB_LISTEN_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		if err := runHealthCheck(addr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	log.Printf("tokenhub version %s", version)
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	srv, err := app.NewServer(cfg)
	if err != nil {
		log.Fatalf("server init error: %v", err)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      300 * time.Second, // allow long LLM streaming responses
	}

	go func() {
		log.Printf("tokenhub listening on %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen error: %v", err)
		}
	}()

	// SIGHUP: hot-reload configuration without restarting.
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	go func() {
		for range reload {
			log.Printf("SIGHUP received, reloading configuration...")
			newCfg, err := app.LoadConfig()
			if err != nil {
				log.Printf("config reload error: %v (keeping current config)", err)
				continue
			}
			srv.Reload(newCfg)
		}
	}()

	// Graceful shutdown: drain in-flight requests, then close resources.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down (draining in-flight requests)...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	if err := srv.Close(); err != nil {
		log.Printf("server close error: %v", err)
	}
	log.Printf("shutdown complete")
}
