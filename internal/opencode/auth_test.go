package opencode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthFilePath_XDGWins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg")
	t.Setenv("HOME", "/should/not/win")
	got := AuthFilePath()
	want := filepath.Join("/custom/xdg", "opencode", "auth.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAuthFilePath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/punny")
	got := AuthFilePath()
	want := filepath.Join("/home/punny", ".local", "share", "opencode", "auth.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAuthFilePath_NoEnv(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	if got := AuthFilePath(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReadAuthEntries_MissingFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // path is set but no file exists
	entries, err := ReadAuthEntries()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty, got %d entries", len(entries))
	}
}

func TestReadAuthEntries_UnsetIsEmpty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	entries, err := ReadAuthEntries()
	if err != nil || len(entries) != 0 {
		t.Fatalf("got %v / %v", entries, err)
	}
}

func TestReadAuthEntries_ParsesProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	const blob = `{
		"anthropic": {"type": "oauth", "access": "secret-access-token", "refresh": "rt"},
		"openai":    {"type": "api",   "key": "sk-test-redacted"},
		"empty":     {"type": "oauth"}
	}`
	if err := os.WriteFile(filepath.Join(dir, "opencode", "auth.json"), []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAuthEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%+v)", len(entries), entries)
	}
	// Sorted alphabetically.
	if entries[0].Provider != "anthropic" || entries[1].Provider != "empty" || entries[2].Provider != "openai" {
		t.Fatalf("not sorted: %+v", entries)
	}
	if !entries[0].HasKey || !entries[2].HasKey {
		t.Fatal("anthropic+openai must report has-key")
	}
	if entries[1].HasKey {
		t.Fatal("empty entry must not report has-key")
	}
	if entries[0].Type != "oauth" || entries[2].Type != "api" {
		t.Fatalf("type mismatch: %+v", entries)
	}
}

func TestReadAuthEntries_MalformedIsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "opencode"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "opencode", "auth.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuthEntries(); err == nil {
		t.Fatal("expected parse error")
	}
}
