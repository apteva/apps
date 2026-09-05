// Analytics v0.1 — generic event store for Apteva apps.
//
// V1 surface: explicit-tracking only. Apps call analytics_track when
// they want an event recorded; queries surface aggregates. Auto-
// capture from the platform event firehose is deferred to v0.2 (waits
// on a global app-events stream endpoint in apteva-server).
package main

import (
	_ "embed"
	"errors"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Embedded manifest. Mirrors apteva.yaml so the binary can validate
// itself at boot and surface its identity over /api/manifest. Keep
// these two files in sync; CI compares.
const manifestYAML = `schema: apteva-app/v1
name: analytics
display_name: Analytics
version: 0.14.0
description: |
  Generic event analytics for Apteva apps. Other apps call
  analytics_track to record typed events; analytics_query / count /
  top / topics surface aggregates over JSON props. Each track emits an
  event.recorded bus event; the dashboard panel is real-time (filters
  by event type / app / props, live event feed). v0.4 adds static-site
  tracking: public write keys + a hosted tag.js + GET /collect, so any
  website can send page views like a classic analytics snippet. v0.6 adds
  opt-in bus auto-capture: analytics subscribes to the platform's all-apps
  event firehose and records what apps already emit — so no app needs a
  dependency on analytics. Toggle it in the panel's Capture tab. v0.7 adds
  saved dashboards with stat, timeseries, top, breakdown, and feed widgets.
  v0.8 adds an event catalog: specs, typed properties, validation,
  spec-driven auto upsert / rollup policies, and analytics_sum.
  v0.8.1 fixes a Catalog load deadlock with the SDK's single SQLite
  connection runtime.
  v0.8.2 lets dashboard stat and timeseries widgets sum numeric event
  fields.
  v0.8.3 adds dashboard-level filters and the Patreon Overview template.
  v0.8.4 scopes agent MCP reads and writes to the platform-assigned
  current project and rejects mismatched project_id args.
  v0.8.5 adds project-safe HTTP APIs, date-correct transactional
  policy ingestion, strict numeric aggregates, and batched dashboards.
  v0.8.6 restores anonymous static-site tracking under the server's
  explicit public-route policy while preserving existing tag URLs.
  v0.8.7 adds Analytics' adaptive themed app icon and updates the app
  to the current Apteva app SDK.
  v0.9 adds measurable objectives and targets over events already stored
  in Analytics. Targets use project-scoped count, sum, or distinct queries;
  no external app dependency or cross-app call is required.
  v0.10 adds a generic project-home widget backed by any saved dashboard,
  plus reusable count, distinct, sum, average, min, max, latest, and change
  aggregations for dashboard stats and timeseries. Existing value/by widgets
  retain their v0.9 sum/distinct behavior without migration.
  v0.11 links objective targets explicitly to saved dashboard metrics and
  renders live goal progress in both the Analytics panel and home widget.
  Objective targets now support every generic numeric aggregation from v0.10;
  dashboard reads evaluate linked progress without writing progress history.
  v0.11.1 renders each linked goal once in the home widget and upgrades metric
  trends to themed filled area charts.
  v0.11.2 keeps area-chart endpoints inside the plot and adds optional generic
  previous-period comparisons for dashboard metrics.
  v0.11.3 keeps trend endpoint markers circular in stretched dashboard cards.
  v0.12 adds read-only mixed-currency aggregation for stats, trends,
  comparisons, and objectives using project-scoped auditable FX rates. Source
  events remain unchanged.
  v0.13 adds generic project-scoped reference sets for governed dimensions,
  active-value validation and discovery, and safe patch semantics for event
  specifications.
author: Apteva
tags: [analytics, events, observability]
scopes: [global]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
    - method: GET
      prefix: /ui/tag.js
      no_auth: true
    - method: GET
      prefix: /collect
      no_auth: true
  permissions:
    - name: events.write
      description: Record events via analytics_track.
    - name: events.read
      description: Query events.
    - name: references.manage
      description: Manage canonical Analytics reference sets and values.
  mcp_tools:
    - name: analytics_track
      description: Record one event in the current project. Project is assigned automatically from the calling agent; do not pass project_id.
      requires: events.write
    - name: analytics_query
      description: Read events with optional filters and group_by.
      requires: events.read
    - name: analytics_count
      description: Count events matching filters.
      requires: events.read
    - name: analytics_top
      description: Top-N values for a JSON props key.
      requires: events.read
    - name: analytics_topics
      description: List distinct (app, topic) pairs seen.
      requires: events.read
    - name: analytics_sum
      description: Sum a numeric property over events, optionally grouped.
      requires: events.read
    - name: analytics_sum_money
      description: Convert and sum mixed-currency event amounts without changing source events.
      requires: events.read
    - name: analytics_fx_rate_upsert
      description: Create or update a project-scoped FX reference rate.
      requires: events.write
    - name: analytics_fx_rates_list
      description: List project-scoped FX reference rates.
      requires: events.read
    - name: analytics_reference_set_upsert
      description: Create or update one project-scoped canonical reference set.
      requires: references.manage
    - name: analytics_reference_value_upsert
      description: Create, update, activate, or deactivate a canonical reference value.
      requires: references.manage
    - name: analytics_reference_values_list
      description: List canonical values in one current-project reference set.
      requires: events.read
    - name: analytics_event_specs_list
      description: List current-project event specs.
      requires: events.read
    - name: analytics_event_specs_for_app
      description: Show active current-project specs for an app before tracking.
      requires: events.read
    - name: analytics_event_spec_get
      description: Get one event spec with properties.
      requires: events.read
    - name: analytics_event_spec_upsert
      description: Create or safely patch an event spec; omitted properties and policies are preserved.
      requires: events.write
    - name: analytics_event_spec_delete
      description: Delete an event spec.
      requires: events.write
    - name: analytics_event_property_upsert
      description: Create or update one property spec, optionally referencing a canonical reference set.
      requires: events.write
    - name: analytics_event_property_delete
      description: Delete one event property spec.
      requires: events.write
    - name: analytics_event_validate
      description: Validate a sample event against the catalog without storing it.
      requires: events.read
    - name: analytics_validate_track
      description: Dry-run analytics_track in the current project without storing it.
      requires: events.read
    - name: analytics_event_violations
      description: List recent event spec validation violations.
      requires: events.read
    - name: analytics_objectives_create
      description: Create an objective with targets over current-project Analytics data.
      requires: events.write
    - name: analytics_objectives_get
      description: Get one objective and its cached target progress.
      requires: events.read
    - name: analytics_objectives_search
      description: Search current-project objectives.
      requires: events.read
    - name: analytics_objectives_update
      description: Update objective metadata or replace its targets.
      requires: events.write
    - name: analytics_objectives_archive
      description: Archive an objective without deleting its history.
      requires: events.write
    - name: analytics_objective_progress
      description: Evaluate objective targets against stored Analytics events.
      requires: events.read
    - name: analytics_objective_metrics_list
      description: List Analytics metric-query templates for objectives.
      requires: events.read
  ui_panels:
    - slot: project.page
      label: Analytics
      icon: trending-up
      entry: /ui/AnalyticsPanel.mjs
  ui_components:
    - name: analytics-dashboard
      label: Analytics
      description: Live saved metrics, trends, and project activity from any Analytics dashboard.
      entry: /ui/AnalyticsDashboardWidget.mjs
      slots: [dashboard.home]
      suggested: true
      visibility: project
      supported_sizes: [half, full]
      default_size: half
      refresh_topics: [event.recorded]
      settings_schema:
        type: object
        properties:
          dashboard_id:
            type: integer
            title: Dashboard ID
            description: Saved Analytics dashboard to show. Leave unset to use the newest dashboard.
            minimum: 1
          show_trends:
            type: boolean
            title: Show trends and details
            default: true
          show_goals:
            type: boolean
            title: Show linked goals
            default: true
          max_metrics:
            type: integer
            title: Maximum metrics
            description: Maximum stat cards displayed before trends and details.
            default: 3
            minimum: 1
            maximum: 6
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: analytics/v0.14.0
    entry: mcp/analytics
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/analytics.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

//go:embed ui/tag.js
var trackingTagJS []byte

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("analytics requires a db block")
	}
	if err := upgradeLegacyPolicyKeys(ctx.AppDB()); err != nil {
		return err
	}
	globalCtx = ctx
	ctx.Logger().Info("analytics mounted")
	// Bus auto-capture subscriber (idles until enabled in the Capture tab).
	startAutoCapture(ctx)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if captureStop != nil {
		captureStop()
	}
	if captureDone != nil {
		<-captureDone
	}
	return nil
}

// HTTPRoutes back the read-only dashboard panel (ui/AnalyticsPanel.mjs).
// Agents use the MCP tools; these endpoints are panel-only aggregates.
func (a *App) HTTPRoutes() []sdk.Route {
	routes := []sdk.Route{
		{Pattern: "/references", Handler: a.handleReferences},
		{Pattern: "/fx-rates", Handler: a.handleFX},
		{Pattern: "/retention", Handler: a.handleRetention},
		{Pattern: "/archive/restore", Handler: a.handleArchiveRestore},
		{Pattern: "/diagnostics", Handler: a.handleHealth},
		{Pattern: "/summary", Handler: a.handleSummary},
		{Pattern: "/series", Handler: a.handleSeries},
		{Pattern: "/top", Handler: a.handleTop},
		// NOT "/events": the app-sdk reserves /events for platform event
		// ingestion (run.go) — registering it here panics the mux at boot.
		{Pattern: "/feed", Handler: a.handleEvents},
		{Pattern: "/dimensions", Handler: a.handleDimensions},

		// Public static-site tracking. Keep /ui/tag.js stable because existing
		// customer sites already embed that URL. Both routes are also declared
		// no_auth in the manifest so the platform gateway allows them through.
		{Method: "GET", Pattern: "/ui/tag.js", Handler: a.handleTrackingTag, NoAuth: true},
		{Method: "GET", Pattern: "/collect", Handler: a.handleCollect, NoAuth: true},

		// Write-key management is operator-only. The gateway requires auth and
		// the handlers also require X-User-ID. Routes are method-prefixed so
		// GET + POST on /keys don't collide on the ServeMux.
		{Method: "GET", Pattern: "/keys", Handler: a.handleKeysList},
		{Method: "POST", Pattern: "/keys", Handler: a.handleKeysCreate},
		{Method: "POST", Pattern: "/keys/revoke", Handler: a.handleKeysRevoke},

		// Bus auto-capture config — operator-only.
		{Method: "GET", Pattern: "/capture", Handler: a.handleCaptureGet},
		{Method: "POST", Pattern: "/capture", Handler: a.handleCaptureSet},

		// Saved dashboards and widget evaluation.
		{Pattern: "/dashboards", Handler: a.handleDashboards},
		{Pattern: "/dashboards/", Handler: a.handleDashboardItem},
		{Pattern: "/widgets/", Handler: a.handleWidgetItem},
		{Pattern: "/query-widget", Handler: a.handleWidgetQuery},
		{Pattern: "/query-dashboard", Handler: a.handleDashboardQuery},
		{Pattern: "/dashboard-filter-options", Handler: a.handleDashboardFilterOptions},

		// Objectives and target progress over already-ingested Analytics data.
		{Pattern: "/objectives", Handler: a.handleObjectives},
		{Pattern: "/objectives/", Handler: a.handleObjectiveItem},
		{Method: "GET", Pattern: "/objective-metrics", Handler: a.handleObjectiveMetrics},

		// Event catalog / tracking plan.
		{Pattern: "/event-specs", Handler: a.handleEventSpecs},
		{Pattern: "/event-specs/", Handler: a.handleEventSpecItem},
		{Pattern: "/event-spec-violations", Handler: a.handleEventSpecViolations},
	}
	for i := range routes {
		routes[i].Handler = boundedHandler(routes[i].Handler)
	}
	return routes
}
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "analytics-retention", Schedule: "@every 5m", Run: a.retentionWorker}}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
		{
			Name:        "analytics_track",
			Description: "Record one event in the current project. Args: event (required), props?, app?, user_id?, session_id?, install_id?, ts? (unix ms; defaults to now). Do not pass project_id; the platform assigns it automatically. Returns {id, ts}.",
			InputSchema: schemaObject(map[string]any{
				"event":       map[string]any{"type": "string"},
				"props":       map[string]any{"type": "object"},
				"app":         map[string]any{"type": "string"},
				"user_id":     map[string]any{"type": "string"},
				"session_id":  map[string]any{"type": "string"},
				"install_id":  map[string]any{"type": "integer"},
				"upsert_key":  map[string]any{"type": "string"},
				"delivery_id": map[string]any{"type": "string", "description": "Stable retry identity; reusing it with different data is rejected."},
				"ts":          map[string]any{"type": "integer"},
			}, []string{"event"}),
			Handler: a.toolTrack,
		},
		{
			Name:        "analytics_query",
			Description: "Read events in the current project. Args: app?, topic?, since? (unix ms), until?, where? (map of \"props.X\" → value, equality only), group_by? (array of \"props.X\" / app / topic / source), limit? (default 100, max 1000). Without group_by returns recent rows; with group_by returns aggregate buckets.",
			InputSchema: schemaObject(map[string]any{
				"app":      map[string]any{"type": "string"},
				"topic":    map[string]any{"type": "string"},
				"since":    map[string]any{"type": "integer"},
				"until":    map[string]any{"type": "integer"},
				"where":    map[string]any{"type": "object"},
				"group_by": map[string]any{"type": "array"},
				"limit":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolQuery,
		},
		{
			Name:        "analytics_count",
			Description: "Count current-project events matching filters. Args: app?, topic?, since?, until?, where?. Returns {count}.",
			InputSchema: schemaObject(map[string]any{
				"app":   map[string]any{"type": "string"},
				"topic": map[string]any{"type": "string"},
				"since": map[string]any{"type": "integer"},
				"until": map[string]any{"type": "integer"},
				"where": map[string]any{"type": "object"},
			}, nil),
			Handler: a.toolCount,
		},
		{
			Name:        "analytics_top",
			Description: "Top-N values for a JSON props key in the current project. Args: by (required, e.g. \"props.platform\"), app?, topic?, since?, until?, where?, limit? (default 10, max 200). Returns [{value, count}].",
			InputSchema: schemaObject(map[string]any{
				"by":    map[string]any{"type": "string"},
				"app":   map[string]any{"type": "string"},
				"topic": map[string]any{"type": "string"},
				"since": map[string]any{"type": "integer"},
				"until": map[string]any{"type": "integer"},
				"where": map[string]any{"type": "object"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"by"}),
			Handler: a.toolTop,
		},
		{
			Name:        "analytics_topics",
			Description: "List distinct (app, topic) pairs seen. Args: app?. Returns [{app, topic, last_ts, count}].",
			InputSchema: schemaObject(map[string]any{
				"app": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolTopics,
		},
		{
			Name:        "analytics_sum",
			Description: "Sum a numeric current-project event field/path. Args value (required, e.g. props.views), app?, topic?, since?, until?, where?, group_by? (array), limit?. Returns buckets with sum/count.",
			InputSchema: schemaObject(map[string]any{
				"value":    map[string]any{"type": "string"},
				"app":      map[string]any{"type": "string"},
				"topic":    map[string]any{"type": "string"},
				"since":    map[string]any{"type": "integer"},
				"until":    map[string]any{"type": "integer"},
				"where":    map[string]any{"type": "object"},
				"group_by": map[string]any{"type": "array"},
				"limit":    map[string]any{"type": "integer"},
			}, []string{"value"}),
			Handler: a.toolSum,
		},
		{
			Name:        "analytics_sum_money",
			Description: "Read-only mixed-currency sum over current-project events. Converts each amount with the latest project FX rate at or before its event/accounting date and never changes source events.",
			InputSchema: schemaObject(map[string]any{
				"value":              map[string]any{"type": "string", "description": "Numeric amount field, for example props.total_cents."},
				"currency_field":     map[string]any{"type": "string", "description": "Currency field, for example props.currency."},
				"reporting_currency": map[string]any{"type": "string", "description": "Three-letter target currency, for example EUR."},
				"amount_unit":        map[string]any{"type": "string", "enum": []string{"minor", "major"}},
				"rate_date_field":    map[string]any{"type": "string", "description": "Optional props.X date; event timestamp is used when omitted."},
				"app":                map[string]any{"type": "string"},
				"topic":              map[string]any{"type": "string"},
				"since":              map[string]any{"type": "integer"},
				"until":              map[string]any{"type": "integer"},
				"where":              map[string]any{"type": "object"},
			}, []string{"value", "currency_field", "reporting_currency", "amount_unit"}),
			Handler: a.toolSumMoney,
		},
		{
			Name:        "analytics_fx_rate_upsert",
			Description: "Create or update one project-scoped FX reference rate. This changes only the rate cache, never Analytics events. One base unit equals rate quote units.",
			InputSchema: schemaObject(map[string]any{
				"base_currency":  map[string]any{"type": "string"},
				"quote_currency": map[string]any{"type": "string"},
				"as_of":          map[string]any{"type": "integer", "description": "Unix milliseconds."},
				"rate":           map[string]any{"type": "number"},
				"source":         map[string]any{"type": "string"},
			}, []string{"base_currency", "quote_currency", "as_of", "rate"}),
			Handler: a.toolFXRateUpsert,
		},
		{
			Name:        "analytics_fx_rates_list",
			Description: "List project-scoped FX reference rates for auditing money aggregates.",
			InputSchema: schemaObject(map[string]any{
				"base_currency":  map[string]any{"type": "string"},
				"quote_currency": map[string]any{"type": "string"},
				"since":          map[string]any{"type": "integer"},
				"until":          map[string]any{"type": "integer"},
				"limit":          map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolFXRatesList,
		},
		{
			Name:        "analytics_reference_set_upsert",
			Description: "Create or update one project-scoped reference set. Reference management is separate from event ingestion.",
			InputSchema: schemaObject(map[string]any{
				"key":         map[string]any{"type": "string"},
				"label":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}, []string{"key", "label"}),
			Handler: a.toolReferenceSetUpsert,
		},
		{
			Name:        "analytics_reference_value_upsert",
			Description: "Create, update, activate, or deactivate one value in a project-scoped reference set.",
			InputSchema: schemaObject(map[string]any{
				"reference_set": map[string]any{"type": "string"},
				"value":         map[string]any{"type": "string"},
				"label":         map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string", "enum": []string{"active", "inactive"}},
				"metadata":      map[string]any{"type": "object"},
			}, []string{"reference_set", "value"}),
			Handler: a.toolReferenceValueUpsert,
		},
		{
			Name:        "analytics_reference_values_list",
			Description: "List canonical values in one current-project reference set. Defaults to active values for agent discovery.",
			InputSchema: schemaObject(map[string]any{
				"reference_set": map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string", "enum": []string{"active", "inactive"}},
				"after":         map[string]any{"type": "integer"},
				"search":        map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, []string{"reference_set"}),
			Handler: a.toolReferenceValuesList,
		},
		{
			Name:        "analytics_event_specs_list",
			Description: "List current-project event specs. Args app?, status?. Returns specs with properties.",
			InputSchema: schemaObject(map[string]any{
				"app":    map[string]any{"type": "string"},
				"status": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolEventSpecsList,
		},
		{
			Name:        "analytics_event_specs_for_app",
			Description: "Show active current-project event specs for an app before tracking. Args app (required). Returns required properties, examples, and ingest/upsert policies.",
			InputSchema: schemaObject(map[string]any{
				"app": map[string]any{"type": "string"},
			}, []string{"app"}),
			Handler: a.toolEventSpecsForApp,
		},
		{
			Name:        "analytics_event_spec_get",
			Description: "Get one current-project event spec by id or by app/topic.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"app":   map[string]any{"type": "string"},
				"topic": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolEventSpecGet,
		},
		{
			Name:        "analytics_event_spec_upsert",
			Description: "Create or safely patch an event spec in the current project. Omitted properties and policies are preserved; clearing requires an explicit clear_* flag.",
			InputSchema: schemaObject(map[string]any{
				"id":                  map[string]any{"type": "integer"},
				"app":                 map[string]any{"type": "string"},
				"topic":               map[string]any{"type": "string"},
				"kind":                map[string]any{"type": "string"},
				"display_name":        map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"category":            map[string]any{"type": "string"},
				"status":              map[string]any{"type": "string"},
				"validation_mode":     map[string]any{"type": "string"},
				"ingest_mode":         map[string]any{"type": "string"},
				"upsert_policy":       map[string]any{"type": "object"},
				"rollup_policy":       map[string]any{"type": "object"},
				"properties":          map[string]any{"type": "array"},
				"updated_at":          map[string]any{"type": "integer", "description": "Version from the fetched spec; stale edits are rejected."},
				"clear_properties":    map[string]any{"type": "boolean"},
				"clear_upsert_policy": map[string]any{"type": "boolean"},
				"clear_rollup_policy": map[string]any{"type": "boolean"},
			}, []string{"app", "topic"}),
			Handler: a.toolEventSpecUpsert,
		},
		{
			Name:        "analytics_event_spec_delete",
			Description: "Delete an event spec by id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolEventSpecDelete,
		},
		{
			Name:        "analytics_event_property_upsert",
			Description: "Create or update one property spec. reference_set optionally enforces an active canonical dimension value.",
			InputSchema: schemaObject(map[string]any{
				"event_spec_id":      map[string]any{"type": "integer"},
				"key":                map[string]any{"type": "string"},
				"type":               map[string]any{"type": "string"},
				"required":           map[string]any{"type": "boolean"},
				"description":        map[string]any{"type": "string"},
				"enum_values":        map[string]any{"type": "array"},
				"reference_set":      map[string]any{"type": "string"},
				"pii_classification": map[string]any{"type": "string"},
				"example_value":      map[string]any{"type": "string"},
			}, []string{"event_spec_id", "key"}),
			Handler: a.toolEventPropertyUpsert,
		},
		{
			Name:        "analytics_event_property_delete",
			Description: "Delete one event property spec.",
			InputSchema: schemaObject(map[string]any{
				"event_spec_id": map[string]any{"type": "integer"},
				"key":           map[string]any{"type": "string"},
			}, []string{"event_spec_id", "key"}),
			Handler: a.toolEventPropertyDelete,
		},
		{
			Name:        "analytics_event_validate",
			Description: "Validate a sample current-project event against specs without storing it.",
			InputSchema: schemaObject(map[string]any{
				"app":        map[string]any{"type": "string"},
				"topic":      map[string]any{"type": "string"},
				"event":      map[string]any{"type": "string"},
				"user_id":    map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
				"upsert_key": map[string]any{"type": "string"},
				"props":      map[string]any{"type": "object"},
			}, []string{"app"}),
			Handler: a.toolEventValidate,
		},
		{
			Name:        "analytics_validate_track",
			Description: "Dry-run analytics_track in the current project without storing it. Args: app?, event/topic, props?, user_id?, session_id?, upsert_key?, ts?. Returns validation status, required-property hints, example payload, and ingest preview.",
			InputSchema: schemaObject(map[string]any{
				"app":        map[string]any{"type": "string"},
				"topic":      map[string]any{"type": "string"},
				"event":      map[string]any{"type": "string"},
				"user_id":    map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
				"upsert_key": map[string]any{"type": "string"},
				"props":      map[string]any{"type": "object"},
				"ts":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolEventValidate,
		},
		{
			Name:        "analytics_event_violations",
			Description: "List recent current-project event spec violations. Args app?, topic?, since?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"app":   map[string]any{"type": "string"},
				"topic": map[string]any{"type": "string"},
				"since": map[string]any{"type": "integer"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolEventViolations,
		},
	}
	return withToolDeadlines(append(tools, a.objectiveTools()...))
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func main() {
	sdk.Run(&App{})
}
