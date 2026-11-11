// Command switchboard is the main router process. It listens on the public
// port and demultiplexes requests to one child process per organization
// over loopback HTTP.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
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

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("switchboard: router listening on %s (%d orgs)", cfg.Listen, len(cfg.Orgs))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("switchboard: %v", err)
	}
}
