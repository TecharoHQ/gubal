#!/usr/bin/env bash
#
# archive-chrome-versions.sh
#
# Downloads Google Chrome .deb packages across a range of major versions
# from known mirrors, verifies them, and uploads to a Tigris bucket with
# a JSON manifest (version, filename, sha256, size, source, upload time).
#
# Runs are incremental (upsert): the existing chrome/manifest.json is pulled at
# startup, any version whose S3 key is already recorded there is skipped (no
# re-download, no re-upload), and newly fetched versions are merged into the
# manifest rather than overwriting it. So `... bucket 144 150` adds 144-150
# without dropping previously-archived versions. This assumes the manifest is
# the source of truth for what's in the bucket (i.e. this script is the only
# writer); a new patch level yields a new key and is fetched normally.
#
# Sources tried in order per version:
#   1. NDViet/google-chrome-stable GitHub releases (majors 144-150, current
#      official debs published per browser-matrix.yml)
#   2. UChicago mirror (has v71+, reliable, official Google debs)
#   3. webnicer/chrome-downloads on GitHub (has v48+, fills the v66-70 gap)
#
# Requirements: curl, jq, aws-cli (configured for Tigris), sha256sum
#
# Tigris auth: this script uses the `aws` CLI pointed at Tigris' S3-compatible
# endpoint. Set these before running (or use `aws configure` / env vars):
#   export AWS_ACCESS_KEY_ID=...
#   export AWS_SECRET_ACCESS_KEY=...
#   export AWS_ENDPOINT_URL_S3=https://fly.storage.tigris.dev
#
# Usage:
#   ./archive-chrome-versions.sh <bucket-name> [min-major] [max-major]
#
# Example:
#   ./archive-chrome-versions.sh chrome-archive 66 150

set -euo pipefail

BUCKET="${1:?Usage: $0 <bucket-name> [min-major] [max-major]}"
MIN_MAJOR="${2:-66}"
MAX_MAJOR="${3:-150}"
TIGRIS_ENDPOINT="${AWS_ENDPOINT_URL_S3:-https://fly.storage.tigris.dev}"

WORKDIR="$(mktemp -d)"
MANIFEST="${WORKDIR}/manifest.jsonl"
EXISTING_MANIFEST="${WORKDIR}/existing_manifest.json"
touch "${MANIFEST}"
trap 'rm -rf "${WORKDIR}"' EXIT

UCHICAGO_BASE="https://mirror.cs.uchicago.edu/google-chrome/pool/main/g/google-chrome-stable"
WEBNICER_BASE="https://raw.githubusercontent.com/webnicer/chrome-downloads/master/x64.deb"
NDVIET_REPO="NDViet/google-chrome-stable"
NDVIET_MIN_MAJOR=144
NDVIET_MAX_MAJOR=150

log() { echo "[$(date -u +%H:%M:%S)] $*" >&2; }

# Pull the existing manifest so we can upsert: already-archived keys are skipped
# and surviving entries are merged back in at the end. Missing manifest (first
# run) is fine — start from an empty array.
log "Fetching existing manifest s3://${BUCKET}/chrome/manifest.json ..."
if aws s3 cp "s3://${BUCKET}/chrome/manifest.json" "${EXISTING_MANIFEST}" \
	--endpoint-url "${TIGRIS_ENDPOINT}" --no-progress 2>/dev/null; then
	log "  Loaded $(jq length "${EXISTING_MANIFEST}") existing entries."
else
	log "  No existing manifest found; starting fresh."
	echo '[]' >"${EXISTING_MANIFEST}"
fi

# Pull the UChicago directory listing once, so we can match major versions
# to their actual patch-level filenames without guessing.
log "Fetching UChicago mirror index..."
curl -fsSL "${UCHICAGO_BASE}/" -o "${WORKDIR}/uchicago_index.html"

# Extract every filename like google-chrome-stable_XX.Y.ZZZZ.WW-1_amd64.deb
grep -oE 'google-chrome-stable_[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+-1_amd64\.deb' \
	"${WORKDIR}/uchicago_index.html" | sort -u >"${WORKDIR}/uchicago_files.txt"

# Pull the NDViet release list once (covers majors 144-150). The .deb download
# URLs live on github.com and don't count against the API rate limit, so this is
# the only GitHub API call we make.
NDVIET_RELEASES="${WORKDIR}/ndviet_releases.json"
if [[ "${MAX_MAJOR}" -ge "${NDVIET_MIN_MAJOR}" && "${MIN_MAJOR}" -le "${NDVIET_MAX_MAJOR}" ]]; then
	log "Fetching NDViet google-chrome-stable releases..."
	curl -fsSL -H "Accept: application/vnd.github+json" \
		"https://api.github.com/repos/${NDVIET_REPO}/releases?per_page=100" \
		-o "${NDVIET_RELEASES}"
fi

# Pull the webnicer directory listing once so the pre-71 fallback can match the
# real patch-level filenames (e.g. google-chrome-stable_66.0.3359.181-1_amd64.deb)
# instead of guessing. One more unauthenticated GitHub API call; the raw .deb
# downloads are served from raw.githubusercontent.com and don't count against the
# API rate limit. Directory listings under 1000 entries come back in one page.
WEBNICER_FILES="${WORKDIR}/webnicer_files.txt"
log "Fetching webnicer chrome-downloads index..."
curl -fsSL -H "Accept: application/vnd.github+json" \
	"https://api.github.com/repos/webnicer/chrome-downloads/contents/x64.deb?per_page=1000" \
	-o "${WORKDIR}/webnicer_index.json"
jq -r '.[].name' "${WORKDIR}/webnicer_index.json" \
	| grep -E '^google-chrome-stable_[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+-1_amd64\.deb$' \
	| sort -u >"${WEBNICER_FILES}"

for major in $(seq "${MIN_MAJOR}" "${MAX_MAJOR}"); do
	log "== Major version ${major} =="

	src=""
	fname=""

	# 1. NDViet GitHub releases for majors 144-150. Tags are the full package
	# version (e.g. 144.0.7559.132-1) and each release carries a deb named
	# google-chrome-stable_<tag>_amd64.deb; pick the highest patch per major.
	if [[ -f "${NDVIET_RELEASES}" && "${major}" -ge "${NDVIET_MIN_MAJOR}" && "${major}" -le "${NDVIET_MAX_MAJOR}" ]]; then
		nd_tag="$(jq -r --arg major "${major}" \
			'.[].tag_name | select(startswith($major + "."))' \
			"${NDVIET_RELEASES}" | sort -V | tail -n1 || true)"
		if [[ -n "${nd_tag}" ]]; then
			fname="google-chrome-stable_${nd_tag}_amd64.deb"
			src="https://github.com/${NDVIET_REPO}/releases/download/${nd_tag}/${fname}"
		fi
	fi

	# 2. UChicago mirror. Find all filenames for this major version, take the
	# highest patch as the representative build (adjust if you want every patch).
	if [[ -z "${src}" ]]; then
		match="$(grep -E "^google-chrome-stable_${major}\." "${WORKDIR}/uchicago_files.txt" | sort -V | tail -n1 || true)"
		if [[ -n "${match}" ]]; then
			fname="${match}"
			src="${UCHICAGO_BASE}/${fname}"
		fi
	fi

	# 3. Fall back to the webnicer archive, which fills the pre-71 gap (66-70)
	# and backstops any major UChicago is missing. Filenames carry the full patch
	# version, e.g. google-chrome-stable_66.0.3359.181-1_amd64.deb; match the
	# highest patch from the fetched listing rather than guessing.
	if [[ -z "${src}" ]]; then
		wn_match="$(grep -E "^google-chrome-stable_${major}\." "${WEBNICER_FILES}" | sort -V | tail -n1 || true)"
		if [[ -n "${wn_match}" ]]; then
			fname="${wn_match}"
			src="${WEBNICER_BASE}/${fname}"
		fi
	fi

	if [[ -z "${src}" ]]; then
		log "  No match found for major ${major} in any source — skipping. Check the NDViet/webnicer repos manually."
		continue
	fi

	key="chrome/${major}/${fname}"

	# Upsert: if this exact key is already recorded, leave it be. Its entry stays
	# in EXISTING_MANIFEST and is merged back in at the end.
	if [[ "$(jq --arg key "${key}" 'any(.[]; .tigris_key == $key)' "${EXISTING_MANIFEST}")" == "true" ]]; then
		log "  Already archived (${key}); skipping download/upload."
		continue
	fi

	dest="${WORKDIR}/${fname}"
	log "  Downloading ${fname} from ${src}"
	if ! curl -fsSL "${src}" -o "${dest}"; then
		log "  Download failed for major ${major}, skipping."
		continue
	fi

	size="$(stat -c%s "${dest}")"
	sha256="$(sha256sum "${dest}" | awk '{print $1}')"

	log "  sha256=${sha256} size=${size}"
	log "  Uploading to s3://${BUCKET}/${key}"
	aws s3 cp "${dest}" "s3://${BUCKET}/${key}" \
		--endpoint-url "${TIGRIS_ENDPOINT}" \
		--no-progress

	jq -n \
		--arg major "${major}" \
		--arg filename "${fname}" \
		--arg sha256 "${sha256}" \
		--arg size "${size}" \
		--arg source "${src}" \
		--arg key "${key}" \
		--arg uploaded_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		'{major: ($major|tonumber), filename: $filename, sha256: $sha256, size: ($size|tonumber), source: $source, tigris_key: $key, uploaded_at: $uploaded_at}' \
		>>"${MANIFEST}"

	rm -f "${dest}"
done

# Merge this run's new entries into the existing manifest (upsert), dedupe by
# S3 key preferring the freshest entry, sort, and push it back.
jq -s '.' "${MANIFEST}" >"${WORKDIR}/new_entries.json"
jq -s '
	(.[0] + .[1])
	| group_by(.tigris_key)
	| map(max_by(.uploaded_at))
	| sort_by(.major, .tigris_key)
' "${EXISTING_MANIFEST}" "${WORKDIR}/new_entries.json" >"${WORKDIR}/manifest.json"

aws s3 cp "${WORKDIR}/manifest.json" "s3://${BUCKET}/chrome/manifest.json" \
	--endpoint-url "${TIGRIS_ENDPOINT}" \
	--content-type application/json

log "Done. Manifest uploaded to s3://${BUCKET}/chrome/manifest.json"
log "Added this run: $(jq length "${WORKDIR}/new_entries.json") | total in manifest: $(jq length "${WORKDIR}/manifest.json")"
