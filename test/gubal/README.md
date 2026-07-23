# Anubis sweep policies

Each `*.yaml` here is a full [Anubis](https://github.com/TecharoHQ/anubis)
`botPolicies` ruleset. Every sweep runs the full browser × version matrix against
**each** ruleset as a separate test pass named by the file's base name (e.g.
`fast.yaml` → the `fast` pass).

These files are read from disk, not compiled in. `chrome-sweep` loads them via
`-policy-dir` (default `test/gubal`), and `gubalctl` reads the same directory and
submits the set to `gubald` in the request. To add a pass: drop a new `*.yaml`
file here. No code change and no rebuild is needed.

Constraints:

- The filename (without `.yaml`) becomes a Kubernetes ConfigMap name
  (`anubis-policy-<name>`), so use DNS-safe names: lowercase letters, digits, `-`.
- Rulesets must **CHALLENGE** browser user-agents, not `ALLOW` them: the smoke Job
  asserts the Anubis challenge page (which contains "Anubis") is served. An
  `ALLOW`ed request is proxied to the backend, whose body lacks "Anubis", and the
  pre-check would fail for reasons unrelated to the browser.
- A ruleset that Anubis rejects makes its pod crashloop; that policy's rollout
  times out and every version under it is reported as `error`.
