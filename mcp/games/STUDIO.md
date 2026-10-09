# Games studio: v0.7.1

Games connects each game's existing backend to source, delivery and measurement.
Install or upgrade Games to v0.7.1 to use these features. Existing installations
keep their player administration behavior; source, delivery and reporting need
the optional bindings described below.

## Setup and ownership

1. Bind **Code >=0.10.0**, **Deploy >=0.27.2**, and **Analytics** in the Games
   installation settings. These are optional for existing player administration.
2. In **Source**, use **Set up deployment** to select or create a Code repository,
   select or create a Deploy target, and choose its platform and environment.
   The existing source/deployment link controls remain available.
3. For a new target, select an immutable recipe version and a configured Deploy
   runner, or enter an Apteva capsule runner URL. Runner tokens stay in Deploy.
   Capture exact sibling dependency snapshots and assign their sibling directory
   names (`engine` for the bundled Kiln iOS recipe). Code handoff snapshots expire;
   capture fresh pins explicitly before another build when a pin is unavailable.
4. Review and confirm the association. Setup saves each resource receipt before
   continuing. Saved progress survives reloads and partial failures. Retry resumes
   completed steps. An uncertain creation blocks retries until the exact resource
   carrying that setup's marker is recovered, or the operator explicitly confirms
   that no resource was created with a written reason. Recovery never guesses the
   newest deployment. Repository, deployment and environment identities are checked
   against the project, source and platform before linking.
5. In **Releases**, choose a build and channel, then **Check readiness**. Each check
   is ready, blocked or unchecked, with its reason and next action. Configuration
   checks remain separate from final-artifact test evidence. Changing the selected
   build or channel clears the displayed assessment. Artifact tests must match the
   current pipeline and selected build, with passing test names, digest and time.
6. Build, download retained artifacts, inspect test evidence and read selected
   build logs. Failed builds show their stage when Deploy reports it. Release or
   promote the explicitly selected build/release through Deploy's existing policy.
   Configure signing, provider connections, listing and policy in Deploy using the
   links from Games. No setup step starts a build or automatically publishes.

### Recipes and configuration

`build-recipes.json` contains versioned configuration data; Games contains no engine
builder. The bundled Kiln iOS recipe installs frozen Bun dependencies, runs source
checks, exports a self-contained Xcode project and uses Deploy's iOS builder for
archiving, signing and IPA export. Final-artifact checks inspect the retained IPA.
The generic artifact recipe is a starting configuration to customize for a project.
**Save a reusable recipe** exposes named preparation commands, generated project
folder, build command, outputs and artifact tests without pipeline JSON. Versions
are immutable and scoped to the project. Commands use Deploy's existing generic
pipeline contract; configuration is written to Deploy for execution.

**Configure an existing game target** applies a recipe and runner to that target's
Deploy environment. Omitted signing/account selections and release policy are
preserved. Runner profiles reference existing deployment environments and expose
only non-secret metadata. OS/architecture compatibility is declared by the profile;
successful matching build evidence verifies execution. External Codemagic/GitHub
workflows must return matching generic pipeline evidence; a selected backend alone
does not prove that its workflow supports artifact tests.

New MCP surfaces: `games_setup_options`, `games_setup`, `games_setup_history`,
`games_setup_reconcile`, `games_recipe_save`, `games_source_pin`, and
`games_target_configure`. Setup requires an immutable `request_key` plus a `setup`
object. `games_release_plan` accepts `build_id` and `channel` and returns an
additional `readiness` object; existing configuration fields remain available.

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
| Google Play Developer | Android package ID; report bucket and report permissions on the connection | Monthly estimated buyer-paid sales or merchant-currency earnings, selected as separate sources |

For Google Play, set the Play Console report bucket on the connection and grant
global report access. Existing OAuth connections may need reconnection to add the
`devstorage.read_only` scope before reports can be downloaded. Bind the connection
to Games' `reporting` role even if Deploy already uses it for publishing.

Scheduled daily imports refresh the last seven completed provider-local dates every six
hours. Google Play imports the two latest available sales months or the previous two
earnings months; `games_metrics_sync {month: "YYYYMM"}` imports an exact older Play
report. Manual daily sync supports a bounded 1–31 day window. The worker
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

Google Play report ZIPs are filtered to the selected package ID before import. Sales
remain buyer-paid estimates that include collected taxes and do not deduct Google fees. Earnings retain transaction type
and merchant currency; do not treat a filtered sum as a payout statement. Play and
Steam installs, Crashlytics/Play vitals and acquisition attribution require their
own supported reporting mappings.
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
- A real Code → Deploy 0.27.2 test called through Games, with more than
  eight MiB of incompressible source and final-artifact test evidence.
- React panel and offline-client tests, TypeScript checks, canonical panel build
  and host React import verification.
- Browser inspection of release and metrics views using a local fixture and the
  dashboard styles. Fixture values are not real game analytics.

Real publisher OAuth, store submission and device performance
require the actual game source, runner and account configuration. Tests use local
fixtures and do not publish to any store.

### Moonhorde acceptance

`MOONHORDE_SOURCE_ROOT=/path/to/games GOWORK=off go test -run
TestIntegration_MoonhordeSetupCodeDeploy -v -timeout 15m` captures actual Moonhorde
and Kiln sources into separate Code repositories, pins the engine snapshot,
configures the deployment through Games, and asks Deploy to export and compile a
real unsigned iOS archive with final-artifact tests. This checks source handoff,
sibling isolation, setup replay, native compilation and evidence. It does not
prove distribution signing, an IPA upload or store publication; those require the
actual game's macOS runner and Apple configuration in Deploy.
