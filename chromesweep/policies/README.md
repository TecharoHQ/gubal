# Anubis sweep policies

Each `*.yaml` here is a full [Anubis](https://github.com/TecharoHQ/anubis)
`botPolicies` ruleset. They are compiled into the `chromesweep` binary via
`go:embed`, and every sweep runs the full browser × version matrix against **each**
ruleset as a separate test pass named by the file's base name (e.g. `default.yaml`
→ the `default` pass).

To add a pass: drop a new `*.yaml` file in this directory and rebuild. No code
change is needed — `LoadPolicies()` discovers it.

Constraints:

- The filename (without `.yaml`) becomes a Kubernetes ConfigMap name
  (`anubis-policy-<name>`), so use DNS-safe names: lowercase letters, digits, `-`.
- Rulesets must **CHALLENGE** browser user-agents, not `ALLOW` them: the smoke Job
  asserts the Anubis challenge page (which contains "Anubis") is served. An
  `ALLOW`ed request is proxied to the backend, whose body lacks "Anubis", and the
  pre-check would fail for reasons unrelated to the browser.
- A ruleset that Anubis rejects makes its pod crashloop; that policy's rollout
  times out and every version under it is reported as `error`.
