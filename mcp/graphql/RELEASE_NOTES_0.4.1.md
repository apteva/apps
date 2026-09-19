# GraphQL 0.4.1

Restore fast Tables scalar-row completion without changing the standard
GraphQL schema, input, validation or query contract introduced in 0.4.0.

- Compiled success-only projection for concrete Tables reads, including
  aliases, fragments, directives, nested relationships, pagination envelopes,
  counts and aggregates. Built-in scalar serializers match the normal engine.
- Standard execution remains available for every other source/output shape.
  Error completion reuses completed reads instead of issuing them again.
- Existing request-local deduplication, small-read batching, large-read bounded
  parallelism, compiled caches, permissions and ordered mutations remain.
- Regression tests cover count/sum/avg/min/max, filtered/grouped and per-parent
  aggregates against real Tables; Database adapter mapping; mixed HTTP/Function
  sources; cursor pagination; schema and realtime isolation; and trusted Auth.

Validation: race-enabled suite with isolated Tables 0.1.24, Auth 0.12.0 and
Functions 1.14.1 processes; Go vet and independent build. SDK v0.82.0 remains
pinned. No installed project, endpoint, Function or data was changed.

Performance: 30 rotating real-process trials after warmup, 30,000 stored rows,
three parallel table reads returning 3,000 rows total with five scalar fields.
Median/p95: 0.4.0 29.71/33.94 ms; 0.4.1 14.44/16.78 ms; direct parallel Tables
5.67/8.63 ms. Results matched exactly. This isolated proxy-gateway measurement
is not production/installed-server latency or a claim of native-call parity.
Projection-only microbenchmarks improved from 15.4–19.6 ms to 1.4–1.5 ms with
about 94% fewer allocated bytes; these exclude source/network I/O.

Boundaries from 0.4.0 remain: relationships do not provide row authorization;
protected subscriptions, full graphql-transport-ws compliance, @oneOf,
interface inheritance and incremental delivery remain unsupported. The
Flexylead Function replacement/access design is a separate deployment step.
