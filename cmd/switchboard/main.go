// Command switchboard is the main router process. It spawns one child
// process per organization, hands each its data key over loopback, listens
// on the public port and demultiplexes requests to the children.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/router"
)

func main() {
	defaultConfig := os.Getenv("SWITCHBOARD_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "config.json"
	}
	configPath := flag.String("config", defaultConfig, "path to router config JSON (or SWITCHBOARD_CONFIG)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("switchboard: %v", err)
	}
	srv, err := router.New(cfg)
	if err != nil {
		log.Fatalf("switchboard: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.StartChildren(ctx); err != nil {
		srv.StopChildren()
		log.Fatalf("switchboard: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
		srv.StopChildren()
	}()

	log.Printf("switchboard: router listening on %s (%d orgs)", cfg.Listen, len(cfg.Orgs))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("switchboard: %v", err)
	}
}
