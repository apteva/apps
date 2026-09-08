# Nested protocol-buffer regression

Functions 1.11.1, 2026-09-08. This fixes the independent protocol-reservation ceiling left in 1.11.0; it does not replace soft worker-memory admission.

## Reproduction and fix

A real HTTP entry point invokes three nested Functions workers, with the leaf making a mock Tables call. Each worker has a 128 MiB execution limit so nested worker capacity fits the unchanged defaults. The parent holds an interactive response reservation; three nested downstream calls require 24 MiB. On the unmodified 1.11.0 admission path, the request returns HTTP 500 with `protocol_memory_limit: nested protocol reserve exhausted`. The default global protocol budget is 128 MiB, but nested calls were capped at one eighth (16 MiB).

The nested allowance is now protected capacity rather than a ceiling. Children may borrow unused shared bytes. `reserveProtocol` still atomically enforces the hard global limit across callback reservations and input frames. Root and background reservation ceilings remain seven eighths and five eighths. A nested call that cannot fit the real global budget fails promptly with a global-budget reason, preserving protection against parent/child waits.

The HTTP regression returns 200 after the fix. A parallel fan-out regression holds three actual child callbacks concurrently (24 MiB of nested reservations), then verifies normal completion and caller cancellation with zero remaining protocol/downstream/queue reservations. A hard-limit regression occupies the remaining global budget and checks 100 consecutive failures without increasing the counter or waiting. API assertions verify 16 MiB protected, 24 MiB reserved and 8 MiB borrowed.

## Scope

No production business handlers or recordings were replayed. The fixtures reproduce the reported scheduler error, not the full remuneration or routing application. No database migration, worker allowance increase or global protocol-limit increase is required. Staging and production need the new app version. Existing memory, integration deadline, readiness, cache and build-diagnostics fixes remain included.

The capacity API and themed panel expose `protocol_capacity`, including the hard limit, available bytes, protected nested allowance and borrowed bytes. Existing fields retain their meaning. All figures are buffer reservations, not measured process RSS.

## Release checks

- Full Functions race suite passed in 112.055 seconds, including existing soft-memory, integration-deadline, readiness and build regressions.
- Focused protocol regressions passed three race-enabled repetitions in 6.585 seconds.
- Native Linux arm64: HTTP-chain, fan-out, cancellation, borrowing telemetry, hard-budget and protection tests passed with cgroup, Landlock and seccomp enforcement. A disposable container used 4 GiB outer memory, two CPUs, 256 PIDs and networking disabled.
- Go vet, strict TypeScript, Bun panel build/import verification and Darwin arm64/Linux amd64/arm64 builds passed. SDK remains published v0.76.0.
- The panel was visually verified in Apteva light and dark themes using synthetic data, with no browser errors.
