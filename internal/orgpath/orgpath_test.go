package orgpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDerivedPaths(t *testing.T) {
	if got := DBPath("/var/lib/switchboard", "acme"); got != "/var/lib/switchboard/acme/org.db" {
		t.Fatalf("DBPath = %q", got)
	}
	if got := ArchiveRoot("/var/lib/switchboard", "acme"); got != "/var/lib/switchboard/acme/archive" {
		t.Fatalf("ArchiveRoot = %q", got)
	}
}

func TestExistsAndDiscover(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir, "acme") {
		t.Fatal("org DB reported existing before creation")
	}
	// Missing data dir: no orgs, no error.
	if ids, err := Discover(filepath.Join(dir, "nope")); err != nil || len(ids) != 0 {
		t.Fatalf("Discover(missing) = %v, %v", ids, err)
	}

	mk := func(orgID string, withDB bool) {
		dbPath := DBPath(dir, orgID)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if withDB {
			if err := os.WriteFile(dbPath, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("acme", true)
	mk("beta", true)
	mk("Bad_ID", true) // invalid org id: not an org
	mk("gamma", false) // valid id but no org.db: not an org
	if err := os.WriteFile(filepath.Join(dir, "delta.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !Exists(dir, "acme") || Exists(dir, "gamma") || Exists(dir, "delta") {
		t.Fatal("Exists broken")
	}
	ids, err := Discover(dir)
	if err != nil || len(ids) != 2 || ids[0] != "acme" || ids[1] != "beta" {
		t.Fatalf("Discover = %v, %v", ids, err)
	}
}
