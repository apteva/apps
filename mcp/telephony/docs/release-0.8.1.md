# Telephony 0.8.1

Cumulative marketplace release containing Telephony 0.8.0 and all preceding
Telephony fixes: sequential routing, inbound protection, answerable offers,
indexed adviser queries, AI handoff ordering and bounded failures, audio
buffering/diagnostics/benchmarks, and configurable connected-call duration.

Includes the DIDWW number ordering and compliance UI added in 0.8.0. This patch
also requests and returns permanent and one-time agreement templates in DIDWW
requirements so mandatory agreements are visible alongside proof requirements.

## Verification

Full standalone Go tests, Go vet/build, frontend tests and typecheck, generated
frontend manifest, and agreement-template regression coverage.

## Scope and limitations

Source and marketplace publication only. No installation upgrade or live calls.
DIDWW features depend on the host integration catalog exposing the matching
DIDWW tools. The updated integration catalog is a separate deliverable; this
release does not update a server's catalog. Permanent-agreement submission is
not yet implemented in the Telephony panel. Existing carrier call paths retain
all preceding fixes.
