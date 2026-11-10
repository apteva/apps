# Database 0.17.1

Online computed collections reach ready state once the primary and declared
indexes are visible to reads. Reopening a database reconciles any missing
indexes on previously built collections while preserving their rows and
distinct memberships.

The existing `db_aggregate` operation runs `merge_distinct` through
AptevaDB's bulk membership executor. It unions the same exact identities
over the selected buckets and uses saved counts for single buckets.
Read batches continue to use one snapshot for all periods. Direct
aggregate diagnostics can report the merge's work and execution time.

This release pins AptevaDB v0.2.0-alpha.21. Existing definitions and
membership records can be used in place.
