package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// policyNameRE and policyNameMaxLen mirror the proto validation on
// SmokeTestRequest.policies (pb/techaro/lol/gubal/v1/gubal.proto) and
// chromesweep.LoadPoliciesFromDir's identical check: the name becomes a
// ConfigMap named anubis-policy-<name>, and 14+49 = 63 is Kubernetes'
// name-length limit. Kept in lockstep with chromesweep/policies.go rather
// than imported from it — see loadPolicyDir's doc comment below on why.
var policyNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

const policyNameMaxLen = 49

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
		name := strings.TrimSuffix(e.Name(), ".yaml")
		if err := validatePolicyName(name); err != nil {
			return nil, fmt.Errorf("policy file %s: %w", e.Name(), err)
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies[name] = string(content)
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("no *.yaml policies in %s", dir)
	}
	return policies, nil
}

// validatePolicyName rejects a policy name that would fail the proto's
// validation of SmokeTestRequest.policies keys, so a bad local file fails
// fast instead of costing a signed request that the server rejects anyway.
func validatePolicyName(name string) error {
	if !policyNameRE.MatchString(name) {
		return fmt.Errorf("invalid policy name %q: must match %s", name, policyNameRE.String())
	}
	if len(name) > policyNameMaxLen {
		return fmt.Errorf("invalid policy name %q: exceeds %d characters", name, policyNameMaxLen)
	}
	return nil
}
