package main

import (
	"context"
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

func main() {
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
