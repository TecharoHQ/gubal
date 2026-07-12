package chromesweep

import "testing"

func TestLocalFrameName(t *testing.T) {
	// Policy + browser + tag are all part of the on-disk frame name so nothing
	// collides across the policy × browser × version matrix.
	if got := localFrameName("default", "chrome", "130"); got != "default-chrome-130.png" {
		t.Fatalf("chrome: %q", got)
	}
	if got := localFrameName("default", "firefox", "130"); got != "default-firefox-130.png" {
		t.Fatalf("firefox: %q", got)
	}
	if got := localFrameName("hard", "chrome", "130"); got != "hard-chrome-130.png" {
		t.Fatalf("hard policy: %q", got)
	}
	// Empty policy (live-policy fallback) omits the prefix.
	if got := localFrameName("", "chrome", "130"); got != "chrome-130.png" {
		t.Fatalf("empty policy: %q", got)
	}
}
