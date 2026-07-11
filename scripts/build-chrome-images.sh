#!/usr/bin/env bash
#
# build-chrome-images.sh
#
# Ingests the manifest produced by archive-chrome-versions.sh and builds one Docker
# image per Chrome version, each on the Ubuntu release contemporary with that Chrome.
#
# The Chrome deb for each entry is pulled from the public Tigris bucket at build time
# (via CHROME_DEB_URL build-arg), so the images are reproducible from the archive.
#
# Requirements: docker, jq, curl
#
# Usage:
#   ./scripts/build-chrome-images.sh [options]
#
# Options:
#   --manifest <path>     Use a local manifest.json instead of fetching from Tigris.
#   --registry <ref>      Image name prefix (default: ghcr.io/techarohq/gubal/chrome).
#   --only <majors>       Comma-separated majors to build (e.g. 90,110,120).
#   --push                docker push each tag after a successful build.
#   --dry-run             Print the docker build commands without running them.
#   -h, --help            Show this help.
#
# Env overrides:
#   CHROME_BUCKET_URL     Public bucket base (default: https://chrome-archive.t3.tigrisfiles.io)
#   REGISTRY              Same as --registry.
#   DOCKER                docker binary (default: docker).
#
# Examples:
#   ./scripts/build-chrome-images.sh --only 120
#   ./scripts/build-chrome-images.sh --push
#   ./scripts/build-chrome-images.sh --manifest ./manifest.json --dry-run

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUCKET_URL="${CHROME_BUCKET_URL:-https://chrome-archive.t3.tigrisfiles.io}"
REGISTRY="${REGISTRY:-ghcr.io/techarohq/gubal/chrome}"
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

# Map a Chrome major version to its contemporary Ubuntu release / Dockerfile.
ubuntu_for_major() {
  local m="$1"
  if   [ "$m" -le 79  ]; then echo "18.04"
  elif [ "$m" -le 99  ]; then echo "20.04"
  elif [ "$m" -le 119 ]; then echo "22.04"
  elif [ "$m" -le 139 ]; then echo "24.04"
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
  log "Fetching manifest from ${BUCKET_URL}/chrome/manifest.json"
  curl -fsSL "${BUCKET_URL}/chrome/manifest.json" -o "${MANIFEST}" \
    || die "failed to fetch manifest"
fi

total="$(jq 'length' "${MANIFEST}")"
log "Manifest holds ${total} versions; registry=${REGISTRY}"

# --- Discover one representative deb per era for the dependency-cache layer ------
#
# Every Chrome version in an era shares the same shared-library dependency closure,
# so the Dockerfiles warm it once from an era-representative deb passed via
# CHROME_DEPS_DEB_URL. We pick the LOWEST major present in each era: it's a real,
# existing deb, its URL is stable as the manifest grows (so the layer stays cached),
# and because targets are >= it, the final per-version install never downgrades.
declare -A DEPS_KEY_FOR_UBUNTU DEPS_MAJOR_FOR_UBUNTU
while IFS=$'\t' read -r d_major d_key; do
  d_ubuntu="$(ubuntu_for_major "${d_major}")"
  cur="${DEPS_MAJOR_FOR_UBUNTU[${d_ubuntu}]:-}"
  if [ -z "${cur}" ] || [ "${d_major}" -lt "${cur}" ]; then
    DEPS_MAJOR_FOR_UBUNTU[${d_ubuntu}]="${d_major}"
    DEPS_KEY_FOR_UBUNTU[${d_ubuntu}]="${d_key}"
  fi
done < <(jq -r '.[] | [.major, .tigris_key] | @tsv' "${MANIFEST}")

# --- Build loop -----------------------------------------------------------------

built=(); failed=(); skipped=()

while IFS=$'\t' read -r major filename sha256 key; do
  selected "${major}" || { skipped+=("${major}"); continue; }

  full_version="$(echo "${filename}" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
  [ -n "${full_version}" ] || { log "!! could not parse version from ${filename}, skipping"; failed+=("${major}?"); continue; }

  ubuntu="$(ubuntu_for_major "${major}")"
  dockerfile="${REPO_ROOT}/docker/Dockerfile.ubuntu-${ubuntu}"
  [ -f "${dockerfile}" ] || { log "!! missing ${dockerfile}, skipping ${full_version}"; failed+=("${full_version}"); continue; }

  deb_url="${BUCKET_URL}/${key}"
  deps_deb_url="${BUCKET_URL}/${DEPS_KEY_FOR_UBUNTU[${ubuntu}]}"
  tag_full="${REGISTRY}:${full_version}"
  tag_major="${REGISTRY}:${major}"

  log "== Chrome ${full_version} -> ubuntu:${ubuntu} =="
  log "   deb: ${deb_url}"
  log "   deps: major ${DEPS_MAJOR_FOR_UBUNTU[${ubuntu}]} (${deps_deb_url})"
  log "   tags: ${tag_full} ${tag_major}"

  build_cmd=("${DOCKER}" build
    -f "${dockerfile}"
    --build-arg "CHROME_DEB_URL=${deb_url}"
    --build-arg "CHROME_DEB_SHA256=${sha256}"
    --build-arg "CHROME_DEPS_DEB_URL=${deps_deb_url}"
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
