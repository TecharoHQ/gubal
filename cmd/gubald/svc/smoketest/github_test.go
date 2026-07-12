package smoketest

import "testing"

func TestSplitRepo(t *testing.T) {
	t.Parallel()

	owner, name, err := splitRepo("TecharoHQ/anubis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "TecharoHQ" || name != "anubis" {
		t.Fatalf("got %q/%q", owner, name)
	}

	if _, _, err := splitRepo("anubis"); err == nil {
		t.Fatal("expected error for repo without a slash")
	}
	if _, _, err := splitRepo("a/b/c"); err == nil {
		t.Fatal("expected error for repo with two slashes")
	}
}

func TestNewGitHubCommenterEmptyToken(t *testing.T) {
	t.Parallel()
	c, err := NewGitHubCommenter("")
	if err != nil {
		t.Fatalf("empty token should not error: %v", err)
	}
	if c == nil {
		t.Fatal("expected a non-nil commenter")
	}
}
