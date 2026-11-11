package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func validConfigJSON() string {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	wrapped := base64.StdEncoding.EncodeToString(make([]byte, 72))
	return `{
	  "listen": "127.0.0.1:8080",
	  "root_key": "` + rootKey + `",
	  "child_binary": "/usr/local/bin/switchboard-org",
	  "orgs": [
	    {
	      "id": "acme",
	      "name": "Acme",
	      "child_addr": "127.0.0.1:9001",
	      "wrapped_data_key": "` + wrapped + `",
	      "storage": {"db": "/var/lib/switchboard/acme/org.db", "archive_root": "/var/lib/switchboard/acme/archive"}
	    }
	  ]
	}`
}

func orgJSON(id string) string {
	wrapped := base64.StdEncoding.EncodeToString(make([]byte, 72))
	return `{"id": "` + id + `", "name": "N", "child_addr": "127.0.0.1:9001", "wrapped_data_key": "` + wrapped + `", "storage": {"db": "/d", "archive_root": "/a"}}`
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfigJSON()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Org("acme") == nil || cfg.Org("nope") != nil {
		t.Fatalf("Org lookup broken: %+v", cfg.Orgs)
	}
	if cfg.ChildBinary != "/usr/local/bin/switchboard-org" {
		t.Fatalf("ChildBinary = %q", cfg.ChildBinary)
	}
}

func TestLoadInvalid(t *testing.T) {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	badRootKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	wrap := func(orgs string) string {
		return `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [` + orgs + `]}`
	}
	cases := map[string]string{
		"bad json":      `{`,
		"bad listen":    `{"listen": "noport", "root_key": "` + rootKey + `", "orgs": []}`,
		"short rootkey": `{"listen": "127.0.0.1:8080", "root_key": "` + badRootKey + `", "orgs": [` + orgJSON("a") + `]}`,
		"dup org":       wrap(orgJSON("a") + `,` + orgJSON("a")),
		"bad org id":    wrap(orgJSON("Bad_ID")),
		"no storage": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"a","name":"A","child_addr":"127.0.0.1:1","wrapped_data_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 72)) + `","storage":{"db":"","archive_root":""}}]}`,
		"no wrapped key": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"a","name":"A","child_addr":"127.0.0.1:1","storage":{"db":"/d","archive_root":"/a"}}]}`,
		"bad wrapped key": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"a","name":"A","child_addr":"127.0.0.1:1","wrapped_data_key":"!!!","storage":{"db":"/d","archive_root":"/a"}}]}`,
	}
	for name, content := range cases {
		if _, err := Load(writeTemp(t, content)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := writeTemp(t, validConfigJSON())
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Orgs[0].Name = "Renamed"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Orgs[0].Name != "Renamed" || reloaded.Orgs[0].WrappedDataKey == "" {
		t.Fatalf("round trip lost data: %+v", reloaded.Orgs[0])
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved config perms = %o, want 600", fi.Mode().Perm())
	}
}
