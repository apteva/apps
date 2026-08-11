// Apteva Community app — Circle/Skool-shaped community platform.
//
// 0.1 scope: multiple communities per install, members, spaces (feed/
// forum/chat), threads, posts, reactions, and DMs. Panel updates live
// via the platform event bus (ctx.Emit on every mutation). No hard
// dependencies on other apps.
//
// Each handler emits domain events the platform fans out to the panel:
//
//	community.created   member.joined        space.created
//	thread.created      post.created         post.reacted
//	post.edited         post.removed         dm.received
//
// Topics are stamped with the "community" app prefix by the platform
// before fanout, so callers in this file use the unprefixed form.
package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: community
display_name: Community
version: 0.12.0
description: |
  Circle/Skool-shaped community platform. Multiple communities per install,
  spaces (feed/forum/chat/course), members, threads, posts, reactions,
  in-app DMs, and full courses (metadata, sections, lessons, resources,
  quizzes, assignments, certificates, drip, enrollments, and progress). Ships both an
  operator dashboard panel and a client-facing React portal at
  /api/apps/community/_install/{install_id}/ui/portal/dist/index.html.
  Each community owns its portal branding and Auth client/organization
  binding, with automatic member linking and course-checkout continuation.
  An optional Domains binding publishes one custom hostname per community;
  native Apteva ingress owns routing and automatic TLS.
  Its public storefront projects only Catalog products actively offered by
  each community, groups one-time and recurring prices under generic product
  routes, and shows published course entitlements before authentication.
  Operators can explicitly mark published lessons as public samples; the
  storefront exposes a safe lesson projection and short-lived Storage video
  URLs without comments, progress, assessments, resources, or member data.
  Reusable instructor profiles support ordered multi-instructor courses,
  primary instructors, Storage avatars, public biographies, credentials,
  links, accomplishments, and live teaching statistics.
  Community uses Checkout for durable guest carts and immediate deferred
  Stripe Elements rendering, then creates and claims the Billing invoice only
  after verified authentication into either a
  one-time course purchase or a recurring Subscriptions lifecycle. It does
  not depend on Commerce or SaaS.
  Embedded Stripe Elements inherit each community's configured primary color.
  The storefront discloses account fields progressively: email first, then
  signup or sign-in password, while profile details stay out of checkout.
  Portal calls use
  @apteva/web-sdk for app HTTP, MCP tools, Auth app hooks, and live events.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/community
icon: /ui/icon.svg
icon_style: monochrome
tags: [community, courses, membership, forum, dms]
scopes: [project, global]
min_apteva_version: "0.11.0"
requires:
  permissions: [db.write.app, platform.apps.call, platform.ingress.read, platform.ingress.write]
  apps:
    - name: auth
      version: ">=0.9.1"
      optional: false
      reason: The member portal authenticates through Auth and maps verified users to Community members.
    - name: catalog
      version: ">=0.3.0"
      optional: false
      reason: Catalog owns the one-time products and immutable prices bound to paid course offers.
    - name: checkout
      version: ">=0.3.1"
      optional: false
      reason: Checkout owns durable guest carts, buyer checkout sessions, Billing invoice conversion, and browser-safe Stripe Elements configuration.
    - name: billing
      version: ">=0.12.3"
      optional: false
      events:
        - invoice.paid
        - invoice.refunded
        - invoice.voided
        - invoice.payment_failed
        - invoice.payment_action_required
        - payment_method.attached
      reason: Billing owns course customers, invoices, hosted or inline Stripe payment sessions, saved payment methods, automatic collection, refunds, and authoritative payment events.
    - name: subscriptions
      version: ">=0.7.2"
      optional: false
      events:
        - subscription.active
        - subscription.trialing
        - subscription.past_due
        - subscription.paused
        - subscription.cancelled
        - subscription.resumed
        - subscription.ended
        - subscription.cycle_due
      reason: Subscriptions owns recurring lifecycle, trials, renewal cycles, grace periods, scheduled cancellation, and resume.
  integrations:
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.read, files.write]
      required: false
      hint: "Bind storage to attach lesson videos + post attachments. Files live under /.community/."
    - role: ffmpeg
      kind: app
      compatible_app_names: [ffmpeg]
      capabilities: [probe]
      required: false
      hint: "When bound, lessons_attach_video auto-fills duration_seconds via ffmpeg_probe."
    - role: domains
      kind: app
      compatible_app_names: [domains]
      capabilities: [dns.list_records, dns.create_record, dns.delete_records]
      required: false
      hint: "Bind Domains to create and safely remove the community subdomain DNS record. Native Apteva ingress owns routing and TLS."
provides:
  http_routes:
    - prefix: /
    - { prefix: /ui/, method: GET, no_auth: true }
    - { prefix: /portal/bootstrap, method: GET, no_auth: true }
    - { prefix: /portal/products, method: GET, no_auth: true }
    - { prefix: /portal/products/, method: GET, no_auth: true }
    - { prefix: /portal/previews/, method: GET, no_auth: true }
    - { prefix: /portal/checkout/prepare, method: POST, no_auth: true }
    - { prefix: /store/, method: GET, no_auth: true }
  mcp_tools:
    - { name: communities_create,  description: "Create a community." }
    - { name: communities_list,    description: "List communities in this scope." }
    - { name: communities_get,     description: "Fetch one community by id or slug." }
    - { name: communities_update,  description: "Update a community's name or description." }
    - { name: communities_archive, description: "Soft-delete a community." }
    - { name: members_create,      description: "Create a member in a community." }
    - { name: members_list,        description: "List members of a community." }
    - { name: members_get,         description: "Fetch one member by id or handle." }
    - { name: members_me,          description: "Return the member linked to the verified Auth user." }
    - { name: members_ensure,      description: "Create or return the active member linked to the verified portal visitor." }
    - { name: members_update,      description: "Update a member's display_name, bio, status, or contact_id." }
    - { name: spaces_create,       description: "Create a space (feed|forum|chat|course) in a community." }
    - { name: spaces_list,         description: "List spaces in a community." }
    - { name: spaces_update,       description: "Update a space's name, visibility, or sort_order." }
    - { name: spaces_archive,      description: "Soft-delete a space." }
    - { name: spaces_add_member,   description: "Add a member to a space." }
    - { name: threads_create,      description: "Open a new thread in a space." }
    - { name: threads_list,        description: "List threads in a space." }
    - { name: threads_pin,         description: "Pin or unpin a thread." }
    - { name: threads_lock,        description: "Lock or unlock a thread." }
    - { name: posts_create,        description: "Post in a thread. Reply by passing reply_to_id." }
    - { name: posts_list,          description: "List posts in a thread, oldest first." }
    - { name: posts_edit,          description: "Edit a post's body. Author only." }
    - { name: posts_react,         description: "Add a reaction to a post. Toggle off by re-sending the same emoji." }
    - { name: posts_remove,        description: "Soft-delete a post." }
    - { name: dms_open,            description: "Open (or fetch) a DM thread between two or more members." }
    - { name: dms_send,            description: "Send a message in a DM thread." }
    - { name: dms_list_threads,    description: "List DM threads a member participates in, with unread counts." }
    - { name: dms_get_thread,      description: "Fetch a DM thread with its messages." }
    - { name: dms_mark_read,       description: "Mark a member's read cursor in a DM thread up to now." }
    - { name: dms_unread_count,    description: "Total unread DM messages for a member across all threads." }
    - { name: courses_create,      description: "Create a course (sugar for spaces_create with kind=course)." }
    - { name: courses_get_details, description: "Fetch course metadata, enrollment rules, and certificate settings." }
    - { name: courses_update_details, description: "Update course description, cover image, summary, instructor, level, tags, pricing, prerequisites, and outcomes." }
    - { name: sections_create,     description: "Create a section inside a course." }
    - { name: sections_list,       description: "List sections of a course." }
    - { name: sections_update,     description: "Update a course section." }
    - { name: sections_delete,     description: "Delete a course section and its lessons." }
    - { name: sections_reorder,    description: "Reorder sections within a course." }
    - { name: lessons_create,      description: "Create a lesson inside a section." }
    - { name: lessons_update,      description: "Update a lesson's title, body, or public preview state." }
    - { name: lessons_delete,      description: "Delete a lesson." }
    - { name: lessons_publish,     description: "Set or clear a lesson's published_at timestamp." }
    - { name: lessons_reorder,     description: "Reorder lessons within a section." }
    - { name: lessons_list,        description: "List lessons in a course." }
    - { name: lessons_get,         description: "Fetch one lesson with full body + caller progress." }
    - { name: lessons_attach_video, description: "Attach a storage file as the lesson's video." }
    - { name: instructor_profiles_create, description: "Create a reusable instructor profile in a community." }
    - { name: instructor_profiles_update, description: "Update an instructor profile." }
    - { name: instructor_profiles_get, description: "Fetch an instructor profile with calculated teaching statistics." }
    - { name: instructor_profiles_list, description: "List instructor profiles in a community." }
    - { name: instructor_profiles_archive, description: "Archive an instructor profile, optionally removing course assignments." }
    - { name: course_instructors_set, description: "Set a course's ordered instructor profiles and primary instructor." }
    - { name: course_instructors_get, description: "Get a course's instructor profiles and calculated statistics." }
    - { name: lesson_resources_add, description: "Attach a storage-backed resource to a lesson." }
    - { name: lesson_resources_list, description: "List storage-backed resources for a lesson." }
    - { name: lesson_bundle_get,  description: "Fetch an available lesson with all member-facing extras." }
    - { name: lesson_resources_delete, description: "Unlink a lesson resource." }
    - { name: quizzes_create,      description: "Create a lesson quiz." }
    - { name: quizzes_update,      description: "Update a lesson quiz." }
    - { name: quizzes_list,        description: "List quizzes for a lesson." }
    - { name: quizzes_delete,      description: "Delete a quiz." }
    - { name: assignments_create,  description: "Create a lesson assignment." }
    - { name: assignments_update,  description: "Update a lesson assignment." }
    - { name: assignments_list,    description: "List assignments for a lesson." }
    - { name: assignments_delete,  description: "Delete an assignment." }
    - { name: certificates_get,    description: "Fetch course certificate settings." }
    - { name: certificates_configure, description: "Configure course certificates backed by optional storage templates." }
    - { name: drip_schedule_set,   description: "Set a lesson drip schedule." }
    - { name: drip_schedule_list,  description: "List course drip schedules." }
    - { name: enrollment_rules_get, description: "Fetch course enrollment rules." }
    - { name: enrollment_rules_set, description: "Set course enrollment rules." }
    - { name: course_enroll,       description: "Enroll a member in a course." }
    - { name: course_enrollments_list, description: "List course enrollments." }
    - { name: course_enrollment_update, description: "Approve, reject, cancel, activate, or complete an enrollment." }
    - { name: lessons_mark_complete, description: "Mark a lesson complete (or in_progress) for a member." }
    - { name: lessons_progress,    description: "Get one member's progress across a course." }
    - { name: course_progress,     description: "Funnel across all members per lesson." }
    - { name: course_analytics,    description: "Course builder analytics summary." }
    - { name: lesson_comments_post, description: "Post a comment on a lesson." }
    - { name: lesson_comments_list, description: "List comments on a lesson, oldest first." }
    - { name: course_offer_get, description: "Get the active Catalog-backed offer for a course." }
    - { name: course_offer_upsert, description: "Bind a course to an active one-time Catalog price." }
    - { name: course_offer_archive, description: "Stop new sales for a course." }
    - { name: course_purchase_start, description: "Start or resume the verified member's Billing checkout." }
    - { name: course_purchase_status, description: "Get and reconcile the verified member's course purchase." }
    - { name: course_purchase_cancel, description: "Cancel an unpaid course purchase." }
    - { name: course_purchases_list, description: "List course purchases for operators." }
    - { name: course_purchase_get, description: "Get a course purchase and reconciliation history." }
    - { name: course_purchase_reconcile, description: "Reconcile a course purchase with Billing." }
    - { name: course_purchase_refund, description: "Request a Billing refund for a course purchase." }
    - { name: membership_plans_list, description: "List recurring course membership plans." }
    - { name: membership_plans_get, description: "Get a recurring course membership plan." }
    - { name: membership_plans_upsert, description: "Create or update a recurring course membership plan." }
    - { name: membership_plans_archive, description: "Archive a recurring course membership plan." }
    - { name: membership_plan_courses_set, description: "Set the courses included in a selected-courses plan." }
    - { name: membership_plan_tags_set, description: "Set the tags included in a course-tags plan." }
    - { name: membership_checkout_start, description: "Start or resume recurring membership checkout." }
    - { name: membership_status, description: "Get the verified member's recurring membership status." }
    - { name: membership_cancel, description: "Cancel a recurring membership." }
    - { name: membership_resume, description: "Resume a scheduled recurring membership cancellation." }
    - { name: membership_subscriptions_list, description: "List recurring Community memberships." }
    - { name: membership_subscription_get, description: "Get a recurring Community membership." }
    - { name: membership_subscription_reconcile, description: "Reconcile a Community membership with Subscriptions." }
    - { name: course_access_explain, description: "Explain a member's effective course access source." }
    - { name: storefront_checkout_start, description: "Start verified checkout for a public Catalog price offered by this community." }
    - { name: storefront_checkout_claim, description: "Claim a prepared Checkout session for the verified member before payment confirmation." }
    - { name: community_domain_options, description: "List available Domains-managed apex domains and the suggested DNS target." }
    - { name: community_domain_attach, description: "Attach one custom hostname using Domains DNS plus native ingress and TLS." }
    - { name: community_domain_status, description: "Return DNS, native ingress, and certificate status for a community hostname." }
    - { name: community_domain_detach, description: "Detach a hostname and remove only the exact DNS record Community created." }
  ui_panels:
    - slot: project.page
      label: Community
      icon: users
      entry: /ui/CommunityPanel.mjs
  # Client-facing portal SPA:
  # /api/apps/community/_install/{install_id}/ui/portal/dist/index.html
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: community/v0.11.3
    entry: mcp/community
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/community.db
  migrations: migrations/
config_schema:
  - name: default_community_slug
    type: text
    default: "main"
    label: Default community slug
    description: "Slug seeded on first boot so single-community installs don't have to pick one. Empty disables auto-seed."
  - name: default_visibility
    type: text
    default: "members"
    label: Default space visibility
    description: "members | public — applied to newly created spaces when not specified."
  - name: lesson_storage_folder
    type: text
    default: ".community/lessons"
    label: Recommended storage folder for lesson videos
    description: "Surfaced in lessons_attach_video's docstring so callers organise uploads consistently."
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("community requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("community mounted")
	go reconcileCommunityDomains(ctx)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "membership-recovery",
		Schedule: "@every 5m",
		Run:      recoverMembershipOperations,
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "invoice.paid", Handler: a.handleBillingEvent},
		{Event: "invoice.refunded", Handler: a.handleBillingEvent},
		{Event: "invoice.voided", Handler: a.handleBillingEvent},
		{Event: "invoice.payment_failed", Handler: a.handleBillingEvent},
		{Event: "invoice.payment_action_required", Handler: a.handleBillingEvent},
		{Event: "subscription.active", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.trialing", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.past_due", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.paused", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.cancelled", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.resumed", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.ended", Handler: a.handleMembershipSubscriptionEvent},
		{Event: "subscription.cycle_due", Handler: a.handleMembershipSubscriptionEvent},
	}
}

func (a *App) handleBillingEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	if err := a.handleCourseBillingEvent(ctx, event); err != nil {
		return err
	}
	return a.handleMembershipBillingEvent(ctx, event)
}

// ─── HTTP routes ─────────────────────────────────────────────────
// Mirror the MCP tools — the panel hits these for reads, the bus
// for writes. Writes still go through MCP for the auth/permission
// gate; HTTP read-only is the fastest path for panel hydrate.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/portal/bootstrap", Handler: a.httpPortalBootstrap, NoAuth: true},
		{Method: "GET", Pattern: "/portal/products", Handler: a.httpPortalProducts, NoAuth: true},
		{Method: "GET", Pattern: "/portal/products/", Handler: a.httpPortalProduct, NoAuth: true},
		{Method: "GET", Pattern: "/portal/previews/", Handler: a.httpPortalLessonPreview, NoAuth: true},
		{Method: "POST", Pattern: "/portal/checkout/prepare", Handler: a.httpPortalCheckoutPrepare, NoAuth: true},
		{Method: "GET", Pattern: "/store/", Handler: a.httpStorefrontRoute, NoAuth: true},
		{Pattern: "/api/", Handler: a.httpPortalGatewayBridge},
		{Pattern: "/communities", Handler: operatorHTTP(a.httpCommunities)},
		{Pattern: "/members", Handler: operatorHTTP(a.httpMembers)},
		{Pattern: "/spaces", Handler: operatorHTTP(a.httpSpaces)},
		{Pattern: "/threads", Handler: operatorHTTP(a.httpThreads)},
		{Pattern: "/posts", Handler: operatorHTTP(a.httpPosts)},
		{Pattern: "/dms", Handler: operatorHTTP(a.httpDMs)},
		{Pattern: "/sections", Handler: operatorHTTP(a.httpSections)},
		{Pattern: "/lessons", Handler: operatorHTTP(a.httpLessons)},
		{Pattern: "/lesson", Handler: operatorHTTP(a.httpLesson)},
		{Pattern: "/", Handler: a.httpPortalHostPage},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{}
	tools = append(tools, communitiesTools()...)
	tools = append(tools, membersTools()...)
	tools = append(tools, spacesTools()...)
	tools = append(tools, threadsTools()...)
	tools = append(tools, postsTools()...)
	tools = append(tools, dmsTools()...)
	tools = append(tools, coursesTools()...)
	tools = append(tools, instructorTools()...)
	tools = append(tools, courseSalesTools()...)
	tools = append(tools, membershipTools()...)
	tools = append(tools, storefrontTools()...)
	tools = append(tools, communityDomainTools()...)
	return secureTools(tools)
}

// ─── helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		writeErr(w, http.StatusInternalServerError, "response encoding failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeDomainErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not found"):
		writeErr(w, http.StatusNotFound, msg)
	case strings.Contains(lower, "required"), strings.Contains(lower, "invalid"),
		strings.Contains(lower, "must "), strings.Contains(lower, "cannot be"):
		writeErr(w, http.StatusBadRequest, msg)
	case strings.Contains(lower, "archived"), strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "only the"), strings.Contains(lower, "not a participant"),
		strings.Contains(lower, "active course enrollment"):
		writeErr(w, http.StatusForbidden, msg)
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

// intArg coerces a numeric arg from MCP (float64 over the wire) or
// from a Go test (int / int64). Returns (val, true) on success, the
// zero default and false otherwise.
func intArg(args map[string]any, key string) (int64, bool) {
	switch v := args[key].(type) {
	case float64:
		if math.Trunc(v) != v {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

func mustStr(args map[string]any, key string) (string, error) {
	v, _ := args[key].(string)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// newID mints a TEXT id with the given short prefix. ~80 bits of entropy
// is enough for per-install collision avoidance and stays grep-able.
func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("community: crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func boundedLimit(args map[string]any, key string, def, max int64) int {
	if v, ok := intArg(args, key); ok && v > 0 {
		if v > max {
			return int(max)
		}
		return int(v)
	}
	return int(def)
}

// scopeProject returns the project context for cross-cutting queries.
// Project-scoped installs always have a value; global installs may have
// "" when the dispatch isn't bound to one project — the caller decides
// whether that's a hard error.
func scopeProject(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	return ctx.CurrentProject()
}

// emit publishes a domain event. Pulled into a helper so future plumbing
// (rate limiting, batch coalescing) lives in one place.
func emit(ctx *sdk.AppCtx, topic string, payload map[string]any) {
	if ctx == nil {
		return
	}
	ctx.Emit(topic, payload)
}

func dbHandle() *sql.DB {
	if globalCtx == nil {
		return nil
	}
	return globalCtx.AppDB()
}

// ensureCommunityVisible returns nil when the community exists and isn't
// archived. Used by every cross-table tool to fail fast with a clean
// error instead of a downstream FK violation.
func ensureCommunityVisible(ctx *sdk.AppCtx, db *sql.DB, communityID string) error {
	var projectID string
	var arch sql.NullString
	err := db.QueryRow(
		`SELECT project_id, archived_at FROM communities WHERE id = ?`, communityID,
	).Scan(&projectID, &arch)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("community %q not found", communityID)
	}
	if err != nil {
		return err
	}
	if err := ensureProjectScope(ctx, communityID, projectID); err != nil {
		return err
	}
	if arch.Valid {
		return fmt.Errorf("community %q is archived", communityID)
	}
	return nil
}

func ensureProjectScope(ctx *sdk.AppCtx, communityID, rowProjectID string) error {
	projectID := scopeProject(ctx)
	if projectID != "" && rowProjectID != projectID {
		return fmt.Errorf("community %q not found", communityID)
	}
	return nil
}

func ensureCommunityReadable(ctx *sdk.AppCtx, c Community) error {
	return ensureProjectScope(ctx, c.ID, c.ProjectID)
}

func ensureSpaceVisible(ctx *sdk.AppCtx, db *sql.DB, spaceID string) (Space, error) {
	s, err := loadSpace(db, spaceID)
	if err != nil {
		return s, err
	}
	if err := ensureCommunityVisible(ctx, db, s.CommunityID); err != nil {
		return s, err
	}
	if s.ArchivedAt != nil {
		return s, fmt.Errorf("space %q is archived", spaceID)
	}
	return s, nil
}

func ensureThreadWritable(ctx *sdk.AppCtx, db *sql.DB, threadID string) (Thread, Space, error) {
	t, err := loadThread(db, threadID)
	if err != nil {
		return t, Space{}, err
	}
	s, err := ensureSpaceVisible(ctx, db, t.SpaceID)
	if err != nil {
		return t, s, err
	}
	if t.Locked {
		return t, s, errors.New("thread is locked")
	}
	return t, s, nil
}

func ensureThreadInVisibleSpace(ctx *sdk.AppCtx, db *sql.DB, threadID string) (Thread, Space, error) {
	t, err := loadThread(db, threadID)
	if err != nil {
		return t, Space{}, err
	}
	s, err := ensureSpaceVisible(ctx, db, t.SpaceID)
	if err != nil {
		return t, s, err
	}
	return t, s, nil
}

// ─── main ────────────────────────────────────────────────────────

func main() {
	sdk.Run(&App{})
}
