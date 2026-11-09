# Database 0.15.3

Keeps bounded AptevaDB reads responsive while report batches run. The default
allows two bulk reads and one reserved bounded read at a time. Explicit
concurrency settings still set the total ceiling. Indexed equality reads qualify
when the engine can prove the work bound. Admission diagnostics report slot wait
and admission class separately from execution time.

Compatible aggregates in an AptevaDB read batch now stream matching documents
once from the batch snapshot. Each aggregate keeps its own accumulator, including
its own distinct counts. Shared scans use bounded memory and fall back to
individual execution on the same snapshot when needed.

The Flexylead Lab benchmark ran four bursts of eight uncached 18-aggregate
reports per scenario, with 96 bounded indexed reads alongside them. Admission
p95 was below 0.01 ms in direct Database, browser-session and agent-key runs.
The report used 12 input scans for 18 aggregates. All report results matched
0.15.2, including headline totals and all 80 commercial rows in each period.

| Report path | Reports/s in 0.15.2 and 0.15.3 candidate | Report p95 in 0.15.2 and 0.15.3 candidate |
| --- | --- | --- |
| Direct Database | 4.25 → 4.18 | 2.39 s → 2.09 s |
| Browser session | 3.10 → 3.98 | 2.96 s → 2.46 s |
| Agent key | 2.44 → 3.67 | 3.54 s → 2.45 s |

The release uses App SDK v0.115.1 and AptevaDB v0.2.0-alpha.18.
