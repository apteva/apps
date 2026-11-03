# Database 0.6.5

Released 3 November 2026. Source tag: `database/v0.6.5`.

`update` and `delete` accept an exact `key` together with a `where` guard.

## Changes

- **Guarded key mutations.** An exact `key` used to exclude `where`, and `ifVersion` requires a `key`, so a revision-checked edit could not also check that the record belongs to the caller's scope (`invalid_argument: use an exact key or a filter`). A `where` given with a `key` is now ANDed with the primary-key equality filter. The record is looked up, the guard and `_version` are checked, and the change is written in the same write transaction:

  | Situation | Result |
  |---|---|
  | Key, guard and revision match | `affected: 1`; `_version` increments once |
  | Stale revision | `version_conflict: record version changed`; nothing changed |
  | Guard fails, with `ifVersion` | `version_conflict: record is absent`; nothing changed |
  | Record missing, with `ifVersion` | The same neutral `version_conflict: record is absent` |
  | Guard fails, without `ifVersion` | `affected: 0` |
  | Concurrent edits with one revision | Exactly one succeeds; the others get `version_conflict` |

  The same rules apply inside write batches. The key must still name exactly the primary-key fields, and cannot be combined with `all: true`. Primary keys and metadata still cannot be set, and `maxAffected` and the other mutation limits are unchanged. `TestGuardedKeyMutations` covers every row above on SQLite, Pebble and AptevaDB, including eight concurrent edits.

No storage change. AptevaDB stays on v0.2.0-alpha.14, format 10. Requests valid on 0.6.4 behave as before.
