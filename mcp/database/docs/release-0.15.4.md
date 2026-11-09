# Database 0.15.4

AptevaDB chooses whether compatible read-batch aggregates should share a scan.
It estimates candidate rows from B-tree metadata and compares the cost of
projecting documents, maintaining exact distinct sets, and handing off chunks
against running independent scans in parallel. It makes the decision once per
batch. Both paths use the batch snapshot and keep independent accumulators.

Small batches of primary-key gets qualify for reserved admission with cold
schema metadata and execute without a worker pool. Read batches accept
`diagnostics: true` to report queue, snapshot and execution timings. Native
diagnostics include admission class and wait, plus the chosen scan path and
reason for compatible aggregate groups.

In the Flexylead Lab, the 18-aggregate month report matched the 0.15.3
results exactly. The direct single-batch median fell from 308 ms to 187 ms.
At eight concurrent batches, direct p95 was 2.27 s and throughput was
3.99 reports/s, against the recorded 1.60 s p95 baseline. Forced sharing
had better eight-batch throughput in a same-host comparison. Permissions
admission p95 was 0.10 ms with browser sessions and 0.74 ms with agent keys.
The direct permissions probe reached 100.44 ms p95.

Database uses App SDK v0.115.1 and pins the AptevaDB candidate estimator at
commit `5eb6119b903d` after v0.2.0-alpha.18.
