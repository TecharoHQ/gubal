#!/usr/bin/env bash
#
# archive-firefox-versions.sh
#
# Downloads Firefox linux-x86_64 release tarballs across a range of major versions
# from Mozilla's official archive, verifies them, and uploads them to the SAME Tigris
# bucket the Chrome cache lives in — under a firefox/ prefix — with a JSON manifest
# (major, version, filename, sha256, size, source, upload time) at firefox/manifest.json.
#
# Selection policy — "the most recent non-beta build of each major":
#   For each major we list every version directory Mozilla actually published
#   (https://archive.mozilla.org/pub/firefox/releases/), drop betas/alphas/rc
#   (anything that isn't X.Y[.Z] with an optional `esr` suffix), and take the highest
#   remaining version. For an ESR-base major (78, 91, 102, 115, 128, 140, ...) that is
#   the final `…esr` patch (e.g. 128.14.0esr) — the best-patched build of that line;
#   for a rapid-release-only major it is the last dot release (e.g. 105.0.3). This
#   mirrors the Chrome archiver's "highest patch per major is the representative build".
#
# The exact tarball filename (Mozilla switched linux tarballs from .tar.bz2 to .tar.xz
# around Firefox 135, and esr builds carry an `esr` suffix) is read from the per-version
# directory listing rather than guessed, so the recorded key/URL is always real.
#
# Runs are incremental (upsert): the existing firefox/manifest.json is pulled at
# startup, any version whose S3 key is already recorded is skipped (no re-download, no
# re-upload), and newly fetched versions are merged in rather than overwriting. This
# assumes the manifest is the source of truth for what's in the firefox/ prefix (i.e.
# this script is the only writer).
#
# Requirements: curl, jq, aws-cli (configured for Tigris), sha256sum
#
# Tigris auth (same as archive-chrome-versions.sh): this script uses the `aws` CLI
# pointed at Tigris' S3-compatible endpoint. Set these before running:
#   export AWS_ACCESS_KEY_ID=...
#   export AWS_SECRET_ACCESS_KEY=...
#   export AWS_ENDPOINT_URL_S3=https://fly.storage.tigris.dev
#
# Usage:
#   ./archive-firefox-versions.sh <bucket-name> [min-major] [max-major]
#
# Defaults: min-major 72 (Firefox 72.0, January 2020 — the first 2020 release), and
# max-major = the highest major currently in Mozilla's archive.
#
# Example (into the same bucket the Chrome debs live in):
#   ./archive-firefox-versions.sh chrome-archive 72 140

set -euo pipefail

BUCKET="${1:?Usage: $0 <bucket-name> [min-major] [max-major]}"
MIN_MAJOR="${2:-72}"
MAX_MAJOR="${3:-}"
TIGRIS_ENDPOINT="${AWS_ENDPOINT_URL_S3:-https://fly.storage.tigris.dev}"

ARCHIVE_BASE="https://archive.mozilla.org/pub/firefox/releases"

WORKDIR="$(mktemp -d)"
MANIFEST="${WORKDIR}/manifest.jsonl"
EXISTING_MANIFEST="${WORKDIR}/existing_manifest.json"
touch "${MANIFEST}"
trap 'rm -rf "${WORKDIR}"' EXIT

log() { echo "[$(date -u +%H:%M:%S)] $*" >&2; }

command -v jq   >/dev/null || { echo "error: jq is required" >&2; exit 1; }
command -v curl >/dev/null || { echo "error: curl is required" >&2; exit 1; }
command -v aws  >/dev/null || { echo "error: aws-cli is required" >&2; exit 1; }

# Pull the existing manifest so we can upsert: already-archived keys are skipped and
# surviving entries are merged back in at the end. Missing manifest (first run) is fine.
log "Fetching existing manifest s3://${BUCKET}/firefox/manifest.json ..."
if aws s3 cp "s3://${BUCKET}/firefox/manifest.json" "${EXISTING_MANIFEST}" \
	--endpoint-url "${TIGRIS_ENDPOINT}" --no-progress 2>/dev/null; then
	log "  Loaded $(jq length "${EXISTING_MANIFEST}") existing entries."
else
	log "  No existing manifest found; starting fresh."
	echo '[]' >"${EXISTING_MANIFEST}"
fi

# Pull the top-level release index once. This is the authoritative list of every
# version directory Mozilla actually published — including the esr dirs, which the
# product-details feed does not map cleanly to download paths.
log "Fetching Mozilla release index..."
curl -fsSL "${ARCHIVE_BASE}/" -o "${WORKDIR}/index.html"
grep -oE 'href="/pub/firefox/releases/[^"/]+/"' "${WORKDIR}/index.html" \
	| sed -E 's#href="/pub/firefox/releases/([^"/]+)/"#\1#' \
	| sort -u >"${WORKDIR}/alldirs.txt"
log "  Index lists $(wc -l <"${WORKDIR}/alldirs.txt") version directories."

# Default the ceiling to the highest major present in the archive.
if [[ -z "${MAX_MAJOR}" ]]; then
	MAX_MAJOR="$(grep -oE '^[0-9]+' "${WORKDIR}/alldirs.txt" | sort -n | tail -n1)"
fi
log "Archiving majors ${MIN_MAJOR}..${MAX_MAJOR} into s3://${BUCKET}/firefox/"

for major in $(seq "${MIN_MAJOR}" "${MAX_MAJOR}"); do
	log "== Major version ${major} =="

	# Non-beta candidates for this major: X.Y[.Z] with an optional esr suffix. This
	# excludes betas (72.0b1), alphas (a1/a2), and rc dirs by construction.
	grep -E "^${major}\.[0-9]+(\.[0-9]+)?(esr)?$" "${WORKDIR}/alldirs.txt" \
		>"${WORKDIR}/cand.txt" || true
	if [[ ! -s "${WORKDIR}/cand.txt" ]]; then
		log "  No non-beta release dir for major ${major} — skipping."
		continue
	fi

	# Most recent = highest by version number. Compare on the numeric part (esr suffix
	# stripped) but keep the real directory name (esr builds live under `<ver>esr/`).
	version="$(awk '{k=$0; sub(/esr$/,"",k); print k"\t"$0}' "${WORKDIR}/cand.txt" \
		| sort -V -k1,1 | tail -n1 | cut -f2)"

	# Read the exact tarball name from the per-version listing (.tar.xz or .tar.bz2).
	listing="${ARCHIVE_BASE}/${version}/linux-x86_64/en-US/"
	if ! curl -fsSL "${listing}" -o "${WORKDIR}/listing.html"; then
		log "  Could not list ${listing} — skipping ${version}."
		continue
	fi
	esc_v="${version//./\\.}"
	fname="$(grep -oE "firefox-${esc_v}\.tar\.(xz|bz2)" "${WORKDIR}/listing.html" \
		| sort -u | head -n1 || true)"
	if [[ -z "${fname}" ]]; then
		log "  No linux-x86_64 tarball for ${version} — skipping."
		continue
	fi

	src="${listing}${fname}"
	key="firefox/${major}/${fname}"

	# Upsert: if this exact key is already recorded, leave it be.
	if [[ "$(jq --arg key "${key}" 'any(.[]; .tigris_key == $key)' "${EXISTING_MANIFEST}")" == "true" ]]; then
		log "  Already archived (${key}); skipping download/upload."
		continue
	fi

	dest="${WORKDIR}/${fname}"
	log "  Downloading ${fname} from ${src}"
	if ! curl -fsSL "${src}" -o "${dest}"; then
		log "  Download failed for ${version}, skipping."
		continue
	fi

	size="$(stat -c%s "${dest}")"
	sha256="$(sha256sum "${dest}" | awk '{print $1}')"

	log "  version=${version} sha256=${sha256} size=${size}"
	log "  Uploading to s3://${BUCKET}/${key}"
	aws s3 cp "${dest}" "s3://${BUCKET}/${key}" \
		--endpoint-url "${TIGRIS_ENDPOINT}" \
		--no-progress

	jq -n \
		--arg major "${major}" \
		--arg version "${version}" \
		--arg filename "${fname}" \
		--arg sha256 "${sha256}" \
		--arg size "${size}" \
		--arg source "${src}" \
		--arg key "${key}" \
		--arg uploaded_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		'{major: ($major|tonumber), version: $version, filename: $filename, sha256: $sha256, size: ($size|tonumber), source: $source, tigris_key: $key, uploaded_at: $uploaded_at}' \
		>>"${MANIFEST}"

	rm -f "${dest}"
done

# Merge this run's new entries into the existing manifest (upsert), dedupe by S3 key
# preferring the freshest entry, sort, and push it back.
jq -s '.' "${MANIFEST}" >"${WORKDIR}/new_entries.json"
jq -s '
	(.[0] + .[1])
	| group_by(.tigris_key)
	| map(max_by(.uploaded_at))
	| sort_by(.major, .tigris_key)
' "${EXISTING_MANIFEST}" "${WORKDIR}/new_entries.json" >"${WORKDIR}/manifest.json"

aws s3 cp "${WORKDIR}/manifest.json" "s3://${BUCKET}/firefox/manifest.json" \
	--endpoint-url "${TIGRIS_ENDPOINT}" \
	--content-type application/json

log "Done. Manifest uploaded to s3://${BUCKET}/firefox/manifest.json"
log "Added this run: $(jq length "${WORKDIR}/new_entries.json") | total in manifest: $(jq length "${WORKDIR}/manifest.json")"
