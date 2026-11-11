#!/bin/sh
set -eu
# Run against a local server checkout with the runtime public-auth preservation fix.
: "${APTEVA_SERVER_SOURCE:?Set APTEVA_SERVER_SOURCE to the local server checkout}"
fixture_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
apps_dir=$(CDPATH= cd -- "$fixture_dir/../../.." && pwd)
smoke_dir=$(mktemp -d /tmp/apteva-browser-smoke.XXXXXX)
cleanup() { chmod -R u+w "$smoke_dir"; rm -rf -- "$smoke_dir"; }
trap cleanup EXIT INT TERM
for app in tests computer environments api auth; do cp -R "$apps_dir/$app" "$smoke_dir/$app"; done
cp -R "$fixture_dir/fixture" "$smoke_dir/fixture"
sdk_dir=$(cd "$apps_dir/tests" && GOWORK=off go list -m -f '{{.Dir}}' github.com/apteva/app-sdk)
ln -s "$sdk_dir" "$smoke_dir/app-sdk"
cp "$APTEVA_SERVER_SOURCE/go.mod" "$smoke_dir/server.mod"
cp "$APTEVA_SERVER_SOURCE/go.sum" "$smoke_dir/server.sum"
go mod edit -modfile="$smoke_dir/server.mod" -replace="github.com/apteva/app-sdk=$sdk_dir"
# Overlay adds one test without changing the server checkout.
SMOKE_DIR="$smoke_dir" FIXTURE_DIR="$fixture_dir" python3 - <<'PY'
import os,json
root=os.environ['SMOKE_DIR']
json.dump({'Replace':{os.path.join(os.environ['APTEVA_SERVER_SOURCE'],'codex_browser_smoke_test.go'):os.path.join(os.environ['FIXTURE_DIR'],'server_smoke_test.go.txt')}},open(root+'/overlay.json','w'))
PY
cd "$APTEVA_SERVER_SOURCE"
APTEVA_BROWSER_SMOKE="$smoke_dir" GOWORK=off go test -overlay="$smoke_dir/overlay.json" -mod=mod -modfile="$smoke_dir/server.mod" -run '^TestBrowserReleaseLocalSmoke$' -count=1 -v .
