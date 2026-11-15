// Package orgpath defines the per-org storage layout: every path is derived
// from the base data dir (the same for all orgs, part of the global config)
// and the org id. An org exists iff its org DB file exists at
// DBPath(dataDir, orgID).
//
// This package is deliberately free of any config-file dependency so the org
// child process can use it: the router passes it only the base data dir and
// the org id (startup flags), and the child derives its own paths.
package orgpath

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/jrsap/switchboard/internal/apikey"
)

// DBPath derives the org DB path from the base data dir and the org id.
// The org exists iff this file exists.
func DBPath(dataDir, orgID string) string {
	return filepath.Join(dataDir, orgID, "org.db")
}

// ArchiveRoot derives the archive root from the base data dir and the org id.
func ArchiveRoot(dataDir, orgID string) string {
	return filepath.Join(dataDir, orgID, "archive")
}

// Exists reports whether the org DB for orgID exists under dataDir.
func Exists(dataDir, orgID string) bool {
	fi, err := os.Stat(DBPath(dataDir, orgID))
	return err == nil && !fi.IsDir()
}

// Discover returns the ids of all orgs found under dataDir — subdirectories
// named by a valid org id that contain an org.db file — sorted. A missing
// dataDir yields no orgs, not an error.
func Discover(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && apikey.ValidOrgID(e.Name()) && Exists(dataDir, e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}
