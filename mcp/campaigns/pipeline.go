// CRUD, lifecycle, send pipeline, public unsubscribe.
//
// Pipeline summary:
//   schedule         — write campaigns.scheduled_at + jobs once-job
//                      pointed at /materialise
//   materialise      — call crm.segments_eval (or list materialise),
//                      bulk-insert recipients, transition to 'sending',
//                      schedule jobs every-job pointed at /tick
//   tick             — claim batch, send each via messaging.send_message,
//                      mark sent/failed; when zero pending, cancel tick
//                      job and transition to 'sent'
//   pause / resume   — cancel / re-create the tick job
//   cancel           — cancel both jobs, mark pending recipients skipped

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Authoring tools ──────────────────────────────────────────────

func (a *App) toolCampaignsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c := &Campaign{
		Name:                strArg(args, "name"),
		Description:         strArg(args, "description"),
		Channel:             strings.ToLower(strings.TrimSpace(strArg(args, "channel"))),
		SenderAddress:       strArg(args, "sender_address"),
		Subject:             strArg(args, "subject"),
		BodyText:            strArg(args, "body_text"),
		BodyHTML:            strArg(args, "body_html"),
		TemplateName:        strArg(args, "template_name"),
		BatchSize:           int64(intArg(args, "batch_size", 0)),
		TickIntervalSeconds: int64(intArg(args, "tick_interval_seconds", 0)),
		OpenTracking:        boolArg(args, "open_tracking"),
		ClickTracking:       boolArg(args, "click_tracking"),
	}
	if listID := int64Arg(args, "list_id"); listID != 0 {
		c.ListID = &listID
	}
	if segmentID := int64Arg(args, "segment_id"); segmentID != 0 {
		c.SegmentID = &segmentID
	}
	out, err := dbCampaignCreate(ctx.AppDB(), pid, c)
	if err != nil {
		return nil, err
	}
	ctx.Emit("campaign.created", map[string]any{"id": out.ID, "name": out.Name})
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbCampaignsList(ctx.AppDB(), pid,
		strArg(args, "status"), strArg(args, "channel"),
		boolArg(args, "include_archived"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"campaigns": out, "count": len(out)}, nil
}

func (a *App) toolCampaignsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return map[string]any{"campaign": nil, "found": false}, nil
	}
	stats, _ := dbRecipientStats(ctx.AppDB(), pid, id)
	c.Stats = stats
	return map[string]any{"campaign": c, "found": true}, nil
}

func (a *App) toolCampaignsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch object required")
	}
	current, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("campaign not found")
	}
	if !validStatusForUpdate(current.Status) {
		return nil, fmt.Errorf("campaign is %s — only draft / paused campaigns can be edited", current.Status)
	}
	out, err := dbCampaignUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	ctx.Emit("campaign.updated", map[string]any{"id": id})
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsClone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	src, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, errors.New("campaign not found")
	}
	clone := *src
	clone.ID = 0
	clone.Status = StatusDraft
	clone.JobIDs = ""
	clone.StartedAt = ""
	clone.CompletedAt = ""
	clone.ArchivedAt = ""
	clone.Error = ""
	clone.ScheduledAt = ""
	if name := strArg(args, "name"); name != "" {
		clone.Name = name
	} else {
		clone.Name = src.Name + " (copy)"
	}
	out, err := dbCampaignCreate(ctx.AppDB(), pid, &clone)
	if err != nil {
		return nil, err
	}
	ctx.Emit("campaign.created", map[string]any{"id": out.ID, "name": out.Name})
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbCampaignArchive(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	ctx.Emit("campaign.archived", map[string]any{"id": id})
	return map[string]any{"archived": true, "id": id}, nil
}

func (a *App) toolCampaignsSendTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "id")
	contactID := int64Arg(args, "contact_id")
	if cid == 0 || contactID == 0 {
		return nil, errors.New("id and contact_id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("campaign not found")
	}
	// Hand the test through CRM's contacts_send_test tool — keeps the
	// activity-logging + suppression checks consistent with normal sends.
	result, err := callCRMResult(ctx, "contacts_send_test", map[string]any{
		"_project_id": pid,
		"id":          contactID,
		"channel":     c.Channel,
		"subject":     c.Subject,
		"body":        c.BodyText,
		"body_html":   c.BodyHTML,
		"from":        c.SenderAddress, // empty falls through to install default
	})
	if err != nil {
		return nil, fmt.Errorf("crm.contacts_send_test: %w", err)
	}
	return result, nil
}

// ─── Lifecycle tools ──────────────────────────────────────────────

func (a *App) toolCampaignsSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	scheduledAt := strArg(args, "scheduled_at")
	if id == 0 || scheduledAt == "" {
		return nil, errors.New("id and scheduled_at required")
	}
	if _, err := time.Parse(time.RFC3339, scheduledAt); err != nil {
		return nil, fmt.Errorf("scheduled_at must be RFC3339: %w", err)
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("campaign not found")
	}
	if c.Status != StatusDraft {
		return nil, fmt.Errorf("campaign is %s; only draft can be scheduled", c.Status)
	}
	if c.SegmentID == nil && c.ListID == nil {
		return nil, errors.New("campaign needs a segment_id or list_id before scheduling")
	}
	// Schedule the materialise job in jobs.
	jobID, err := scheduleMaterialiseJob(ctx, pid, c, scheduledAt)
	if err != nil {
		return nil, fmt.Errorf("schedule materialise: %w", err)
	}
	if _, err := dbCampaignUpdate(ctx.AppDB(), pid, id, map[string]any{
		"scheduled_at": scheduledAt,
	}); err != nil {
		return nil, err
	}
	if err := dbCampaignSetJobIDs(ctx.AppDB(), pid, id, fmt.Sprintf("%d", jobID)); err != nil {
		return nil, err
	}
	out, _ := dbCampaignSetStatus(ctx.AppDB(), pid, id, StatusScheduled, "", false, false)
	ctx.Emit("campaign.scheduled", map[string]any{"id": id, "scheduled_at": scheduledAt, "job_id": jobID})
	return map[string]any{"campaign": out, "job_id": jobID}, nil
}

func (a *App) toolCampaignsStartNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("campaign not found")
	}
	if c.Status != StatusDraft && c.Status != StatusScheduled {
		return nil, fmt.Errorf("campaign is %s; only draft / scheduled can be started", c.Status)
	}
	if c.SegmentID == nil && c.ListID == nil {
		return nil, errors.New("campaign needs a segment_id or list_id before starting")
	}
	// Cancel any pre-scheduled materialise job — we're firing manually.
	cancelOwnedJobs(ctx, pid, c)
	// Run materialise inline (synchronously) to give immediate feedback.
	if err := materialiseCampaign(ctx, pid, c); err != nil {
		return nil, fmt.Errorf("materialise: %w", err)
	}
	out, _ := dbCampaignGet(ctx.AppDB(), pid, id)
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("campaign not found")
	}
	if c.Status != StatusSending {
		return nil, fmt.Errorf("campaign is %s; only sending can be paused", c.Status)
	}
	cancelOwnedJobs(ctx, pid, c)
	out, _ := dbCampaignSetStatus(ctx.AppDB(), pid, id, StatusPaused, "", false, false)
	_ = dbCampaignSetJobIDs(ctx.AppDB(), pid, id, "")
	ctx.Emit("campaign.paused", map[string]any{"id": id})
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil || c.Status != StatusPaused {
		return nil, fmt.Errorf("only paused campaigns can be resumed")
	}
	if err := startTickJob(ctx, pid, c); err != nil {
		return nil, fmt.Errorf("start tick job: %w", err)
	}
	out, _ := dbCampaignSetStatus(ctx.AppDB(), pid, id, StatusSending, "", false, false)
	ctx.Emit("campaign.resumed", map[string]any{"id": id})
	return map[string]any{"campaign": out}, nil
}

func (a *App) toolCampaignsCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCampaignGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("campaign not found")
	}
	if c.Status == StatusSent || c.Status == StatusCancelled {
		return nil, fmt.Errorf("campaign is %s; no-op", c.Status)
	}
	cancelOwnedJobs(ctx, pid, c)
	skipped, _ := dbRecipientCancelPending(ctx.AppDB(), pid, id)
	out, _ := dbCampaignSetStatus(ctx.AppDB(), pid, id, StatusCancelled, "cancelled by user", false, true)
	_ = dbCampaignSetJobIDs(ctx.AppDB(), pid, id, "")
	ctx.Emit("campaign.cancelled", map[string]any{"id": id, "skipped": skipped})
	return map[string]any{"campaign": out, "skipped_pending": skipped}, nil
}

func (a *App) toolCampaignsRecipients(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	out, err := dbRecipientsList(ctx.AppDB(), pid, id, strArg(args, "status"), intArg(args, "limit", 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"recipients": out, "count": len(out)}, nil
}

func (a *App) toolCampaignsStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	stats, err := dbRecipientStats(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stats": stats}, nil
}

// ─── Send pipeline core ───────────────────────────────────────────

// materialiseCampaign expands the audience into campaign_recipients
// rows and transitions the campaign to 'sending'. Idempotent on
// re-run (INSERT OR IGNORE on the unique campaign_id+contact_id).
//
// Pre-flight suppression: each address is checked via
// messaging.suppression_check. Suppressed addresses land as 'skipped'
// rather than 'pending', so the tick loop never tries to send them.
func materialiseCampaign(ctx *sdk.AppCtx, pid string, c *Campaign) error {
	if c == nil {
		return errors.New("nil campaign")
	}
	if _, err := dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusMaterialising, "", true, false); err != nil {
		return err
	}

	// Resolve audience: prefer segment, fall back to list.
	var contactIDs []int64
	var err error
	if c.SegmentID != nil && *c.SegmentID != 0 {
		contactIDs, err = evalSegment(ctx, pid, *c.SegmentID)
	} else if c.ListID != nil && *c.ListID != 0 {
		contactIDs, err = listMembers(ctx, pid, *c.ListID)
	} else {
		err = errors.New("no audience source")
	}
	if err != nil {
		_, _ = dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusFailed, fmtError("audience: %s", err.Error()), false, true)
		return err
	}
	if len(contactIDs) == 0 {
		_, _ = dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusSent, "no recipients", false, true)
		return nil
	}

	// Resolve each contact's address by calling crm.contacts_get for
	// each. Cheap enough for v0.1 audiences (≤ a few thousand). v0.2
	// can add a bulk crm.contacts_addresses_for_channel tool to do
	// this in one round-trip.
	var rows []Recipient
	for _, cid := range contactIDs {
		addr, ok := contactAddressForChannel(ctx, pid, cid, c.Channel)
		if !ok {
			rows = append(rows, Recipient{ContactID: cid, Address: "", Status: RecipSkipped, Error: "no " + c.Channel + " address"})
			continue
		}
		// Pre-flight suppression check via messaging.
		if suppressed, reason := suppressionCheck(ctx, c.Channel, addr); suppressed {
			rows = append(rows, Recipient{ContactID: cid, Address: addr, Status: RecipSkipped, Error: "suppressed: " + reason})
			continue
		}
		rows = append(rows, Recipient{ContactID: cid, Address: addr, Status: RecipPending})
	}
	if _, err := runRecipientBulk(ctx, pid, c.ID, rows); err != nil {
		_, _ = dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusFailed, fmtError("insert recipients: %s", err.Error()), false, true)
		return err
	}

	// Schedule the tick job.
	if err := startTickJob(ctx, pid, c); err != nil {
		_, _ = dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusFailed, fmtError("start tick: %s", err.Error()), false, true)
		return err
	}
	if _, err := dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusSending, "", false, false); err != nil {
		return err
	}
	ctx.Emit("campaign.sending", map[string]any{"id": c.ID, "audience_size": len(contactIDs)})
	return nil
}

// runRecipientBulk inserts the materialise output. Differs from
// dbRecipientsBulkInsert in that it preserves per-row status + error
// strings (so pre-flight skips show up immediately).
func runRecipientBulk(ctx *sdk.AppCtx, pid string, campaignID int64, rows []Recipient) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO campaign_recipients
			(campaign_id, project_id, contact_id, address, status, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var inserted int64
	for _, r := range rows {
		var errArg any
		if r.Error != "" {
			errArg = truncate(r.Error, 500)
		}
		res, err := stmt.Exec(campaignID, pid, r.ContactID, r.Address, r.Status, errArg)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// tickCampaign claims a batch and sends each. When the queue is
// empty we cancel the tick job and transition to 'sent'.
func tickCampaign(ctx *sdk.AppCtx, pid string, c *Campaign) error {
	if c == nil {
		return errors.New("nil campaign")
	}
	if c.Status != StatusSending {
		// Pause / cancel happened between scheduling and tick — no-op.
		return nil
	}
	batch := int(c.BatchSize)
	if batch <= 0 {
		batch = 100
	}
	claimed, err := dbRecipientClaimBatch(ctx.AppDB(), pid, c.ID, batch)
	if err != nil {
		return err
	}
	for _, r := range claimed {
		out, sendErr := callMessagingSend(ctx, map[string]any{
			"_project_id":     pid,
			"channel":         c.Channel,
			"to":              r.Address,
			"subject":         c.Subject,
			"body":            renderRecipientBody(c.BodyText, c, r),
			"body_html":       renderRecipientBody(c.BodyHTML, c, r),
			"from":            c.SenderAddress,
			"idempotency_key": fmt.Sprintf("campaign:%d:recipient:%d", c.ID, r.ID),
			"template_name":   c.TemplateName,
		})
		if sendErr != nil {
			_ = dbRecipientMarkFailed(ctx.AppDB(), pid, r.ID, sendErr.Error())
			ctx.Logger().Warn("campaign tick: send failed",
				"campaign_id", c.ID, "recipient_id", r.ID, "err", sendErr.Error())
			continue
		}
		messagingID, _ := out["id"].(float64)
		_ = dbRecipientMarkSent(ctx.AppDB(), pid, r.ID, int64(messagingID))
	}

	// Wind down. When pending == 0 and nothing's still in 'sending'
	// (claimed but not finalised), we're done.
	pending, _ := dbRecipientCountPending(ctx.AppDB(), pid, c.ID)
	if pending == 0 {
		cancelOwnedJobs(ctx, pid, c)
		_, _ = dbCampaignSetStatus(ctx.AppDB(), pid, c.ID, StatusSent, "", false, true)
		_ = dbCampaignSetJobIDs(ctx.AppDB(), pid, c.ID, "")
		ctx.Emit("campaign.sent", map[string]any{"id": c.ID})
	}
	return nil
}

// renderRecipientBody — v0.1 placeholder. Today we just pass the body
// through verbatim. v0.2 will inject the unsubscribe URL into HTML
// bodies and substitute {{first_name}}-style merge tags.
func renderRecipientBody(body string, _ *Campaign, _ Recipient) string {
	return body
}

// ─── Cross-app helpers (CRM / messaging / jobs) ───────────────────

// evalSegment calls crm.segments_eval and returns the contact ids.
// For static segments this reads the snapshot; for dynamic it
// re-evaluates. Caller decides whether to materialise first.
func evalSegment(ctx *sdk.AppCtx, pid string, segID int64) ([]int64, error) {
	var out struct {
		ContactIDs []int64 `json:"contact_ids"`
		Count      int64   `json:"count"`
	}
	if err := ctx.PlatformAPI().CallAppResult("crm", "segments_eval",
		map[string]any{"_project_id": pid, "id": segID, "limit": 50000}, &out); err != nil {
		return nil, err
	}
	return out.ContactIDs, nil
}

// listMembers fetches the contact_ids of every active member of a
// list via crm.lists_eval. Mirrors evalSegment so materialise can
// branch on which audience source the campaign was given without
// reshaping its result.
func listMembers(ctx *sdk.AppCtx, pid string, listID int64) ([]int64, error) {
	var out struct {
		ContactIDs []int64 `json:"contact_ids"`
		Count      int64   `json:"count"`
	}
	if err := ctx.PlatformAPI().CallAppResult("crm", "lists_eval",
		map[string]any{"_project_id": pid, "id": listID, "limit": 50000}, &out); err != nil {
		return nil, fmt.Errorf("crm.lists_eval: %w", err)
	}
	return out.ContactIDs, nil
}

// contactAddressForChannel asks CRM for one contact's address on the
// given channel. Returns ("", false) if the contact has no usable
// address — caller marks the recipient 'skipped'.
func contactAddressForChannel(ctx *sdk.AppCtx, pid string, contactID int64, channel string) (string, bool) {
	var out struct {
		Contact struct {
			PrimaryEmail string `json:"primary_email"`
			PrimaryPhone string `json:"primary_phone"`
			Channels     []struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			} `json:"channels"`
		} `json:"contact"`
	}
	if err := ctx.PlatformAPI().CallAppResult("crm", "contacts_get",
		map[string]any{"_project_id": pid, "id": contactID}, &out); err != nil {
		return "", false
	}
	switch channel {
	case ChannelEmail:
		if out.Contact.PrimaryEmail != "" {
			return out.Contact.PrimaryEmail, true
		}
		for _, ch := range out.Contact.Channels {
			if ch.Kind == "email" && ch.Value != "" {
				return ch.Value, true
			}
		}
	case ChannelSMS, ChannelWhatsApp:
		if out.Contact.PrimaryPhone != "" {
			return out.Contact.PrimaryPhone, true
		}
		for _, ch := range out.Contact.Channels {
			if ch.Kind == "phone" && ch.Value != "" {
				return ch.Value, true
			}
		}
	}
	return "", false
}

// callMessagingSend invokes messaging.send_message and returns the
// unwrapped JSON-RPC envelope payload as a map.
func callMessagingSend(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("messaging", "send_message", args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// suppressionCheck calls messaging.suppression_check. Older messaging
// versions (no tool) treated as not-suppressed; messaging itself does
// the check inside send_message anyway as a backstop.
func suppressionCheck(ctx *sdk.AppCtx, channel, address string) (bool, string) {
	var out struct {
		Suppressed bool   `json:"suppressed"`
		Reason     string `json:"reason"`
	}
	if err := ctx.PlatformAPI().CallAppResult("messaging", "suppression_check",
		map[string]any{"channel": channel, "address": address}, &out); err != nil {
		return false, ""
	}
	return out.Suppressed, out.Reason
}

// callCRMResult is a thin wrapper for crm tool invocations. Returns
// the unwrapped JSON-RPC payload as map[string]any.
func callCRMResult(ctx *sdk.AppCtx, tool string, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("crm", tool, args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// publicBaseURL returns the URL prefix tracking links use. Reads
// install config first, falls back to platform's reverse-proxy URL.
func publicBaseURL(ctx *sdk.AppCtx) string {
	if v := strings.TrimSpace(ctx.Config().Get("public_base_url")); v != "" {
		return strings.TrimRight(v, "/")
	}
	// Platform proxy is the safe default — the unsubscribe endpoint is
	// mounted under /api/apps/campaigns/.
	return "/api/apps/campaigns"
}

// ─── Jobs integration ─────────────────────────────────────────────

func scheduleMaterialiseJob(ctx *sdk.AppCtx, pid string, c *Campaign, scheduledAt string) (int64, error) {
	args := map[string]any{
		"_project_id": pid,
		"name":        fmt.Sprintf("campaign-%d-materialise", c.ID),
		"owner_app":   "campaigns",
		"schedule": map[string]any{
			"kind":   "once",
			"run_at": scheduledAt,
		},
		"target": map[string]any{
			"kind": "http",
			"app":  "campaigns",
			"path": fmt.Sprintf("/campaigns/%d/materialise", c.ID),
		},
		"max_retries": 3,
	}
	return callJobsSchedule(ctx, args)
}

func startTickJob(ctx *sdk.AppCtx, pid string, c *Campaign) error {
	tickInterval := int(c.TickIntervalSeconds)
	if tickInterval <= 0 {
		// Read install default config.
		if v := ctx.Config().Get("default_tick_seconds"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				tickInterval = n
			}
		}
	}
	if tickInterval <= 0 {
		tickInterval = 60
	}
	args := map[string]any{
		"_project_id": pid,
		"name":        fmt.Sprintf("campaign-%d-tick", c.ID),
		"owner_app":   "campaigns",
		"schedule": map[string]any{
			"kind":           "every",
			"every_seconds":  tickInterval,
		},
		"target": map[string]any{
			"kind": "http",
			"app":  "campaigns",
			"path": fmt.Sprintf("/campaigns/%d/tick", c.ID),
		},
		"max_retries": 3,
	}
	jobID, err := callJobsSchedule(ctx, args)
	if err != nil {
		return err
	}
	current, _ := dbCampaignGet(ctx.AppDB(), pid, c.ID)
	prefix := ""
	if current != nil && current.JobIDs != "" {
		prefix = current.JobIDs + ","
	}
	return dbCampaignSetJobIDs(ctx.AppDB(), pid, c.ID, prefix+fmt.Sprintf("%d", jobID))
}

// callJobsSchedule wraps the jobs MCP tool. Returns the job id.
func callJobsSchedule(ctx *sdk.AppCtx, args map[string]any) (int64, error) {
	var out struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_schedule", args, &out); err != nil {
		return 0, err
	}
	return out.Job.ID, nil
}

// cancelOwnedJobs cancels every job id stored in c.JobIDs. Best-
// effort; failure to cancel one doesn't block the others.
func cancelOwnedJobs(ctx *sdk.AppCtx, pid string, c *Campaign) {
	if c == nil || c.JobIDs == "" {
		return
	}
	for _, s := range strings.Split(c.JobIDs, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		if _, err := ctx.PlatformAPI().CallApp("jobs", "jobs_cancel",
			map[string]any{"_project_id": pid, "id": id}); err != nil {
			ctx.Logger().Warn("cancelOwnedJobs: cancel failed", "id", id, "err", err.Error())
		}
	}
}

// ─── HTTP wrappers ────────────────────────────────────────────────

func (a *App) handleHTTPCampaignsList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	includeArchived := q.Get("include_archived") == "1" || q.Get("include_archived") == "true"
	out, err := dbCampaignsList(globalCtx.AppDB(), pid, q.Get("status"), q.Get("channel"), includeArchived)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"campaigns": out, "count": len(out)})
}

func (a *App) handleHTTPCampaignsCreate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	body["_project_id"] = pid
	out, err := a.toolCampaignsCreate(globalCtx, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPCampaignGet(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := dbCampaignGet(globalCtx.AppDB(), pid, id)
	if err != nil || c == nil {
		httpErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	stats, _ := dbRecipientStats(globalCtx.AppDB(), pid, id)
	c.Stats = stats
	httpJSON(w, map[string]any{"campaign": c})
}

func (a *App) handleHTTPCampaignUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := a.toolCampaignsUpdate(globalCtx, map[string]any{"_project_id": pid, "id": id, "patch": patch})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPCampaignDelete(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbCampaignArchive(globalCtx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	globalCtx.Emit("campaign.archived", map[string]any{"id": id})
	httpJSON(w, map[string]any{"archived": true, "id": id})
}

func (a *App) handleHTTPRecipientsList(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := dbRecipientsList(globalCtx.AppDB(), pid, id, q.Get("status"), limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"recipients": out, "count": len(out)})
}

func (a *App) handleHTTPStats(w http.ResponseWriter, r *http.Request, id int64) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := dbRecipientStats(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"stats": stats})
}

// One-shot lifecycle wrappers — all just thin forwarders to the MCP
// handlers so the panel can use a unified API helper.

func (a *App) handleHTTPSchedule(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "schedule")
}
func (a *App) handleHTTPStartNow(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "start_now")
}
func (a *App) handleHTTPPause(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "pause")
}
func (a *App) handleHTTPResume(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "resume")
}
func (a *App) handleHTTPCancel(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "cancel")
}
func (a *App) handleHTTPSendTest(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "send_test")
}
func (a *App) handleHTTPClone(w http.ResponseWriter, r *http.Request, id int64) {
	a.runLifecycle(w, r, id, "clone")
}

func (a *App) runLifecycle(w http.ResponseWriter, r *http.Request, id int64, action string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	body["_project_id"] = pid
	body["id"] = id
	var out any
	switch action {
	case "schedule":
		out, err = a.toolCampaignsSchedule(globalCtx, body)
	case "start_now":
		out, err = a.toolCampaignsStartNow(globalCtx, body)
	case "pause":
		out, err = a.toolCampaignsPause(globalCtx, body)
	case "resume":
		out, err = a.toolCampaignsResume(globalCtx, body)
	case "cancel":
		out, err = a.toolCampaignsCancel(globalCtx, body)
	case "send_test":
		out, err = a.toolCampaignsSendTest(globalCtx, body)
	case "clone":
		out, err = a.toolCampaignsClone(globalCtx, body)
	default:
		httpErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── Internal pipeline endpoints (called by jobs) ─────────────────

func (a *App) handleHTTPMaterialise(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := dbCampaignGet(globalCtx.AppDB(), pid, id)
	if err != nil || c == nil {
		httpErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if c.Status != StatusScheduled && c.Status != StatusDraft {
		// Idempotent guard — jobs may retry.
		httpJSON(w, map[string]any{"ok": true, "status": c.Status, "noop": true})
		return
	}
	if err := materialiseCampaign(globalCtx, pid, c); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleHTTPTick(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := dbCampaignGet(globalCtx.AppDB(), pid, id)
	if err != nil || c == nil {
		httpErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if err := tickCampaign(globalCtx, pid, c); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}

// ─── Public unsubscribe ───────────────────────────────────────────
//
// Token is a 32-byte random string base64url-encoded (43 chars).
// Generated at materialise-time, persisted, and looked up here. No
// HMAC verification needed because the token itself is the secret.

func (a *App) handleHTTPUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	u, pid, err := dbUnsubscribeTokenLookup(globalCtx.AppDB(), token)
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if u == nil {
		http.Error(w, "invalid token", http.StatusNotFound)
		return
	}
	rec, err := dbRecipientByID(globalCtx.AppDB(), pid, u.RecipientID)
	if err != nil || rec == nil {
		http.Error(w, "recipient gone", http.StatusGone)
		return
	}
	if u.UsedAt == "" {
		_ = dbRecipientMarkUnsubscribed(globalCtx.AppDB(), pid, rec.ID)
		_ = dbUnsubscribeTokenMarkUsed(globalCtx.AppDB(), token)
		// Mirror to messaging's suppression list so future campaigns
		// from any sender skip this address.
		if _, err := globalCtx.PlatformAPI().CallApp("messaging", "suppression_add", map[string]any{
			"_project_id": pid,
			"address":     rec.Address,
			"reason":      "unsubscribe",
		}); err != nil {
			globalCtx.Logger().Warn("unsubscribe: suppression_add failed",
				"address", rec.Address, "err", err.Error())
		}
		globalCtx.Emit("campaign.unsubscribed", map[string]any{
			"campaign_id":  u.CampaignID,
			"recipient_id": u.RecipientID,
			"address":      rec.Address,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(unsubscribeConfirmationHTML(rec.Address)))
}

func unsubscribeConfirmationHTML(addr string) string {
	return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Unsubscribed</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;margin:0;padding:48px 24px;background:#fafafa;color:#222}
.card{max-width:520px;margin:0 auto;background:#fff;border:1px solid #e5e5e5;border-radius:8px;padding:32px}
h1{margin:0 0 12px;font-size:20px}
p{margin:8px 0;line-height:1.5;color:#555}
code{background:#f0f0f0;padding:2px 6px;border-radius:4px;font-size:13px}
</style></head><body>
<div class="card">
<h1>You're unsubscribed.</h1>
<p>We've removed <code>` + escapeHTML(addr) + `</code> from this list and won't send to it again.</p>
<p>If this was a mistake, reply to any prior message from us and we'll fix it.</p>
</div></body></html>`
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// generateUnsubscribeToken — random 32 bytes, base64url-no-padding.
// Written via dbUnsubscribeTokenCreate during materialise. (For v0.1
// tokens are actually issued lazily — at first send, not at
// materialise — so the unsubscribe footer can include them.)
func generateUnsubscribeToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}
