package chromesweep

import (
	"os"
	"path/filepath"
	"testing"
)

// writePolicyDir builds a temp dir holding the given filename -> content pairs.
func writePolicyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadPoliciesFromDir(t *testing.T) {
	dir := writePolicyDir(t, map[string]string{
		"fast.yaml":           "bots: [fast]",
		"default-config.yaml": "bots: [default]",
		"notes.txt":           "not a policy",
	})

	got, err := LoadPoliciesFromDir(dir)
	if err != nil {
		t.Fatalf("LoadPoliciesFromDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 policies (non-yaml skipped), got %d: %+v", len(got), got)
	}
	// Sorted ascending, extension stripped.
	if got[0].Name != "default-config" || got[1].Name != "fast" {
		t.Fatalf("names = %q, %q; want default-config, fast", got[0].Name, got[1].Name)
	}
	if string(got[0].Content) != "bots: [default]" {
		t.Fatalf("default-config content = %q", got[0].Content)
	}
}

func TestLoadPoliciesFromDirErrors(t *testing.T) {
	if _, err := LoadPoliciesFromDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory must error")
	}
	dir := writePolicyDir(t, map[string]string{"README.md": "no rulesets here"})
	if _, err := LoadPoliciesFromDir(dir); err == nil {
		t.Fatal("a directory with no *.yaml must error")
	}
}

func TestPoliciesFromMap(t *testing.T) {
	got := PoliciesFromMap(map[string]string{
		"fast":           "bots: [fast]",
		"default-config": "bots: [default]",
		"preact":         "bots: [preact]",
	})
	if len(got) != 3 {
		t.Fatalf("want 3 policies, got %d", len(got))
	}
	// Sorted, so pass ordering never depends on map iteration order.
	for i, want := range []string{"default-config", "fast", "preact"} {
		if got[i].Name != want {
			t.Fatalf("policy %d = %q, want %q", i, got[i].Name, want)
		}
	}
	if string(got[1].Content) != "bots: [fast]" {
		t.Fatalf("fast content = %q", got[1].Content)
	}
	if PoliciesFromMap(nil) != nil {
		t.Fatal("an empty map must yield nil")
	}
}
