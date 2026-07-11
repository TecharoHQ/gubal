# chrome-sweep

Tests a list of Chrome image tags one after another against the in-cluster
Anubis + httpdebug setup. For each tag it re-points the `chrome` Deployment,
waits for rollout, runs the `chrome-smoke` Job (`k8s/smoke-job.yaml`), and
records pass/fail plus the captured screenshot.

## Prerequisites

All in namespace `ci`: the Anubis deployment (`k8s/anubis`), the `anubis-key`
secret, the `chrome` Deployment/Service, and the `chrome-bully-data` PVC.

## Usage

    go build -o ./var/chrome-sweep ./cmd/chrome-sweep
    ./var/chrome-sweep -out ./var/sweep 110 120 130 150

Outputs `report.md`, `report.json`, and `frames/<tag>.png` under `-out`.
Exit code is non-zero if any version did not pass.

## Key flags

- `-namespace` (default `ci`), `-deployment` (`chrome`), `-container` (`chrome`)
- `-image-repo` (default `ghcr.io/techarohq/gubal/chrome`)
- `-job-manifest` (default `k8s/smoke-job.yaml`)
- `-ready-timeout` (default `3m`), `-job-timeout` (default `4m`)
- `-kubeconfig` (defaults to `$KUBECONFIG` or `~/.kube/config`)
