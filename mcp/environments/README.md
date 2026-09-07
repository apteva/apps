# Environments

This directory contains the Environments sidecar and its dashboard panel.

## Updates and lifecycle

`environment_update`, HTTP PATCH, and HTTP PUT preserve omitted definition fields.
Fields within `spec` are merged; a supplied array replaces that array, so `[]`
explicitly clears it. Updating an unknown definition returns an error.

Runs whose runtime cleanup fails stay `stopping`. Reconciliation retries cleanup
before permitting a replacement runtime for the same definition. Reconciliation
processes all active runs independently of the 200-entry history list.

## Assertions and voice evidence

`mcp_tool_call` requires `mcp` and matches names qualified by that server. Omitting
`tool` means any tool in that namespace; unqualified names do not count.

The runtime telemetry API returns at most 1,000 events without historical paging.
Assertions return an explicit error when that window is saturated. Voice calls
collect incremental, overlapping windows and deduplicate by event ID, preserving
previously observed events throughout the call. A saturated window marks evidence
incomplete and causes the call to fail rather than publish a misleading result.
The recorded failure explains the missing evidence.

Carrier callback events without a `.failed` suffix represent successful HTTP
acceptance. Rejected or undelivered callbacks include status/error details in a
`.failed` event and fail the simulated call.

## Performance and retention

The catalog is cached for one minute per install; concurrent refreshes are
coalesced, and independent upstream reads run concurrently. The panel polls live
state every five seconds and catalogs every minute. Fixture decoration is batched.

Set `ENVIRONMENTS_RETENTION_DAYS` on the sidecar to a positive day count to enable
history cleanup. Unset or `0` disables deletion (the default). Each reconciliation
removes at most 100 terminal runs older than the cutoff, including their voice
recordings, call results, fixture state, and events. Active runs, runs with an
unfinished voice call, environment definitions, and reusable snapshots are kept.
If file removal fails, database records are retained so cleanup can retry.

## Validation

From this directory:

```sh
GOWORK=off go test -race -cover ./...
GOWORK=off go vet ./...
cd ui
bun install --frozen-lockfile
bun test
```

From the apps repository root, rebuild the shipped panel:

```sh
bun run scripts/build-panels.ts --app environments
```
