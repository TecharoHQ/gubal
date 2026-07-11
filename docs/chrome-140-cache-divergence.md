# Chrome 140 archive divergence (Tigris edge-cache hack)

**Status:** active workaround as of 2026-07-11. Remove once the Tigris cache bug is fixed.

## TL;DR

Chrome 140's `.deb` is stored in the `chrome-archive` bucket under its **sha256 as
the object key**, not under its canonical Chrome filename:

|                                                  | value                                                                             |
| ------------------------------------------------ | --------------------------------------------------------------------------------- |
| Canonical filename (kept in manifest `filename`) | `google-chrome-stable_140.0.7339.207-1_amd64.deb`                                 |
| Actual object key (manifest `tigris_key`)        | `chrome/140/e60d2df8410925e22d8ea2a24e9a79a84fdb9062a4c6f3af2dac61e630833ecb.deb` |
| sha256                                           | `e60d2df8410925e22d8ea2a24e9a79a84fdb9062a4c6f3af2dac61e630833ecb`                |
| size                                             | 120353168 bytes                                                                   |

140 is the dependency-layer representative for the Ubuntu 26.04 era, so every
26.04-era image (140–150+) pulls this deb via `CHROME_DEPS_DEB_URL`.

## Why

1. The archive originally ingested a **truncated 122880-byte** copy of 140 from the
   UChicago mirror, which serves the stub with `HTTP 200` (see
   `[[uchicago-mirror-truncated-debs]]` / the mirror bug report). This broke
   `build-chrome-images.sh` with `E: Invalid archive member header`.
2. We re-fetched the full 120 MB deb from `dl.google.com` and re-uploaded it to the
   canonical key. Origin was correct, but the **Tigris `t3.tigrisfiles.io` edge
   cache pinned the truncated copy** at the canonical path and would not
   invalidate — not on overwrite, not on `delete`+reupload, not with a
   cache-busting query string. It even re-served a version that had been deleted at
   origin.
3. A never-cached key is served fresh. So the good bytes were relocated to the
   sha256 key, and the manifest was pointed at it. `filename` stays canonical
   because `build-chrome-images.sh` parses the Chrome version out of it (a sha256
   filename has no dotted version and would fail that parse).

## The catch: `manifest.json` is cache-pinned too

The same t3 cache is **also pinning `chrome/manifest.json`**. So a build that fetches
the manifest from the default `https://chrome-archive.t3.tigrisfiles.io` still sees
the stale manifest (old canonical key) and the stale deb. Until the cache clears,
build against the uncached origin endpoint:

```sh
CHROME_BUCKET_URL=https://chrome-archive.fly.storage.tigris.dev \
  scripts/build-chrome-images.sh --only 150
```

The `fly.storage.tigris.dev` virtual-host endpoint serves the fresh manifest (sha256
key) and the full deb (`X-Tigris-Read-Source` reflects origin, not the poisoned
edge). Verified end-to-end 2026-07-11.

## Where the divergence lives

- `chrome/manifest.json` — 140 entry has `tigris_key` = the sha256 key plus a `note`
  field describing this.
- `scripts/archive-chrome-versions.sh` — a `HACK(2026-07-11)` block overrides the
  object key for 140 so re-archive runs reuse the sha256 key instead of recreating
  (and re-poisoning) the canonical one.
- `scripts/build-chrome-images.sh` — follows `tigris_key` from the manifest; also now
  passes `CHROME_DEPS_DEB_SHA256` so a truncated deps deb fails with a clear checksum
  error instead of a cryptic apt one.
- `.github/workflows/build-chrome-images.yml` — sets
  `CHROME_BUCKET_URL: https://chrome-archive.fly.storage.tigris.dev` so CI reads the
  fresh manifest/debs from origin instead of the cache-pinned t3 CDN.

## Removing the hack (once Tigris fixes the cache)

1. Confirm `https://chrome-archive.t3.tigrisfiles.io/chrome/140/google-chrome-stable_140.0.7339.207-1_amd64.deb`
   serves the full 120353168-byte deb (not the 122880 stub) and that
   `chrome/manifest.json` on t3 is fresh.
2. Re-upload the deb to the canonical key, point the 140 manifest entry back at
   `chrome/140/google-chrome-stable_140.0.7339.207-1_amd64.deb`, drop the `note`.
3. Delete the sha256 object and remove the `HACK` block from
   `archive-chrome-versions.sh`.
4. Remove the `CHROME_BUCKET_URL` override from
   `.github/workflows/build-chrome-images.yml` so CI goes back to the t3 CDN.
5. Delete this file.

(The `CHROME_DEPS_DEB_SHA256` verification added to the Dockerfiles and
`build-chrome-images.sh` is a permanent hardening — keep it.)
