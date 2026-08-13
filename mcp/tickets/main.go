package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("tickets requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("tickets mounted", "version", "0.1.3")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/tickets", Handler: a.handleTickets},
		{Pattern: "/tickets/", Handler: a.handleTicket},
		{Pattern: "/areas", Handler: a.handleAreas},
		{Pattern: "/areas/", Handler: a.handleArea},
		{Pattern: "/portal", Handler: a.handlePortal},
		{Pattern: "/p/", Handler: a.handlePublicPortal, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		tool("tickets_create", "Create a client feedback or support ticket.", createSchema(), a.toolCreate),
		tool("tickets_list", "Search and filter tickets.", objectSchema(map[string]any{
			"q": sString(), "status": enumSchema(statusValues), "area": sString(), "type": enumSchema(typeValues),
			"priority": enumSchema(priorityValues), "requester_email": sString(), "limit": sInteger(), "offset": sInteger(),
		}, nil), a.toolList),
		tool("tickets_get", "Fetch one ticket with comments, attachments, links, and chronological history.", idSchema(), a.toolGet),
		tool("tickets_update", "Patch ticket fields and record every changed value.", updateSchema(), a.toolUpdate),
		tool("tickets_set_status", "Move a ticket through its workflow and record the transition.", objectSchema(map[string]any{
			"id": sInteger(), "status": enumSchema(statusValues), "reason": sString(), "actor_name": sString(),
		}, []string{"id", "status"}), a.toolSetStatus),
		tool("tickets_comment", "Add a public comment visible to the client.", commentSchema(), a.toolComment),
		tool("tickets_add_internal_note", "Add an internal team/agent note hidden from the client.", commentSchema(), a.toolInternalNote),
		tool("tickets_edit_comment", "Edit a comment while preserving its previous body as a revision.", objectSchema(map[string]any{
			"id": sInteger(), "comment_id": sInteger(), "body": sString(), "actor_name": sString(),
		}, []string{"id", "comment_id", "body"}), a.toolEditComment),
		tool("tickets_add_attachment", "Attach an existing Storage file or upload base64 content through optional Storage.", attachmentSchema(), a.toolAddAttachment),
		tool("tickets_link", "Link a task, Code issue, deployment, URL, or other record.", linkSchema(), a.toolLink),
		tool("tickets_history", "Read append-only ticket history.", objectSchema(map[string]any{
			"id": sInteger(), "include_internal": sBool(),
		}, []string{"id"}), a.toolHistory),
		tool("ticket_areas_list", "List configurable feedback areas.", objectSchema(nil, nil), a.toolAreasList),
		tool("ticket_areas_create", "Create a feedback area.", objectSchema(map[string]any{
			"name": sString(), "slug": sString(), "color": sString(), "sort_order": sInteger(),
		}, []string{"name"}), a.toolAreaCreate),
		tool("ticket_areas_update", "Update or archive a feedback area.", objectSchema(map[string]any{
			"id": sInteger(), "name": sString(), "slug": sString(), "color": sString(), "sort_order": sInteger(), "archived": sBool(),
		}, []string{"id"}), a.toolAreaUpdate),
		tool("tickets_portal_get", "Get or configure the secure client intake portal.", objectSchema(map[string]any{
			"title": sString(), "welcome_text": sString(), "enabled": sBool(), "rotate_token": sBool(),
		}, nil), a.toolPortal),
	}
}

func tool(name, description string, schema map[string]any, handler sdk.ToolHandlerCtx) sdk.Tool {
	return sdk.Tool{Name: name, Description: description, InputSchema: schema, HandlerCtx: handler}
}

var statusValues = []string{"new", "acknowledged", "planned", "in_progress", "waiting_client", "resolved", "closed"}
var typeValues = []string{"feedback", "bug", "feature", "change_request", "question", "support"}
var priorityValues = []string{"low", "normal", "high", "urgent"}

func createSchema() map[string]any {
	return objectSchema(map[string]any{
		"title": sString(), "description": sString(), "area": sString(), "area_id": sInteger(),
		"type": enumSchema(typeValues), "priority": enumSchema(priorityValues), "source": sString(),
		"requester_name": sString(), "requester_email": sString(), "requester_organization": sString(),
		"requester_crm_contact_id": sInteger(), "due_at": sString(), "actor_name": sString(),
	}, []string{"title"})
}

func updateSchema() map[string]any {
	p := createSchema()["properties"].(map[string]any)
	copyProps := map[string]any{"id": sInteger(), "assignee_kind": sString(), "assignee_ref": sString(), "assignee_name": sString()}
	for k, v := range p {
		copyProps[k] = v
	}
	delete(copyProps, "source")
	return objectSchema(copyProps, []string{"id"})
}

func commentSchema() map[string]any {
	return objectSchema(map[string]any{"id": sInteger(), "body": sString(), "author_name": sString()}, []string{"id", "body"})
}

func attachmentSchema() map[string]any {
	return objectSchema(map[string]any{
		"id": sInteger(), "comment_id": sInteger(), "file_id": map[string]any{"oneOf": []any{sInteger(), sString()}}, "content_base64": sString(),
		"name": sString(), "content_type": sString(), "size_bytes": sInteger(), "url": sString(),
		"visibility": enumSchema([]string{"public", "internal"}), "actor_name": sString(),
	}, []string{"id", "name"})
}

func linkSchema() map[string]any {
	return objectSchema(map[string]any{
		"id": sInteger(), "kind": sString(), "label": sString(), "app_name": sString(), "external_id": sString(),
		"url": sString(), "metadata": map[string]any{"type": "object"}, "actor_name": sString(),
	}, []string{"id", "kind"})
}

func idSchema() map[string]any             { return objectSchema(map[string]any{"id": sInteger()}, []string{"id"}) }
func sString() map[string]any              { return map[string]any{"type": "string"} }
func sInteger() map[string]any             { return map[string]any{"type": "integer"} }
func sBool() map[string]any                { return map[string]any{"type": "boolean"} }
func enumSchema(v []string) map[string]any { return map[string]any{"type": "string", "enum": v} }
func objectSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func requestCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func requireProject(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil {
		return "", errors.New("app context unavailable")
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		return "", errors.New("project_id is required")
	}
	if err := ensureProject(ctx.AppDB(), pid); err != nil {
		return "", err
	}
	return pid, nil
}

func actorFrom(ctx context.Context, args map[string]any, fallback string) Actor {
	name := strings.TrimSpace(stringArg(args, "actor_name"))
	if caller := sdk.CallerFrom(ctx); caller != nil {
		if caller.SubjectID != "" {
			if name == "" {
				name = caller.SubjectEmail
			}
			return Actor{Kind: firstNonEmpty(caller.SubjectType, "human"), Ref: caller.SubjectID, Name: name}
		}
		if caller.AgentID > 0 {
			if name == "" {
				name = fmt.Sprintf("Agent %d", caller.AgentID)
			}
			return Actor{Kind: "agent", Ref: strconv.FormatInt(caller.AgentID, 10), Name: name}
		}
	}
	if name == "" {
		name = strings.Title(strings.ReplaceAll(fallback, "_", " "))
	}
	return Actor{Kind: fallback, Name: name}
}

func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func int64Arg(args map[string]any, key string) int64 {
	v := args[key]
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func emitTicket(ctx *sdk.AppCtx, topic string, ticket *Ticket, extra map[string]any) {
	if ctx == nil || ticket == nil {
		return
	}
	payload := map[string]any{"id": ticket.ID, "key": ticket.Key, "title": ticket.Title, "status": ticket.Status, "type": ticket.Type, "priority": ticket.Priority, "area": ticket.AreaSlug}
	for k, v := range extra {
		payload[k] = v
	}
	ctx.EmitWithProject(topic, ticket.ProjectID, payload)
}
