# Anubis on Kubernetes

Anubis guarding its own [`httpdebug`](https://github.com/Xe/x) backend, served over
HTTPS at `anubis-ci.alrest.cetacean.club`.

## Topology

One pod, three containers sharing a network namespace:

```
Internet → Service :443 → relayd (:8443, TLS) → anubis (:8080) → httpdebug (:5000)
                Service :80 ───────────────────→ anubis (:8080)  (plaintext)
```

- **httpdebug** — the backend Anubis protects; reflects requests back for debugging.
- **anubis** — proof-of-work gate. `TARGET=http://localhost:5000`. `PUBLIC_URL` is
  left unset on purpose; setting it in this sidecar topology breaks redirect
  construction (`redir=null`).
- **relayd** — terminates HTTPS using the `anubis-tls` cert and proxies to Anubis.

The Service is a plain ClusterIP; its assigned IP is routable on the alrest network,
so the `DNSEndpoint` publishes an A record straight to it (same pattern as glance).

All resources live in the `ci` namespace.

## Deploy

### 0. Create the signing key secret (once, not committed)

```sh
kubectl create secret generic anubis-key -n ci \
  --from-literal=ED25519_PRIVATE_KEY_HEX=$(openssl rand -hex 32)
```

### 1. Bring up the Deployment + Service

```sh
kubectl apply -n ci -f anubis.yaml
```

### 2. Read the assigned ClusterIP

```sh
kubectl get svc anubis -n ci -o jsonpath='{.spec.clusterIP}'
```

### 3. Write that IP into the DNSEndpoint

Replace `REPLACE_WITH_CLUSTER_IP` in `certificate.yaml` under the DNSEndpoint's
`targets:` with the IP from step 2.

### 4. Apply everything (cert + DNS record)

```sh
kubectl apply -k .
```

cert-manager issues `anubis-tls` via the `letsencrypt-prod` ClusterIssuer's DNS-01
solver (no HTTP reachability required); relayd picks the cert up from the mounted
secret. externaldns publishes the A record. Once both settle, browse to
<https://anubis-ci.alrest.cetacean.club>.

Applying the DNSEndpoint in step 4 (rather than in step 1's `-k`) avoids publishing
a placeholder A record before the ClusterIP is known.

## Notes

- **No NetworkPolicy.** Unlike the chrome pod's locked-down CDP port, Anubis is a
  deliberately public endpoint. If you want to restrict egress or ingress later,
  add one here.
