# Anubis deployment for `k8s/`

## Goal

Add Anubis to the `k8s/` deployment as a self-contained, TLS-terminated public
endpoint at `anubis-ci.alrest.cetacean.club`. Anubis sits in front of its own
`httpdebug` backend, `relayd` terminates HTTPS from a cert-manager `Certificate`,
and an `externaldns` `DNSEndpoint` publishes an A record to the Service's
ClusterIP (routable on the alrest network, like the glance reference).

## Layout — a second kustomization

Anubis gets its own kustomize root, separate from the existing chrome one, so the
two have independent lifecycles. The existing `k8s/kustomization.yaml` (chrome) is
left untouched.

```
k8s/anubis/
  anubis.yaml        # Deployment (httpdebug + anubis + relayd) + Service (ClusterIP)
  certificate.yaml   # Certificate (anubis-tls) + DNSEndpoint
  kustomization.yaml # resources: [anubis.yaml, certificate.yaml]
  README.md          # secret bootstrap + multi-stage apply
```

## The pod — three containers, one network namespace

Mirrors the Anubis Kubernetes docs (Anubis-in-front-of-backend) fused with the
glance relayd-TLS-sidecar pattern.

Traffic path:

```
Internet → ClusterIP :443 → relayd (:8443, TLS) → anubis (:8080) → httpdebug (:5000)
              (also :80 → anubis :8080 plaintext, like glance)     ^ its own backend
```

| container  | image                            | key env                                                                                                                                                                                            | ports      |
| ---------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| `httpdebug`| `ghcr.io/xe/x/httpdebug:latest`  | `BIND=:5000`                                                                                                                                                                                        | 5000       |
| `anubis`   | `ghcr.io/techarohq/anubis:latest`| `BIND=:8080`, `TARGET=http://localhost:5000`, `DIFFICULTY=4`, `METRICS_BIND=:9090`, `SERVE_ROBOTS_TXT=true`, `OG_PASSTHROUGH=true`, `OG_EXPIRY_TIME=24h`, `ED25519_PRIVATE_KEY_HEX` from secret. No `PUBLIC_URL`. | 8080, 9090 |
| `relayd`   | `ghcr.io/xe/x/relayd:latest`     | `BIND=:8443`, `PROXY_TO=http://localhost:8080`; mounts `anubis-tls` secret at `/xe/pki`                                                                                                             | 8443       |

Ports differ deliberately (shared netns): httpdebug on 5000 so it doesn't collide
with anubis on 8080. `PUBLIC_URL` is intentionally unset — the docs warn it breaks
redirect construction (`redir=null`) in this sidecar topology.

Each container keeps a tight securityContext (drop ALL caps, no privilege
escalation, `RuntimeDefault` seccomp), matching the chrome deployment and glance
reference. `anubis` and `relayd` run non-root as uid/gid 1000.

## Service — ClusterIP, glance ports

`app: anubis` selector, auto-assigned ClusterIP:

- `port 80 → targetPort 8080` (name `http`, anubis direct)
- `port 443 → targetPort 8443` (name `https`, relayd/TLS)

Byte-for-byte the glance Service shape.

## TLS + DNS (`certificate.yaml`)

- **Certificate** `anubis-tls`: `dnsNames: [anubis-ci.alrest.cetacean.club]`,
  `issuerRef` `letsencrypt-prod` ClusterIssuer, `duration: 2160h` (90d),
  `renewBefore: 360h` (15d), usages `digital signature` + `key encipherment` —
  cloned from glance. Issues via the ClusterIssuer's DNS-01 solver, so it does
  **not** depend on the IP being live or reachable.
- **DNSEndpoint**: A record `anubis-ci.alrest.cetacean.club -> <ClusterIP>`,
  TTL 3600. Ships with a placeholder target filled in during stage 2.

## The signing key (not committed)

The `anubis-key` secret is created out-of-band and referenced by name — never
checked in:

```sh
kubectl create secret generic anubis-key \
  --from-literal=ED25519_PRIVATE_KEY_HEX=$(openssl rand -hex 32)
```

## Multi-stage apply (documented in README)

1. Create the `anubis-key` secret (above).
2. **Stage 1** — `kubectl apply -f k8s/anubis/anubis.yaml`: Deployment + Service
   come up, Kubernetes assigns the ClusterIP.
3. Read it: `kubectl get svc anubis -o jsonpath='{.spec.clusterIP}'`.
4. Write that IP into the DNSEndpoint `targets:` in `certificate.yaml`.
5. **Stage 2** — `kubectl apply -k k8s/anubis`: applies everything, now with the
   correct DNS record and the Certificate.

Bootstrapping the DNSEndpoint via stage 2 (rather than the initial `-k`) avoids
publishing a placeholder A record.

## Out of scope (YAGNI)

No NetworkPolicy for Anubis — it is a deliberately public endpoint, unlike the
locked-down CDP port on the chrome pod. Noted as a possible follow-up in the
README rather than built now.
