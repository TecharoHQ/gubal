# chrome-sweep

Tests a list of Chrome image tags in bounded parallel against the in-cluster
Anubis + httpdebug setup. For each tag it creates a per-version chrome
Deployment/Service/NetworkPolicy and a `chrome-smoke-<tag>` Job (all labelled
`app: chrome-<tag>`, all in namespace `ci`), waits for rollout, runs the smoke
Job (`k8s/smoke-job.yaml`), records pass/fail plus the captured screenshot, then
tears that version's resources down. One shared PVC collects every version's
frames.

## Prerequisites

All in namespace `ci`: the Anubis deployment (`k8s/anubis`), the `anubis-key`
secret, and the `chrome-bully-data` PVC. The per-version chrome resources are
created by the tool — they must NOT pre-exist under the same names.

## Usage

    go build -o ./var/chrome-sweep ./cmd/chrome-sweep
    ./var/chrome-sweep -out ./var/sweep 110 120 130 150

Outputs `report.md`, `report.json`, and `frames/<tag>.png` under `-out`, plus
`report.zip` bundling `report.json` and every captured frame (not the Markdown).
Both reports record the Anubis image the run was tested against. Exit code is
non-zero if any version did not pass.

## Anubis version

Anubis is a shared singleton in front of every chrome version. The image the
sweep tests against defaults to the ref declared in the Anubis manifest
(`-anubis-manifest`, read from disk — never hardcoded). Pass `-anubis-image` to
override it: the live Anubis Deployment is re-imaged for the run and restored to
its previous image afterward.

    ./var/chrome-sweep -anubis-image ghcr.io/techarohq/anubis:v1.20.0 120 150

## Key flags

- `-parallelism` (default `8`) — max versions tested at once
- `-anubis-image` (default: the ref from `-anubis-manifest`) — override the Anubis image for the run
- `-anubis-manifest` (`k8s/anubis/anubis.yaml`), `-anubis-container` (`anubis`)
- `-namespace` (default `ci`), `-deployment` (base name `chrome`), `-container` (`chrome`)
- `-image-repo` (default `ghcr.io/techarohq/gubal/chrome`)
- `-deployment-manifest` (`k8s/deployment.yaml`), `-service-manifest` (`k8s/service.yaml`), `-networkpolicy-manifest` (`k8s/networkpolicy.yaml`), `-job-manifest` (`k8s/smoke-job.yaml`)
- `-ready-timeout` (default `3m`), `-job-timeout` (default `4m`)
- `-kubeconfig` (defaults to `$KUBECONFIG` or `~/.kube/config`)
