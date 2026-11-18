package apikey

import "testing"

func TestParseRoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		key, err := New("acme")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		orgID, secret, ok := Parse(key)
		if !ok || orgID != "acme" || secret == "" {
			t.Fatalf("Parse(%q) = %q, %q, %v", key, orgID, secret, ok)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, key := range []string{"", "acme_x", "sb_", "sb__x", "sb_acme", "sb_acme_", "sb_ACME_x", "sb_-bad_x"} {
		if _, _, ok := Parse(key); ok {
			t.Errorf("Parse(%q) accepted, want reject", key)
		}
	}
}

func TestNewRejectsBadOrg(t *testing.T) {
	if _, err := New("Not Valid"); err == nil {
		t.Fatal("expected error for invalid org id")
	}
}

func TestHashSecret(t *testing.T) {
	key, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, _ := Parse(key)
	h1 := HashSecret(secret)
	if h1 == secret || len(h1) != 64 {
		t.Fatalf("HashSecret must be a sha-256 hex digest, got %q", h1)
	}
	if HashSecret(secret) != h1 {
		t.Fatal("HashSecret not deterministic")
	}
	if HashSecret("other") == h1 {
		t.Fatal("HashSecret collision for different secrets")
	}
}

func TestPrefix(t *testing.T) {
	if Prefix("acme") != "sb_acme" {
		t.Fatalf("Prefix(acme) = %q", Prefix("acme"))
	}
}
