package chromesweep

import "testing"

func TestLocalFrameName(t *testing.T) {
	if got := localFrameName("chrome", "130"); got != "chrome-130.png" {
		t.Fatalf("chrome: %q", got)
	}
	// Same tag, different browser must not collide.
	if got := localFrameName("firefox", "130"); got != "firefox-130.png" {
		t.Fatalf("firefox: %q", got)
	}
}
