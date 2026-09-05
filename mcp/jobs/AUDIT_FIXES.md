# Jobs v0.1.14 release notes

**v0.1.14** addresses the audit of release commit `070d47f46a15a7aec1a698e7c43bb393b111ab19` (`jobs/v0.1.13`). The release tag is `jobs/v0.1.14`.

## Findings addressed

| Audit finding | Change and evidence |
|---|---|
| 1. Cron hangs across DST | Real-minute traversal on matching calendar dates; spring/fall termination and repeated-hour tests. |
| 2. Non-UTC due-time comparisons | UTC writes plus migration of old due/lease timestamps; regression tests for Paris/New York and preservation of existing retry keys. |
| 3. Projectless jobs omitted | Installation-wide dispatcher visits partitions stored in Jobs, including the empty partition. Real sidecar test dispatches global and project jobs together. Administrative HTTP/panel scope omits the rejected empty project query. |
| 4. Queued or stale claims execute | Acquire capacity before claiming, validate ownership before delivery, renew leases, cancel actual network requests, and preserve cancellation during finalization. Covers concurrent file-backed claims, stale ownership, queued cancellation, renewal and shutdown. |
| 5. Cross-project target routing | Trusted MCP caller scope/ownership, explicit routing metadata, event-agent project validation and deferred owner/grant checks. Tests cover spoofed owner/project, denied/revoked grants and cross-project event delivery. |
| 6. Credential exposure | Safe JSON projections on every job response; compact list queries avoid loading execution payloads. Payloads, headers, URL paths/query credentials, delivery keys and arbitrary response/error bodies are hidden. Raw targets remain intact for execution. |
| 7. HTTP body errors ignored | Body read failures and post-header deadlines are failures; bounded response reads. Slow-body regression passes. |
| 8. Run-now consumes schedule | Independent child occurrence with separate retry identity and history under the original job. Duplicate queued manual requests return 409; queued children are cancelled with their parent. Completion events distinguish manual status from parent status. |
| 9. Leap-day/impossible cron | Eight-year search horizon, impossible-schedule rejection and no pending jobs with an absent valid next occurrence. Leap-day regression passes. |
| 10. Overflow and fractional values | Strict integer parsing, bounded intervals/retries/IDs, safe cron steps and capped backoff. UI and tool schemas document bounds. |
| 11. Stale UI responses | Request generations, abort-on-unmount, immediate selection invalidation, keyed project/install state, parallel detail fetches, coalesced refresh events and captured action identity. Controlled UI race tests pass. |
| 12. Misleading once timezone | Browser timezone label plus resolved UTC preview; unrelated timezone field hidden for once/every. Verified in a real browser. |
| 13. Missing pagination | Exclusive ID cursors for jobs and runs, exact `has_more`, name/ID search, monotonic IDs after retention, and load-more controls in native and legacy panels. |
| 14. Incorrect HTTP event scope | Mutations emit with their resolved project; HTTP event regression passes. |
| 15. Expensive random preview | Generate each local day's slots once; compare hash bytes without allocating strings. Local 50-run benchmark improves from 629 ms / 94.8 MB to 1.31 ms / 0.89 MB allocated per operation. |
| 16. Blocking per-project workers | SDK workers only wake a shared bounded pool. Round-robin project selection allows a fast project's job to run while another project's requests are blocked. |
| 17. History scans/growth | Separate indexed pending/expired claim paths, cursor indexes, bounded history and terminal-job pruning, parent protection for active children and monotonic job/run sequences. |
| 18. Unbounded inputs/work | 128 KiB REST requests, 64 KiB targets, 10,000 active occurrences per project, numeric limits, response limits, request-aware database deadlines and cancellable target transports. |
| 19. Hidden persistence failures | Propagate scan/row/transaction failures; durably insert a running attempt before delivery. Injected finalization failure leaves recoverable state and surfaces an infrastructure error. Logs and `/status` expose dispatcher failures. |
| 20. Unknown suffix mutates a job | Exact item route matching; unknown suffixes return 404. Regression verifies that deletion of an unknown route does not cancel the job. |

Additional improvements include explicit HTTP destination/redirect policy, accessible modal focus and Escape handling, action locks, legacy-panel error handling, one embedded manifest source, and a reproducible verification script.

## Validation

- **90 Go tests**, including **6 real-sidecar integration tests**, pass; **70.1% statement coverage** in the release verification suite.
- **7 UI regression tests** pass, exercising actual component callbacks and controlled response ordering.
- `go test -race`, `go vet`, Go build, strict TypeScript typechecking and the production panel build pass.
- **govulncheck v1.7.0: no vulnerabilities found**, with Go 1.26.6, SDK v0.74.1 and x/sys v0.44.0. `bun audit` also reports no vulnerabilities in the locked panel development dependencies.
- Real browser smoke checks use the actual panel with local fixture endpoints: 105 jobs load across two pages, details are redacted, modal focus starts inside the dialog, Escape closes it, and once/cron timezone fields behave correctly. No browser warnings/errors were reported. This is a fixture-backed smoke test, not a test against production or its complete dashboard styling.
- Benchmark on the same Apple M1 Pro: `BenchmarkAuditRandomPreview50` is approximately **1,307,669 ns/op**, **889,764 B/op**, **11,351 allocations/op**, versus the audit baseline **629,364,542 ns/op**, **94,769,080 B/op**, **2,147,946 allocations/op**. Timings are local measurements, not production latency guarantees.

Run `./verify.sh` from this directory to install locked development dependencies, typecheck/build the panel, run UI/Go/integration/race tests, run vet, build a temporary binary and scan Go dependencies. `bun audit` checks the panel development dependencies. `GOWORK=off` prevents the workspace SDK overlay from masking the actual pinned release dependency.

## Upgrade notes

- Publishing this release does not itself apply migrations to an existing installation; they run when that installation upgrades.
- Requires Go **1.26.6+**. SDK **v0.74.1** was selected by fetched tag ancestry, not version sorting.
- Migration 004 preserves old occurrence identity strings while canonicalizing due times. Back up the database before applying a production upgrade; the schema additions are not a reversible downgrade migration.
- Run retention defaults to **30 days**. New terminal-job retention defaults to **90 days** and also removes their remaining history; either can be disabled with `0`.
- Target payloads/credentials and arbitrary target responses are no longer readable through list/get/run APIs. Clients that need to create a replacement job must retain their original input.
- Global MCP scheduling requires platform-bound caller headers. Direct fixed-project MCP calls and the authorized administrative HTTP surface remain available. Grant checks fail closed at delivery when current owner authorization cannot be verified.
- Private HTTP destinations remain enabled by default for existing internal webhooks. Set `allow_private_http=false` to restrict them to public addresses. Cross-origin redirects are blocked in either mode.
- The dispatcher now has one concurrency limit per installation; `dispatch_batch_size` is no longer a live-dispatch configuration setting.
- Delivery remains at least once. Interval/cron misfires skip missed occurrences; random schedules replay logical occurrences. Cancellation cannot reverse an already accepted external action.

The original review also suggested optional product features: editing, pause/resume, creation deduplication, configurable misfire policies and retry classification/jitter. Those remain feature work; this patch addresses the twenty concrete defects and documents the existing delivery policies.
