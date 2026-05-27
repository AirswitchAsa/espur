package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsure_CreatesAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "thread")
	if err := EnsureWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() == 0 {
		t.Fatal("AGENTS.md is empty")
	}

	// Mutate AGENTS.md to confirm we don't overwrite it on second call.
	custom := []byte("# user-owned content\n")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(got) != string(custom) {
		t.Fatalf("AGENTS.md was overwritten on second call")
	}
}
