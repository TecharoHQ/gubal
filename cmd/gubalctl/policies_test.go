package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPolicyDir(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"default-config.yaml": "bots: [default]",
		"fast.yaml":           "bots: [fast]",
		"README.md":           "not a policy",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := loadPolicyDir(dir)
	if err != nil {
		t.Fatalf("loadPolicyDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 policies (non-yaml skipped), got %d: %v", len(got), got)
	}
	// Keyed by base name without extension — the name gubald turns into a ConfigMap.
	if got["default-config"] != "bots: [default]" {
		t.Fatalf("default-config = %q", got["default-config"])
	}
	if got["fast"] != "bots: [fast]" {
		t.Fatalf("fast = %q", got["fast"])
	}
}

func TestLoadPolicyDirErrors(t *testing.T) {
	if _, err := loadPolicyDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory must error")
	}
	if _, err := loadPolicyDir(t.TempDir()); err == nil {
		t.Fatal("an empty directory must error")
	}
}
