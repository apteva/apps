#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
export GOWORK=off
bun install --frozen-lockfile
bun run typecheck
bun build ui/JobsPanel.tsx --outdir ui --entry-naming JobsPanel.mjs --target browser --format esm --external react --external react/jsx-runtime --sourcemap=external --minify
bun test ui/JobsPanel.test.ts
go test -tags integration ./... -count=1 -cover -timeout=180s
go test -race ./... -count=1 -timeout=180s
go vet ./...
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
go build -o "$build_dir/jobs" .
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
