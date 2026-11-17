# Instances 0.17.2

Monitoring ingestion no longer scans retained incident history. Before, every
flush that carried an incident update (open incidents are re-exported on every
poll) walked every stored incident detail across all hosts to enforce the
detail budgets, once per host per flush. On a fleet of 50 hosts with 200
retained incidents each, one flush took about 6.5 s; it now takes about 12 ms.

Retained detail bytes are kept in `hosts.detail_bytes` (per host) and the
`detail_bytes` metadata row (per install) by SQLite triggers on `incidents`.
Every path that writes, replaces, evicts or deletes detail — including the
storage-full retry, retention deletes, the incident cap, host removal and
rolled-back transactions — keeps the counters exact. Commits check the two
counters; only when a budget is exceeded are the oldest details evicted, in
small indexed batches, until it fits. Summaries are kept.

On upgrade the column, triggers and `incidents_host_detail` index are added and
the counters are computed once from blob lengths (row headers only). Counters
are reconciled again on every start. The 16 MiB per-host and quarter-of-budget
install limits, the hard database size limit, retention tiers and incident
summaries are unchanged. Eviction is now strictly oldest first.
