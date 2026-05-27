// Package secrets implements the at-rest credential encryption layer described
// in docs/specs/secrets.dog.md. Plaintext is encrypted with an age X25519 identity
// loaded from the ESPUR_MASTER_KEY env var at boot.
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// ErrMasterKeyMismatch is returned by SelfTest when an existing blob cannot
// be decrypted with the current master key.
var ErrMasterKeyMismatch = errors.New("secrets: master key mismatch")

// Vault holds the age identity for the running process. It is constructed
// once at boot from the master-key env var and never written to disk.
type Vault struct {
	id        *age.X25519Identity
	recipient age.Recipient
}

// New parses an age secret-key string (`AGE-SECRET-KEY-...`) into a Vault.
// Spec: secrets.dog.md TODO(decision) — pinned to raw secret-key in env.
func New(masterKey string) (*Vault, error) {
	masterKey = strings.TrimSpace(masterKey)
	if masterKey == "" {
		return nil, errors.New("secrets: ESPUR_MASTER_KEY is empty")
	}
	id, err := age.ParseX25519Identity(masterKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: parse master key: %w", err)
	}
	return &Vault{id: id, recipient: id.Recipient()}, nil
}

// Encrypt returns the age ciphertext of plaintext, using the vault's identity
// as the sole recipient.
func (v *Vault) Encrypt(plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.recipient)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decrypt is the inverse of Encrypt. Returns ErrMasterKeyMismatch on a wrong
// identity (age does not currently distinguish "wrong key" from "malformed"
// so we wrap any decryption failure).
func (v *Vault) Decrypt(ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), v.id)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMasterKeyMismatch, err)
	}
	return io.ReadAll(r)
}

// SelfTest implements the boot self-test from docs/specs/secrets.dog.md:
// pick any existing blob and verify it decrypts. Caller is responsible for
// providing the blob (or nil, which is a "no blobs yet" valid state).
func (v *Vault) SelfTest(blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	_, err := v.Decrypt(blob)
	return err
}

// GenerateIdentity returns a freshly-minted age secret-key string. Useful for
// tests and one-off deploy bootstrapping (`espur genkey`-style, not wired up
// yet in v0.1).
func GenerateIdentity() (string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
