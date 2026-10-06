#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
# Bun is build tooling only. The deployed sidecar and browser mesh engine are Go.
GOWORK=off GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o ui/engine.wasm ./cmd/wasm
cp "$(GOWORK=off go env GOROOT)/lib/wasm/wasm_exec.js" ui/wasm_exec.js
cp "$(GOWORK=off go env GOROOT)/LICENSE" ui/wasm_exec.LICENSE
GOWORK=off go run ./cmd/examples
bun run scripts/build-panel.ts
GOWORK=off go build -trimpath -o /tmp/apteva-3d-studio .
