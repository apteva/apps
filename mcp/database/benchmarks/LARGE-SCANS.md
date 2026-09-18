# Large-query verification — 7 September 2026

The fixed 10,000-candidate Pebble scan limit is removed. Queries can search and aggregate larger collections through the same app contract, with execution deadlines and retained-memory budgets.

The timings below are the first verification after removing the limit. A subsequent optimization improves record iteration, decoding, and aggregation; see [updated scan measurements](OPTIMIZED-SCANS.md). These original measurements are retained for reference.

## One million records, both adapters

All eight read checks passed with the default 30-second query deadline. These are single functional scale measurements, not medians or a statistical performance comparison. Times include `engine.Manager.Execute` and JSON output encoding, excluding transport and correctness assertions. They use a different dataset from the historical scale matrix and should not be pooled with it.

| Operation | SQLite | Pebble |
|---|---:|---:|
| Count all 1,000,000 records | 0.241 s | 0.240 s |
| Unindexed filter matching the last record | 0.730 s | 13.606 s |
| Unindexed descending sort, top 10 records | 0.542 s | 3.808 s |
| Group all records into four buckets; count, sum, avg, min, max | 0.742 s | 3.823 s |

The test asserts exact returned counts, the late-match value, all ten sorted values, and every aggregate metric in each group. Both temporary databases loaded successfully in 1,000-record atomic batches. Loading took 39.935 seconds for SQLite and 64.446 seconds for Pebble, including data generation. Those load times use existing adapter durability settings and are not an equivalent-durability write comparison.

Dataset: one database and collection per adapter; automatic primary index only; fixed-width IDs; numeric `n`; four text buckets; 128-byte padding per record. Adapters ran sequentially, SQLite then Pebble. Queries followed loading without flushing caches. Platform: macOS arm64, Go 1.25.1, CGO disabled. No existing app data was opened.

[Raw results](results/large-scans-1000000-2026-09-07.json) · [Source hashes and machine metadata](results/large-scans-1000000-2026-09-07-source.json)

## Execution changes

- Pebble streams candidates without counting cumulative input bytes against retained memory.
- Non-indexed find sorting keeps only the best requested page plus one pagination lookahead record in a heap. It does not collect all matches before sorting.
- Broad unordered scans read record values directly. Unfiltered `count(*)` scans keys without decoding every record.
- Aggregate memory grows with retained groups and metric values, with a 16 MiB accounting budget. More than 10,000 small groups can succeed; too much group state fails explicitly. The budget estimates retained values and overhead, not total process RSS.
- Both adapters default `find`, `count`, and `aggregate` to 30 seconds. Top-level `timeoutMs` accepts up to 60,000 ms; omitted/0 uses the default, and earlier caller deadlines win. It is not part of the cursor query signature.
- Other endpoints retain their two-second deadline. The 1,000-record page size and 4 MiB response limit remain. Synchronous index creation is still limited to 10,000 existing records, independently of query scanning; create indexes before large imports.

An unindexed query may still visit the entire collection. A result limit bounds returned records, not execution time. Deadline and retained-memory failures return errors, not partial counts or aggregates.

## Regression coverage

The regular tests cover an 11,000-record collection with more than 20 MiB of input, an aggregate over all records, a match beyond the old ceiling, 11,000 small aggregate groups, projected top-page sorting, and cursor continuation. A separate test scans more than 20 MiB while retaining a tiny sorted page, then checks that oversized aggregation group state is rejected. Timeout validation, earlier caller deadlines, and cancellation during both ordered and unordered Pebble iteration are also covered.

The complete test suite and race suite passed; the additional mid-iteration cancellation test also passed under the race detector. The CGO-disabled full suite and `go vet -tags largescan ./...` passed.

## Reproduce

From the Database app directory, choose an unused output filename:

```sh
GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 \
DB_LARGE_SCAN_ROWS=1000000 \
DB_LARGE_SCAN_OUTPUT="$PWD/benchmarks/results/large-scans-new.json" \
go test -p 2 -tags largescan -run '^TestLargeScanBenchmark$' -count=1 -v ./engine
```

The harness creates and removes temporary databases. Output creation is exclusive so it cannot overwrite an earlier result. This is a focused correctness/scale check, not the older repeated matrix harness, which still imposes its historical two-second caller deadline.
