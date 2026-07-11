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

Outputs `report.md`, `report.json`, and `frames/<tag>.png` under `-out`.
Exit code is non-zero if any version did not pass.

## Key flags

- `-parallelism` (default `8`) — max versions tested at once
- `-namespace` (default `ci`), `-deployment` (base name `chrome`), `-container` (`chrome`)
- `-image-repo` (default `ghcr.io/techarohq/gubal/chrome`)
- `-deployment-manifest` (`k8s/deployment.yaml`), `-service-manifest` (`k8s/service.yaml`), `-networkpolicy-manifest` (`k8s/networkpolicy.yaml`), `-job-manifest` (`k8s/smoke-job.yaml`)
- `-ready-timeout` (default `3m`), `-job-timeout` (default `4m`)
- `-kubeconfig` (defaults to `$KUBECONFIG` or `~/.kube/config`)
