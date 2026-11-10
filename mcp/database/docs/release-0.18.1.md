# Database 0.18.1

AptevaDB read batches can reuse one snapshot and execution when they mix
direct get, find and aggregate operations. Filters, ordering, limits,
cursor inputs and batch order remain part of the reuse key. Authorization
continues to run for each caller.

The result cache retains compact binary entries with the existing 512 KiB
entry limit, 32 MiB process budget and 30-second TTL. Batch diagnostics
identify skipped reuse and results that exceed the entry limit.

The original 21-operation Flexylead benchmark plan ran once for eight
cold requests and zero times for eight warm requests. The benchmark used
both browser login and agent keys on an isolated copy of the apteva-test
lab. A committed test call invalidated the cached result, and all 32
centres matched the raw report before and after the write.
