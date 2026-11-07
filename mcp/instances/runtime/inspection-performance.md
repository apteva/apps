# Native inspection staging comparison — 2026-10-21

One-core DigitalOcean `test-render`, using a uniquely named native listener
fixture, isolated temporary data/release directories and the existing pinned SSH
identity. Production was untouched. The fixture unit and files were removed
after each test. The real PAM configuration was not modified: its opt-in
transformation was tested only against a temporary copy.

Each active phase inspected the same kind of running executable with a real TCP
listener and a restore receipt, 20 times on a two-second schedule. The baseline
used the released 0.8.0 `RestoreStatus` followed by `Inspect`. The new path folds
first-use recovery into inspection and then reuses one SSH session. Both verify
executable identity and listener ownership. CPU was sampled from `/proc/stat`
nominally every 250 ms over about 40 seconds, after setup activity settled.

| Measurement | 0.8.0 | New inspection | New repeat | Idle control |
|---|---:|---:|---:|---:|
| Inspection requests | 20 | 20 | 20 | 0 |
| Inspection SSH connections/sessions opened | 40 | 1 | 1 | 0 |
| Median inspection duration | 1,450 ms | 95 ms | 100 ms | — |
| 95th percentile duration (including cold first poll) | 2,407 ms | 850 ms | 868 ms | — |
| Mean total host CPU | 24.7% | 12.7% | 7.3% | 6.7% |
| 95th percentile host CPU | 61.5% | 58.3% | 31.0% | 37.5% |
| Maximum 250 ms host CPU sample | 92.3% | 92.3% | 44.4% | 92.0% |

This is a sequential whole-host comparison, not exclusive CPU accounting for
Instances: other services remained running and background load varied. The
results show 97.5% fewer new SSH sessions and about 93% lower median inspection
latency, with lower mean host CPU in both new runs. The idle control still
had a 92% sample, so these measurements do **not** establish that every host CPU
burst has been eliminated or identify the source of remaining bursts.

Additional checks passed on staging: stopped-app detection, controller/inspection
transport restart and receipt revalidation, recovery after the old-data rename, successful retry of an unmarked partial upload,
listener absence, and the PAM transformation against a temporary copy. Local
regressions cover completed-receipt persistence, first-use/identity revalidation,
unknown outcomes, session reuse, cancellation isolation, bounded responses,
archive validation, SQLite WAL preservation and interrupted restore states.

## Repeating the staging test

The opt-in integration test accepts only the pinned remote inventory entry named
`test-render`, requires one CPU, creates a unique service, and cleans up its owned
resources. It builds the tiny listener fixture from `testdata/inspection-fixture`
for the detected Linux architecture unless a fixture path is supplied.

```sh
GOWORK=off \
INSTANCES_INSPECTION_STAGING_DB=/path/to/instances/app.db \
INSTANCES_INSPECTION_STAGING_ID=1 \
INSTANCES_INSPECTION_REPORT=/tmp/instances-inspection.json \
go test -run '^TestNativeInspectionStaging$' -count=1 -v .
```

Use `INSTANCES_INSPECTION_IDLE=1` for the same sampler/setup with no inspection
requests. The JSON report contains CPU samples, cumulative counters and sorted
inspection durations; it contains no credentials. The sampler's own one SSH
connection, setup, cleanup and failure/recovery checks are excluded from the
inspection-session count. An idle control has zero inspection requests even
though it retains the same running fixture and sampler.
