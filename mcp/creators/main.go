// Creators app — Patreon-like memberships, gated posts, and file drops.
//
// Creators owns creator-domain state: tiers, members, memberships,
// posts, entitlements, and portal tokens. Storage owns bytes, Billing
// owns customers/invoices/payments, CRM owns broader contact history,
// and Messaging owns delivery.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestFS embed.FS

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	raw, err := manifestFS.ReadFile("apteva.yaml")
	if err != nil {
		panic("missing embedded manifest: " + err.Error())
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("creators requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("creators mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "creator-lifecycle",
		Schedule: "@every 1m",
		Run: func(context.Context, *sdk.AppCtx) error {
			return runCreatorLifecycle(globalCtx)
		},
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "invoice.paid", Handler: a.handleInvoicePaid},
		{Event: "invoice.refunded", Handler: a.handleInvoiceInvalidated},
		{Event: "invoice.voided", Handler: a.handleInvoiceInvalidated},
	}
}

// HTTPRoutes exposes the dashboard/admin API plus public/member portal
// reads. Public routes still go through apteva-server's app proxy; NoAuth
// only skips the SDK's sidecar token gate for direct/public delivery.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/spaces", Handler: a.handleSpaces},
		{Pattern: "/space", Handler: a.handleSpace},
		{Pattern: "/tiers", Handler: a.handleTiers},
		{Pattern: "/tiers/", Handler: a.handleTierItem},
		{Pattern: "/members", Handler: a.handleMembers},
		{Pattern: "/members/", Handler: a.handleMemberItem},
		{Pattern: "/posts", Handler: a.handlePosts},
		{Pattern: "/posts/", Handler: a.handlePostItem},
		{Pattern: "/collections", Handler: a.handleCollections},
		{Pattern: "/collections/", Handler: a.handleCollectionItem},
		{Pattern: "/attachments", Handler: a.handleAttachments},
		{Pattern: "/attachments/", Handler: a.handleAttachmentItem},
		{Pattern: "/activity", Handler: a.handleEvents},
		{Pattern: "/metrics", Handler: a.handleMetrics},
		{Pattern: "/public/", Handler: a.handlePublic, NoAuth: true},
		{Pattern: "/member/", Handler: a.handleMemberPortal, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "creators_space_create", Description: "Create a creator space in this project. Args: name, slug?, description?, default_currency?, metadata?.", InputSchema: schemaObject(map[string]any{"name": sString(), "slug": sString(), "description": sString(), "default_currency": sString(), "metadata": sObject()}, []string{"name"}), Handler: a.toolSpaceCreate},
		{Name: "creators_space_list", Description: "List creator spaces for this project.", InputSchema: schemaObject(nil, nil), Handler: a.toolSpaceList},
		{Name: "creators_space_get", Description: "Fetch creator space settings. Args: space_id? or space_slug?; defaults to the project's first space.", InputSchema: schemaObject(map[string]any{"space_id": sInteger(), "space_slug": sString()}, nil), Handler: a.toolSpaceGet},
		{Name: "creators_space_update", Description: "Update creator space branding, slug, and defaults. Args: space_id? or space_slug?, name?, slug?, description?, default_currency?, metadata?.", InputSchema: schemaObject(map[string]any{"space_id": sInteger(), "space_slug": sString(), "name": sString(), "slug": sString(), "description": sString(), "default_currency": sString(), "metadata": sObject()}, nil), Handler: a.toolSpaceUpdate},
		{Name: "creators_tier_create", Description: "Create a membership tier. Args: space_id? or space_slug?, name, price_cents?, currency?, interval? (month|year|one_time), description?, benefits? [string], sort_order?.", InputSchema: spaceSchema(map[string]any{"name": sString(), "price_cents": sInteger(), "currency": sString(), "interval": sString(), "description": sString(), "benefits": sArray("string"), "sort_order": sInteger()}, []string{"name"}), Handler: a.toolTierCreate},
		{Name: "creators_tier_list", Description: "List tiers in a creator space. Args: space_id? or space_slug?, archived? (default false).", InputSchema: spaceSchema(map[string]any{"archived": sBool()}, nil), Handler: a.toolTierList},
		{Name: "creators_tier_update", Description: "Update a tier. Args: space_id? or space_slug?, id, patch {name, slug, description, price_cents, currency, interval, benefits, sort_order, archived}.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "patch": sObject()}, []string{"id", "patch"}), Handler: a.toolTierUpdate},
		{Name: "creators_member_upsert", Description: "Find or create a member by email. Args: space_id? or space_slug?, email, display_name?, tier_id?, status?, sync_crm?, sync_billing?.", InputSchema: spaceSchema(map[string]any{"email": sString(), "display_name": sString(), "tier_id": sInteger(), "status": sString(), "sync_crm": sBool(), "sync_billing": sBool()}, []string{"email"}), Handler: a.toolMemberUpsert},
		{Name: "creators_member_list", Description: "List members. Args: space_id? or space_slug?, status?, tier_id?, q?, limit?.", InputSchema: spaceSchema(map[string]any{"status": sString(), "tier_id": sInteger(), "q": sString(), "limit": sInteger()}, nil), Handler: a.toolMemberList},
		{Name: "creators_member_set_tier", Description: "Assign a member to a tier. Args: space_id? or space_slug?, member_id, tier_id (0 clears), status?.", InputSchema: spaceSchema(map[string]any{"member_id": sInteger(), "tier_id": sInteger(), "status": sString()}, []string{"member_id"}), Handler: a.toolMemberSetTier},
		{Name: "creators_member_set_status", Description: "Set member status. Args: space_id? or space_slug?, member_id, status, current_period_end?.", InputSchema: spaceSchema(map[string]any{"member_id": sInteger(), "status": sString(), "current_period_end": sString()}, []string{"member_id", "status"}), Handler: a.toolMemberSetStatus},
		{Name: "creators_member_rotate_portal_token", Description: "Revoke the current member portal token and issue a new 90-day token. Args: space_id? or space_slug?, member_id.", InputSchema: spaceSchema(map[string]any{"member_id": sInteger()}, []string{"member_id"}), Handler: a.toolMemberRotatePortalToken},
		{Name: "creators_post_create", Description: "Create a creator post. Args: space_id? or space_slug?, title, body?, slug?, visibility?, tier_ids?, collection_ids?, status?, scheduled_at?.", InputSchema: spaceSchema(map[string]any{"title": sString(), "body": sString(), "slug": sString(), "visibility": sString(), "tier_ids": sArray("integer"), "collection_ids": sArray("integer"), "status": sString(), "scheduled_at": sString()}, []string{"title"}), Handler: a.toolPostCreate},
		{Name: "creators_post_update", Description: "Update a post. Args: space_id? or space_slug?, id, patch.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "patch": sObject()}, []string{"id", "patch"}), Handler: a.toolPostUpdate},
		{Name: "creators_post_publish", Description: "Publish a post now or schedule it. Args: space_id? or space_slug?, id, scheduled_at?.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "scheduled_at": sString()}, []string{"id"}), Handler: a.toolPostPublish},
		{Name: "creators_post_list", Description: "List posts. Args: space_id? or space_slug?, status?, visibility?, q?, limit?.", InputSchema: spaceSchema(map[string]any{"status": sString(), "visibility": sString(), "q": sString(), "limit": sInteger()}, nil), Handler: a.toolPostList},
		{Name: "creators_post_get", Description: "Fetch one post with attachments. Args: space_id? or space_slug?, id OR slug.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "slug": sString()}, nil), Handler: a.toolPostGet},
		{Name: "creators_collection_create", Description: "Create an ordered collection of posts. Args: space_id? or space_slug?, title, slug?, description?, status?, cover_storage_file_id?, metadata?, sort_order?.", InputSchema: spaceSchema(map[string]any{"title": sString(), "slug": sString(), "description": sString(), "status": sString(), "cover_storage_file_id": sInteger(), "metadata": sObject(), "sort_order": sInteger()}, []string{"title"}), Handler: a.toolCollectionCreate},
		{Name: "creators_collection_list", Description: "List collections in a creator space. Args: space_id? or space_slug?, status?.", InputSchema: spaceSchema(map[string]any{"status": sString()}, nil), Handler: a.toolCollectionList},
		{Name: "creators_collection_get", Description: "Fetch one collection and its ordered posts. Args: space_id? or space_slug?, id OR slug.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "slug": sString()}, nil), Handler: a.toolCollectionGet},
		{Name: "creators_collection_update", Description: "Update collection details. Args: space_id? or space_slug?, id, patch.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "patch": sObject()}, []string{"id", "patch"}), Handler: a.toolCollectionUpdate},
		{Name: "creators_collection_set_posts", Description: "Replace a collection's ordered posts. Args: space_id? or space_slug?, id, post_ids in display order.", InputSchema: spaceSchema(map[string]any{"id": sInteger(), "post_ids": sArray("integer")}, []string{"id", "post_ids"}), Handler: a.toolCollectionSetPosts},
		{Name: "creators_collection_archive", Description: "Archive a collection without deleting its posts. Args: space_id? or space_slug?, id.", InputSchema: spaceSchema(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolCollectionArchive},
		{Name: "creators_attachment_upload", Description: "Upload bytes to storage and attach them to a post. Args: space_id? or space_slug?, post_id, filename, content_base64, content_type?, visibility?, tier_ids?.", InputSchema: spaceSchema(map[string]any{"post_id": sInteger(), "filename": sString(), "content_base64": sString(), "content_type": sString(), "visibility": sString(), "tier_ids": sArray("integer")}, []string{"post_id", "filename", "content_base64"}), Handler: a.toolAttachmentUpload},
		{Name: "creators_attachment_add_from_storage", Description: "Attach an existing storage file to a creator post. Args: space_id? or space_slug?, post_id, storage_file_id, filename?, content_type?, size_bytes?, visibility?, tier_ids?.", InputSchema: spaceSchema(map[string]any{"post_id": sInteger(), "storage_file_id": sInteger(), "filename": sString(), "content_type": sString(), "size_bytes": sInteger(), "visibility": sString(), "tier_ids": sArray("integer")}, []string{"post_id", "storage_file_id"}), Handler: a.toolAttachmentAddFromStorage},
		{Name: "creators_file_get_download_link", Description: "Mint a storage signed URL after checking access. Args: space_id? or space_slug?, attachment_id, member_id OR portal_token, ttl_seconds?.", InputSchema: spaceSchema(map[string]any{"attachment_id": sInteger(), "member_id": sInteger(), "portal_token": sString(), "ttl_seconds": sInteger()}, []string{"attachment_id"}), Handler: a.toolFileGetDownloadLink},
		{Name: "creators_payment_link_create", Description: "Create or resume an idempotent billing invoice for a membership period. Args: space_id? or space_slug?, member_id, tier_id?, periods?, idempotency_key?, success_url?, cancel_url?.", InputSchema: spaceSchema(map[string]any{"member_id": sInteger(), "tier_id": sInteger(), "periods": sInteger(), "idempotency_key": sString(), "success_url": sString(), "cancel_url": sString()}, []string{"member_id"}), Handler: a.toolPaymentLinkCreate},
		{Name: "creators_send_post_update", Description: "Send a post announcement to eligible members through messaging. Args: space_id? or space_slug?, post_id, subject?, intro?, dry_run?.", InputSchema: spaceSchema(map[string]any{"post_id": sInteger(), "subject": sString(), "intro": sString(), "dry_run": sBool()}, []string{"post_id"}), Handler: a.toolSendPostUpdate},
		{Name: "creators_events_list", Description: "List recent creator activity. Args: space_id? or space_slug?, limit?.", InputSchema: spaceSchema(map[string]any{"limit": sInteger()}, nil), Handler: a.toolEventsList},
	}
}

// ─── Domain types ─────────────────────────────────────────────────

type Space struct {
	ID              int64           `json:"id"`
	ProjectID       string          `json:"project_id"`
	Name            string          `json:"name"`
	Slug            string          `json:"slug"`
	Description     string          `json:"description"`
	AvatarFileID    *int64          `json:"avatar_file_id,omitempty"`
	BannerFileID    *int64          `json:"banner_file_id,omitempty"`
	DefaultCurrency string          `json:"default_currency"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

type Tier struct {
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id"`
	SpaceID     int64           `json:"space_id"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	PriceCents  int64           `json:"price_cents"`
	Currency    string          `json:"currency"`
	Interval    string          `json:"interval"`
	Benefits    json.RawMessage `json:"benefits"`
	SortOrder   int             `json:"sort_order"`
	ArchivedAt  string          `json:"archived_at,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type Member struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SpaceID            int64           `json:"space_id"`
	Email              string          `json:"email"`
	DisplayName        string          `json:"display_name"`
	Status             string          `json:"status"`
	TierID             *int64          `json:"tier_id,omitempty"`
	CRMContactID       *int64          `json:"crm_contact_id,omitempty"`
	BillingCustomerID  *int64          `json:"billing_customer_id,omitempty"`
	PortalToken        string          `json:"-"`
	CurrentPeriodStart string          `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   string          `json:"current_period_end,omitempty"`
	PortalTokenExpires string          `json:"portal_token_expires_at,omitempty"`
	PortalTokenRevoked string          `json:"portal_token_revoked_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

type MembershipPayment struct {
	ID               int64
	ProjectID        string
	SpaceID          int64
	MemberID         int64
	TierID           int64
	BillingInvoiceID sql.NullInt64
	IdempotencyKey   string
	Status           string
	PeriodCount      int
	AmountCents      int64
	Currency         string
	PeriodStart      string
	PeriodEnd        string
	PaidAt           string
	UpdatedAt        string
}

type Post struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	SpaceID       int64           `json:"space_id"`
	Title         string          `json:"title"`
	Slug          string          `json:"slug"`
	Body          string          `json:"body"`
	Status        string          `json:"status"`
	Visibility    string          `json:"visibility"`
	TierIDs       json.RawMessage `json:"tier_ids"`
	PublishedAt   string          `json:"published_at,omitempty"`
	ScheduledAt   string          `json:"scheduled_at,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	Attachments   []Attachment    `json:"attachments,omitempty"`
	CollectionIDs []int64         `json:"collection_ids,omitempty"`
}

type Attachment struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	SpaceID       int64           `json:"space_id"`
	PostID        int64           `json:"post_id"`
	StorageFileID int64           `json:"storage_file_id"`
	Filename      string          `json:"filename"`
	ContentType   string          `json:"content_type"`
	SizeBytes     int64           `json:"size_bytes"`
	Visibility    string          `json:"visibility"`
	TierIDs       json.RawMessage `json:"tier_ids"`
	CreatedAt     string          `json:"created_at"`
}

type storageFileMetadata struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type storageFileLookupResult struct {
	ID          int64                `json:"id"`
	Name        string               `json:"name"`
	ContentType string               `json:"content_type"`
	SizeBytes   int64                `json:"size_bytes"`
	File        *storageFileMetadata `json:"file"`
	Found       bool                 `json:"found"`
}

type storageUploadResult struct {
	FileID int64                `json:"file_id"`
	ID     int64                `json:"id"`
	URL    string               `json:"url"`
	File   *storageFileMetadata `json:"file"`
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (a *App) handleSpaces(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		spaces, err := listSpaces(ctx.AppDB(), pid)
		writeOrErr(w, map[string]any{"spaces": spaces}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		space, err := createSpace(ctx, pid, args)
		writeOrErr(w, map[string]any{"space": space}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleSpace(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		space, err := spaceFromRequest(ctx, pid, r)
		writeOrErr(w, map[string]any{"space": space}, err)
	case http.MethodPut, http.MethodPatch:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if sid := parseInt64(r.URL.Query().Get("space_id")); sid > 0 {
			patch["space_id"] = sid
		}
		if slug := strings.TrimSpace(r.URL.Query().Get("space_slug")); slug != "" {
			patch["space_slug"] = slug
		}
		space, err := updateSpace(ctx, pid, patch)
		writeOrErr(w, map[string]any{"space": space}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleTiers(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		tiers, err := listTiers(ctx.AppDB(), pid, space.ID, r.URL.Query().Get("archived") == "1")
		writeOrErr(w, tiers, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		tier, err := createTier(ctx, pid, space.ID, args)
		writeOrErr(w, map[string]any{"tier": tier}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleTierItem(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	id, err := idFromPath(r.URL.Path, "/tiers/")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		tier, err := getTier(ctx.AppDB(), pid, space.ID, id)
		writeOrErr(w, map[string]any{"tier": tier, "found": tier != nil}, err)
	case http.MethodPut, http.MethodPatch:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		tier, err := updateTier(ctx, pid, space.ID, id, patch)
		writeOrErr(w, map[string]any{"tier": tier}, err)
	case http.MethodDelete:
		tier, err := updateTier(ctx, pid, space.ID, id, map[string]any{"archived": true})
		writeOrErr(w, map[string]any{"tier": tier}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleMembers(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		members, err := listMembers(ctx.AppDB(), pid, space.ID, memberFilters{
			status: r.URL.Query().Get("status"),
			q:      r.URL.Query().Get("q"),
			tierID: parseInt64(r.URL.Query().Get("tier_id")),
			limit:  parseInt(r.URL.Query().Get("limit"), 100),
		})
		writeOrErr(w, redactMembers(members), err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		member, created, extras, err := upsertMember(ctx, pid, space.ID, args)
		out := map[string]any{"member": redactMember(member), "was_created": created}
		if created && member != nil {
			out["portal_token"] = member.PortalToken
			out["portal_token_expires_at"] = member.PortalTokenExpires
		}
		for k, v := range extras {
			out[k] = v
		}
		writeOrErr(w, out, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleMemberItem(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/members/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "member id required")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		member, err := getMember(ctx.AppDB(), pid, space.ID, id)
		writeOrErr(w, map[string]any{"member": redactMember(member), "found": member != nil}, err)
	case len(parts) == 1 && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		member, err := updateMember(ctx, pid, space.ID, id, patch)
		writeOrErr(w, map[string]any{"member": redactMember(member)}, err)
	case len(parts) == 2 && parts[1] == "portal-token" && r.Method == http.MethodPost:
		member, token, err := rotatePortalToken(ctx, pid, space.ID, id)
		writeOrErr(w, map[string]any{"member": redactMember(member), "portal_token": token, "portal_token_expires_at": memberExpiry(member)}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handlePosts(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		posts, err := listPosts(ctx.AppDB(), pid, space.ID, postFilters{
			status:     r.URL.Query().Get("status"),
			visibility: r.URL.Query().Get("visibility"),
			q:          r.URL.Query().Get("q"),
			limit:      parseInt(r.URL.Query().Get("limit"), 100),
		})
		writeOrErr(w, posts, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		post, err := createPost(ctx, pid, space.ID, args)
		writeOrErr(w, map[string]any{"post": post}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handlePostItem(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/posts/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "post id required")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		post, err := getPost(ctx.AppDB(), pid, space.ID, id, true)
		writeOrErr(w, map[string]any{"post": post, "found": post != nil}, err)
	case len(parts) == 1 && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		post, err := updatePost(ctx, pid, space.ID, id, patch)
		writeOrErr(w, map[string]any{"post": post}, err)
	case len(parts) == 2 && parts[1] == "publish" && r.Method == http.MethodPost:
		var body map[string]any
		_ = readJSON(r, &body)
		post, err := publishPost(ctx, pid, space.ID, id, strArg(body, "scheduled_at"))
		writeOrErr(w, map[string]any{"post": post}, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleAttachments(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, 405, "method not allowed")
		return
	}
	var args map[string]any
	if err := readJSON(r, &args); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	att, err := addAttachment(ctx, pid, space.ID, args)
	writeOrErr(w, map[string]any{"attachment": att}, err)
}

func (a *App) handleAttachmentItem(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/attachments/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "attachment id required")
		return
	}
	if len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodPost {
		var args map[string]any
		_ = readJSON(r, &args)
		args["attachment_id"] = id
		link, err := getDownloadLink(ctx, pid, space.ID, args)
		writeOrErr(w, link, err)
		return
	}
	httpErr(w, 405, "method not allowed")
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	events, err := listEvents(ctx.AppDB(), pid, space.ID, parseInt(r.URL.Query().Get("limit"), 50))
	writeOrErr(w, events, err)
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	metrics, err := membershipMetrics(ctx.AppDB(), pid, space.ID)
	writeOrErr(w, metrics, err)
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpErr(w, http.StatusMethodNotAllowed, "GET or HEAD only")
		return
	}
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/public/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, 404, "space not found")
		return
	}
	space, err := getSpaceBySlug(ctx.AppDB(), pid, parts[0])
	if err != nil || space == nil {
		httpErr(w, 404, "space not found")
		return
	}
	if len(parts) == 4 && parts[1] == "files" && parts[3] == "download" {
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		link, err := getDownloadLink(ctx, space.ProjectID, space.ID, map[string]any{"attachment_id": id})
		writeOrErr(w, link, err)
		return
	}
	if len(parts) >= 2 && parts[1] == "collections" {
		a.handlePublicCollections(w, r, ctx, space, parts)
		return
	}
	if len(parts) >= 3 && parts[1] == "posts" {
		post, err := getPostBySlug(ctx.AppDB(), space.ProjectID, space.ID, parts[2], true)
		if err != nil || post == nil || post.Status != "published" || post.Visibility != "public" {
			httpErr(w, 404, "post not found")
			return
		}
		post.Attachments = accessibleAttachments(nil, post, post.Attachments)
		writeJSON(w, map[string]any{"space": space, "post": post})
		return
	}
	posts, err := listPosts(ctx.AppDB(), space.ProjectID, space.ID, postFilters{status: "published", visibility: "public", limit: 20})
	if err == nil {
		err = hydrateAccessibleAttachments(ctx.AppDB(), space.ProjectID, space.ID, posts, nil)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"space": space, "posts": posts})
}

func (a *App) handleMemberPortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpErr(w, http.StatusMethodNotAllowed, "GET or HEAD only")
		return
	}
	ctx := contextForRequest(r)
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/member/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, 404, "member not found")
		return
	}
	member, err := getMemberByToken(ctx.AppDB(), parts[0])
	if err != nil || member == nil {
		httpErr(w, 404, "member not found")
		return
	}
	if len(parts) == 4 && parts[1] == "files" && parts[3] == "download" {
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		link, err := getDownloadLink(ctx, member.ProjectID, member.SpaceID, map[string]any{
			"attachment_id": id,
			"portal_token":  parts[0],
		})
		writeOrErr(w, link, err)
		return
	}
	if len(parts) >= 2 && parts[1] == "collections" {
		a.handleMemberCollections(w, r, ctx, member, parts)
		return
	}
	posts, err := listPosts(ctx.AppDB(), member.ProjectID, member.SpaceID, postFilters{status: "published", limit: 50})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	visible := make([]Post, 0, len(posts))
	for _, p := range posts {
		if memberCanAccessPost(member, &p) {
			visible = append(visible, p)
		}
	}
	if err := hydrateAccessibleAttachments(ctx.AppDB(), member.ProjectID, member.SpaceID, visible, member); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"member": redactMember(member), "posts": visible})
}

// ─── Tool handlers ────────────────────────────────────────────────

func (a *App) toolSpaceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := createSpace(ctx, pid, args)
	return map[string]any{"space": space}, err
}

func (a *App) toolSpaceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	spaces, err := listSpaces(ctx.AppDB(), pid)
	return map[string]any{"spaces": spaces, "count": len(spaces)}, err
}

func (a *App) toolSpaceGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	return map[string]any{"space": space}, err
}

func (a *App) toolSpaceUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := updateSpace(ctx, pid, args)
	return map[string]any{"space": space}, err
}

func (a *App) toolTierCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	tier, err := createTier(ctx, pid, space.ID, args)
	return map[string]any{"tier": tier}, err
}

func (a *App) toolTierList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	tiers, err := listTiers(ctx.AppDB(), pid, space.ID, boolArg(args, "archived"))
	return map[string]any{"tiers": tiers, "count": len(tiers)}, err
}

func (a *App) toolTierUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	patch, _ := args["patch"].(map[string]any)
	tier, err := updateTier(ctx, pid, space.ID, int64Arg(args, "id"), patch)
	return map[string]any{"tier": tier}, err
}

func (a *App) toolMemberUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	member, created, extras, err := upsertMember(ctx, pid, space.ID, args)
	out := map[string]any{"member": redactMember(member), "was_created": created}
	if created && member != nil {
		out["portal_token"] = member.PortalToken
		out["portal_token_expires_at"] = member.PortalTokenExpires
	}
	for k, v := range extras {
		out[k] = v
	}
	return out, err
}

func (a *App) toolMemberList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	members, err := listMembers(ctx.AppDB(), pid, space.ID, memberFilters{
		status: strArg(args, "status"),
		q:      strArg(args, "q"),
		tierID: int64Arg(args, "tier_id"),
		limit:  intArg(args, "limit", 100),
	})
	return map[string]any{"members": redactMembers(members), "count": len(members)}, err
}

func (a *App) toolMemberSetTier(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	member, err := updateMember(ctx, pid, space.ID, int64Arg(args, "member_id"), map[string]any{
		"tier_id": int64Arg(args, "tier_id"),
		"status":  strArg(args, "status"),
	})
	return map[string]any{"member": redactMember(member)}, err
}

func (a *App) toolMemberSetStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	patch := map[string]any{"status": strArg(args, "status")}
	if _, ok := args["current_period_end"]; ok {
		patch["current_period_end"] = strArg(args, "current_period_end")
	}
	member, err := updateMember(ctx, pid, space.ID, int64Arg(args, "member_id"), patch)
	return map[string]any{"member": redactMember(member)}, err
}

func (a *App) toolMemberRotatePortalToken(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	member, token, err := rotatePortalToken(ctx, pid, space.ID, int64Arg(args, "member_id"))
	return map[string]any{"member": redactMember(member), "portal_token": token, "portal_token_expires_at": memberExpiry(member)}, err
}

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	post, err := createPost(ctx, pid, space.ID, args)
	return map[string]any{"post": post}, err
}

func (a *App) toolPostUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	patch, _ := args["patch"].(map[string]any)
	post, err := updatePost(ctx, pid, space.ID, int64Arg(args, "id"), patch)
	return map[string]any{"post": post}, err
}

func (a *App) toolPostPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	post, err := publishPost(ctx, pid, space.ID, int64Arg(args, "id"), strArg(args, "scheduled_at"))
	return map[string]any{"post": post}, err
}

func (a *App) toolPostList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	posts, err := listPosts(ctx.AppDB(), pid, space.ID, postFilters{
		status:     strArg(args, "status"),
		visibility: strArg(args, "visibility"),
		q:          strArg(args, "q"),
		limit:      intArg(args, "limit", 100),
	})
	return map[string]any{"posts": posts, "count": len(posts)}, err
}

func (a *App) toolPostGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	var post *Post
	if id := int64Arg(args, "id"); id > 0 {
		post, err = getPost(ctx.AppDB(), pid, space.ID, id, true)
	} else {
		post, err = getPostBySlug(ctx.AppDB(), pid, space.ID, strArg(args, "slug"), true)
	}
	return map[string]any{"post": post, "found": post != nil}, err
}

func (a *App) toolAttachmentUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	att, err := uploadAttachment(ctx, pid, space.ID, args)
	return map[string]any{"attachment": att}, err
}

func (a *App) toolAttachmentAddFromStorage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	att, err := addAttachment(ctx, pid, space.ID, args)
	return map[string]any{"attachment": att}, err
}

func (a *App) toolFileGetDownloadLink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return getDownloadLink(ctx, pid, space.ID, args)
}

func (a *App) toolPaymentLinkCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return createPaymentLink(ctx, pid, space.ID, args)
}

func (a *App) toolSendPostUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return sendPostUpdate(ctx, pid, space.ID, args)
}

func (a *App) toolEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	events, err := listEvents(ctx.AppDB(), pid, space.ID, intArg(args, "limit", 50))
	return map[string]any{"events": events, "count": len(events)}, err
}

// ─── DB operations ────────────────────────────────────────────────

func ensureSpace(ctx *sdk.AppCtx, pid string) (*Space, error) {
	if s, err := getDefaultSpace(ctx.AppDB(), pid); err != nil || s != nil {
		return s, err
	}
	currency := strings.ToUpper(configString(ctx, "default_currency", "USD"))
	if currency == "" {
		currency = "USD"
	}
	if !validCurrency(currency) {
		return nil, fmt.Errorf("invalid default currency %q", currency)
	}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO creator_spaces (project_id, name, slug, default_currency) VALUES (?, ?, ?, ?)`,
		pid, "Creator Space", "creator", currency,
	)
	if err != nil {
		return nil, err
	}
	return getDefaultSpace(ctx.AppDB(), pid)
}

func createSpace(ctx *sdk.AppCtx, pid string, args map[string]any) (*Space, error) {
	name := cleanString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	slug := slugify(name)
	if requested := strArg(args, "slug"); requested != "" {
		slug = slugify(requested)
	}
	currency := strings.ToUpper(strArg(args, "default_currency"))
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if currency == "" {
		currency = "USD"
	}
	if !validCurrency(currency) {
		return nil, fmt.Errorf("invalid default currency %q", currency)
	}
	meta := json.RawMessage("{}")
	if md, ok := args["metadata"].(map[string]any); ok {
		meta, _ = json.Marshal(md)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO creator_spaces (project_id, name, slug, description, default_currency, metadata) VALUES (?, ?, ?, ?, ?, ?)`,
		pid, name, slug, strArg(args, "description"), currency, string(meta),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	space, err := getSpace(ctx.AppDB(), pid, id)
	if err == nil {
		_ = logEvent(ctx, pid, id, "space.created", "agent", "space", id, map[string]any{"name": name})
	}
	return space, err
}

func listSpaces(db *sql.DB, pid string) ([]Space, error) {
	rows, err := db.Query(`SELECT id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at FROM creator_spaces WHERE project_id=? ORDER BY id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		s, err := scanSpace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func getDefaultSpace(db *sql.DB, pid string) (*Space, error) {
	row := db.QueryRow(`SELECT id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at FROM creator_spaces WHERE project_id=? ORDER BY id LIMIT 1`, pid)
	return scanSpace(row)
}

func getSpace(db *sql.DB, pid string, id int64) (*Space, error) {
	row := db.QueryRow(`SELECT id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at FROM creator_spaces WHERE project_id=? AND id=?`, pid, id)
	return scanSpace(row)
}

func getSpaceBySlug(db *sql.DB, pid, slug string) (*Space, error) {
	row := db.QueryRow(`SELECT id, project_id, name, slug, description, avatar_file_id, banner_file_id, default_currency, metadata, created_at, updated_at FROM creator_spaces WHERE project_id=? AND slug=? LIMIT 1`, pid, slug)
	return scanSpace(row)
}

func scanSpace(row interface{ Scan(...any) error }) (*Space, error) {
	var s Space
	var avatar, banner sql.NullInt64
	var meta string
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Slug, &s.Description, &avatar, &banner, &s.DefaultCurrency, &meta, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if avatar.Valid {
		s.AvatarFileID = &avatar.Int64
	}
	if banner.Valid {
		s.BannerFileID = &banner.Int64
	}
	s.Metadata = rawJSON(meta, "{}")
	return &s, nil
}

func updateSpace(ctx *sdk.AppCtx, pid string, patch map[string]any) (*Space, error) {
	space, err := spaceFromArgs(ctx, pid, patch)
	if err != nil {
		return nil, err
	}
	sets := []string{}
	args := []any{}
	if v := cleanString(patch["name"]); v != "" {
		sets, args = append(sets, "name=?"), append(args, v)
	}
	if v := cleanString(patch["slug"]); v != "" {
		sets, args = append(sets, "slug=?"), append(args, slugify(v))
	}
	if v, ok := patch["description"].(string); ok {
		sets, args = append(sets, "description=?"), append(args, v)
	}
	if v := strings.ToUpper(cleanString(patch["default_currency"])); v != "" {
		if !validCurrency(v) {
			return nil, fmt.Errorf("invalid default currency %q", v)
		}
		sets, args = append(sets, "default_currency=?"), append(args, v)
	}
	if md, ok := patch["metadata"].(map[string]any); ok {
		raw, _ := json.Marshal(md)
		sets, args = append(sets, "metadata=?"), append(args, string(raw))
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
		args = append(args, pid, space.ID)
		if _, err := ctx.AppDB().Exec(`UPDATE creator_spaces SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, args...); err != nil {
			return nil, err
		}
	}
	s, err := getSpace(ctx.AppDB(), pid, space.ID)
	if err == nil {
		_ = logEvent(ctx, pid, s.ID, "space.updated", "agent", "space", s.ID, patch)
	}
	return s, err
}

func createTier(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Tier, error) {
	name := cleanString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	currency := strings.ToUpper(strArg(args, "currency"))
	if currency == "" {
		space, _ := getSpace(ctx.AppDB(), pid, spaceID)
		if space != nil {
			currency = space.DefaultCurrency
		}
	}
	if currency == "" {
		currency = "USD"
	}
	if !validCurrency(currency) {
		return nil, fmt.Errorf("invalid currency %q", currency)
	}
	price := int64Arg(args, "price_cents")
	if price < 0 || price > 1_000_000_000_000 {
		return nil, errors.New("price_cents must be between 0 and 1000000000000")
	}
	interval := strArg(args, "interval")
	if interval == "" {
		interval = "month"
	}
	if !validInterval(interval) {
		return nil, fmt.Errorf("invalid interval %q", interval)
	}
	benefits := jsonArray(args["benefits"])
	slug := slugify(name)
	if requested := strArg(args, "slug"); requested != "" {
		slug = slugify(requested)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO tiers (project_id, space_id, name, slug, description, price_cents, currency, interval, benefits_json, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, spaceID, name, slug, strArg(args, "description"), price, currency, interval, string(benefits), intArg(args, "sort_order", 0),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	tier, err := getTier(ctx.AppDB(), pid, spaceID, id)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "tier.created", "agent", "tier", id, map[string]any{"name": name})
	}
	return tier, err
}

func listTiers(db *sql.DB, pid string, spaceID int64, archived bool) ([]Tier, error) {
	q := `SELECT id, project_id, space_id, name, slug, description, price_cents, currency, interval, benefits_json, sort_order, COALESCE(archived_at,''), created_at, updated_at FROM tiers WHERE project_id=? AND space_id=?`
	if !archived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY sort_order, price_cents, id`
	rows, err := db.Query(q, pid, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tier
	for rows.Next() {
		t, err := scanTier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func getTier(db *sql.DB, pid string, spaceID, id int64) (*Tier, error) {
	row := db.QueryRow(`SELECT id, project_id, space_id, name, slug, description, price_cents, currency, interval, benefits_json, sort_order, COALESCE(archived_at,''), created_at, updated_at FROM tiers WHERE project_id=? AND space_id=? AND id=?`, pid, spaceID, id)
	return scanTier(row)
}

func scanTier(row interface{ Scan(...any) error }) (*Tier, error) {
	var t Tier
	var benefits string
	if err := row.Scan(&t.ID, &t.ProjectID, &t.SpaceID, &t.Name, &t.Slug, &t.Description, &t.PriceCents, &t.Currency, &t.Interval, &benefits, &t.SortOrder, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t.Benefits = rawJSON(benefits, "[]")
	return &t, nil
}

func updateTier(ctx *sdk.AppCtx, pid string, spaceID, id int64, patch map[string]any) (*Tier, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	if patch == nil {
		return nil, errors.New("patch required")
	}
	sets := []string{}
	args := []any{}
	if v := cleanString(patch["name"]); v != "" {
		sets, args = append(sets, "name=?"), append(args, v)
	}
	if v := cleanString(patch["slug"]); v != "" {
		sets, args = append(sets, "slug=?"), append(args, slugify(v))
	}
	if v, ok := patch["description"].(string); ok {
		sets, args = append(sets, "description=?"), append(args, v)
	}
	if _, ok := patch["price_cents"]; ok {
		price := int64Arg(patch, "price_cents")
		if price < 0 || price > 1_000_000_000_000 {
			return nil, errors.New("price_cents must be between 0 and 1000000000000")
		}
		sets, args = append(sets, "price_cents=?"), append(args, price)
	}
	if v := strings.ToUpper(cleanString(patch["currency"])); v != "" {
		if !validCurrency(v) {
			return nil, fmt.Errorf("invalid currency %q", v)
		}
		sets, args = append(sets, "currency=?"), append(args, v)
	}
	if v := cleanString(patch["interval"]); v != "" {
		if !validInterval(v) {
			return nil, fmt.Errorf("invalid interval %q", v)
		}
		sets, args = append(sets, "interval=?"), append(args, v)
	}
	if _, ok := patch["benefits"]; ok {
		sets, args = append(sets, "benefits_json=?"), append(args, string(jsonArray(patch["benefits"])))
	}
	if _, ok := patch["sort_order"]; ok {
		sets, args = append(sets, "sort_order=?"), append(args, intArg(patch, "sort_order", 0))
	}
	if b, ok := patch["archived"].(bool); ok {
		if b {
			sets = append(sets, "archived_at=CURRENT_TIMESTAMP")
		} else {
			sets = append(sets, "archived_at=NULL")
		}
	}
	if len(sets) == 0 {
		return getTier(ctx.AppDB(), pid, spaceID, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, pid, spaceID, id)
	if _, err := ctx.AppDB().Exec(`UPDATE tiers SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND space_id=? AND id=?`, args...); err != nil {
		return nil, err
	}
	tier, err := getTier(ctx.AppDB(), pid, spaceID, id)
	if tier == nil && err == nil {
		err = fmt.Errorf("tier %d not found", id)
	}
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "tier.updated", "agent", "tier", id, patch)
	}
	return tier, err
}

type memberFilters struct {
	status string
	q      string
	tierID int64
	limit  int
}

func upsertMember(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Member, bool, map[string]any, error) {
	email := strings.ToLower(strings.TrimSpace(strArg(args, "email")))
	if !strings.Contains(email, "@") {
		return nil, false, nil, errors.New("valid email required")
	}
	display := strings.TrimSpace(strArg(args, "display_name"))
	status, statusProvided := args["status"].(string)
	status = strings.TrimSpace(status)
	if status == "" {
		status = "lead"
	}
	if !validMemberStatus(status) {
		return nil, false, nil, fmt.Errorf("invalid member status %q", status)
	}
	tierID := int64Arg(args, "tier_id")
	if tierID > 0 {
		if tier, err := getTier(ctx.AppDB(), pid, spaceID, tierID); err != nil || tier == nil {
			if err != nil {
				return nil, false, nil, err
			}
			return nil, false, nil, fmt.Errorf("tier %d not found", tierID)
		}
	}
	token, err := randomToken()
	if err != nil {
		return nil, false, nil, fmt.Errorf("generate portal token: %w", err)
	}
	expiresAt := time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	res, err := ctx.AppDB().Exec(
		`INSERT OR IGNORE INTO members (project_id, space_id, email, display_name, status, tier_id, portal_token, portal_token_expires_at)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?,0), ?, ?)`,
		pid, spaceID, email, display, status, tierID, token, expiresAt,
	)
	if err != nil {
		return nil, false, nil, err
	}
	affected, _ := res.RowsAffected()
	created := affected > 0
	if !created {
		patch := map[string]any{}
		if display != "" {
			patch["display_name"] = display
		}
		if statusProvided {
			patch["status"] = status
		}
		if tierID > 0 {
			patch["tier_id"] = tierID
		}
		if len(patch) > 0 {
			existing, _ := getMemberByEmail(ctx.AppDB(), pid, spaceID, email)
			if existing != nil {
				_, _ = updateMember(ctx, pid, spaceID, existing.ID, patch)
			}
		}
	}
	member, err := getMemberByEmail(ctx.AppDB(), pid, spaceID, email)
	if err != nil {
		return nil, false, nil, err
	}
	extras := map[string]any{}
	if boolArg(args, "sync_billing") {
		if id, err := syncBillingCustomer(ctx, pid, member); err == nil && id > 0 {
			_ = setMemberExternalIDs(ctx.AppDB(), pid, spaceID, member.ID, 0, id)
			member, _ = getMember(ctx.AppDB(), pid, spaceID, member.ID)
			extras["billing_customer_synced"] = true
		} else if err != nil {
			extras["billing_sync_error"] = err.Error()
		}
	}
	if boolArg(args, "sync_crm") {
		if id, err := syncCRMContact(ctx, pid, member); err == nil && id > 0 {
			_ = setMemberExternalIDs(ctx.AppDB(), pid, spaceID, member.ID, id, 0)
			member, _ = getMember(ctx.AppDB(), pid, spaceID, member.ID)
			extras["crm_contact_synced"] = true
		} else if err != nil {
			extras["crm_sync_error"] = err.Error()
		}
	}
	if created {
		_ = logEvent(ctx, pid, spaceID, "member.created", "agent", "member", member.ID, map[string]any{"email": email})
	} else {
		_ = logEvent(ctx, pid, spaceID, "member.updated", "agent", "member", member.ID, map[string]any{"email": email})
	}
	return member, created, extras, nil
}

func listMembers(db *sql.DB, pid string, spaceID int64, f memberFilters) ([]Member, error) {
	limit := clampLimit(f.limit, 100, 500)
	where := []string{"project_id=?", "space_id=?"}
	args := []any{pid, spaceID}
	if f.status != "" {
		where, args = append(where, "status=?"), append(args, f.status)
	}
	if f.tierID > 0 {
		where, args = append(where, "tier_id=?"), append(args, f.tierID)
	}
	if f.q != "" {
		where, args = append(where, "(email LIKE ? OR display_name LIKE ?)"), append(args, "%"+f.q+"%", "%"+f.q+"%")
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+memberColumns()+` FROM members WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func getMember(db *sql.DB, pid string, spaceID, id int64) (*Member, error) {
	row := db.QueryRow(`SELECT `+memberColumns()+` FROM members WHERE project_id=? AND space_id=? AND id=?`, pid, spaceID, id)
	return scanMember(row)
}

func getMemberByEmail(db *sql.DB, pid string, spaceID int64, email string) (*Member, error) {
	row := db.QueryRow(`SELECT `+memberColumns()+` FROM members WHERE project_id=? AND space_id=? AND email=?`, pid, spaceID, strings.ToLower(email))
	return scanMember(row)
}

func getMemberByToken(db *sql.DB, token string) (*Member, error) {
	row := db.QueryRow(`SELECT `+memberColumns()+` FROM members
		WHERE portal_token=? AND portal_token_revoked_at IS NULL
		AND portal_token_expires_at IS NOT NULL AND datetime(portal_token_expires_at) > datetime('now')`, token)
	return scanMember(row)
}

func rotatePortalToken(ctx *sdk.AppCtx, pid string, spaceID, memberID int64) (*Member, string, error) {
	if memberID <= 0 {
		return nil, "", errors.New("member_id required")
	}
	if member, err := getMember(ctx.AppDB(), pid, spaceID, memberID); err != nil || member == nil {
		if err != nil {
			return nil, "", err
		}
		return nil, "", errors.New("member not found")
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate portal token: %w", err)
	}
	expires := time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(`UPDATE members
		SET portal_token=?, portal_token_expires_at=?, portal_token_revoked_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND space_id=? AND id=?`, token, expires, pid, spaceID, memberID); err != nil {
		return nil, "", err
	}
	member, err := getMember(ctx.AppDB(), pid, spaceID, memberID)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "member.portal_token_rotated", "agent", "member", memberID, nil)
	}
	return member, token, err
}

func memberColumns() string {
	return `id, project_id, space_id, email, display_name, status, tier_id, crm_contact_id, billing_customer_id, portal_token, COALESCE(current_period_start,''), COALESCE(current_period_end,''), COALESCE(portal_token_expires_at,''), COALESCE(portal_token_revoked_at,''), metadata, created_at, updated_at`
}

func scanMember(row interface{ Scan(...any) error }) (*Member, error) {
	var m Member
	var tier, crm, billing sql.NullInt64
	var meta string
	if err := row.Scan(&m.ID, &m.ProjectID, &m.SpaceID, &m.Email, &m.DisplayName, &m.Status, &tier, &crm, &billing, &m.PortalToken, &m.CurrentPeriodStart, &m.CurrentPeriodEnd, &m.PortalTokenExpires, &m.PortalTokenRevoked, &meta, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if tier.Valid {
		m.TierID = &tier.Int64
	}
	if crm.Valid {
		m.CRMContactID = &crm.Int64
	}
	if billing.Valid {
		m.BillingCustomerID = &billing.Int64
	}
	m.Metadata = rawJSON(meta, "{}")
	return &m, nil
}

func memberExpiry(member *Member) string {
	if member == nil {
		return ""
	}
	return member.PortalTokenExpires
}

func updateMember(ctx *sdk.AppCtx, pid string, spaceID, id int64, patch map[string]any) (*Member, error) {
	if id == 0 {
		return nil, errors.New("member id required")
	}
	sets := []string{}
	args := []any{}
	if v, ok := patch["display_name"].(string); ok {
		sets, args = append(sets, "display_name=?"), append(args, v)
	}
	if v := strArg(patch, "status"); v != "" {
		if !validMemberStatus(v) {
			return nil, fmt.Errorf("invalid member status %q", v)
		}
		sets, args = append(sets, "status=?"), append(args, v)
	}
	if _, ok := patch["tier_id"]; ok {
		tid := int64Arg(patch, "tier_id")
		if tid == 0 {
			sets = append(sets, "tier_id=NULL")
		} else {
			if t, err := getTier(ctx.AppDB(), pid, spaceID, tid); err != nil || t == nil {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("tier %d not found", tid)
			}
			sets, args = append(sets, "tier_id=?"), append(args, tid)
		}
	}
	for _, field := range []string{"current_period_start", "current_period_end"} {
		if v, ok := patch[field].(string); ok {
			if v == "" {
				sets = append(sets, field+"=NULL")
			} else {
				if _, err := parseTimestamp(v); err != nil {
					return nil, fmt.Errorf("invalid %s: %w", field, err)
				}
				sets, args = append(sets, field+"=?"), append(args, v)
			}
		}
	}
	if md, ok := patch["metadata"].(map[string]any); ok {
		raw, _ := json.Marshal(md)
		sets, args = append(sets, "metadata=?"), append(args, string(raw))
	}
	if len(sets) == 0 {
		return getMember(ctx.AppDB(), pid, spaceID, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, pid, spaceID, id)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND space_id=? AND id=?`, args...); err != nil {
		return nil, err
	}
	m, err := getMember(ctx.AppDB(), pid, spaceID, id)
	if m == nil && err == nil {
		err = fmt.Errorf("member %d not found", id)
	}
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "member.updated", "agent", "member", id, patch)
		if _, statusChanged := patch["status"]; statusChanged {
			_ = syncCRMState(ctx, pid, m)
		} else if _, tierChanged := patch["tier_id"]; tierChanged {
			_ = syncCRMState(ctx, pid, m)
		}
	}
	return m, err
}

func setMemberExternalIDs(db *sql.DB, pid string, spaceID, id, crmID, billingID int64) error {
	sets := []string{}
	args := []any{}
	if crmID > 0 {
		sets, args = append(sets, "crm_contact_id=?"), append(args, crmID)
	}
	if billingID > 0 {
		sets, args = append(sets, "billing_customer_id=?"), append(args, billingID)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, pid, spaceID, id)
	_, err := db.Exec(`UPDATE members SET `+strings.Join(sets, ", ")+`, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND space_id=? AND id=?`, args...)
	return err
}

type postFilters struct {
	status     string
	visibility string
	q          string
	limit      int
}

func createPost(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Post, error) {
	title := cleanString(args["title"])
	if title == "" {
		return nil, errors.New("title required")
	}
	visibility := strArg(args, "visibility")
	if visibility == "" {
		visibility = "members"
	}
	if !validVisibility(visibility) {
		return nil, fmt.Errorf("invalid visibility %q", visibility)
	}
	status := strArg(args, "status")
	if status == "" {
		status = "draft"
	}
	if !validPostStatus(status) {
		return nil, fmt.Errorf("invalid status %q", status)
	}
	slug := slugify(title)
	if requested := strArg(args, "slug"); requested != "" {
		slug = slugify(requested)
	}
	tierIDs := jsonIntArray(args["tier_ids"])
	if err := validateTierIDs(ctx.AppDB(), pid, spaceID, tierIDs); err != nil {
		return nil, err
	}
	collectionIDs, collectionsProvided, err := collectionIDsFromMap(args)
	if err != nil {
		return nil, err
	}
	if collectionsProvided {
		if err := validateCollectionIDs(ctx.AppDB(), pid, spaceID, collectionIDs); err != nil {
			return nil, err
		}
	}
	scheduledAt := strArg(args, "scheduled_at")
	if status == "scheduled" {
		if scheduledAt == "" {
			return nil, errors.New("scheduled_at required when status is scheduled")
		}
		if _, err := parseTimestamp(scheduledAt); err != nil {
			return nil, fmt.Errorf("invalid scheduled_at: %w", err)
		}
	}
	var publishedAt any
	if status == "published" {
		publishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, space_id, title, slug, body, status, visibility, tier_ids_json, published_at, scheduled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''))`,
		pid, spaceID, title, slug, strArg(args, "body"), status, visibility, string(tierIDs), publishedAt, scheduledAt,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if collectionsProvided {
		if err := setPostCollections(ctx, pid, spaceID, id, collectionIDs); err != nil {
			_, _ = ctx.AppDB().Exec(`DELETE FROM posts WHERE project_id=? AND space_id=? AND id=?`, pid, spaceID, id)
			return nil, err
		}
	}
	post, err := getPost(ctx.AppDB(), pid, spaceID, id, true)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "post.created", "agent", "post", id, map[string]any{"title": title})
	}
	return post, err
}

func listPosts(db *sql.DB, pid string, spaceID int64, f postFilters) ([]Post, error) {
	limit := clampLimit(f.limit, 100, 500)
	where := []string{"project_id=?", "space_id=?"}
	args := []any{pid, spaceID}
	if f.status != "" {
		where, args = append(where, "status=?"), append(args, f.status)
	}
	if f.visibility != "" {
		where, args = append(where, "visibility=?"), append(args, f.visibility)
	}
	if f.q != "" {
		where, args = append(where, "(title LIKE ? OR body LIKE ?)"), append(args, "%"+f.q+"%", "%"+f.q+"%")
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+postColumns()+` FROM posts WHERE `+strings.Join(where, " AND ")+` ORDER BY COALESCE(published_at, created_at) DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	postPointers := make([]*Post, len(out))
	for i := range out {
		postPointers[i] = &out[i]
	}
	if err := hydratePostCollectionIDs(db, pid, spaceID, postPointers); err != nil {
		return nil, err
	}
	return out, nil
}

func getPost(db *sql.DB, pid string, spaceID, id int64, withAttachments bool) (*Post, error) {
	row := db.QueryRow(`SELECT `+postColumns()+` FROM posts WHERE project_id=? AND space_id=? AND id=?`, pid, spaceID, id)
	p, err := scanPost(row)
	if err != nil || p == nil {
		return p, err
	}
	p.CollectionIDs, err = listCollectionIDsForPost(db, pid, spaceID, p.ID)
	if err == nil && withAttachments {
		p.Attachments, err = listAttachments(db, pid, spaceID, p.ID)
	}
	return p, err
}

func getPostBySlug(db *sql.DB, pid string, spaceID int64, slug string, withAttachments bool) (*Post, error) {
	row := db.QueryRow(`SELECT `+postColumns()+` FROM posts WHERE project_id=? AND space_id=? AND slug=?`, pid, spaceID, slug)
	p, err := scanPost(row)
	if err != nil || p == nil {
		return p, err
	}
	p.CollectionIDs, err = listCollectionIDsForPost(db, pid, spaceID, p.ID)
	if err == nil && withAttachments {
		p.Attachments, err = listAttachments(db, pid, spaceID, p.ID)
	}
	return p, err
}

func postColumns() string {
	return `id, project_id, space_id, title, slug, body, status, visibility, tier_ids_json, COALESCE(published_at,''), COALESCE(scheduled_at,''), created_at, updated_at`
}

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	var p Post
	var tiers string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.SpaceID, &p.Title, &p.Slug, &p.Body, &p.Status, &p.Visibility, &tiers, &p.PublishedAt, &p.ScheduledAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.TierIDs = rawJSON(tiers, "[]")
	return &p, nil
}

func updatePost(ctx *sdk.AppCtx, pid string, spaceID, id int64, patch map[string]any) (*Post, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	if patch == nil {
		return nil, errors.New("patch required")
	}
	sets := []string{}
	args := []any{}
	collectionIDs, collectionsProvided, err := collectionIDsFromMap(patch)
	if err != nil {
		return nil, err
	}
	if collectionsProvided {
		if err := validateCollectionIDs(ctx.AppDB(), pid, spaceID, collectionIDs); err != nil {
			return nil, err
		}
	}
	if v := cleanString(patch["title"]); v != "" {
		sets, args = append(sets, "title=?"), append(args, v)
	}
	if v := cleanString(patch["slug"]); v != "" {
		sets, args = append(sets, "slug=?"), append(args, slugify(v))
	}
	if v, ok := patch["body"].(string); ok {
		sets, args = append(sets, "body=?"), append(args, v)
	}
	if v := strArg(patch, "status"); v != "" {
		if !validPostStatus(v) {
			return nil, fmt.Errorf("invalid status %q", v)
		}
		sets, args = append(sets, "status=?"), append(args, v)
		if v == "published" {
			sets = append(sets, "published_at=COALESCE(published_at, CURRENT_TIMESTAMP)")
		}
	}
	if v := strArg(patch, "visibility"); v != "" {
		if !validVisibility(v) {
			return nil, fmt.Errorf("invalid visibility %q", v)
		}
		sets, args = append(sets, "visibility=?"), append(args, v)
	}
	if _, ok := patch["tier_ids"]; ok {
		tierIDs := jsonIntArray(patch["tier_ids"])
		if err := validateTierIDs(ctx.AppDB(), pid, spaceID, tierIDs); err != nil {
			return nil, err
		}
		sets, args = append(sets, "tier_ids_json=?"), append(args, string(tierIDs))
	}
	if v, ok := patch["scheduled_at"].(string); ok {
		if v == "" {
			sets = append(sets, "scheduled_at=NULL")
		} else {
			if _, err := parseTimestamp(v); err != nil {
				return nil, fmt.Errorf("invalid scheduled_at: %w", err)
			}
			sets, args = append(sets, "scheduled_at=?"), append(args, v)
		}
	}
	if len(sets) == 0 && !collectionsProvided {
		return getPost(ctx.AppDB(), pid, spaceID, id, true)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
		args = append(args, pid, spaceID, id)
		result, err := ctx.AppDB().Exec(`UPDATE posts SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND space_id=? AND id=?`, args...)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return nil, fmt.Errorf("post %d not found", id)
		}
	}
	if collectionsProvided {
		post, err := getPost(ctx.AppDB(), pid, spaceID, id, false)
		if err != nil {
			return nil, err
		}
		if post == nil {
			return nil, fmt.Errorf("post %d not found", id)
		}
		if err := setPostCollections(ctx, pid, spaceID, id, collectionIDs); err != nil {
			return nil, err
		}
	}
	post, err := getPost(ctx.AppDB(), pid, spaceID, id, true)
	if post == nil && err == nil {
		err = fmt.Errorf("post %d not found", id)
	}
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "post.updated", "agent", "post", id, patch)
	}
	return post, err
}

func publishPost(ctx *sdk.AppCtx, pid string, spaceID, id int64, scheduledAt string) (*Post, error) {
	patch := map[string]any{}
	if scheduledAt != "" {
		when, err := parseTimestamp(scheduledAt)
		if err != nil {
			return nil, fmt.Errorf("invalid scheduled_at: %w", err)
		}
		if !when.After(time.Now().UTC()) {
			return nil, errors.New("scheduled_at must be in the future")
		}
		patch["status"] = "scheduled"
		patch["scheduled_at"] = scheduledAt
	} else {
		patch["status"] = "published"
	}
	post, err := updatePost(ctx, pid, spaceID, id, patch)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "post.published", "agent", "post", id, map[string]any{"scheduled_at": scheduledAt})
	}
	return post, err
}

func uploadAttachment(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Attachment, error) {
	post, err := getPost(ctx.AppDB(), pid, spaceID, int64Arg(args, "post_id"), false)
	if err != nil || post == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("post not found")
	}
	filename := strArg(args, "filename")
	if filename == "" {
		return nil, errors.New("filename required")
	}
	content := strArg(args, "content_base64")
	if content == "" {
		return nil, errors.New("content_base64 required")
	}
	if base64.StdEncoding.DecodedLen(len(content)) > 25*1024*1024 {
		return nil, errors.New("attachment exceeds the 25 MiB upload limit")
	}
	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, errors.New("content_base64 is invalid")
	}
	size := int64(len(raw))
	space, _ := getSpace(ctx.AppDB(), pid, spaceID)
	folder := fmt.Sprintf("/creators/%s/posts/%s", space.Slug, post.Slug)
	var out storageUploadResult
	call := map[string]any{
		"name":           filename,
		"folder":         folder,
		"content_base64": content,
		"visibility":     "private",
		"_project_id":    pid,
	}
	if ct := strArg(args, "content_type"); ct != "" {
		call["content_type"] = ct
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_upload", call, &out); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	fileID := out.FileID
	if fileID == 0 {
		fileID = out.ID
	}
	if fileID == 0 && out.File != nil {
		fileID = out.File.ID
	}
	if fileID == 0 {
		return nil, errors.New("storage.files_upload returned no file ID")
	}
	args["storage_file_id"] = fileID
	args["size_bytes"] = size
	return addAttachment(ctx, pid, spaceID, args)
}

func addAttachment(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Attachment, error) {
	postID := int64Arg(args, "post_id")
	fileID := int64Arg(args, "storage_file_id")
	if postID == 0 || fileID == 0 {
		return nil, errors.New("post_id and storage_file_id required")
	}
	if p, err := getPost(ctx.AppDB(), pid, spaceID, postID, false); err != nil || p == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("post %d not found", postID)
	}
	file, err := getStorageFileMetadata(ctx, pid, fileID)
	if err != nil {
		return nil, fmt.Errorf("storage.files_get: %w", err)
	}
	if file == nil || file.ID != fileID {
		return nil, fmt.Errorf("storage file %d not found", fileID)
	}
	args["filename"] = file.Name
	args["content_type"] = file.ContentType
	args["size_bytes"] = file.SizeBytes
	vis := strArg(args, "visibility")
	if vis == "" {
		vis = "inherit"
	}
	if !validAttachmentVisibility(vis) {
		return nil, fmt.Errorf("invalid attachment visibility %q", vis)
	}
	tierIDs := jsonIntArray(args["tier_ids"])
	if err := validateTierIDs(ctx.AppDB(), pid, spaceID, tierIDs); err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO attachments (project_id, space_id, post_id, storage_file_id, filename, content_type, size_bytes, visibility, tier_ids_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, spaceID, postID, fileID, strArg(args, "filename"), strArg(args, "content_type"), int64Arg(args, "size_bytes"), vis, string(tierIDs),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	att, err := getAttachment(ctx.AppDB(), pid, spaceID, id)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "attachment.added", "agent", "attachment", id, map[string]any{"post_id": postID, "storage_file_id": fileID})
	}
	return att, err
}

func getStorageFileMetadata(ctx *sdk.AppCtx, pid string, fileID int64) (*storageFileMetadata, error) {
	var out storageFileLookupResult
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_get", map[string]any{
		"id": fileID, "_project_id": pid,
	}, &out); err != nil {
		return nil, err
	}
	if out.File != nil {
		return out.File, nil
	}
	if out.ID == 0 {
		return nil, nil
	}
	return &storageFileMetadata{
		ID: out.ID, Name: out.Name, ContentType: out.ContentType, SizeBytes: out.SizeBytes,
	}, nil
}

func listAttachments(db *sql.DB, pid string, spaceID, postID int64) ([]Attachment, error) {
	rows, err := db.Query(`SELECT `+attachmentColumns()+` FROM attachments WHERE project_id=? AND space_id=? AND post_id=? ORDER BY id`, pid, spaceID, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func hydrateAccessibleAttachments(db *sql.DB, pid string, spaceID int64, posts []Post, member *Member) error {
	for i := range posts {
		attachments, err := listAttachments(db, pid, spaceID, posts[i].ID)
		if err != nil {
			return err
		}
		posts[i].Attachments = accessibleAttachments(member, &posts[i], attachments)
	}
	return nil
}

func accessibleAttachments(member *Member, post *Post, attachments []Attachment) []Attachment {
	visible := make([]Attachment, 0, len(attachments))
	for i := range attachments {
		if memberCanAccessAttachment(member, post, &attachments[i]) {
			visible = append(visible, attachments[i])
		}
	}
	return visible
}

func getAttachment(db *sql.DB, pid string, spaceID, id int64) (*Attachment, error) {
	row := db.QueryRow(`SELECT `+attachmentColumns()+` FROM attachments WHERE project_id=? AND space_id=? AND id=?`, pid, spaceID, id)
	return scanAttachment(row)
}

func attachmentColumns() string {
	return `id, project_id, space_id, post_id, storage_file_id, filename, content_type, size_bytes, visibility, tier_ids_json, created_at`
}

func scanAttachment(row interface{ Scan(...any) error }) (*Attachment, error) {
	var a Attachment
	var tiers string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.SpaceID, &a.PostID, &a.StorageFileID, &a.Filename, &a.ContentType, &a.SizeBytes, &a.Visibility, &tiers, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.TierIDs = rawJSON(tiers, "[]")
	return &a, nil
}

func getDownloadLink(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (map[string]any, error) {
	att, err := getAttachment(ctx.AppDB(), pid, spaceID, int64Arg(args, "attachment_id"))
	if err != nil || att == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("attachment not found")
	}
	post, err := getPost(ctx.AppDB(), pid, spaceID, att.PostID, false)
	if err != nil || post == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("post not found")
	}
	var member *Member
	if id := int64Arg(args, "member_id"); id > 0 {
		member, err = getMember(ctx.AppDB(), pid, spaceID, id)
	} else if tok := strArg(args, "portal_token"); tok != "" {
		member, err = getMemberByToken(ctx.AppDB(), tok)
	}
	if err != nil {
		return nil, err
	}
	if member != nil && (member.ProjectID != pid || member.SpaceID != spaceID) {
		return nil, errors.New("member credential does not belong to this creator space")
	}
	if !memberCanAccessAttachment(member, post, att) {
		return nil, errors.New("member is not entitled to this file")
	}
	ttl := intArg(args, "ttl_seconds", 3600)
	if ttl <= 0 || ttl > 604800 {
		ttl = 3600
	}
	var out map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"id":          att.StorageFileID,
		"ttl_seconds": ttl,
		"_project_id": pid,
	}, &out); err != nil {
		return nil, fmt.Errorf("storage.files_get_url: %w", err)
	}
	_ = logEvent(ctx, pid, spaceID, "attachment.download_link", "member", "attachment", att.ID, map[string]any{"member_id": int64Arg(args, "member_id")})
	return map[string]any{"attachment": att, "download": out}, nil
}

// memberCanAccessAttachment is intentionally small and testable: public
// post/file content is open; otherwise active/comped members must satisfy
// post gates and then attachment-specific gates.
func memberCanAccessAttachment(member *Member, post *Post, att *Attachment) bool {
	if post == nil || att == nil {
		return false
	}
	vis := att.Visibility
	if vis == "inherit" {
		return memberCanAccessPost(member, post)
	}
	if vis == "public" && post.Visibility == "public" {
		return true
	}
	if !memberCanAccessStatus(member) {
		return false
	}
	if !memberCanAccessPost(member, post) {
		return false
	}
	if vis == "public" || vis == "members" || vis == "inherit" {
		return true
	}
	if vis == "tier" {
		return memberTierIn(member, att.TierIDs)
	}
	return false
}

func memberCanAccessPost(member *Member, post *Post) bool {
	if post == nil || post.Status != "published" {
		return false
	}
	switch post.Visibility {
	case "public":
		return true
	case "members":
		return memberCanAccessStatus(member)
	case "tier":
		return memberCanAccessStatus(member) && memberTierIn(member, post.TierIDs)
	default:
		return false
	}
}

func memberCanAccessStatus(member *Member) bool {
	if member == nil {
		return false
	}
	if member.Status == "comped" {
		return true
	}
	if member.Status != "active" {
		return false
	}
	if member.CurrentPeriodEnd == "" {
		return true
	}
	end, err := parseTimestamp(member.CurrentPeriodEnd)
	return err == nil && end.After(time.Now().UTC())
}

func memberTierIn(member *Member, raw json.RawMessage) bool {
	if member == nil || member.TierID == nil {
		return false
	}
	var ids []int64
	if json.Unmarshal(raw, &ids) != nil {
		return false
	}
	for _, id := range ids {
		if id == *member.TierID {
			return true
		}
	}
	return false
}

func createPaymentLink(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (map[string]any, error) {
	member, err := getMember(ctx.AppDB(), pid, spaceID, int64Arg(args, "member_id"))
	if err != nil || member == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("member not found")
	}
	tierID := int64Arg(args, "tier_id")
	if tierID == 0 && member.TierID != nil {
		tierID = *member.TierID
	}
	tier, err := getTier(ctx.AppDB(), pid, spaceID, tierID)
	if err != nil || tier == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("tier not found")
	}
	if tier.ArchivedAt != "" {
		return nil, errors.New("cannot create a payment for an archived tier")
	}
	if tier.PriceCents <= 0 {
		return nil, errors.New("payment links require a tier with a positive price")
	}
	if !validCurrency(tier.Currency) {
		return nil, fmt.Errorf("invalid tier currency %q", tier.Currency)
	}
	customerID := int64(0)
	if member.BillingCustomerID != nil {
		customerID = *member.BillingCustomerID
	}
	if customerID == 0 {
		customerID, err = syncBillingCustomer(ctx, pid, member)
		if err != nil {
			return nil, err
		}
		_ = setMemberExternalIDs(ctx.AppDB(), pid, spaceID, member.ID, 0, customerID)
	}
	periods := intArg(args, "periods", 0)
	if periods == 0 {
		periods = intArg(args, "months", 1) // compatibility with v0.1.x callers
	}
	if periods <= 0 || periods > 120 {
		return nil, errors.New("periods must be between 1 and 120")
	}
	if tier.Interval == "one_time" && periods != 1 {
		return nil, errors.New("one-time tiers require periods=1")
	}
	idem := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idem == "" {
		anchor := member.CurrentPeriodEnd
		if anchor == "" {
			anchor = "initial"
		}
		idem = fmt.Sprintf("member:%d:tier:%d:period:%s", member.ID, tier.ID, anchor)
	}
	if len(idem) > 200 {
		return nil, errors.New("idempotency_key must be 200 characters or fewer")
	}
	amount := tier.PriceCents * int64(periods)
	payment, created, err := reserveMembershipPayment(ctx.AppDB(), pid, spaceID, member.ID, tier.ID, idem, periods, amount, tier.Currency)
	if err != nil {
		return nil, err
	}
	if payment.Status == "paid" {
		return map[string]any{"member": redactMember(member), "tier": tier, "payment": paymentView(payment), "already_paid": true}, nil
	}
	if !created && !payment.BillingInvoiceID.Valid && payment.Status == "creating" {
		return nil, errors.New("payment creation is already in progress; retry shortly with the same idempotency_key")
	}
	desc := fmt.Sprintf("%s membership - %d %s period(s)", tier.Name, periods, tier.Interval)
	var invResp struct {
		Invoice struct {
			ID     int64  `json:"id"`
			Number string `json:"number"`
		} `json:"invoice"`
	}
	if payment.BillingInvoiceID.Valid {
		invResp.Invoice.ID = payment.BillingInvoiceID.Int64
	} else {
		err = ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "invoices_create", map[string]any{
			"customer_id": customerID,
			"currency":    tier.Currency,
			"line_items": []any{map[string]any{
				"description":      desc,
				"quantity":         1,
				"unit_price_cents": amount,
				"metadata":         map[string]any{"creators_member_id": member.ID, "creators_tier_id": tier.ID, "creators_payment_id": payment.ID},
			}},
			"metadata":    map[string]any{"source_app": "creators", "member_id": member.ID, "tier_id": tier.ID, "creators_payment_id": payment.ID},
			"_project_id": pid,
		}, &invResp)
		if err != nil {
			_ = markMembershipPaymentFailed(ctx.AppDB(), payment.ID)
			return nil, fmt.Errorf("billing.invoices_create: %w", err)
		}
		if err := attachMembershipInvoice(ctx.AppDB(), payment.ID, invResp.Invoice.ID); err != nil {
			return nil, fmt.Errorf("persist billing invoice mapping: %w", err)
		}
	}
	var finalResp struct {
		Invoice struct {
			ID     int64  `json:"id"`
			Number string `json:"number"`
		} `json:"invoice"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "invoices_finalize", map[string]any{"invoice_id": invResp.Invoice.ID, "_project_id": pid}, &finalResp); err != nil {
		return nil, fmt.Errorf("billing.invoices_finalize: %w", err)
	}
	var link map[string]any
	linkArgs := map[string]any{"invoice_id": finalResp.Invoice.ID, "_project_id": pid}
	if v := strArg(args, "success_url"); v != "" {
		linkArgs["success_url"] = v
	}
	if v := strArg(args, "cancel_url"); v != "" {
		linkArgs["cancel_url"] = v
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "invoices_send_payment_link", linkArgs, &link); err != nil {
		return nil, fmt.Errorf("billing.invoices_send_payment_link: %w", err)
	}
	_ = logEvent(ctx, pid, spaceID, "payment_link.created", "agent", "member", member.ID, map[string]any{"invoice_id": finalResp.Invoice.ID, "tier_id": tier.ID})
	payment, _ = getMembershipPaymentByID(ctx.AppDB(), payment.ID)
	return map[string]any{"member": redactMember(member), "tier": tier, "invoice": finalResp.Invoice, "payment": paymentView(payment), "payment_link": link}, nil
}

func reserveMembershipPayment(db *sql.DB, pid string, spaceID, memberID, tierID int64, key string, periods int, amount int64, currency string) (*MembershipPayment, bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO membership_payments
		(project_id, space_id, member_id, tier_id, idempotency_key, period_count, amount_cents, currency)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, pid, spaceID, memberID, tierID, key, periods, amount, currency)
	if err != nil {
		return nil, false, err
	}
	affected, _ := res.RowsAffected()
	p, err := getMembershipPaymentByKey(db, pid, spaceID, key)
	if err == nil && p != nil && (p.MemberID != memberID || p.TierID != tierID || p.PeriodCount != periods || p.AmountCents != amount || p.Currency != currency) {
		return nil, false, errors.New("idempotency_key was already used with different payment parameters")
	}
	return p, affected > 0, err
}

func getMembershipPaymentByKey(db *sql.DB, pid string, spaceID int64, key string) (*MembershipPayment, error) {
	return scanMembershipPayment(db.QueryRow(membershipPaymentSelect()+` WHERE project_id=? AND space_id=? AND idempotency_key=?`, pid, spaceID, key))
}

func getMembershipPaymentByID(db *sql.DB, id int64) (*MembershipPayment, error) {
	return scanMembershipPayment(db.QueryRow(membershipPaymentSelect()+` WHERE id=?`, id))
}

func getMembershipPaymentByInvoice(db *sql.DB, pid string, invoiceID int64) (*MembershipPayment, error) {
	return scanMembershipPayment(db.QueryRow(membershipPaymentSelect()+` WHERE project_id=? AND billing_invoice_id=?`, pid, invoiceID))
}

func membershipPaymentSelect() string {
	return `SELECT id, project_id, space_id, member_id, tier_id, billing_invoice_id, idempotency_key,
		status, period_count, amount_cents, currency, COALESCE(period_start,''), COALESCE(period_end,''),
		COALESCE(paid_at,''), updated_at FROM membership_payments`
}

func scanMembershipPayment(row interface{ Scan(...any) error }) (*MembershipPayment, error) {
	var p MembershipPayment
	if err := row.Scan(&p.ID, &p.ProjectID, &p.SpaceID, &p.MemberID, &p.TierID, &p.BillingInvoiceID,
		&p.IdempotencyKey, &p.Status, &p.PeriodCount, &p.AmountCents, &p.Currency,
		&p.PeriodStart, &p.PeriodEnd, &p.PaidAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func attachMembershipInvoice(db *sql.DB, paymentID, invoiceID int64) error {
	_, err := db.Exec(`UPDATE membership_payments SET billing_invoice_id=?, status='open', updated_at=CURRENT_TIMESTAMP WHERE id=?`, invoiceID, paymentID)
	return err
}

func markMembershipPaymentFailed(db *sql.DB, paymentID int64) error {
	_, err := db.Exec(`UPDATE membership_payments SET status='failed', updated_at=CURRENT_TIMESTAMP WHERE id=? AND billing_invoice_id IS NULL`, paymentID)
	return err
}

func paymentView(p *MembershipPayment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id": p.ID, "billing_invoice_id": nullableInt64(p.BillingInvoiceID), "status": p.Status,
		"periods": p.PeriodCount, "amount_cents": p.AmountCents, "currency": p.Currency,
		"period_start": p.PeriodStart, "period_end": p.PeriodEnd, "paid_at": p.PaidAt,
	}
}

func (a *App) handleInvoicePaid(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	if status := strings.ToLower(cleanString(event.Data["status"])); status != "paid" {
		return nil
	}
	invoiceID := int64FromAny(event.Data["id"])
	if invoiceID <= 0 || event.ProjectID == "" {
		return nil
	}
	payment, err := getMembershipPaymentByInvoice(ctx.AppDB(), event.ProjectID, invoiceID)
	if err != nil || payment == nil || payment.Status == "paid" {
		return err
	}
	member, err := getMember(ctx.AppDB(), payment.ProjectID, payment.SpaceID, payment.MemberID)
	if err != nil || member == nil {
		if err != nil {
			return err
		}
		return errors.New("membership payment references a missing member")
	}
	tier, err := getTier(ctx.AppDB(), payment.ProjectID, payment.SpaceID, payment.TierID)
	if err != nil || tier == nil {
		if err != nil {
			return err
		}
		return errors.New("membership payment references a missing tier")
	}
	start, end := membershipPeriod(member, tier.Interval, payment.PeriodCount, time.Now().UTC())
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE membership_payments SET status='paid', period_start=?, period_end=NULLIF(?,''), paid_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status<>'paid'`, start, end, payment.ID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE members SET tier_id=?, status='active', current_period_start=?, current_period_end=NULLIF(?,''), updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND space_id=? AND id=?`, tier.ID, start, end, payment.ProjectID, payment.SpaceID, member.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	member, _ = getMember(ctx.AppDB(), payment.ProjectID, payment.SpaceID, member.ID)
	_ = syncCRMState(ctx, payment.ProjectID, member)
	_ = logEvent(ctx, payment.ProjectID, payment.SpaceID, "membership.paid", "billing", "member", member.ID, map[string]any{"invoice_id": invoiceID, "tier_id": tier.ID, "period_end": end})
	return nil
}

func (a *App) handleInvoiceInvalidated(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	if event.Name() == "invoice.refunded" && strings.EqualFold(cleanString(event.Data["status"]), "paid") {
		return nil // partial refund; the invoice remains fully settled
	}
	invoiceID := int64FromAny(event.Data["id"])
	if invoiceID <= 0 || event.ProjectID == "" {
		return nil
	}
	payment, err := getMembershipPaymentByInvoice(ctx.AppDB(), event.ProjectID, invoiceID)
	if err != nil || payment == nil {
		return err
	}
	wasPaid := payment.Status == "paid"
	if _, err := ctx.AppDB().Exec(`UPDATE membership_payments SET status='void', updated_at=CURRENT_TIMESTAMP WHERE id=?`, payment.ID); err != nil {
		return err
	}
	if !wasPaid {
		return nil
	}
	var newer int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM membership_payments WHERE project_id=? AND space_id=? AND member_id=? AND status='paid' AND id>?`, payment.ProjectID, payment.SpaceID, payment.MemberID, payment.ID).Scan(&newer); err != nil {
		return err
	}
	if newer > 0 {
		return nil
	}
	member, err := getMember(ctx.AppDB(), payment.ProjectID, payment.SpaceID, payment.MemberID)
	if err != nil || member == nil || member.TierID == nil || *member.TierID != payment.TierID {
		return err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE members SET status='past_due', updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND space_id=?`, member.ID, payment.ProjectID, payment.SpaceID); err != nil {
		return err
	}
	member, _ = getMember(ctx.AppDB(), payment.ProjectID, payment.SpaceID, member.ID)
	_ = syncCRMState(ctx, payment.ProjectID, member)
	_ = logEvent(ctx, payment.ProjectID, payment.SpaceID, "membership.payment_reversed", "billing", "member", member.ID, map[string]any{"invoice_id": invoiceID})
	return nil
}

func membershipPeriod(member *Member, interval string, periods int, paidAt time.Time) (string, string) {
	start := paidAt.UTC()
	if member != nil && member.CurrentPeriodEnd != "" {
		if currentEnd, err := parseTimestamp(member.CurrentPeriodEnd); err == nil && currentEnd.After(start) {
			start = currentEnd
		}
	}
	if interval == "one_time" {
		return start.Format(time.RFC3339), ""
	}
	end := start
	if interval == "year" {
		end = end.AddDate(periods, 0, 0)
	} else {
		end = end.AddDate(0, periods, 0)
	}
	return start.Format(time.RFC3339), end.Format(time.RFC3339)
}

func runCreatorLifecycle(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("creators lifecycle worker has no app context")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE membership_payments SET status='failed', updated_at=CURRENT_TIMESTAMP
		WHERE status='creating' AND billing_invoice_id IS NULL AND datetime(updated_at) <= datetime('now', '-10 minutes')`); err != nil {
		return err
	}
	type postRef struct {
		id, spaceID int64
		pid         string
	}
	rows, err := ctx.AppDB().Query(`SELECT id, project_id, space_id FROM posts
		WHERE status='scheduled' AND scheduled_at IS NOT NULL AND datetime(scheduled_at) <= datetime('now')`)
	if err != nil {
		return err
	}
	var posts []postRef
	for rows.Next() {
		var p postRef
		if err := rows.Scan(&p.id, &p.pid, &p.spaceID); err != nil {
			rows.Close()
			return err
		}
		posts = append(posts, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range posts {
		res, err := ctx.AppDB().Exec(`UPDATE posts SET status='published', published_at=CURRENT_TIMESTAMP, scheduled_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND space_id=? AND status='scheduled'`, p.id, p.pid, p.spaceID)
		if err != nil {
			ctx.Logger().Warn("scheduled creator post failed", "post_id", p.id, "err", err.Error())
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			_ = logEvent(ctx, p.pid, p.spaceID, "post.published", "scheduler", "post", p.id, nil)
		}
	}

	type memberRef struct {
		id, spaceID int64
		pid         string
	}
	rows, err = ctx.AppDB().Query(`SELECT id, project_id, space_id FROM members
		WHERE status='active' AND current_period_end IS NOT NULL AND datetime(current_period_end) <= datetime('now')`)
	if err != nil {
		return err
	}
	var members []memberRef
	for rows.Next() {
		var m memberRef
		if err := rows.Scan(&m.id, &m.pid, &m.spaceID); err != nil {
			rows.Close()
			return err
		}
		members = append(members, m)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, ref := range members {
		res, err := ctx.AppDB().Exec(`UPDATE members SET status='past_due', updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND space_id=? AND status='active'`, ref.id, ref.pid, ref.spaceID)
		if err != nil {
			ctx.Logger().Warn("membership expiration failed", "member_id", ref.id, "err", err.Error())
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			member, _ := getMember(ctx.AppDB(), ref.pid, ref.spaceID, ref.id)
			_ = syncCRMState(ctx, ref.pid, member)
			_ = logEvent(ctx, ref.pid, ref.spaceID, "membership.expired", "scheduler", "member", ref.id, nil)
		}
	}
	return nil
}

func membershipMetrics(db *sql.DB, pid string, spaceID int64) (map[string]any, error) {
	rows, err := db.Query(`SELECT t.currency, t.price_cents, t.interval
		FROM members m JOIN tiers t ON t.id=m.tier_id AND t.space_id=m.space_id
		WHERE m.project_id=? AND m.space_id=? AND m.status='active'
		AND (m.current_period_end IS NULL OR datetime(m.current_period_end) > datetime('now'))
		AND EXISTS (SELECT 1 FROM membership_payments p
			WHERE p.project_id=m.project_id AND p.space_id=m.space_id
			AND p.member_id=m.id AND p.tier_id=t.id AND p.status='paid')`, pid, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byCurrency := map[string]int64{}
	paidMembers := 0
	for rows.Next() {
		var currency, interval string
		var price int64
		if err := rows.Scan(&currency, &price, &interval); err != nil {
			return nil, err
		}
		switch interval {
		case "month":
			byCurrency[currency] += price
			paidMembers++
		case "year":
			byCurrency[currency] += (price + 6) / 12
			paidMembers++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"paid_recurring_members": paidMembers, "mrr_by_currency": byCurrency}, nil
}

func syncBillingCustomer(ctx *sdk.AppCtx, pid string, member *Member) (int64, error) {
	var out struct {
		Customer struct {
			ID int64 `json:"id"`
		} `json:"customer"`
		WasCreated bool `json:"was_created"`
	}
	defaults := map[string]any{}
	if member.DisplayName != "" {
		defaults["name"] = member.DisplayName
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "customers_upsert_by_email", map[string]any{
		"email":       member.Email,
		"defaults":    defaults,
		"_project_id": pid,
	}, &out); err != nil {
		return 0, err
	}
	return out.Customer.ID, nil
}

func syncCRMContact(ctx *sdk.AppCtx, pid string, member *Member) (int64, error) {
	var out struct {
		Contact struct {
			ID int64 `json:"id"`
		} `json:"contact"`
		WasCreated bool `json:"was_created"`
	}
	defaults := map[string]any{}
	if member.DisplayName != "" {
		defaults["display_name"] = member.DisplayName
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", map[string]any{
		"kind":        "email",
		"value":       member.Email,
		"defaults":    defaults,
		"source":      "creators",
		"_project_id": pid,
	}, &out); err != nil {
		return 0, err
	}
	copy := *member
	copy.CRMContactID = &out.Contact.ID
	if err := syncCRMState(ctx, pid, &copy); err != nil {
		return 0, err
	}
	return out.Contact.ID, nil
}

func syncCRMState(ctx *sdk.AppCtx, pid string, member *Member) error {
	if ctx == nil || member == nil || member.CRMContactID == nil || *member.CRMContactID <= 0 {
		return nil
	}
	if err := ensureCRMStateAttributes(ctx, pid, member.SpaceID); err != nil {
		return err
	}
	var out map[string]any
	return ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_update", map[string]any{
		"id":          *member.CRMContactID,
		"patch":       map[string]any{"attributes": crmStateAttributes(member)},
		"source":      "creators",
		"_project_id": pid,
	}, &out)
}

func ensureCRMStateAttributes(ctx *sdk.AppCtx, pid string, spaceID int64) error {
	prefix := fmt.Sprintf("creators_space_%d_", spaceID)
	definitions := []struct {
		key   string
		label string
	}{
		{prefix + "status", fmt.Sprintf("Creators space %d status", spaceID)},
		{prefix + "tier_id", fmt.Sprintf("Creators space %d tier ID", spaceID)},
		{prefix + "period_end", fmt.Sprintf("Creators space %d period end", spaceID)},
	}
	for i, definition := range definitions {
		var out map[string]any
		if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_define_attribute", map[string]any{
			"key": definition.key, "label": definition.label, "type": "text",
			"sort_order": 1000 + i, "_project_id": pid,
		}, &out); err != nil {
			return fmt.Errorf("crm.contacts_define_attribute %s: %w", definition.key, err)
		}
	}
	return nil
}

func crmStateAttributes(member *Member) []any {
	tierID := ""
	if member.TierID != nil {
		tierID = strconv.FormatInt(*member.TierID, 10)
	}
	prefix := fmt.Sprintf("creators_space_%d_", member.SpaceID)
	return []any{
		map[string]any{"key": prefix + "status", "value": member.Status, "source": "creators"},
		map[string]any{"key": prefix + "tier_id", "value": tierID, "source": "creators"},
		map[string]any{"key": prefix + "period_end", "value": member.CurrentPeriodEnd, "source": "creators"},
	}
}

func sendPostUpdate(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (map[string]any, error) {
	post, err := getPost(ctx.AppDB(), pid, spaceID, int64Arg(args, "post_id"), false)
	if err != nil || post == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("post not found")
	}
	members, err := eligibleMembersForPost(ctx.AppDB(), pid, spaceID, post)
	if err != nil {
		return nil, err
	}
	subject := strArg(args, "subject")
	if subject == "" {
		subject = post.Title
	}
	body := strArg(args, "intro")
	if body != "" {
		body += "\n\n"
	}
	body += post.Body
	if boolArg(args, "dry_run") {
		return map[string]any{"dry_run": true, "eligible": len(members), "subject": subject}, nil
	}
	sent := 0
	errs := []string{}
	for _, m := range members {
		var out map[string]any
		err := ctx.WithProject(pid).PlatformAPI().CallAppResult("messaging", "send_message", map[string]any{
			"to":              "mailto:" + m.Email,
			"subject":         subject,
			"body":            body,
			"from":            configString(ctx, "sender", ""),
			"idempotency_key": fmt.Sprintf("creators-post-%d-member-%d-version-%s", post.ID, m.ID, compactKey(post.UpdatedAt)),
			"_project_id":     pid,
		}, &out)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", m.Email, err))
			continue
		}
		sent++
	}
	_ = logEvent(ctx, pid, spaceID, "post_update.sent", "agent", "post", post.ID, map[string]any{"sent": sent, "errors": len(errs)})
	return map[string]any{"eligible": len(members), "sent": sent, "errors": errs}, nil
}

func eligibleMembersForPost(db *sql.DB, pid string, spaceID int64, post *Post) ([]Member, error) {
	rows, err := db.Query(`SELECT `+memberColumns()+` FROM members
		WHERE project_id=? AND space_id=? AND status IN ('active','comped') ORDER BY id`, pid, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		if memberCanAccessPost(member, post) {
			out = append(out, *member)
		}
	}
	return out, rows.Err()
}

func logEvent(ctx *sdk.AppCtx, pid string, spaceID int64, kind, actor, subjectType string, subjectID int64, data any) error {
	raw, _ := json.Marshal(data)
	_, err := ctx.AppDB().Exec(
		`INSERT INTO creator_events (project_id, space_id, kind, actor, subject_type, subject_id, data_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, spaceID, kind, actor, subjectType, subjectID, string(raw),
	)
	if err == nil {
		ctx.WithProject(pid).Emit(kind, map[string]any{"subject_type": subjectType, "subject_id": subjectID, "data": data})
	}
	return err
}

func listEvents(db *sql.DB, pid string, spaceID int64, limit int) ([]map[string]any, error) {
	limit = clampLimit(limit, 50, 200)
	rows, err := db.Query(`SELECT id, kind, actor, subject_type, subject_id, data_json, created_at FROM creator_events WHERE project_id=? AND space_id=? ORDER BY id DESC LIMIT ?`, pid, spaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, subjectID int64
		var kind, actor, subjectType, dataJSON, createdAt string
		if err := rows.Scan(&id, &kind, &actor, &subjectType, &subjectID, &dataJSON, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "kind": kind, "actor": actor, "subject_type": subjectType,
			"subject_id": subjectID, "data": jsonObject(dataJSON), "created_at": createdAt,
		})
	}
	return out, rows.Err()
}

// ─── Helpers ──────────────────────────────────────────────────────

func contextForRequest(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func projectFromRequest(ctx *sdk.AppCtx, r *http.Request) (string, error) {
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		return pid, nil
	}
	if pid := r.Header.Get("X-Apteva-Project-ID"); pid != "" {
		return pid, nil
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject(), nil
	}
	return "", errors.New("project_id required for global creators installs")
}

func projectFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if pid := strArg(args, "_project_id"); pid != "" {
		return pid, nil
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject(), nil
	}
	return "", errors.New("_project_id required for global creators installs")
}

func spaceFromRequest(ctx *sdk.AppCtx, pid string, r *http.Request) (*Space, error) {
	if sid := parseInt64(r.URL.Query().Get("space_id")); sid > 0 {
		s, err := getSpace(ctx.AppDB(), pid, sid)
		if err != nil || s != nil {
			return s, err
		}
		return nil, fmt.Errorf("space %d not found", sid)
	}
	if slug := strings.TrimSpace(r.URL.Query().Get("space_slug")); slug != "" {
		s, err := getSpaceBySlug(ctx.AppDB(), pid, slug)
		if err != nil || s != nil {
			return s, err
		}
		return nil, fmt.Errorf("space %q not found", slug)
	}
	return ensureSpace(ctx, pid)
}

func spaceFromArgs(ctx *sdk.AppCtx, pid string, args map[string]any) (*Space, error) {
	if sid := int64Arg(args, "space_id"); sid > 0 {
		s, err := getSpace(ctx.AppDB(), pid, sid)
		if err != nil || s != nil {
			return s, err
		}
		return nil, fmt.Errorf("space %d not found", sid)
	}
	if slug := strArg(args, "space_slug"); slug != "" {
		s, err := getSpaceBySlug(ctx.AppDB(), pid, slug)
		if err != nil || s != nil {
			return s, err
		}
		return nil, fmt.Errorf("space %q not found", slug)
	}
	return ensureSpace(ctx, pid)
}

func readJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(nil, r.Body, 36<<20)
	return json.NewDecoder(r.Body).Decode(out)
}

func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func idFromPath(path, prefix string) (int64, error) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return 0, errors.New("id required")
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("valid id required")
	}
	return id, nil
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func spaceSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	props["space_id"] = sInteger()
	props["space_slug"] = sString()
	return schemaObject(props, required)
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }
func sBool() map[string]any    { return map[string]any{"type": "boolean"} }
func sObject() map[string]any  { return map[string]any{"type": "object"} }
func sArray(t string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": t}}
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func cleanString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolArg(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, _ := m[key].(bool)
	return v
}

func intArg(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return def
	}
}

func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func parseInt(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func parseInt64(raw string) int64 {
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "item"
	}
	return s
}

func randomToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func rawJSON(s, fallback string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		s = fallback
	}
	if !json.Valid([]byte(s)) {
		s = fallback
	}
	return json.RawMessage(s)
}

func jsonObject(s string) map[string]any {
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func jsonArray(v any) json.RawMessage {
	switch x := v.(type) {
	case []string:
		raw, _ := json.Marshal(x)
		return raw
	case []any:
		raw, _ := json.Marshal(x)
		return raw
	default:
		return json.RawMessage("[]")
	}
}

func jsonIntArray(v any) json.RawMessage {
	var out []int64
	switch x := v.(type) {
	case []int64:
		out = x
	case []int:
		for _, n := range x {
			out = append(out, int64(n))
		}
	case []any:
		for _, item := range x {
			switch n := item.(type) {
			case float64:
				out = append(out, int64(n))
			case int:
				out = append(out, int64(n))
			case int64:
				out = append(out, n)
			}
		}
	}
	raw, _ := json.Marshal(out)
	return raw
}

func validInterval(s string) bool {
	switch s {
	case "month", "year", "one_time":
		return true
	default:
		return false
	}
}

func validCurrency(s string) bool {
	return regexp.MustCompile(`^[A-Z]{3}$`).MatchString(s)
}

func parseTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("timestamp must be RFC3339")
}

func validateTierIDs(db *sql.DB, pid string, spaceID int64, raw json.RawMessage) error {
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return errors.New("tier_ids must be an array of integers")
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return errors.New("tier_ids must contain unique positive IDs")
		}
		seen[id] = true
		tier, err := getTier(db, pid, spaceID, id)
		if err != nil {
			return err
		}
		if tier == nil {
			return fmt.Errorf("tier %d does not belong to this creator space", id)
		}
	}
	return nil
}

func int64FromAny(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

func nullableInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func compactKey(value string) string {
	value = regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(value, "")
	if value == "" {
		return "initial"
	}
	return value
}

func validMemberStatus(s string) bool {
	switch s {
	case "lead", "active", "past_due", "paused", "cancelled", "comped":
		return true
	default:
		return false
	}
}

func validPostStatus(s string) bool {
	switch s {
	case "draft", "published", "scheduled", "archived":
		return true
	default:
		return false
	}
}

func validVisibility(s string) bool {
	switch s {
	case "public", "members", "tier", "private":
		return true
	default:
		return false
	}
}

func validAttachmentVisibility(s string) bool {
	switch s {
	case "inherit", "public", "members", "tier", "private":
		return true
	default:
		return false
	}
}

func redactMember(m *Member) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"id": m.ID, "email": m.Email, "display_name": m.DisplayName,
		"status": m.Status, "tier_id": m.TierID,
		"current_period_end": m.CurrentPeriodEnd,
	}
}

func redactMembers(in []Member) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for i := range in {
		out = append(out, redactMember(&in[i]))
	}
	return out
}

func main() {
	sdk.Run(&App{})
}
