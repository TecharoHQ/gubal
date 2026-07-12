package chromesweep

import "testing"

func TestLoadPolicies(t *testing.T) {
	got, err := LoadPolicies()
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want >= 2 embedded policies, got %d", len(got))
	}

	names := map[string]bool{}
	for i, p := range got {
		names[p.Name] = true
		if len(p.Content) == 0 {
			t.Fatalf("policy %q has empty content", p.Name)
		}
		// Name must be the filename without extension: no ".yaml", no slash.
		if got, bad := p.Name, ".yaml"; len(got) >= len(bad) && got[len(got)-len(bad):] == bad {
			t.Fatalf("policy name %q still has extension", p.Name)
		}
		// Sorted ascending by Name.
		if i > 0 && got[i-1].Name > p.Name {
			t.Fatalf("policies not sorted: %q before %q", got[i-1].Name, p.Name)
		}
	}
	for _, want := range []string{"default", "hard"} {
		if !names[want] {
			t.Fatalf("missing policy %q; have %v", want, names)
		}
	}
}
