package chromesweep

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// policyNameRE and policyNameMaxLen mirror the proto validation on
// SmokeTestRequest.policies (pb/techaro/lol/gubal/v1/gubal.proto): the name
// becomes a ConfigMap named anubis-policy-<name>, and 14+49 = 63 is
// Kubernetes' name-length limit.
var policyNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

const policyNameMaxLen = 49

// Policy is one Anubis botPolicies ruleset. Name is the file's base name without
// the .yaml extension (e.g. "default-config"); it names the test pass in reports
// and the per-policy ConfigMap that carries the ruleset into the Anubis pod.
type Policy struct {
	Name    string
	Content []byte
}

// LoadPoliciesFromDir reads every *.yaml in dir and returns the rulesets sorted
// by Name, so a sweep's pass ordering is stable. Non-YAML files and
// subdirectories are ignored.
//
// A missing directory, or one holding no rulesets, is an error: since policies
// are no longer compiled into the binary, an empty set means a misconfigured run
// rather than a deliberate one.
func LoadPoliciesFromDir(dir string) ([]Policy, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading policy dir %s: %w", dir, err)
	}
	var policies []Policy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		if err := validatePolicyName(name); err != nil {
			return nil, fmt.Errorf("policy file %s: %w", e.Name(), err)
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies = append(policies, Policy{
			Name:    name,
			Content: content,
		})
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("no *.yaml policies in %s", dir)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}

// validatePolicyName rejects a policy name that would fail the proto's
// validation of SmokeTestRequest.policies keys, so a bad local file surfaces
// a clear error here instead of an empty-named policy silently disabling
// application (see the sweep.go "" == "no policy" sentinel) or a cryptic
// Twirp compilation error downstream.
func validatePolicyName(name string) error {
	if !policyNameRE.MatchString(name) {
		return fmt.Errorf("invalid policy name %q: must match %s", name, policyNameRE.String())
	}
	if len(name) > policyNameMaxLen {
		return fmt.Errorf("invalid policy name %q: exceeds %d characters", name, policyNameMaxLen)
	}
	return nil
}

// PoliciesFromMap converts a wire map of policy name -> ruleset YAML into
// policies sorted by name, so a sweep's pass ordering does not depend on Go's
// randomized map iteration. An empty map yields nil, which chromesweep.Run
// treats as "sweep once against Anubis's live ruleset".
func PoliciesFromMap(m map[string]string) []Policy {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	policies := make([]Policy, 0, len(names))
	for _, name := range names {
		policies = append(policies, Policy{Name: name, Content: []byte(m[name])})
	}
	return policies
}
