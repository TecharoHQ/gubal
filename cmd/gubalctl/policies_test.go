package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadPolicyDirRejectsBadNames(t *testing.T) {
	for name, fname := range map[string]string{
		"underscore":         "Bad_Name.yaml",
		"too long":           strings.Repeat("a", 50) + ".yaml",
		"empty (bare .yaml)": ".yaml",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, fname), []byte("bots: []"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadPolicyDir(dir); err == nil {
				t.Fatalf("%s: want error, got none", name)
			}
		})
	}
}

func TestLoadPolicyDirAcceptsShippedNames(t *testing.T) {
	// The four policy names actually shipped under test/gubal/ must keep
	// loading — this is the proto's DNS-1123-ish pattern, not an arbitrary one.
	dir := t.TempDir()
	for _, name := range []string{"default-config", "fast", "metarefresh", "preact"} {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte("bots: []"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := loadPolicyDir(dir)
	if err != nil {
		t.Fatalf("shipped policy names must load: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 policies, got %d", len(got))
	}
}
