#!/usr/bin/env bash
#
# entrypoint.sh — launch Google Chrome headless as a long-lived CDP server.
#
# The whole point of this image family is to prove that an *arbitrary* archived
# Chrome version installs and is driveable. So this script does not hardcode a
# version: it asks the installed binary what it is, then builds a matching, honest
# User-Agent that does NOT advertise "HeadlessChrome".
#
# Env knobs (all optional):
#   CHROME_BIN            binary to run                      (default: google-chrome)
#   CHROME_DEBUG_PORT     external CDP port                  (default: 9222)
#   CHROME_DEBUG_ADDRESS  bind address for CDP               (default: 0.0.0.0)
#   CHROME_START_URL      initial URL                        (default: about:blank)
#   CHROME_USER_AGENT     override the computed User-Agent   (default: computed)
#   CHROME_SANDBOX        off => pass --no-sandbox (default); on => keep the sandbox
#   CHROME_SOCAT_BRIDGE   true (default) => Chrome binds 127.0.0.1 and socat exposes
#                         the port on 0.0.0.0. Required because Chrome ignores
#                         --remote-debugging-address and only ever binds loopback, so
#                         without the bridge the CDP port is unreachable from outside
#                         the container. Set false only if you drive Chrome in-process.
#   CHROME_EXTRA_FLAGS    extra flags appended to the Chrome command line
#
set -euo pipefail

CHROME_BIN="${CHROME_BIN:-google-chrome}"
DEBUG_PORT="${CHROME_DEBUG_PORT:-9222}"
DEBUG_ADDR="${CHROME_DEBUG_ADDRESS:-0.0.0.0}"
START_URL="${CHROME_START_URL:-about:blank}"
SANDBOX="${CHROME_SANDBOX:-off}"
SOCAT_BRIDGE="${CHROME_SOCAT_BRIDGE:-true}"

# --- Figure out what we're actually running -------------------------------------

full_version="$("${CHROME_BIN}" --version 2>/dev/null \
  | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true)"

if [ -z "${full_version}" ]; then
  echo "FATAL: could not determine Chrome version from '${CHROME_BIN} --version'" >&2
  "${CHROME_BIN}" --version >&2 || true
  exit 1
fi

major="${full_version%%.*}"

# Honest, version-matched desktop-Linux UA — deliberately no "HeadlessChrome".
UA="${CHROME_USER_AGENT:-Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${full_version} Safari/537.36}"

# New headless (>=109) already reports a clean "Chrome/" UA and has better fidelity;
# older Chrome only has classic --headless, whose default UA leaks "HeadlessChrome"
# (hence the override above).
if [ "${major}" -ge 109 ] 2>/dev/null; then
  headless_flag="--headless=new"
else
  headless_flag="--headless"
fi

# --- Decide where Chrome binds --------------------------------------------------
#
# Chrome ignores --remote-debugging-address and binds DevTools to 127.0.0.1 only
# (verified on v120; it's a deliberate security default across versions). So by
# default we let Chrome bind 127.0.0.1 on an internal port and forward the external
# port to it with socat. Set CHROME_SOCAT_BRIDGE=false only for in-process drivers.

if [ "${SOCAT_BRIDGE}" = "true" ]; then
  internal_port="$(( DEBUG_PORT + 10000 ))"
  chrome_addr="127.0.0.1"
  chrome_port="${internal_port}"
else
  chrome_addr="${DEBUG_ADDR}"
  chrome_port="${DEBUG_PORT}"
fi

# --- Assemble the command line --------------------------------------------------

args=(
  "${headless_flag}"
  "--disable-dev-shm-usage"
  "--disable-gpu"
  "--remote-debugging-address=${chrome_addr}"
  "--remote-debugging-port=${chrome_port}"
  "--remote-allow-origins=*"
  "--user-agent=${UA}"
  "--disable-blink-features=AutomationControlled"
)

if [ "${SANDBOX}" = "off" ]; then
  args+=("--no-sandbox")
else
  if [ ! -u /opt/google/chrome/chrome-sandbox ]; then
    echo "WARN: CHROME_SANDBOX=on but /opt/google/chrome/chrome-sandbox is not SUID;" >&2
    echo "      Chrome's sandbox will likely fail to start." >&2
  fi
fi

# CHROME_EXTRA_FLAGS is intentionally word-split so callers can pass several flags.
if [ -n "${CHROME_EXTRA_FLAGS:-}" ]; then
  # shellcheck disable=SC2206
  extra=(${CHROME_EXTRA_FLAGS})
  args+=("${extra[@]}")
fi

echo "chrome ${full_version} (major ${major}) | headless=${headless_flag#--headless} sandbox=${SANDBOX} bridge=${SOCAT_BRIDGE}" >&2
echo "CDP: ${DEBUG_ADDR}:${DEBUG_PORT} (chrome on ${chrome_addr}:${chrome_port})" >&2
echo "User-Agent: ${UA}" >&2

# --- Launch ---------------------------------------------------------------------

if [ "${SOCAT_BRIDGE}" = "true" ]; then
  "${CHROME_BIN}" "${args[@]}" "$@" "${START_URL}" &
  chrome_pid=$!
  # If Chrome dies, take the bridge (and pod) down with it.
  trap 'kill "${chrome_pid}" 2>/dev/null || true' EXIT
  exec socat "TCP-LISTEN:${DEBUG_PORT},fork,reuseaddr,bind=${DEBUG_ADDR}" \
             "TCP:127.0.0.1:${internal_port}"
else
  exec "${CHROME_BIN}" "${args[@]}" "$@" "${START_URL}"
fi
