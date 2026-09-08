# Legacy text defaults — Tables 0.1.21

A 0.1.14 → 0.1.20 upgrade could compile successfully but fail mounting when
`columns_meta.default_value` contained a raw text value such as `standard` or
`EUR`. The old loader silently ignored the JSON parsing error. The newer schema
loader returned the error, and startup loads every pending table's schema.

All three schema-loading paths now share a decoder: valid JSON retains its
original types and integer precision; a parse failure on a **text** column is
interpreted as the original literal text. Other column types retain parsing
errors with column/type context. Empty metadata and SQL NULL continue to mean
no default; JSON `""` remains an explicit empty string. Valid JSON literals are
not reinterpreted based on column type. New create/alter validation is unchanged.

No metadata is rewritten and no SQL migration is added. Existing user rows,
physical defaults, indexes, committed migration markers and row identities are
preserved. The existing per-table transactions resume the unfinished tables and
skip completed tables. Normal inserts can use the recovered text defaults.

Tests cover the observed raw values, typed JSON defaults, malformed non-text
values, all loaders, insert behavior, already-committed tables, restart,
identity/index retention and nullable defaults. An opt-in snapshot test copies a
consistent SQLite backup to a temporary database before mounting twice, then
checks hashes of every original user-table column, counts, table root pages,
column metadata and SQLite integrity. It never migrates the supplied file.

```sh
GOWORK=off go test -race ./...
GOWORK=off TABLES_UPGRADE_SNAPSHOT=/path/to/consistent-backup.db \
  go test -v -run TestUpgradeDatabaseSnapshotPreservesAllRows .
```

This patch is based on the complete 0.1.20 source, preserving its read diagnostics,
correlation, queue/deadline behavior and existing upgrade compatibility. Local
uncommitted work in the original apps checkout is left untouched.
