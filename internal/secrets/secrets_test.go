package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("sk-test-api-key-1234567890")
	ct, err := v.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatalf("ciphertext contains plaintext")
	}
	got, err := v.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypt mismatch: %q vs %q", got, plain)
	}
}

func TestSelfTest_WrongKey(t *testing.T) {
	k1, _ := GenerateIdentity()
	k2, _ := GenerateIdentity()
	v1, _ := New(k1)
	v2, _ := New(k2)
	ct, err := v1.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.SelfTest(ct); !errors.Is(err, ErrMasterKeyMismatch) {
		t.Fatalf("expected ErrMasterKeyMismatch, got %v", err)
	}
}

func TestSelfTest_EmptyDB(t *testing.T) {
	k, _ := GenerateIdentity()
	v, _ := New(k)
	if err := v.SelfTest(nil); err != nil {
		t.Fatalf("empty blob should be OK: %v", err)
	}
}

func TestNew_EmptyKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error on empty key")
	}
}
