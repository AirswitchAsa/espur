package connman

import (
	"context"
	"errors"

	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
)

// vaultSessionStore backs a WeChat adapter's session with the encrypted
// credentials table (scope "connection"), so the bot token never lands on disk
// in plaintext. It implements wechat.SessionStore.
type vaultSessionStore struct {
	db    *store.DB
	vault *secrets.Vault
	id    string
}

// Load returns the decrypted session blob, or (nil, nil) when none is stored.
func (v *vaultSessionStore) Load() ([]byte, error) {
	cred, err := v.db.GetCredential(context.Background(), store.ConnScope, v.id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(cred.Blob) == 0 {
		return nil, nil
	}
	return v.vault.Decrypt(cred.Blob)
}

// Save encrypts and upserts the session blob.
func (v *vaultSessionStore) Save(data []byte) error {
	blob, err := v.vault.Encrypt(data)
	if err != nil {
		return err
	}
	return v.db.PutCredential(context.Background(), store.Credential{
		Scope: store.ConnScope, ID: v.id, Kind: "platform_session", Status: "set", Blob: blob,
	})
}
