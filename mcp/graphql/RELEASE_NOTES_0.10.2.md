# GraphQL v0.10.2

Raises the `aggregate_pipeline` declared-source ceiling from 32 to 64. A shared
constant governs validation and its error message for versions 1, 2 and 3,
including version 3's per-statement Tables batch compilation. This permits a
33-source resolver configuration to be published without changing its SQL.

Every declared source still must exist, be an active Tables source, have a fixed
valid mapping and be unique. SQL placeholders must reference declared tables,
the bound source must be included, and unsupported inherited scopes remain
rejected. Request permissions and native Tables SQL authorization remain active.

Declarations are an access allowlist, not scheduled work: allowing 64 does not
fetch all declared sources or add batch operations/parallel queries. Stage, SQL,
row, byte, execution, snapshot and concurrency limits, projection metadata and
request coalescing are unchanged. This does not address SQL query timeouts or
include application-specific calculations.

Validation: full Go race suite, vet and source build; 32/33/64 acceptance and
empty/65/invalid-source rejection across versions 1/2/3; unchanged execution input
and budget checks; 64-source request permission isolation; real Tables 0.2.16
integration with 33 configured tables, dependent stages, one read-snapshot batch,
concurrent writes and rejected native unauthorized SQL access.

SDK remains pinned to the latest main-line tag v0.99.0. Tables >=0.2.16 is unchanged.
No running installation or production deployment is updated by this release.
