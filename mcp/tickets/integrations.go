package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolCreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	ticket, err := createTicket(ctx.AppDB(), pid, args, actor)
	if err != nil {
		return nil, err
	}
	ticket = bestEffortCRMLink(ctx, ticket, actor)
	decorateTicketURLs(ctx, ticket)
	emitTicket(ctx, "ticket.created", ticket, nil)
	return map[string]any{"ticket": ticket}, nil
}

func (a *App) toolList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	filter := TicketFilter{Q: stringArg(args, "q"), Status: stringArg(args, "status"), Area: stringArg(args, "area"), Type: stringArg(args, "type"), Priority: stringArg(args, "priority"), RequesterEmail: stringArg(args, "requester_email"), Limit: int(int64Arg(args, "limit")), Offset: int(int64Arg(args, "offset"))}
	tickets, total, err := listTickets(ctx.AppDB(), pid, filter)
	if err != nil {
		return nil, err
	}
	for _, t := range tickets {
		decorateTicketURLs(ctx, t)
	}
	return map[string]any{"tickets": tickets, "count": len(tickets), "total": total, "offset": filter.Offset}, nil
}

func (a *App) toolGet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	detail, err := ticketDetail(ctx.AppDB(), pid, int64Arg(args, "id"), true)
	if err != nil {
		return nil, err
	}
	decorateTicketURLs(ctx, detail.Ticket)
	decorateAttachmentURLs(ctx, detail.Ticket, detail.Attachments)
	return detail, nil
}

func (a *App) toolUpdate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	ticket, changes, err := updateTicket(ctx.AppDB(), pid, int64Arg(args, "id"), args, actor)
	if err != nil {
		return nil, err
	}
	if _, emailChanged := changes["requester_email"]; emailChanged {
		ticket = bestEffortCRMLink(ctx, ticket, actor)
	}
	decorateTicketURLs(ctx, ticket)
	if len(changes) > 0 {
		emitTicket(ctx, "ticket.updated", ticket, map[string]any{"changes": changes})
		bestEffortCRMActivity(ctx, ticket, fmt.Sprintf("%s updated", ticket.Key))
	}
	return map[string]any{"ticket": ticket, "changes": changes}, nil
}

func (a *App) toolSetStatus(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	before, err := getTicket(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	ticket, err := setTicketStatus(ctx.AppDB(), pid, before.ID, stringArg(args, "status"), stringArg(args, "reason"), actor)
	if err != nil {
		return nil, err
	}
	decorateTicketURLs(ctx, ticket)
	topic := "ticket.status.changed"
	if ticket.Status == "resolved" {
		topic = "ticket.resolved"
	}
	if (before.Status == "resolved" || before.Status == "closed") && ticket.Status != "resolved" && ticket.Status != "closed" {
		topic = "ticket.reopened"
	}
	emitTicket(ctx, topic, ticket, map[string]any{"from": before.Status, "to": ticket.Status, "reason": stringArg(args, "reason")})
	bestEffortCRMActivity(ctx, ticket, fmt.Sprintf("%s status: %s → %s", ticket.Key, before.Status, ticket.Status))
	return map[string]any{"ticket": ticket}, nil
}

func (a *App) toolComment(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.addCommentTool(callCtx, ctx, args, "public")
}
func (a *App) toolInternalNote(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.addCommentTool(callCtx, ctx, args, "internal")
}
func (a *App) addCommentTool(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any, visibility string) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	comment, err := addComment(ctx.AppDB(), pid, int64Arg(args, "id"), visibility, stringArg(args, "body"), actor)
	if err != nil {
		return nil, err
	}
	ticket, _ := getTicket(ctx.AppDB(), pid, comment.TicketID)
	topic := "ticket.commented"
	if visibility == "internal" {
		topic = "ticket.internal_note.added"
	}
	emitTicket(ctx, topic, ticket, map[string]any{"comment_id": comment.ID})
	if visibility == "public" {
		bestEffortCRMActivity(ctx, ticket, fmt.Sprintf("Public reply on %s: %s", ticket.Key, truncate(comment.Body, 180)))
	}
	return map[string]any{"comment": comment}, nil
}

func (a *App) toolEditComment(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	comment, err := editComment(ctx.AppDB(), pid, int64Arg(args, "id"), int64Arg(args, "comment_id"), stringArg(args, "body"), actor)
	if err != nil {
		return nil, err
	}
	return map[string]any{"comment": comment}, nil
}

func (a *App) toolAddAttachment(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	attachment, err := addAttachment(ctx, pid, int64Arg(args, "id"), args, actor)
	if err != nil {
		return nil, err
	}
	ticket, _ := getTicket(ctx.AppDB(), pid, attachment.TicketID)
	decorateTicketURLs(ctx, ticket)
	decorateAttachmentURLs(ctx, ticket, []*Attachment{attachment})
	emitTicket(ctx, "ticket.attachment.added", ticket, map[string]any{"attachment_id": attachment.ID, "name": attachment.Name})
	return map[string]any{"attachment": attachment}, nil
}

func (a *App) toolLink(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorFrom(callCtx, args, "agent")
	link, err := addLink(ctx.AppDB(), pid, int64Arg(args, "id"), args, actor)
	if err != nil {
		return nil, err
	}
	ticket, _ := getTicket(ctx.AppDB(), pid, link.TicketID)
	emitTicket(ctx, "ticket.link.added", ticket, map[string]any{"link_id": link.ID, "kind": link.Kind})
	return map[string]any{"link": link}, nil
}

func (a *App) toolHistory(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	events, err := listEvents(ctx.AppDB(), pid, int64Arg(args, "id"), boolArg(args, "include_internal"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events, "count": len(events)}, nil
}
func (a *App) toolAreasList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	areas, err := listAreas(ctx.AppDB(), pid, boolArg(args, "include_archived"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"areas": areas}, nil
}
func (a *App) toolAreaCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	area, err := createArea(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"area": area}, nil
}
func (a *App) toolAreaUpdate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	area, err := updateArea(ctx.AppDB(), pid, int64Arg(args, "id"), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"area": area}, nil
}
func (a *App) toolPortal(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	portal, err := updatePortal(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	portal.IntakeURL = publicAppBase(ctx) + "/p/" + url.PathEscape(portal.Token)
	return map[string]any{"portal": portal}, nil
}

func bestEffortCRMLink(ctx *sdk.AppCtx, ticket *Ticket, actor Actor) *Ticket {
	if ctx == nil || ticket == nil || ctx.PlatformAPI() == nil {
		return ticket
	}
	if ticket.RequesterCRMContactID == nil && ticket.RequesterEmail != "" {
		var got struct {
			Contact struct {
				ID int64 `json:"id"`
			} `json:"contact"`
		}
		defaults := map[string]any{"display_name": ticket.RequesterName, "company": ticket.RequesterOrganization, "source": "tickets"}
		err := ctx.WithProject(ticket.ProjectID).PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", map[string]any{"kind": "email", "value": ticket.RequesterEmail, "defaults": defaults, "source": "tickets"}, &got)
		if err != nil {
			return ticket
		}
		if got.Contact.ID > 0 {
			_, _ = ctx.AppDB().Exec(`UPDATE tickets SET requester_crm_contact_id=?,updated_at=? WHERE project_id=? AND id=?`, got.Contact.ID, nowUTC(), ticket.ProjectID, ticket.ID)
			_ = appendEvent(ctx.AppDB(), ticket.ProjectID, ticket.ID, "integration.crm.linked", "internal", actor, map[string]any{"contact_id": got.Contact.ID})
			ticket, _ = getTicket(ctx.AppDB(), ticket.ProjectID, ticket.ID)
		}
	}
	bestEffortCRMActivity(ctx, ticket, fmt.Sprintf("%s created: %s", ticket.Key, ticket.Title))
	return ticket
}

func bestEffortCRMActivity(ctx *sdk.AppCtx, ticket *Ticket, body string) {
	if ctx == nil || ticket == nil || ticket.RequesterCRMContactID == nil || ctx.PlatformAPI() == nil {
		return
	}
	var out map[string]any
	_ = ctx.WithProject(ticket.ProjectID).PlatformAPI().CallAppResult("crm", "contacts_log_activity", map[string]any{"contact_id": *ticket.RequesterCRMContactID, "kind": "note", "body": body, "source": "tickets"}, &out)
}

func addAttachment(ctx *sdk.AppCtx, pid string, ticketID int64, args map[string]any, actor Actor) (*Attachment, error) {
	name := stringArg(args, "name")
	if name == "" {
		return nil, errors.New("name is required")
	}
	visibility := firstNonEmpty(stringArg(args, "visibility"), "public")
	fileID := stringArg(args, "file_id")
	fileURL := stringArg(args, "url")
	size := int64Arg(args, "size_bytes")
	contentType := firstNonEmpty(stringArg(args, "content_type"), "application/octet-stream")
	if content := stringArg(args, "content_base64"); content != "" {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, errors.New("content_base64 is invalid")
		}
		if len(decoded) > 10*1024*1024 {
			return nil, errors.New("attachment exceeds 10 MB")
		}
		size = int64(len(decoded))
		if ctx.PlatformAPI() == nil {
			return nil, errors.New("Storage is not available")
		}
		var got struct {
			ID     any    `json:"id"`
			FileID any    `json:"file_id"`
			URL    string `json:"url"`
		}
		err = ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{"name": name, "content_base64": content, "folder": "tickets/" + strconv.FormatInt(ticketID, 10), "content_type": contentType, "visibility": "private", "source": "tickets"}, &got)
		if err != nil {
			return nil, fmt.Errorf("Storage is optional but required for uploads: %w", err)
		}
		fileID = fmt.Sprint(firstNonNil(got.FileID, got.ID))
		fileURL = got.URL
	}
	if fileID == "" {
		return nil, errors.New("file_id or content_base64 is required")
	}
	var commentID *int64
	if id := int64Arg(args, "comment_id"); id > 0 {
		commentID = &id
	}
	return addAttachmentRecord(ctx.AppDB(), pid, ticketID, commentID, fileID, name, contentType, size, fileURL, visibility, actor)
}

func decorateAttachmentURLs(ctx *sdk.AppCtx, ticket *Ticket, attachments []*Attachment) {
	if ticket == nil || ticket.PortalToken == "" {
		return
	}
	base := publicAppBase(ctx) + "/p/ticket/" + url.PathEscape(ticket.PortalToken)
	for _, a := range attachments {
		if a != nil && a.Visibility == "public" {
			a.URL = base + "/attachments/" + strconv.FormatInt(a.ID, 10) + "/content"
		}
	}
}

func decorateTicketURLs(ctx *sdk.AppCtx, ticket *Ticket) {
	if ticket == nil || ticket.PortalToken == "" {
		return
	}
	ticket.PortalURL = publicAppBase(ctx) + "/p/ticket/" + url.PathEscape(ticket.PortalToken)
}

func publicAppBase(ctx *sdk.AppCtx) string {
	base := ""
	installID := int64(0)
	if ctx != nil && ctx.PlatformAPI() != nil {
		if identity, err := ctx.PlatformAPI().WhoAmI(); err == nil && identity != nil {
			base = strings.TrimRight(strings.TrimSpace(identity.PublicURL), "/")
			installID = identity.InstallID
		}
		if base == "" {
			if info, err := ctx.PlatformInfo(); err == nil && info != nil {
				base = strings.TrimRight(strings.TrimSpace(info.PublicURL), "/")
			}
		}
	}
	if base == "" {
		base = strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/")
	}
	if base == "" {
		base = "http://localhost:5280"
	}
	if installID > 0 {
		return base + "/api/apps/tickets/_install/" + strconv.FormatInt(installID, 10)
	}
	return base + "/api/apps/tickets"
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil && fmt.Sprint(v) != "" && fmt.Sprint(v) != "<nil>" {
			return v
		}
	}
	return ""
}
func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
