# Games studio: v0.3.0

Games connects each game's existing backend to source, delivery and measurement.
Install or upgrade Games to v0.3.0 to use these features. Existing installations
keep their player administration behavior; source, delivery and reporting need
the optional bindings described below.

## Setup and ownership

1. Bind **Code >=0.10.0**, **Deploy >=0.26.0**, and **Analytics** in the Games
   installation settings. These are optional for existing player administration.
2. In **Source**, discover and link a Code repository. Multiple games may
   intentionally share a repository. A link records its repository and installation
   identity; replacing either requires reattachment.
3. Configure a deployment in Deploy, then link its environment in Games. Choose
   Android, iOS, desktop or Steam. One deployment/environment belongs to one game.
   The deployment must reference that game's linked Code source.
4. In **Releases**, check readiness, build, inspect final-artifact tests and publish
   or promote through Deploy. Inspect logs, change Android rollout fractions or
   halt Android/expire TestFlight releases through the scoped controls. An engine export is a configured command; Games does
   not select an engine, install a toolchain or create publisher credentials.

Deploy remains the source of truth for source subdirectories, engine scripts,
build hosts, signing, store accounts, listing documents and policy. Editing game
catalog text does not overwrite localized store descriptions. **Store listing**
reads and explicitly saves Deploy's document; applying the document to a store is
still a separate Deploy action.

Starting configurations are in `examples/`. Supply the referenced scripts and
pin their engine/toolchain versions on the runner. Mobile examples are export
skeletons: complete their package/bundle identity, signing, artifact tests and
release policy in Deploy. Use Deploy's own `examples/steamworks-target.json` for
upload receipts, publishing and branch observation. No example is ready to submit
to a store without project-specific configuration.

## Delivery safety and recovery

All studio MCP tools require an explicit `game_id`, except the portfolio list.
Build/release tools also require a persistent `request_key`. Repeating the same
key returns its recorded result; changing its arguments is rejected.

Games saves a dispatch intent before calling Deploy. A timeout, missing receipt or
restart leaves the target blocked for reconciliation. Games never guesses that
the newest remote build was created by its request. Supply the exact verified
build/release ID using `games_delivery_reconcile` with `confirm: true`. If inspection
confirms that nothing was created (for example a policy rejected a request before
creating a release), use `resolution: "not_created"`, `confirm: true` and a written
`reason`; a later retry needs a new request key. This declaration does not cancel
or undo any remote store operation.

The current Deploy detail API returns ten recent builds/releases per environment.
The Games picker and ID validation deliberately use those retained results. Older
records can be operated on directly in Deploy. Games request history is separately
paginated. Portfolio summaries are cached at the last target refresh and show
their timestamp; loading the portfolio makes no provider calls.

Deploy enforces its channel policy and exact-artifact checks. Policy approval must
be performed through Deploy by an authorized caller. Games does not forward an
invented approver identity. Availability observations remain separate from upload,
review and branch-assignment state; unconfirmed availability stays unconfirmed.

## Reporting

Bind provider connections to the **reporting** role, then map each external app
under **Metrics**. Credentials stay in the integration platform. The AdMob catalog
entry is reused unchanged.

| Provider | Required mapping | Imported data |
|---|---|---|
| AdMob | Publisher ID, AdMob app ID, Network or mediation | Daily impressions, clicks and estimated earnings |
| GA4 | Property ID and game stream ID | Daily active users, sessions and event counts |
| App Store Connect | App ID and vendor number | Daily units and sales-report proceeds, separated by currency |

Scheduled imports refresh the last seven completed provider-local dates every six
hours. Manual `games_metrics_sync` supports a bounded 1–31 day window. The worker
claims a durable lease; errors preserve last success and retry after 30 minutes.
Incomplete or warning-bearing AdMob reports and thresholded/sampled GA4 reports
fail visibly. They are not imported as zero. Empty **complete** daily reports
replace previous results, so provider corrections can remove old rows.

Analytics stores one replaceable `games.provider_daily` event per game/source/day.
Its `props.facts` contains the complete daily result, including currencies and
reporting basis. Monetary micros and large counts stay exact decimal strings.
No float-based conversion is used for stored money. Dashboards show source facts
separately: do not add Network to mediation totals, combine store proceeds with
gross revenue, or sum daily unique users into monthly unique users.

The current automatic import adapters are the three providers above. Play/Steam
sales, Crashlytics/Play vitals and acquisition attribution require their own
supported reporting mappings; this change does not fabricate those measurements.
Player-side `performance_summary` events support game-provided diagnostics but
are not a replacement for a crash-reporting SDK or physical-device benchmarks.

## Gameplay telemetry

`POST /v2/games/{game_id}/events` accepts 1–50 events using a normal Games player
token, including a guest token. No account signup is required, and no platform
credential belongs in a game build. The opt-in TypeScript client is
`client/telemetry.ts`; native engines can implement the same JSON contract.

```json
{"events":[{"id":"unique-event-id","name":"run_completed","session_id":"session-id","run_id":"run-id","release":"1.0.0-12","environment":"testing","time":"2026-09-07T12:00:00Z","props":{"duration_seconds":240,"score":1200}}]}
```

Allowed names: `session_started`, `run_started`, `run_completed`, `run_failed`,
`tutorial_completed`, `performance_summary`. Each event needs a stable ID, session,
release, environment and time within seven days. Properties are bounded scalars.
These events cannot change authoritative scores, rewards or achievements.

The client buffers at most 500 events, sends 50 at a time, preserves IDs across
retries, supports an engine-provided persistence adapter and makes no network
request when disabled. Call `flush()` on an appropriate foreground/network
schedule and refresh tokens through the supplied callback. The server caps a
game's pending telemetry backlog at 10,000 events, applies per-player limits,
deduplicates transactional receipts and retains them for eight days.

Games outbox delivery now supplies stable `upsert_key` and `delivery_id` values.
An absent Analytics binding postpones delivery without exhausting retries. Failed
delivery remains visible through the existing event retry tooling. Gameplay and
provider events each have one ingestion path; they are not also emitted as a
second `games` AppBus event.

## Validation

- Existing and new Go unit tests, including ownership, idempotency, provider
  corrections, money precision, optional dependencies and telemetry isolation.
- Race checks and real Auth/Games sidecar tests.
- A real Code 0.10.0 → Deploy 0.26.0 test called through Games, with more than
  eight MiB of incompressible source and final-artifact test evidence.
- React panel and offline-client tests, TypeScript checks, canonical panel build
  and host React import verification.
- Browser inspection of release and metrics views using a local fixture and the
  dashboard styles. Fixture values are not real game analytics.

Real publisher OAuth, store submission, engine exports and device performance
require the actual game source, runner and account configuration. Tests use local
fixtures and do not publish to any store.
