# GraphQL 0.24.0

Database pipeline aggregate steps now accept `relation` and `match`, using
Database 0.15.0's bounded related-parent aggregation. Supply the same scope,
context and match rules as the find step so a Quality-style page, exact total
and average score agree. The outgoing operation remains `aggregate`.

- Context limits and source definitions stay server configuration. Identity and
  permissions bind before execution; client search belongs in match. Existing
  when/access_when, requested-scope and mixed-OR restrictions remain in effect.
- A page and summary use the existing same-snapshot read batch. Aggregate metrics
  retain existing number/null handling and count each matching parent once.
- Realtime dependencies include all parent, related-data and permission sources.
  Permission rechecks before delivery remain active, including under changing grants.
- Publication probes the real Database operation. An unsupported Database gives
  a clear 0.15.0 requirement and leaves the serving API release intact.
- Existing plain, pipeline and legacy relational configurations remain compatible.
  There is no app-database migration; app-sdk remains v0.115.0.

The independent fixture and real sidecars pass across SQLite, Pebble and
AptevaDB: match yields 2 parents, 60 points and a 50% average, while capped and
blocked entries cannot match. Coverage includes literal search, null/empty
scopes, permission denial, permission changes and page/summary agreement.
Full module/race tests, vet and builds pass locally; release CI runs these and
all three adapter sidecar suites on Linux and macOS.
