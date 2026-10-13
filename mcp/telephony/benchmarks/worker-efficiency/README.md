# Telephony worker efficiency — 0.12.20

## Scope

Local audit and regression fixes based on released 0.12.19. No instance was installed, restarted or configured. SDK v0.102.0 is the newest available descendant tag; builds and tests use `GOWORK=off`.

The comparison uses real application migrations and the SDK's Go SQLite driver, with 12,941 completed calls, 5,906 stored recordings, zero live calls and no carrier requests. The control is extracted from the released tag. Wall and process CPU times are measured; these are not production CPU percentages or physical disk-write counts.

## Changes

- Historical retention runs independently once a minute in batches of at most 100. Credential updates exclude already-cleared rows and use a partial index. Recording-protected calls get a later recheck so they cannot starve deletable calls. Child foreign-key indexes prevent archive scans during call deletion.
- Auto-answer, activation, legacy announcements, recording import, interrupted imports, recording expiration, provider reconciliation and external ringing select indexed work candidates. Activation starts from ready handoffs and looks up calls by primary key. Recording cleanup uses a correlated call lookup instead of materializing historical calls.
- Commit-triggered routing includes external legs and auto-answer. Active work retains one-second reconciliation and offer/decision deadlines. Idle reconciliation is slower. Retained expired/invalid deadlines retry after one second; fresh commits wake immediately. Unchanged ring progression does not notify browsers.
- Healthy SSE with explicit heartbeats uses 30-second reconciliation (the existing 20-second authenticated stream lease still forces fresh hints). Missing/failed streaming restores fast polling; stale heartbeats restore it within 15 seconds. Standalone softphones reconcile at 30 seconds only after a status push while live; older-server and reconnect fallback retain the configured fast interval. Requests never overlap.
- Access checks read current policy revision and a separate change epoch every time. Only immutable JSON parsing is cached; principals and mutable grants are rebuilt per request. Raw policy edits, deletion/recreation, revocation and project isolation remain authoritative.
- AI policy ticks read compact call state; cold runtimes alone load persisted JSON. Scalar/event comparisons replace two serializations per tick. Failed persistence remains dirty for retry.
- WebRTC receive scheduling arms a timer for the next queued packet and stays asleep when empty. Inbound and outbound SIP session timers wake at their negotiated deadlines or on refresh changes. Local SIP prompt producers wait on bounded pacer progress notifications. Pending audio includes the current write, preventing early prompt completion.

Media formats, FEC defaults, 20 ms send pacing, queue/age limits, permission leases, recording verification and live call deadlines are retained.

## Fresh local gains

| Idle work | Released wall time/run | Optimized wall time/run |
|---|---:|---:|
| Lifecycle | 44.12 ms | approximately 0.37 ms |
| Auto-answer candidates | 4.86 ms | approximately 0.02 ms |
| Activation candidates | 3.69 ms | approximately 0.06 ms |
| Recording jobs | 7.43 ms | approximately 0.22 ms |

The former credential sweep matched 12,941 already-cleared calls each run (155,292/minute). The idle regression now proves **zero database changes across every worker**. The fixed worker schedule falls from 578 to 343 invocations per project per minute while idle; active deadlines and commit wakes are additional, intentional work.

A policy fixture with 100 additional users reduced principal-check CPU from 0.316 to 0.009 ms and allocated bytes from about 131 KB to 1.7 KB per check. Fresh revision/epoch and media-session checks remain in place.

See `local-idle-comparison.json` for raw measurements. Reproduce the candidate and credential control probe:

```sh
cd mcp/telephony
env GOWORK=off TELEPHONY_EFFICIENCY_BENCHMARK=1 \
 TELEPHONY_EFFICIENCY_RESULTS=/tmp/telephony-efficiency.json \
 go test -run '^TestEfficiencyPerformance$' -count=1 -v .
```

## Gates and limits

Regression coverage includes bounded cleanup, recording preservation/rechecks, cancellation, project isolation, candidate query plans without ANALYZE, indexed foreign-key deletion, immediate routing wakes/backoff, policy revocation/cache isolation, retrying AI persistence, SIP refresh extension/expiry, and waiting for completed pacer work. Browser tests cover push/fallback/nonoverlap and unchanged call/reconnect flows.

The mixed load harness now runs with the archived database and background workers, plus bidirectional UDP/SRTP, portable/native Opus/FEC, recording, call lists and notifications. Under simultaneous heavy checks on this Mac with GOMAXPROCS=4, both 32-call variants passed; 64 calls exceeded timing limits. The portable variant dropped audio; the native variant generated replacement audio. This is retained as overload/capacity evidence. A local or CI pass does not guarantee an arbitrary production host, CPU spike or network interruption will preserve every frame. The Linux four-core mixed-load gate must pass before publishing.

### Isolated Mac recheck

After heavy validation stopped, native Opus/FEC passed **32 and 64 concurrent calls**, with zero discarded, replacement or recording-dropped audio. Portable Opus passed 32; at 64 it discarded zero audio but produced 260 ms of replacement audio across the streams, failing the unchanged zero-replacement gate. The distinction is retained in `local-capacity-check.json`; it is not hidden by increasing latency limits or weakening the test. Native 64-call process CPU was about 145% of one core during the 16.4-second paced workload (GOMAXPROCS=4). Linux CI independently checks both codecs.
