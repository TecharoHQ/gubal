package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadPolicyDir reads every *.yaml in dir into the name -> ruleset map gubald
// expects, keyed by the file's base name without its extension. Non-YAML files
// and subdirectories are ignored.
//
// This intentionally does not reuse chromesweep.LoadPoliciesFromDir: importing
// that package would link client-go into what is meant to be a thin CI client.
func loadPolicyDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading policy dir %s: %w", dir, err)
	}
	policies := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies[strings.TrimSuffix(e.Name(), ".yaml")] = string(content)
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("no *.yaml policies in %s", dir)
	}
	return policies, nil
}
