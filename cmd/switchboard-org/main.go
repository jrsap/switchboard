// Command switchboard-org is one organization's child process. The router
// spawns it with the org id, loopback address, storage descriptor and a
// spawn token; the child opens its own org DB, serves /healthz, and waits
// for the router's loopback handshake that delivers the unwrapped org data
// key before it serves anything else.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/child"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
)

func main() {
	orgID := flag.String("org", "", "org id (required)")
	addr := flag.String("addr", "", "loopback listen address host:port (required)")
	dbPath := flag.String("db", "", "org DB path from the storage descriptor (required)")
	archiveRoot := flag.String("archive-root", "", "archive root from the storage descriptor (required)")
	spawnToken := flag.String("spawn-token", "", "token the router must present in the handshake (required)")
	flag.Parse()

	if *orgID == "" || *addr == "" || *dbPath == "" || *archiveRoot == "" || *spawnToken == "" {
		log.Fatal("switchboard-org: -org, -addr, -db, -archive-root and -spawn-token are required")
	}

	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(*dbPath))
	if err != nil {
		log.Fatalf("switchboard-org: open org db: %v", err)
	}
	db, err := orgdb.Open(sqlDB)
	if err != nil {
		log.Fatalf("switchboard-org: %v", err)
	}

	srv := child.New(*orgID, *spawnToken, db)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("switchboard-org: org %q listening on %s (db %s, archive root %s), waiting for handshake",
		*orgID, *addr, *dbPath, *archiveRoot)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("switchboard-org: %v", err)
	}
}
