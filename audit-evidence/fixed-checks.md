# Final checks — 5 September 2026

Commands ran from `mcp/crm` with `GOWORK=off` unless noted.

- `go test ./... -count=1`: PASS (fixed-tests.txt).
- `go test -race -cover ./... -count=1`: PASS, 53.7% coverage (fixed-race.txt).
- `go vet ./...`: exit 0 (empty fixed-vet.txt).
- `go test -tags integration -run '^TestSidecar_' -v -count=1`: five PASS (fixed-integration.txt).
- `bun audit-ui-repro.ts`, from repo root: PASS (fixed-ui.jsonl).
- `bun run scripts/build-panels.ts --app crm`, from repo root: PASS (fixed-panel-build.txt).
- TypeScript no-emit check using the dashboard's installed compiler and React types: exit 0 (empty fixed-typecheck.txt). Configuration retained in fixed-typecheck-config.json.
- `git diff --check`: exit 0.
- Suppression lookup benchmarks: fixed-benchmark.txt. Index construction is outside the timed lookup loop; this is not end-to-end throughput.

Browser observations and limitations are in CRM-FIX-PROGRESS.md. No production
provider send was attempted. Unprefixed evidence files record the original audit.
