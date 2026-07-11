#!/usr/bin/env bash
#
# build-firefox-images.sh
#
# Ingests a Firefox archive manifest and builds one Docker image per Firefox version,
# each on the Ubuntu release contemporary with that Firefox. The mirror image of
# build-chrome-images.sh, but simpler: Firefox ships a self-contained tarball on
# archive.mozilla.org, so there is no shared-library "deps deb" pre-warm layer — each
# image just curls its tarball (via the FIREFOX_TARBALL_URL build-arg) and unpacks it.
#
# The archival side (mirroring Mozilla's tarballs to the bucket + producing the
# manifest) lives elsewhere; this script only consumes the manifest. It expects the
# same shape as the Chrome manifest, one object per version:
#     { "major": 125, "filename": "firefox-125.0.tar.xz",
#       "sha256": "…", "tigris_key": "firefox/125/firefox-125.0.tar.xz" }
#
# Requirements: docker, jq, curl
#
# Usage:
#   ./scripts/build-firefox-images.sh [options]
#
# Options:
#   --manifest <path>     Use a local manifest.json instead of fetching from the bucket.
#   --registry <ref>      Image name prefix (default: ghcr.io/techarohq/gubal/firefox).
#   --only <majors>       Comma-separated majors to build (e.g. 78,99,125).
#   --push                docker push each tag after a successful build.
#   --dry-run             Print the docker build commands without running them.
#   -h, --help            Show this help.
#
# Env overrides:
#   FIREFOX_BUCKET_URL    Public bucket base (default: https://chrome-archive.t3.tigrisfiles.io,
#                         the same bucket the Chrome cache uses — Firefox lives under firefox/).
#   REGISTRY              Same as --registry.
#   DOCKER                docker binary (default: docker).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUCKET_URL="${FIREFOX_BUCKET_URL:-https://chrome-archive.t3.tigrisfiles.io}"
REGISTRY="${REGISTRY:-ghcr.io/techarohq/gubal/firefox}"
DOCKER="${DOCKER:-docker}"

MANIFEST_PATH=""
ONLY=""
PUSH=false
DRY_RUN=false

log() { echo "[$(date -u +%H:%M:%S)] $*" >&2; }
die() { echo "error: $*" >&2; exit 1; }

usage() { sed -n '2,/^set -euo/{/^set -euo/d;p}' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) MANIFEST_PATH="${2:?}"; shift 2 ;;
    --registry) REGISTRY="${2:?}"; shift 2 ;;
    --only)     ONLY="${2:?}"; shift 2 ;;
    --push)     PUSH=true; shift ;;
    --dry-run)  DRY_RUN=true; shift ;;
    -h|--help)  usage; exit 0 ;;
    *)          die "unknown option: $1 (try --help)" ;;
  esac
done

command -v jq   >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"
command -v "${DOCKER}" >/dev/null || die "${DOCKER} is required"

# Map a Firefox major version to its contemporary Ubuntu release / Dockerfile. Boundaries
# track the Firefox major shipping around each Ubuntu LTS release: 60~18.04, 75~20.04,
# 99~22.04, 125~24.04, 137~26.04.
ubuntu_for_major() {
  local m="$1"
  if   [ "$m" -le 74  ]; then echo "18.04"
  elif [ "$m" -le 98  ]; then echo "20.04"
  elif [ "$m" -le 124 ]; then echo "22.04"
  elif [ "$m" -le 136 ]; then echo "24.04"
  else                        echo "26.04"
  fi
}

# True if $1 is in the comma-separated --only list (or the list is empty).
selected() {
  [ -z "${ONLY}" ] && return 0
  local m="$1" want
  IFS=',' read -ra want <<< "${ONLY}"
  for w in "${want[@]}"; do [ "$w" = "$m" ] && return 0; done
  return 1
}

# --- Load the manifest ----------------------------------------------------------

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT
MANIFEST="${WORKDIR}/manifest.json"

if [ -n "${MANIFEST_PATH}" ]; then
  [ -f "${MANIFEST_PATH}" ] || die "manifest not found: ${MANIFEST_PATH}"
  cp "${MANIFEST_PATH}" "${MANIFEST}"
  log "Using local manifest ${MANIFEST_PATH}"
else
  log "Fetching manifest from ${BUCKET_URL}/firefox/manifest.json"
  curl -fsSL "${BUCKET_URL}/firefox/manifest.json" -o "${MANIFEST}" \
    || die "failed to fetch manifest"
fi

total="$(jq 'length' "${MANIFEST}")"
log "Manifest holds ${total} versions; registry=${REGISTRY}"

# --- Build loop -----------------------------------------------------------------

built=(); failed=(); skipped=()

while IFS=$'\t' read -r major filename sha256 key; do
  selected "${major}" || { skipped+=("${major}"); continue; }

  full_version="$(echo "${filename}" | grep -oE '[0-9]+(\.[0-9]+)+' | head -n1)"
  [ -n "${full_version}" ] || { log "!! could not parse version from ${filename}, skipping"; failed+=("${major}?"); continue; }

  ubuntu="$(ubuntu_for_major "${major}")"
  dockerfile="${REPO_ROOT}/docker/Dockerfile.firefox-ubuntu-${ubuntu}"
  [ -f "${dockerfile}" ] || { log "!! missing ${dockerfile}, skipping ${full_version}"; failed+=("${full_version}"); continue; }

  tarball_url="${BUCKET_URL}/${key}"
  tag_full="${REGISTRY}:${full_version}"
  tag_major="${REGISTRY}:${major}"

  log "== Firefox ${full_version} -> ubuntu:${ubuntu} =="
  log "   tarball: ${tarball_url}"
  log "   tags: ${tag_full} ${tag_major}"

  build_cmd=("${DOCKER}" build
    -f "${dockerfile}"
    --build-arg "FIREFOX_TARBALL_URL=${tarball_url}"
    --build-arg "FIREFOX_TARBALL_SHA256=${sha256}"
    -t "${tag_full}" -t "${tag_major}"
    "${REPO_ROOT}")

  if ${DRY_RUN}; then
    echo "+ ${build_cmd[*]}"
    ${PUSH} && echo "+ ${DOCKER} push ${tag_full} && ${DOCKER} push ${tag_major}"
    built+=("${full_version}")
    continue
  fi

  if ! "${build_cmd[@]}"; then
    log "   BUILD FAILED for ${full_version}"
    failed+=("${full_version}")
    continue
  fi

  if ${PUSH}; then
    if "${DOCKER}" push "${tag_full}" && "${DOCKER}" push "${tag_major}"; then
      log "   pushed ${tag_full}"
    else
      log "   PUSH FAILED for ${full_version}"
      failed+=("${full_version}")
      continue
    fi
  fi

  built+=("${full_version}")
done < <(jq -r '.[] | [.major, .filename, .sha256, .tigris_key] | @tsv' "${MANIFEST}")

# --- Summary --------------------------------------------------------------------

echo
log "Done. built=${#built[@]} failed=${#failed[@]} skipped=${#skipped[@]}"
[ "${#built[@]}"  -gt 0 ] && log "  built:  ${built[*]}"
[ "${#failed[@]}" -gt 0 ] && log "  failed: ${failed[*]}"

[ "${#failed[@]}" -eq 0 ]
