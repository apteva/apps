package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleTickets(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	pid, err := requireProject(ctx)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		filter := TicketFilter{Q: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status"), Area: r.URL.Query().Get("area"), Type: r.URL.Query().Get("type"), Priority: r.URL.Query().Get("priority"), RequesterEmail: r.URL.Query().Get("requester_email"), Limit: intQuery(r, "limit", 100), Offset: intQuery(r, "offset", 0)}
		tickets, total, err := listTickets(ctx.AppDB(), pid, filter)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		for _, t := range tickets {
			decorateTicketURLs(ctx, t)
		}
		writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets, "count": len(tickets), "total": total, "offset": filter.Offset})
	case http.MethodPost:
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		if body["source"] == nil {
			body["source"] = "team"
		}
		actor := actorFromHTTP(r, "human")
		ticket, err := createTicket(ctx.AppDB(), pid, body, actor)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		ticket = bestEffortCRMLink(ctx, ticket, actor)
		decorateTicketURLs(ctx, ticket)
		emitTicket(ctx, "ticket.created", ticket, nil)
		writeJSON(w, http.StatusCreated, map[string]any{"ticket": ticket})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTicket(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	pid, err := requireProject(ctx)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/tickets/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id <= 0 {
		httpErr(w, errors.New("ticket id required"), http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	actor := actorFromHTTP(r, "human")
	switch {
	case r.Method == http.MethodGet && action == "":
		detail, err := ticketDetail(ctx.AppDB(), pid, id, true)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		decorateTicketURLs(ctx, detail.Ticket)
		hydrateAttachmentURLs(ctx, pid, detail.Attachments)
		writeJSON(w, http.StatusOK, detail)
	case r.Method == http.MethodPatch && action == "":
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		ticket, changes, err := updateTicket(ctx.AppDB(), pid, id, body, actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		if _, ok := changes["requester_email"]; ok {
			ticket = bestEffortCRMLink(ctx, ticket, actor)
		}
		decorateTicketURLs(ctx, ticket)
		if len(changes) > 0 {
			emitTicket(ctx, "ticket.updated", ticket, map[string]any{"changes": changes})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "changes": changes})
	case r.Method == http.MethodPost && action == "status":
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		before, err := getTicket(ctx.AppDB(), pid, id)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		ticket, err := setTicketStatus(ctx.AppDB(), pid, id, stringArg(body, "status"), stringArg(body, "reason"), actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		decorateTicketURLs(ctx, ticket)
		topic := "ticket.status.changed"
		if ticket.Status == "resolved" {
			topic = "ticket.resolved"
		}
		if (before.Status == "resolved" || before.Status == "closed") && ticket.Status != "resolved" && ticket.Status != "closed" {
			topic = "ticket.reopened"
		}
		emitTicket(ctx, topic, ticket, map[string]any{"from": before.Status, "to": ticket.Status})
		writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket})
	case r.Method == http.MethodPost && (action == "comments" || action == "internal-notes"):
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		visibility := "public"
		if action == "internal-notes" {
			visibility = "internal"
		}
		comment, err := addComment(ctx.AppDB(), pid, id, visibility, stringArg(body, "body"), actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		ticket, _ := getTicket(ctx.AppDB(), pid, id)
		topic := "ticket.commented"
		if visibility == "internal" {
			topic = "ticket.internal_note.added"
		}
		emitTicket(ctx, topic, ticket, map[string]any{"comment_id": comment.ID})
		writeJSON(w, http.StatusCreated, map[string]any{"comment": comment})
	case r.Method == http.MethodPatch && action == "comments" && len(parts) > 2:
		commentID, _ := strconv.ParseInt(parts[2], 10, 64)
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		comment, err := editComment(ctx.AppDB(), pid, id, commentID, stringArg(body, "body"), actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"comment": comment})
	case r.Method == http.MethodPost && action == "attachments":
		body, err := decodeMapLimited(w, r, 14<<20)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		attachment, err := addAttachment(ctx, pid, id, body, actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		ticket, _ := getTicket(ctx.AppDB(), pid, id)
		emitTicket(ctx, "ticket.attachment.added", ticket, map[string]any{"attachment_id": attachment.ID})
		writeJSON(w, http.StatusCreated, map[string]any{"attachment": attachment})
	case r.Method == http.MethodPost && action == "links":
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		link, err := addLink(ctx.AppDB(), pid, id, body, actor)
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		ticket, _ := getTicket(ctx.AppDB(), pid, id)
		emitTicket(ctx, "ticket.link.added", ticket, map[string]any{"link_id": link.ID})
		writeJSON(w, http.StatusCreated, map[string]any{"link": link})
	case r.Method == http.MethodGet && action == "history":
		events, err := listEvents(ctx.AppDB(), pid, id, r.URL.Query().Get("include_internal") != "false")
		if err != nil {
			httpStoreErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleAreas(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	pid, err := requireProject(ctx)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		areas, err := listAreas(ctx.AppDB(), pid, r.URL.Query().Get("include_archived") == "true")
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"areas": areas})
	case http.MethodPost:
		body, err := decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		area, err := createArea(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"area": area})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}
func (a *App) handleArea(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, "PATCH", http.StatusMethodNotAllowed)
		return
	}
	ctx := requestCtx(r)
	pid, err := requireProject(ctx)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/areas/"), "/"), 10, 64)
	body, err := decodeMap(r)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	area, err := updateArea(ctx.AppDB(), pid, id, body)
	if err != nil {
		httpStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"area": area})
}

func (a *App) handlePortal(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	pid, err := requireProject(ctx)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	args := map[string]any{}
	if r.Method == http.MethodPatch {
		args, err = decodeMap(r)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "GET or PATCH", http.StatusMethodNotAllowed)
		return
	}
	portal, err := updatePortal(ctx.AppDB(), pid, args)
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	portal.IntakeURL = publicAppBase(ctx) + "/p/" + portal.Token
	writeJSON(w, http.StatusOK, map[string]any{"portal": portal})
}

func actorFromHTTP(r *http.Request, fallback string) Actor {
	name := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Email"))
	ref := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-ID"))
	kind := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Type"))
	if kind == "" {
		kind = fallback
	}
	if name == "" {
		name = "Team member"
	}
	return Actor{Kind: kind, Ref: ref, Name: name}
}
func splitPath(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	out := []string{}
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
func intQuery(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return fallback
	}
	return n
}
func decodeMap(r *http.Request) (map[string]any, error) { return decodeMapLimited(nil, r, 1<<20) }
func decodeMapLimited(w http.ResponseWriter, r *http.Request, max int64) (map[string]any, error) {
	if w != nil {
		r.Body = http.MaxBytesReader(w, r.Body, max)
	}
	var body map[string]any
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func httpErr(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func httpStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	httpErr(w, err, http.StatusBadRequest)
}
