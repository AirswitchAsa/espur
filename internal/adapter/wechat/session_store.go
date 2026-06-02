package wechat

import "os"

// SessionStore persists the adapter's session blob (bot_token + ids + base_url
// + poll cursor, marshalled as JSON by the adapter). Injecting it lets a
// web-managed connection back the session with the encrypted credentials table
// (no plaintext token on disk), while the standalone cmd/wechat-login helper and
// tests use a plain file. Load returns (nil, nil) when no session exists yet.
type SessionStore interface {
	Load() ([]byte, error)
	Save(data []byte) error
}

// fileStore persists the session blob to a single JSON file (mode 0600, atomic
// write). Used by cmd/wechat-login for headless first-login and by tests.
type fileStore struct{ path string }

// NewFileStore returns a file-backed SessionStore at path.
func NewFileStore(path string) SessionStore { return &fileStore{path: path} }

func (f *fileStore) Load() ([]byte, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (f *fileStore) Save(data []byte) error {
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
