package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func validConfigJSON() string {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return `{
	  "listen": "127.0.0.1:8080",
	  "root_key": "` + rootKey + `",
	  "orgs": [
	    {
	      "id": "acme",
	      "name": "Acme",
	      "child_addr": "127.0.0.1:9001",
	      "storage": {"db": "/var/lib/switchboard/acme/org.db", "archive_root": "/var/lib/switchboard/acme/archive"}
	    }
	  ]
	}`
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
}

func TestLoadInvalid(t *testing.T) {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	badRootKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	cases := map[string]string{
		"bad json":      `{`,
		"bad listen":    `{"listen": "noport", "root_key": "` + rootKey + `", "orgs": []}`,
		"short rootkey": `{"listen": "127.0.0.1:8080", "root_key": "` + badRootKey + `", "orgs": [{"id":"a","name":"A","child_addr":"127.0.0.1:1","storage":{"db":"/d","archive_root":"/a"}}]}`,
		"no orgs":       `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": []}`,
		"dup org": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"a","name":"A","child_addr":"127.0.0.1:1","storage":{"db":"/d","archive_root":"/a"}},
			{"id":"a","name":"B","child_addr":"127.0.0.1:2","storage":{"db":"/d2","archive_root":"/a2"}}]}`,
		"bad org id": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"Bad_ID","name":"A","child_addr":"127.0.0.1:1","storage":{"db":"/d","archive_root":"/a"}}]}`,
		"no storage": `{"listen": "127.0.0.1:8080", "root_key": "` + rootKey + `", "orgs": [
			{"id":"a","name":"A","child_addr":"127.0.0.1:1","storage":{"db":"","archive_root":""}}]}`,
	}
	for name, content := range cases {
		if _, err := Load(writeTemp(t, content)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
