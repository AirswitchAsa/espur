// Auth-file inspection. Espur does not own OAuth flows — it delegates to
// `opencode auth login` which writes ~/.local/share/opencode/auth.json
// (overridden by XDG_DATA_HOME). See docs/specs/oauth.dog.md "Delegation model."
// This file is the read-only view the web UI uses to display per-provider
// status.

package opencode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// AuthEntry describes one provider entry in opencode's auth.json. Fields
// are the union of what opencode writes; the only ones used downstream are
// Type and Key (presence-of-key). The actual token bytes are never read by
// Espur; we just confirm a credential exists.
type AuthEntry struct {
	Provider string `json:"provider"`
	Type     string `json:"type"` // "api" | "oauth" | "wellknown" etc.
	HasKey   bool   `json:"has_key"`
}

// AuthFilePath returns the path opencode reads/writes its auth file at,
// honouring XDG_DATA_HOME (and falling back to $HOME/.local/share/opencode).
// Returns "" if neither var is set, which the caller treats as "no auth
// configured yet."
func AuthFilePath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "opencode", "auth.json")
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "share", "opencode", "auth.json")
	}
	return ""
}

// ReadAuthEntries parses opencode's auth.json (if present) and returns one
// AuthEntry per configured provider, sorted by provider id for stable UI
// rendering. A missing file is not an error — the slice is empty.
//
// The file shape opencode uses (as of 1.15.x) is a JSON object keyed by
// provider, with values that are themselves objects carrying at minimum a
// `type` field. We tolerate unknown keys.
func ReadAuthEntries() ([]AuthEntry, error) {
	p := AuthFilePath()
	if p == "" {
		return nil, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]AuthEntry, 0, len(raw))
	for provider, v := range raw {
		typ, _ := v["type"].(string)
		hasKey := false
		for _, k := range []string{"key", "access", "refresh", "access_token"} {
			if s, ok := v[k].(string); ok && s != "" {
				hasKey = true
				break
			}
		}
		out = append(out, AuthEntry{Provider: provider, Type: typ, HasKey: hasKey})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}
