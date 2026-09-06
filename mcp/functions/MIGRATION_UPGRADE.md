# Functions 1.8.1 migration regression results

The reported production state was reproduced with synthetic data, not a copy of the Scaleway database. The fixture used the original 001–004 schema and indexes and applied exactly the first 11 statements of 1.8.0 migration 005: existing identity/configuration columns, no invocation-ID index, no four timing columns, and no 005 receipt.

| Check | Result |
|---|---|
| Database size before recovery | 9,423,540,224 bytes (9.424 GB) |
| Existing invocation rows | 2,238,000 |
| Original 1.8.0 binary retry | Reproduced duplicate-column startup failure |
| Kill patched process in index-creation phase | Repair rolled back; original partial state and identity preserved |
| Real SQLite write lock held during startup | 65 seconds; health stayed 503/initializing |
| Startup after releasing the lock | 82.14 seconds |
| Total startup including lock delay | 147.19 seconds |
| Restart after successful repair | 43.88 seconds |
| SQLite full integrity check | ok |
| 005 receipts after recovery | Exactly one |
| Existing row count and function identity | Preserved |
| Create and invoke a new Node function after repair | Passed |

The index phase took approximately 42 seconds according to application progress timestamps. Startup also scans existing invocations to recover interrupted work. These timings are from a local macOS Apple M1 Pro with constrained free disk space, not Scaleway hardware. Matching file size and failure state does not imply identical production row sizes, row counts or I/O performance. The declared startup budget is 1,800 seconds and requires Apteva 0.50.2+.

Additional checks:

- All 17 possible committed prefixes (zero through 16 statements) recover, with stable function and artifact identities and exactly one receipt on retry.
- Cancellation during column changes, backfill and after index creation rolls back schema and receipt; retry succeeds.
- Conflicting existing column/index definitions fail without recording completion.
- Full Go suite: passed (27.645 s). Full race suite: passed (36.023 s). Final migration and manifest regressions also passed under the race detector (3.852 s).
- go vet and diff checks passed. Native, Linux amd64 and Linux arm64 binaries embed SDK v0.76.0, confirmed through Go build metadata.
- Linux arm64: a separate 84 MB real-process fixture passed index-phase kill/retry, schema/receipt/identity/integrity checks, restart, and Go function compilation/invocation. The disposable container had no network, 768 MiB memory, 128 processes and two CPUs. Inner Landlock/seccomp remained enabled; the outer seccomp filter was removed to permit sandbox setup and inner cgroups were explicitly disabled because the container did not delegate its cgroup filesystem. Production settings were not changed.

## Reproduce

Build both versions with `GOWORK=off`, then run from the 1.8.1 Functions directory:

```sh
python3 scripts/test-migration-upgrade.py \
  --binary /tmp/functions-1.8.1 \
  --baseline-binary /tmp/functions-1.8.0 \
  --target-bytes 9420000000 --hold-seconds 65
```

Allow space for the 9.42 GB database plus indexes, journal and temporary files. The script creates and removes its own temporary fixture and never opens an existing app database. Use `--runtime go` for an environment with Go but no Node.
