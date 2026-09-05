#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export ANALYTICS_BROWSER_DIR="${ANALYTICS_BROWSER_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/analytics-browser.XXXXXX")}"
mkdir -p "$ANALYTICS_BROWSER_DIR/assets"
rm -f "$ANALYTICS_BROWSER_DIR/url"
bun build browser/entry.tsx --outdir "$ANALYTICS_BROWSER_DIR/assets" --target browser --production --define 'process.env.NODE_ENV="production"'
ANALYTICS_BROWSER_HARNESS=1 GOWORK=off go test -run '^TestBrowserHarness$' -v -timeout 10m > "$ANALYTICS_BROWSER_DIR/server.log" 2>&1 &
server_pid=$!
cleanup() {
 if [[ -s "$ANALYTICS_BROWSER_DIR/url" ]]; then curl -sf -X POST "$(cat "$ANALYTICS_BROWSER_DIR/url")/__test/stop" >/dev/null || true; fi
 wait "$server_pid" || true
 echo "Browser evidence: $ANALYTICS_BROWSER_DIR"
}
trap cleanup EXIT
for ((i=0;i<120;i++)); do
 if [[ -s "$ANALYTICS_BROWSER_DIR/url" ]]; then break; fi
 if ! kill -0 "$server_pid" 2>/dev/null; then cat "$ANALYTICS_BROWSER_DIR/server.log"; exit 1; fi
 sleep 1
done
if [[ ! -s "$ANALYTICS_BROWSER_DIR/url" ]]; then cat "$ANALYTICS_BROWSER_DIR/server.log"; kill "$server_pid"; exit 1; fi
bunx --no-install playwright test
