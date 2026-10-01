# Pebble scan optimization — 7 September 2026

The Database app now reuses its record iterator, materializes only query fields while scanning, and accumulates aggregate metrics in typed state. The three million-record operations all improved. SQLite remains faster for the tested unindexed filter and sort; Pebble is faster for the tested four-group aggregate in the new run.

## Results

One million records per adapter. Current measurements are medians of three sequential executions on the same loaded database. Green marks the lower current median, not statistical significance.

| Operation | SQLite now | Pebble now | Previously reported Pebble |
|---|---:|---:|---:|
| Unindexed filter matching the last record | 🟢 **0.261 s** | 0.758 s | 13.606 s |
| Unindexed descending sort, top 10 | 🟢 **0.556 s** | 1.130 s | 3.808 s |
| Four-group count, sum, avg, min, max | 0.757 s | 🟢 **0.610 s** | 3.823 s |

Relative to the previously reported single-run Pebble values, the observed improvements are approximately **18×**, **3.4×**, and **6.3×**. Those ratios compare separate local runs, not a controlled alternating before/after experiment. The first optimized execution was also faster: 0.758 s / 1.097 s / 0.619 s respectively. No cache flush or cold-cache claim is made.

| Operation | SQLite min–max (3 executions) | Pebble min–max (3 executions) |
|---|---:|---:|
| Last-record filter | 0.222–1.382 s | 0.664–0.767 s |
| Top-10 sort | 0.536–0.571 s | 1.097–1.204 s |
| Four-group aggregate | 0.749–0.811 s | 0.603–0.619 s |
| Count all | 0.024–3.080 s | 0.241–0.314 s |

All **24 read checks passed** (four workloads × three executions × two adapters) within the default 30-second deadline. Exact counts, late-match values, all ten sorted values, and each grouped metric were asserted. The count-all median was 0.052 s on SQLite and 0.258 s on Pebble; its large SQLite first-execution variation illustrates why the individual samples are retained.

The dataset and operations match the [initial large-scan verification](LARGE-SCANS.md): an automatic primary index, no secondary indexes, numeric values, four text buckets, and 128-byte padding. Each adapter loaded one million synthetic records into a fresh temporary database. SQLite ran before Pebble. Three complete query sequences followed each load. Timers include `engine.Manager.Execute` and output JSON encoding, excluding transport, seeding, and correctness assertions. No performance test or race test ran concurrently with the timed run; other user applications and OS activity were uncontrolled.

[Final raw samples](results/optimized-scans-final-1000000-2026-09-07.json) · [Timed source hashes and machine metadata](results/optimized-scans-final-1000000-2026-09-07-source.json)

## What changed

1. **Reuse record iteration.** Previously, an index scan performed a separate `Get` for every candidate, repeatedly setting up table/index reads. It now keeps a record iterator open, advances adjacent records with `Next`, and seeks when the next primary key is not adjacent. It preserves the selected index's ordering and checks exact keys, including compound, numeric, and escaped text keys.
2. **Materialize fewer values.** A parser and scratch record map are reused per scan. The decoder materializes only fields needed by predicates, ordering, projection, or metrics. Full records are decoded after filtering when required for output. Retained values are copied out of parser buffers; retained records have their own maps.
3. **Accumulate without per-row formatting.** Grouped aggregation resolves field types once, keeps counts and sums in typed accumulators, and formats portable decimal strings once per output group. Single-field groups use comparable scalar keys instead of JSON-encoding a one-item tuple on every input row.

Storage format, index definitions, write durability, timeouts, and retained-memory budgets are unchanged. Existing data requires no migration. The fast scan decoder uses [fastjson v1.6.10](https://pkg.go.dev/github.com/valyala/fastjson@v1.6.10); API JSON parsing stays with `encoding/json`. A standard-decoder fallback preserves valid deeply nested JSON beyond the fast parser's nesting limit, and nested JSON numbers retain their exact representation.

## Profiling and intermediate evidence

A separate profiled baseline reproduced the original slow behavior: 15.071 s for the filter, 4.088 s for the sort, and 4.039 s for aggregation. The filter profile attributed most sampled work to repeated storage reads beneath `Snapshot.Get`. Profiling can affect timings, so this run is kept separate from the timing comparison.

[Baseline profile run](results/scan-profile-baseline-2026-09-07.json) · [CPU profile summaries](results/scan-profile-baseline-2026-09-07-summary.txt)

The first implementation, before typed aggregate accumulators, measured Pebble medians of 0.977 s / 1.161 s / 2.500 s. The final aggregation change reduced that workload further. These are separate runs with uncontrolled host/cache variation, not an isolated attribution of every speedup.

[Intermediate raw samples](results/optimized-scans-1000000-2026-09-07.json)

## Verification and reproduction

The full test suite passes with CGO disabled, the full race suite passes, and `go vet -tags largescan ./...` passes. Regression coverage includes typed values and nulls, exact JSON integers, parser-buffer ownership, deep JSON fallback, numeric/compound/escaped primary keys, indexed and unindexed cursor pagination, missing record references, aggregates with empty inputs and multiple grouping types, existing scan/memory bounds, cancellation, and write atomicity. After the timed revision, an additional error-path guard was added to propagate a failed record-iterator `Next` before attempting another seek; final test/race/vet verification includes that guard.

From the Database app directory, use a fresh output path:

```sh
GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 \
DB_LARGE_SCAN_ROWS=1000000 DB_LARGE_SCAN_REPEATS=3 \
DB_LARGE_SCAN_OUTPUT="$PWD/benchmarks/results/optimized-scans-new.json" \
go test -p 2 -tags largescan -run '^TestLargeScanBenchmark$' -count=1 -v ./engine
```

Set `DB_LARGE_SCAN_PROFILE_DIR` only for a separate profiling run. Temporary databases are removed automatically, and existing output files are not overwritten.
