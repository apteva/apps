# Managed runtime recovery

`GET /_apteva/runtime/app/status` requires the process's Apteva app credential and is served even while normal app routes return `runtime_not_admitting`. It exposes deployment ID, generation, admission, fencing reason/time, lease expiry, last successful ownership renewal, last ownership failure/time, and the current error. Responses must not be cached.

`/health` distinguishes process startup from call readiness: passive standby/activation can pass startup checks with `admitting: false`; expired/fenced execution returns HTTP 503. Never interpret standby process readiness as call admission.

## Management outages

A transient callback failure does not revoke an unexpired verified lease. Existing calls may continue while it is valid. Expiry or permanent authorization/ownership failure fences the execution. New calls are rejected; carrier bridges and SIP listeners are stopped with the initiating reason recorded. Fencing cannot be undone in the same process.

After this exact deployment verifies ownership again, status advertises `recovery.action: restart`. Apteva Server must independently verify the current owner, inspect the execution and reread authenticated status before replacing it. The controller confirms the previous execution stopped before activating a new generation. A displaced owner cannot request recovery of its successor. This policy prevents duplicate writers; it cannot recover an already disconnected phone call.

## Recording reconciliation

Standby validation is read-only. Active initialization may remove a missing local-file reference only when the recording is failed, storage failed, duration zero, original/storage size each 0–44 bytes, no Storage copy exists, and no local recorder is active. This matches a failed empty WAV recorder. The recording row, call association and failure reason remain. Missing completed/nonempty recordings, unsafe paths, nonregular files and original-size mismatches remain errors.

## Repeatable local checks

Run with `GOWORK=off`:

- `go test -race -run 'TestManagedRuntime|TestRuntimeHealth' .`
- `go test ./...`

`TestManagedRuntimeLeaseOutageRestoresSIPOnlyInReplacement` exercises a real UDP SIP listener, temporary management failure, expiry, socket release and replacement admission. Server's companion controller tests exercise automatic authenticated detection, stop-before-prepare, replacement, cooldown and rejection of stale ownership. Together they cover the component contract; they are not a real cross-provider outage or live carrier test.
