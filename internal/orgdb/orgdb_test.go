package orgdb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/database"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(filepath.Join(t.TempDir(), "org.db")))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := Open(sqlDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func TestMeta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.GetMeta(MetaWrappedDataKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := db.SetMeta(MetaWrappedDataKey, "wrapped123"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	v, err := db.GetMeta(MetaWrappedDataKey)
	if err != nil || v != "wrapped123" {
		t.Fatalf("GetMeta = %q, %v", v, err)
	}
	if err := db.SetMeta(MetaWrappedDataKey, "wrapped456"); err != nil {
		t.Fatalf("SetMeta overwrite: %v", err)
	}
	if v, _ := db.GetMeta(MetaWrappedDataKey); v != "wrapped456" {
		t.Fatalf("want overwrite, got %q", v)
	}
}

func TestMembers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	admin, err := db.AddMember("alice", "admin")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if admin.Role != "admin" || admin.CreatedAt.IsZero() {
		t.Fatalf("unexpected member: %+v", admin)
	}
	if _, err := db.AddMember("bob", "member"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := db.AddMember("alice", "member"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate member: want ErrConflict, got %v", err)
	}
	if _, err := db.AddMember("carol", "superuser"); err == nil {
		t.Fatal("invalid role accepted")
	}
	members, err := db.ListMembers()
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 || members[0].Username != "alice" || members[1].Username != "bob" {
		t.Fatalf("unexpected members: %+v", members)
	}
	if _, err := db.GetMemberByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestProviders(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	p, err := db.AddProvider("openai", "https://api.openai.com/v1", "sealed-key-1")
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if p.ID == 0 || p.Name != "openai" || p.BaseURL != "https://api.openai.com/v1" || p.APIKey != "sealed-key-1" || p.CreatedAt.IsZero() {
		t.Fatalf("unexpected provider: %+v", p)
	}
	if _, err := db.AddProvider("openai", "https://other.example.com/v1", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate provider: want ErrConflict, got %v", err)
	}
	for _, bad := range []struct{ name, url string }{
		{"bad name", "https://api.openai.com/v1"},
		{"-lead", "https://api.openai.com/v1"},
		{"ok", "ftp://x"},
		{"ok", "https://"},
		{"ok", "notaurl"},
	} {
		if _, err := db.AddProvider(bad.name, bad.url, ""); err == nil {
			t.Errorf("AddProvider(%q, %q) accepted, want reject", bad.name, bad.url)
		}
	}
	if _, err := db.AddProvider("local", "http://127.0.0.1:11434/v1", ""); err != nil {
		t.Fatalf("AddProvider without key: %v", err)
	}
	if _, err := db.GetProviderByName("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := db.GetProviderByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	byID, err := db.GetProviderByID(p.ID)
	if err != nil || byID.Name != "openai" {
		t.Fatalf("GetProviderByID = %+v, %v", byID, err)
	}
	providers, err := db.ListProviders()
	if err != nil || len(providers) != 2 || providers[0].Name != "local" || providers[1].Name != "openai" {
		t.Fatalf("ListProviders = %+v, %v", providers, err)
	}
	if err := db.DeleteProvider(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}
	if err := db.DeleteProvider(p.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := db.GetProviderByName("openai"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
}

func TestModels(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	p, err := db.AddProvider("openai", "https://api.openai.com/v1", "sealed")
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	other, err := db.AddProvider("local", "http://127.0.0.1:11434/v1", "")
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	m, err := db.AddModel(p.ID, "gpt-4o", true, true, []string{"low", "high", "low"})
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if m.ProviderName != "openai" || m.Name != "gpt-4o" || !m.ToolCalling || !m.Vision {
		t.Fatalf("unexpected model: %+v", m)
	}
	if len(m.ReasoningLevels) != 2 || m.ReasoningLevels[0] != "low" || m.ReasoningLevels[1] != "high" {
		t.Fatalf("reasoning levels not deduped: %v", m.ReasoningLevels)
	}
	if _, err := db.AddModel(p.ID, "gpt-4o", false, false, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate model: want ErrConflict, got %v", err)
	}
	if _, err := db.AddModel(999, "x", false, false, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown provider: want ErrNotFound, got %v", err)
	}
	if _, err := db.AddModel(p.ID, "bad name", false, false, nil); err == nil {
		t.Fatal("invalid model name accepted")
	}
	if _, err := db.AddModel(p.ID, "ok-model", false, false, []string{"bad level"}); err == nil {
		t.Fatal("invalid reasoning level accepted")
	}

	m2, err := db.AddModel(other.ID, "llama3.1", false, false, nil)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if m2.ReasoningLevels == nil || len(m2.ReasoningLevels) != 0 {
		t.Fatalf("nil levels should normalize to empty slice: %#v", m2.ReasoningLevels)
	}

	got, err := db.GetModel(p.ID, "gpt-4o")
	if err != nil || got.ID != m.ID || !got.ToolCalling {
		t.Fatalf("GetModel = %+v, %v", got, err)
	}
	if _, err := db.GetModel(p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	all, err := db.ListModels()
	if err != nil || len(all) != 2 || all[0].ProviderName != "local" || all[1].Name != "gpt-4o" {
		t.Fatalf("ListModels = %+v, %v", all, err)
	}
	byProvider, err := db.ListModelsByProvider(p.ID)
	if err != nil || len(byProvider) != 1 || byProvider[0].Name != "gpt-4o" {
		t.Fatalf("ListModelsByProvider = %+v, %v", byProvider, err)
	}

	if err := db.DeleteModel(p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing model: want ErrNotFound, got %v", err)
	}
	if err := db.DeleteModel(p.ID, "gpt-4o"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}

	// Deleting the provider cascades to its models.
	if _, err := db.AddModel(p.ID, "gpt-4o-mini", false, true, []string{"none"}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if err := db.DeleteProvider(p.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	left, err := db.ListModels()
	if err != nil || len(left) != 1 || left[0].ProviderName != "local" {
		t.Fatalf("after cascade: %+v, %v", left, err)
	}
}

func TestReadMeta(t *testing.T) {
	// ReadMeta reads org_meta from an already-open DB without migrating.
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(filepath.Join(t.TempDir(), "org.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(MetaOrgName, "Acme"); err != nil {
		t.Fatal(err)
	}
	if v, err := ReadMeta(sqlDB, MetaOrgName); err != nil || v != "Acme" {
		t.Fatalf("ReadMeta = %q, %v", v, err)
	}
	if _, err := ReadMeta(sqlDB, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestNormalizeReasoningLevels(t *testing.T) {
	levels, err := NormalizeReasoningLevels([]string{"high", "low", "high", "medium"})
	if err != nil || len(levels) != 3 || levels[0] != "high" || levels[1] != "low" || levels[2] != "medium" {
		t.Fatalf("NormalizeReasoningLevels = %v, %v", levels, err)
	}
	many := make([]string, maxReasoningLevels+1)
	for i := range many {
		many[i] = "l" + strconv.Itoa(i)
	}
	if _, err := NormalizeReasoningLevels(many); err == nil {
		t.Fatal("too many levels accepted")
	}
	if _, err := NormalizeReasoningLevels([]string{""}); err == nil {
		t.Fatal("empty level accepted")
	}
}

func TestAPIKeys(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	m, err := db.AddMember("alice", "admin")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := db.AddAPIKey(m.ID, "ci", "hash1"); err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	if _, err := db.AddAPIKey(m.ID, "", "hash2"); err != nil {
		t.Fatalf("AddAPIKey empty label: %v", err)
	}
	if _, err := db.AddAPIKey(m.ID, "dup", "hash1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate hash: want ErrConflict, got %v", err)
	}
	if _, err := db.AddAPIKey(999, "x", "hash3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown member: want ErrNotFound, got %v", err)
	}
	if _, err := db.AddAPIKey(m.ID, "x", ""); err == nil {
		t.Fatal("empty hash accepted")
	}

	keys, err := db.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 2 || keys[0].Label != "ci" || keys[1].Label != "" || keys[0].Username != "alice" {
		t.Fatalf("unexpected keys: %+v", keys)
	}
	if keys[0].CreatedAt.IsZero() || keys[0].Revoked() {
		t.Fatalf("unexpected key fields: %+v", keys[0])
	}

	got, err := db.GetAPIKeyByHash("hash2")
	if err != nil || got.ID != keys[1].ID {
		t.Fatalf("GetAPIKeyByHash = %+v, %v", got, err)
	}
	if _, err := db.GetAPIKeyByHash("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if err := db.RevokeAPIKey(keys[0].ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	revoked, err := db.GetAPIKeyByID(keys[0].ID)
	if err != nil || !revoked.Revoked() || revoked.RevokedAt == nil {
		t.Fatalf("revoked key: %+v, %v", revoked, err)
	}
	if err := db.RevokeAPIKey(keys[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double revoke: want ErrNotFound, got %v", err)
	}
	if err := db.RevokeAPIKey(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key revoke: want ErrNotFound, got %v", err)
	}
}

func TestAliases(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	p1, err := db.AddProvider("openai", "https://api.openai.com/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := db.AddProvider("anthropic", "https://api.anthropic.com/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddModel(p1.ID, "gpt-4o", true, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddModel(p1.ID, "gpt-4o-mini", false, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddModel(p2.ID, "claude-sonnet", true, true, nil); err != nil {
		t.Fatal(err)
	}

	// Invalid names, empty entry list, malformed entries.
	if _, err := db.AddAlias("bad name!", []string{"openai/gpt-4o"}); err == nil {
		t.Fatal("invalid alias name accepted")
	}
	if _, err := db.AddAlias("big", nil); err == nil {
		t.Fatal("empty entry list accepted")
	}
	if _, err := db.AddAlias("big", []string{"openai"}); err == nil {
		t.Fatal("entry without model accepted")
	}
	if _, err := db.AddAlias("big", []string{"/gpt-4o"}); err == nil {
		t.Fatal("entry without provider accepted")
	}

	// Entries must reference existing providers and models.
	if _, err := db.AddAlias("big", []string{"nope/gpt-4o"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown provider: want ErrNotFound, got %v", err)
	}
	if _, err := db.AddAlias("big", []string{"openai/nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown model: want ErrNotFound, got %v", err)
	}
	if _, err := db.AddAlias("big", []string{"anthropic/gpt-4o"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("model on wrong provider: want ErrNotFound, got %v", err)
	}

	// Duplicate entries are rejected.
	if _, err := db.AddAlias("big", []string{"openai/gpt-4o", "openai/gpt-4o"}); err == nil {
		t.Fatal("duplicate entry accepted")
	}

	// Happy path: order is preserved as failover order.
	big, err := db.AddAlias("big", []string{"openai/gpt-4o", "anthropic/claude-sonnet", "openai/gpt-4o-mini"})
	if err != nil {
		t.Fatalf("AddAlias: %v", err)
	}
	if big.Name != "big" || big.CreatedAt.IsZero() || len(big.Entries) != 3 {
		t.Fatalf("unexpected alias: %+v", big)
	}
	want := []string{"openai/gpt-4o", "anthropic/claude-sonnet", "openai/gpt-4o-mini"}
	for i, e := range big.Entries {
		if e.Position != i || e.String() != want[i] {
			t.Fatalf("entry %d: %+v, want position %d %q", i, e, i, want[i])
		}
	}
	if _, err := db.AddAlias("big", []string{"openai/gpt-4o"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate alias: want ErrConflict, got %v", err)
	}

	// A model name may contain "/"; the cut is at the first "/".
	if _, err := db.AddModel(p1.ID, "org/team/model", false, false, nil); err != nil {
		t.Fatal(err)
	}
	slash, err := db.AddAlias("slashy", []string{"openai/org/team/model"})
	if err != nil {
		t.Fatalf("AddAlias with slash in model name: %v", err)
	}
	if len(slash.Entries) != 1 || slash.Entries[0].String() != "openai/org/team/model" {
		t.Fatalf("slash entry: %+v", slash.Entries)
	}

	// Lookups and listing.
	got, err := db.GetAliasByName("big")
	if err != nil || got.ID != big.ID || len(got.Entries) != 3 {
		t.Fatalf("GetAliasByName = %+v, %v", got, err)
	}
	gotByID, err := db.GetAliasByID(big.ID)
	if err != nil || gotByID.Name != "big" || gotByID.Entries[1].String() != "anthropic/claude-sonnet" {
		t.Fatalf("GetAliasByID = %+v, %v", gotByID, err)
	}
	if _, err := db.GetAliasByName("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	all, err := db.ListAliases()
	if err != nil || len(all) != 2 || all[0].Name != "big" || all[1].Name != "slashy" {
		t.Fatalf("ListAliases = %+v, %v", all, err)
	}
	if len(all[0].Entries) != 3 || all[0].Entries[0].Position != 0 || all[0].Entries[2].Position != 2 {
		t.Fatalf("ListAliases entry order: %+v", all[0].Entries)
	}

	// Providers/models referenced by an alias cannot be deleted.
	if err := db.DeleteProvider(p1.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProvider referenced: want ErrConflict, got %v", err)
	}
	if err := db.DeleteModel(p2.ID, "claude-sonnet"); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteModel referenced: want ErrConflict, got %v", err)
	}

	// Delete removes the alias and cascades to its entries.
	if err := db.DeleteAlias("big"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAliasByName("big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := db.DeleteAlias("big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
	// Now the providers/models can be deleted once no alias references them.
	if err := db.DeleteAlias("slashy"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProvider(p1.ID); err != nil {
		t.Fatalf("DeleteProvider after alias deletes: %v", err)
	}
	all, err = db.ListAliases()
	if err != nil || len(all) != 0 {
		t.Fatalf("ListAliases after deletes = %+v, %v", all, err)
	}
}
