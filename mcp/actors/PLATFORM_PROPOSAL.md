# Actors platform roadmap

Updated 2026-09-19. This supersedes the ownership recommendation in the earlier Web platform proposal. The product is a separate `actors` app with a direct Computer dependency. Web remains independent.

## Product boundary

Actors turns website workflows into reusable, versioned operations callable by agents and API clients. Site-specific URLs, selectors, contracts and workflows live in actor definitions or future actor code. No Patreon or other provider behavior belongs in the engine. Examples are optional files, never automatically installed actors.

| App | Ownership |
|---|---|
| Actors | Definitions, versions, operations, tasks, runs, queues, datasets, publications and catalog |
| Computer | Browser primitives, saved login contexts, browser backends, live views and proxy configuration |
| Web | Independent one-off search, page extraction, crawl, map, research and snapshots |
| Jobs | Schedule calculation and trigger delivery |
| Storage | Private artifact bytes and exports |
| API | External endpoint routing and authentication |
| Functions / Containers | Potential isolated code infrastructure, subject to capability validation |

Actors must not require Web. Browser workflows and extraction call Computer directly. A caller may orchestrate Web and Actors, but that does not make Web an Actors runtime dependency. New reusable workflow capabilities belong in Actors. Web's old extractor surface remains a legacy compatibility concern, not the location for a second evolving actor engine.

## Current implementation: v0.1.0

The app starts from the existing Web extractor engine, extracted without Web's search, research, crawl, cache or snapshot tool handlers. It adds named operations, immutable revision history, pinned tasks, saved context selection, fill/key/scroll/element assertions, context exclusion, cancellation-aware Computer calls, private exports, page-level dataset commits and cursor reads. Schedules are Actors-owned and pinned to revisions; access is project-scoped. The app includes a panel, MCP tools, HTTP routes and a generic page-reader example.

This is a runnable initial product, not completion of the target platform. Current definition schema 1 uses a flat output field/type map, bounded sequential steps and one worker process. It retains an in-memory dataset bounded to 32 MiB for exports even though pages are persisted independently. There is no automatic checkpoint replay, distributed queue, full JSON Schema, untrusted code execution, public API publishing or webhook service yet. README documents operational limits and test commands.

## Target object model

An actor is a reusable package. A version pins its implementation and contracts. An operation is a named entry point. A task pins an operation, inputs and account bindings. Each invocation produces a run, its dataset and diagnostic artifacts. A publication exposes an operation through HTTP, MCP or a catalog entry.

Keep mutable drafts separate from released versions in the later release model. Retried runs use original snapshots; tasks default to pinned versions. Optional release channels record the resolved version on every run. Importing an actor never imports credentials or private datasets.

## Execution capabilities

Extend declarative workflows with bounded conditions, loops, subflows, structured waits, list/detail traversal, cursor/load-more/infinite-scroll pagination, typed variables and checkpoints. Validate inputs and output items with a documented bounded JSON Schema subset. Keep templates separate from arbitrary executable code.

Add code actors for complex behavior. Begin with Bun/TypeScript and an SDK for inputs, queue access, datasets, state, logs and Computer sessions. Separate builds from execution; pin dependency locks, source/image digests, SDK and runtime versions. Additional runtimes follow demonstrated need.

Code actors must run outside the Actors process with enforced CPU, memory, time, filesystem, network and capability restrictions. Existing Functions workers are not automatically an adequate sandbox for arbitrary catalog code. Verify isolation before enabling untrusted packages. Never supply the parent app token to actor code.

Hybrid operations can combine official integration calls, direct HTTP and browser steps. Browser response observation and page evaluation require supported Computer capabilities, constrained collection, bounded payloads and redaction. Remote browser providers will have different capability matrices.

## Authentication and account lifecycle

Computer owns persistent login state. Actors references authorized context/connection IDs. Add explicit account bindings and preflight checks, connection health, revocation and a `waiting_for_auth` run state. Reconnection happens through a Computer session; resume opens a fresh session and reconstructs a safe checkpoint.

Do not put credentials in definition snapshots or logs. Scope cached authenticated results to account and project or disable their cache. Context locks must coordinate Actors concurrency; broader coordination with direct Computer clients requires a Computer-owned locking contract. Support parallel cloned contexts only when a backend can provide safe isolation.

## Durable execution and crawling

Introduce attempts with owner leases, heartbeats and fencing tokens. Require the current fence for state transitions, queue acknowledgments and dataset commits. Recover expired owners without allowing late workers to commit. Reconcile browser/runner resources after cancellation or failure.

Persist safe step position, frontier, pagination cursor, dedupe keys and committed output positions. A checkpoint specifies reconstruction rather than serializing an arbitrary browser page. Replay only safe steps. Continue at-least-once execution with idempotent record commits, explicit submission keys and payload-conflict detection.

A durable request queue records method, URL, relevant input identity, unique key, depth, priority, attempts and leases. Support per-run or task-level deduplication, host throttling, fairness, backoff and page/item/time/spend budgets. Do not assume URL alone identifies non-GET work. Validate network destinations and redirects through the executor boundary.

Start distributed workers behind one metadata authority. Do not share SQLite files between hosts. Multi-replica control-plane availability needs a separately validated persistence and coordination design.

Writes require operation-specific commit boundaries and reconciliation. A timed-out publish/send action might already have succeeded. If the outcome cannot be determined, report `outcome_unknown`; do not blindly retry. Exactly-once external writes cannot be promised without target support.

## Dataset and state services

Replace bounded in-memory export accumulation with immutable streamed chunks in Storage and transactional indexes in Actors. Publish chunks only after their metadata commits; recover abandoned staged chunks. Coordinate queue acknowledgments and item commits so crashes cannot lose acknowledged work. Stable item keys suppress replay duplicates.

Support schema validation, consistent cursor snapshots, projections, bounded filters, incremental reads, asynchronous JSON/JSONL/CSV exports and explicit finalization/completeness. A failed run may retain useful partial output. Separate immutable run evidence from optional named cross-run datasets and upserts.

Add scoped task state for incremental synchronization, advancing cursors only after corresponding results commit. Enforce byte/item limits and reference-aware retention for datasets, screenshots, exports and traces.

## API, MCP and automation

Keep generic invocation tools as the baseline. Add stable operation publication via API, generated OpenAPI, restricted account bindings, operation authorization and bounded synchronous waiting. Asynchronous submission returns a run ID; waiting longer is not a prerequisite to using the API. Client retries must not enqueue duplicate logical runs.

Named MCP tools are optional publication adapters after verifying gateway discovery and per-principal visibility. They are not required for generic invocation.

Jobs delivers scheduled tasks; Actors tracks execution. Add overlap and backlog policies. A transactional outbox records completion events alongside state transitions. Signed webhooks use stable event IDs, retries, secret rotation and delivery history. Callback failure must not rerun the actor. Payloads contain safe metadata and authorized result references.

## Authoring and distribution

Expand the UI with workflow/code editing, schemas, account binding, small test runs, live browser views, dataset inspection and version comparisons. Agent assistance may draft or repair actors; changes become drafts validated against fixtures before promotion. Do not silently rewrite production definitions after a site changes.

Catalog entries include docs, operations, version/source identity, permissions, account needs, sample outputs, runtime requirements and test status. Begin with project and organization sharing. Public discovery, publisher trust and paid distribution are later layers. Imported actors use the receiving project's account bindings and grants.

Track queue/execution latency, errors, page/item counts, bytes, browser time, code resources and optional LLM usage. Distinguish estimated costs from measured costs and unknown data from zero. Enforce budgets at admission and during execution.

## Delivery gates

| Milestone | Remaining work and exit criteria |
|---|---|
| Initial app | Implemented v0.1 foundation; tests cover direct Computer calls, pinned operations/tasks, project isolation, schedules and partial datasets |
| Reliable authenticated operations | Full contract validation, auth attention/resume, robust waits and checkpoint recovery; reference workflows survive login expiry and worker crashes |
| Published website APIs | API adapter, OpenAPI, bounded waits, outbox/webhooks and schedule overlap; retried invocation and callback delivery do not duplicate work |
| Production crawling | Durable queues, streamed datasets, fair distributed workers; a controlled 10,000-page crawl survives crashes with bounded memory and no lost acknowledged data |
| Code actors | Isolated builds/execution, SDK, hybrid extraction and write reconciliation; untrusted actors cannot access parent secrets or unauthorized capabilities |
| Catalog maturity | Shared templates, channels, regression testing and optional named MCP publication; another project installs an actor with isolated accounts/data |

A generic authenticated fixture site should exercise saved login, expired login, pagination, detail pages and layout changes. Optional real-site examples can be added as definitions; Patreon is one possible example, never a mandatory integration or test dependency.

## Web migration

Do not silently transfer app-local IDs or assume the two databases share identity. A later migration must export/import definitions, preserve an explicit old-to-new mapping, copy required historical references deliberately and replace Jobs targets without duplicating scheduled occurrences. Existing Web extractor clients can then use compatibility forwarding. Web's basic tools stay available throughout. No destructive Web migration is included in v0.1 creation.
