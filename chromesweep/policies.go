package chromesweep

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed policies/*.yaml
var policyFS embed.FS

// Policy is one embedded Anubis botPolicies ruleset. Name is the file's base name
// without the .yaml extension (e.g. "default"); it names the test pass in reports
// and the per-policy ConfigMap that carries the ruleset into the Anubis pod.
type Policy struct {
	Name    string
	Content []byte
}

// LoadPolicies reads every policies/*.yaml file embedded into the binary and
// returns them sorted by Name (the filename without its .yaml extension). Adding a
// new file to chromesweep/policies/ is all it takes to add a test pass.
func LoadPolicies() ([]Policy, error) {
	entries, err := policyFS.ReadDir("policies")
	if err != nil {
		return nil, fmt.Errorf("reading embedded policies: %w", err)
	}
	var policies []Policy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		content, err := policyFS.ReadFile(path.Join("policies", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies = append(policies, Policy{
			Name:    strings.TrimSuffix(e.Name(), ".yaml"),
			Content: content,
		})
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}
