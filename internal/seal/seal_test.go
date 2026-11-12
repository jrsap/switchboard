package seal

import (
	"bytes"
	"errors"
	"testing"
)

func TestWrapUnwrapRoundTrip(t *testing.T) {
	root, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		dataKey, err := NewKey()
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := Wrap(root, dataKey)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		got, err := Unwrap(root, wrapped)
		if err != nil {
			t.Fatalf("Unwrap: %v", err)
		}
		if !bytes.Equal(got, dataKey) {
			t.Fatal("unwrapped key does not match")
		}
	}
}

func TestUnwrapWrongKeyFails(t *testing.T) {
	root, _ := NewKey()
	other, _ := NewKey()
	dataKey, _ := NewKey()
	wrapped, err := Wrap(root, dataKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(other, wrapped); err == nil {
		t.Fatal("expected error unwrapping with the wrong root key")
	}
}

func TestUnwrapMalformed(t *testing.T) {
	root, _ := NewKey()
	if _, err := Unwrap(root, "!!!not-base64!!!"); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("want ErrBadPayload, got %v", err)
	}
	if _, err := Unwrap(root, "aGVsbG8="); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("want ErrBadPayload for short payload, got %v", err)
	}
}

func TestBadKeySize(t *testing.T) {
	if _, err := Wrap([]byte("short"), make([]byte, KeySize)); err == nil {
		t.Fatal("expected error for short root key")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key, _ := NewKey()
	ct, err := Encrypt(key, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Decrypt(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != `{"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("round trip mismatch: %s", pt)
	}
}
