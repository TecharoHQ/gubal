package main

import (
	"fmt"
	"strings"
)

// parseVersions trims and de-empties the given tags, rejects duplicates, and
// errors if nothing usable remains.
func parseVersions(args []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(args))
	for _, a := range args {
		t := strings.TrimSpace(a)
		if t == "" {
			continue
		}
		if seen[t] {
			return nil, fmt.Errorf("duplicate version %q", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	return out, nil
}
