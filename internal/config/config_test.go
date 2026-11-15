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
	  "child_binary": "/usr/local/bin/switchboard-org",
	  "global_db": "/var/lib/switchboard/global.db",
	  "data_dir": "/var/lib/switchboard"
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
	if cfg.ChildBinary != "/usr/local/bin/switchboard-org" {
		t.Fatalf("ChildBinary = %q", cfg.ChildBinary)
	}
	if cfg.GlobalDB != "/var/lib/switchboard/global.db" || cfg.DataDir != "/var/lib/switchboard" {
		t.Fatalf("GlobalDB/DataDir = %q/%q", cfg.GlobalDB, cfg.DataDir)
	}
	if cfg.DataDirOrDefault() != "/var/lib/switchboard" {
		t.Fatalf("DataDirOrDefault = %q", cfg.DataDirOrDefault())
	}
}

func TestLoadInvalid(t *testing.T) {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	badRootKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	cases := map[string]string{
		"bad json":       `{`,
		"bad listen":     `{"listen": "noport", "root_key": "` + rootKey + `"}`,
		"missing listen": `{"root_key": "` + rootKey + `"}`,
		"short rootkey":  `{"listen": "127.0.0.1:8080", "root_key": "` + badRootKey + `"}`,
		"no rootkey":     `{"listen": "127.0.0.1:8080"}`,
		"bad rootkey":    `{"listen": "127.0.0.1:8080", "root_key": "!!!"}`,
	}
	for name, content := range cases {
		if _, err := Load(writeTemp(t, content)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestDataDirOrDefault(t *testing.T) {
	rootKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg, err := Load(writeTemp(t, `{"listen": "127.0.0.1:8080", "root_key": "`+rootKey+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDirOrDefault() != DefaultDataDir {
		t.Fatalf("DataDirOrDefault = %q, want %q", cfg.DataDirOrDefault(), DefaultDataDir)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := writeTemp(t, validConfigJSON())
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = "/elsewhere"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.DataDir != "/elsewhere" || reloaded.RootKey != cfg.RootKey {
		t.Fatalf("round trip lost data: %+v", reloaded)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved config perms = %o, want 600", fi.Mode().Perm())
	}
}
