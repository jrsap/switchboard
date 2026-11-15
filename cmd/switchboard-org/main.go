// Command switchboard-org is one organization's child process. It is never
// given the router config file: the router spawns it with the org id, the
// base data dir, a loopback address and a spawn token as startup flags, and
// the child derives its own storage paths from the org id and the base data
// dir (<data-dir>/<org>/org.db, <data-dir>/<org>/archive/). It opens its org
// DB (the org exists iff that file exists), serves /healthz, and waits for
// the router's loopback handshake that delivers the unwrapped org data key
// before it serves anything else.
package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/apikey"
	"github.com/jrsap/switchboard/internal/child"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/orgpath"
)

func main() {
	orgID := flag.String("org", "", "org id (required)")
	addr := flag.String("addr", "", "loopback listen address host:port (required)")
	dataDir := flag.String("data-dir", "", "base directory for per-org storage, passed by the router (required)")
	spawnToken := flag.String("spawn-token", "", "token the router must present in the handshake (required)")
	flag.Parse()

	if *orgID == "" || *addr == "" || *dataDir == "" || *spawnToken == "" {
		log.Fatal("switchboard-org: -org, -addr, -data-dir and -spawn-token are required")
	}
	if !apikey.ValidOrgID(*orgID) {
		log.Fatalf("switchboard-org: invalid org id %q", *orgID)
	}

	// Derive this org's storage from the org id and the base data dir; the
	// org exists iff its org DB exists.
	dbPath := orgpath.DBPath(*dataDir, *orgID)
	archiveRoot := orgpath.ArchiveRoot(*dataDir, *orgID)
	if !orgpath.Exists(*dataDir, *orgID) {
		log.Fatalf("switchboard-org: org %q does not exist (no org DB at %s)", *orgID, dbPath)
	}

	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(dbPath))
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
		*orgID, *addr, dbPath, archiveRoot)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("switchboard-org: %v", err)
	}
}
