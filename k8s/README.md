# Proving Chrome images on Kubernetes

These images run headless Chrome as a long-lived CDP server on port 9222. You drive
them from a separate controller pod (puppeteer/playwright) or prove them with the
included smoke Job.

## Isolation model

You are running **arbitrary archived Chrome versions**, many with public,
never-to-be-patched sandbox-escape CVEs. Do not trust Chrome's own sandbox as a
boundary — put the boundary at the pod/kernel edge:

- **Kata Containers** (`runtimeClassName: kata`) gives each pod its own VM. That is the
  real isolation, so `CHROME_SANDBOX=off` (the default, adding `--no-sandbox`) is fine.
- `automountServiceAccountToken: false`, dropped capabilities, and a tight
  `NetworkPolicy` keep a compromised browser from reaching the cluster or the internet.
- CDP on 9222 is unauthenticated remote control of the browser. **Never** expose it
  beyond the controller pod. `networkpolicy.yaml` enforces that.

`gVisor` (`runtimeClassName: gvisor`) is a lighter alternative to Kata with the same
intent; swap the runtimeClassName if that's what your cluster runs.

### Turning Chrome's own sandbox back on

If you'd rather keep the in-browser sandbox (in addition to Kata), set
`CHROME_SANDBOX=on`. Chrome's user-namespace sandbox then needs the node to allow
unprivileged user namespaces (`sysctl kernel.unprivileged_userns_clone=1`,
`user.max_user_namespaces > 0`) and a seccomp profile that permits the namespace
syscalls (`RuntimeDefault` usually suffices; otherwise ship the Chromium team's
`chrome.json` as a `Localhost` profile). The image keeps `chrome-sandbox` `root:root
4755` so this path works.

## Usage

```sh
# 1. Point the Deployment at the version you want to prove:
sed -i 's#chrome:120#chrome:110#' deployment.yaml   # or edit by hand

kubectl apply -f deployment.yaml
kubectl apply -f networkpolicy.yaml

# 2. Prove it (asserts install + reachability + non-Headless UA):
kubectl apply -f smoke-job.yaml
kubectl wait --for=condition=complete job/chrome-smoke --timeout=120s
kubectl logs job/chrome-smoke
```

To sweep many versions, template `deployment.yaml` per tag (kustomize/helm/envsubst)
and run the smoke Job against each.

## Two Chrome quirks these images work around

Both are verified against Chrome 120 and hold across versions:

1. **Chrome ignores `--remote-debugging-address` and binds DevTools to `127.0.0.1`
   only.** The entrypoint therefore runs a **socat bridge by default**
   (`CHROME_SOCAT_BRIDGE=true`): Chrome listens on loopback and socat exposes `9222`
   on `0.0.0.0`. Without it the port is unreachable from any other pod. Set it to
   `false` only if you drive Chrome from an in-process/sidecar client on localhost.

2. **DevTools rejects DNS-name `Host` headers** (anti DNS-rebinding) with HTTP 500;
   only IP-literal or `localhost` Host headers are accepted. So you cannot just hit
   `http://chrome:9222/json/version` through the Service — connect by **pod IP**, or
   send `Host: localhost:9222`. The probes and smoke Job already do this.

## Driving it for real

From a pod labelled `role=chrome-controller` (so the NetworkPolicy admits it), connect
by **pod IP** so the Host header is an IP literal DevTools accepts:

```js
// puppeteer — resolve the pod IP (e.g. from a headless Service or the Endpoints API),
// not the Service DNS name, then:
const browser = await puppeteer.connect({ browserURL: `http://${podIP}:9222` });
```

A headless Service (`clusterIP: None`) is the easy way to get pod IPs in DNS. The
`--remote-allow-origins=*` flag baked into the entrypoint keeps Chrome from rejecting
the WebSocket upgrade.
