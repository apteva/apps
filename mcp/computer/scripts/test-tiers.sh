#!/usr/bin/env bash
# Run from any directory. Tier 3 includes configured Patreon test-account flows.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off
export APTEVA_HEADLESS_BROWSER=1
case "${1:-all}" in
  1) go test -race -short -count=1 -timeout 10m ./... ;;
  2)
    export RUN_COMPUTER_APP_BROWSER_TESTS=1 RUN_COMPUTER_ENVIRONMENT_TESTS=1
    export RUN_COMPUTER_DOWNLOAD_TESTS=1 RUN_COMPUTER_PRESENTATION_TESTS=1
    export RUN_COMPUTER_OUTCOME_TESTS=1 RUN_COMPUTER_SEMANTIC_TESTS=1
    export RUN_COMPUTER_NAVIGATION_TESTS=1 RUN_COMPUTER_GUARDED_CLICK_TESTS=1
    export RUN_COMPUTER_SELECTOR_CLICK_TESTS=1
    # Browser-heavy packages compete for rendering and timers on one host.
    go test -p 1 -race -count=1 -timeout 30m ./...
    ;;
  3)
    export RUN_COMPUTER_LLM_TESTS=1
    # Live Patreon workflows are discovered by the same TestLLM prefix. Set a
    # disposable creator URL plus context to enable them. Use provider context
    # and Browserbase credentials to test this checkout in isolated sidecars.
    if [[ "${COMPUTER_TIER3_REQUIRE_PATREON:-0}" == "1" ]]; then
      : "${COMPUTER_PATREON_CREATOR_URL:?set the disposable test creator URL}"
      : "${COMPUTER_PATREON_DRAFT_URL:?set the dedicated editor draft URL}"
      : "${COMPUTER_PATREON_ORIGINAL_TEXT:?set the exact original draft body}"
      if [[ -z "${COMPUTER_PATREON_CONTEXT_ID:-}${COMPUTER_PATREON_PROVIDER_CONTEXT_ID:-}" ]]; then
        echo "Patreon tier 3 requires a saved context" >&2; exit 2
      fi
    fi
    # Point COMPUTER_LLM_CODEX_BIN at a current authenticated Codex binary.
    "${COMPUTER_LLM_CODEX_BIN:-codex}" --version
    go test -p 1 -count=1 -v -run '^TestLLM' -timeout 90m ./...
    ;;
  ui)
    bun test ui
    bun scripts/typecheck-ui.ts
    bun ../../scripts/build-panels.ts --app computer
    ;;
  all)
    for tier in 1 2 3 ui; do bash scripts/test-tiers.sh "$tier"; done
    ;;
  *) echo "usage: $0 [1|2|3|ui|all]" >&2; exit 2 ;;
esac
