#!/usr/bin/env bash
#
# firefox-entrypoint.sh — front an archived Firefox with foxbridge so the image
# speaks the Chrome DevTools Protocol on :9222, exactly like the Chrome era images.
#
# foxbridge (github.com/VulpineOS/foxbridge) launches Firefox itself and translates
# CDP calls into Firefox's WebDriver BiDi protocol, so existing CDP tooling
# (chromedp, Puppeteer, ...) can drive Firefox unchanged. Plain upstream Firefox
# speaks BiDi but NOT Juggler, so the default backend here is `bidi`.
#
# Env knobs (all optional):
#   FIREFOX_BIN            Firefox binary                     (default: /opt/firefox/firefox)
#   FOXBRIDGE_BIN          foxbridge binary                   (default: foxbridge)
#   CDP_PORT              external CDP port                   (default: 9222)
#   CDP_ADDRESS          bind address for the CDP port       (default: 0.0.0.0)
#   FOXBRIDGE_BACKEND     juggler|bidi                        (default: bidi)
#   FOXBRIDGE_BIDI_PORT   Firefox BiDi port foxbridge drives  (default: 9223)
#   FIREFOX_HEADLESS      true => run headless                (default: true)
#   FOXBRIDGE_SOCAT_BRIDGE true (default) => foxbridge binds an internal loopback
#                         port and socat re-exposes it on CDP_ADDRESS. Robust whether
#                         foxbridge binds loopback or all interfaces. Set false to let
#                         foxbridge bind CDP_PORT directly (only if you drive it in-pod).
#   FOXBRIDGE_EXTRA_FLAGS extra flags appended to the foxbridge command line
#
set -euo pipefail

FIREFOX_BIN="${FIREFOX_BIN:-/opt/firefox/firefox}"
FOXBRIDGE_BIN="${FOXBRIDGE_BIN:-foxbridge}"
CDP_PORT="${CDP_PORT:-9222}"
CDP_ADDRESS="${CDP_ADDRESS:-0.0.0.0}"
BACKEND="${FOXBRIDGE_BACKEND:-bidi}"
BIDI_PORT="${FOXBRIDGE_BIDI_PORT:-9223}"
HEADLESS="${FIREFOX_HEADLESS:-true}"
SOCAT_BRIDGE="${FOXBRIDGE_SOCAT_BRIDGE:-true}"

# --- Sanity: we need a real browser to drive -------------------------------------
#
# This image family is browser-less on its own (the foxbridge base has no Firefox);
# fail loudly rather than let foxbridge spawn nothing.
if [ ! -x "${FIREFOX_BIN}" ]; then
  echo "FATAL: Firefox binary not found or not executable at '${FIREFOX_BIN}'." >&2
  echo "       The foxbridge base image carries no browser — run a firefox-* image," >&2
  echo "       or point FIREFOX_BIN at a mounted Firefox." >&2
  exit 1
fi

full_version="$("${FIREFOX_BIN}" --version 2>/dev/null \
  | grep -oE '[0-9]+(\.[0-9]+)+' | head -n1 || true)"

# --- Decide where foxbridge's CDP server binds -----------------------------------
#
# We don't rely on foxbridge honouring a bind address, so by default foxbridge listens
# on an internal port and socat forwards the external port to it. This is safe whether
# foxbridge binds loopback or 0.0.0.0 (the internal port differs from the external one,
# so there's no conflict), and mirrors the Chrome image's socat bridge.
if [ "${SOCAT_BRIDGE}" = "true" ]; then
  internal_port="$(( CDP_PORT + 10000 ))"
  fox_port="${internal_port}"
else
  fox_port="${CDP_PORT}"
fi

# --- Assemble the foxbridge command line -----------------------------------------

args=(
  "--backend" "${BACKEND}"
  "--binary" "${FIREFOX_BIN}"
  "--port" "${fox_port}"
  "--bidi-port" "${BIDI_PORT}"
)

if [ "${HEADLESS}" = "true" ]; then
  args+=("--headless")
fi

# FOXBRIDGE_EXTRA_FLAGS is intentionally word-split so callers can pass several flags.
if [ -n "${FOXBRIDGE_EXTRA_FLAGS:-}" ]; then
  # shellcheck disable=SC2206
  extra=(${FOXBRIDGE_EXTRA_FLAGS})
  args+=("${extra[@]}")
fi

echo "firefox ${full_version:-unknown} | backend=${BACKEND} headless=${HEADLESS} bridge=${SOCAT_BRIDGE}" >&2
echo "CDP: ${CDP_ADDRESS}:${CDP_PORT} (foxbridge on 127.0.0.1:${fox_port}, Firefox BiDi :${BIDI_PORT})" >&2

# --- Launch ----------------------------------------------------------------------

if [ "${SOCAT_BRIDGE}" = "true" ]; then
  "${FOXBRIDGE_BIN}" "${args[@]}" "$@" &
  fox_pid=$!
  # If foxbridge dies, take the bridge (and pod) down with it.
  trap 'kill "${fox_pid}" 2>/dev/null || true' EXIT
  exec socat "TCP-LISTEN:${CDP_PORT},fork,reuseaddr,bind=${CDP_ADDRESS}" \
             "TCP:127.0.0.1:${internal_port}"
else
  exec "${FOXBRIDGE_BIN}" "${args[@]}" "$@"
fi
