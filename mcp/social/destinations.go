package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// An account is the identity publishing AS; a destination is where it publishes
// TO. Adapters translate this shared contract into a provider's board/collection
// API. Facebook Page selection continues to identify the publishing account.
type destinationAdapter struct {
	Kind                              string   `json:"kind"`
	Label                             string   `json:"label"`
	Required                          bool     `json:"required"`
	List                              bool     `json:"list"`
	Create                            bool     `json:"create"`
	SetDefault                        bool     `json:"set_default"`
	Privacy                           []string `json:"privacy,omitempty"`
	ListTool, CreateTool, DefaultTool string   `json:"-"`
	ListItems, CursorArg, CursorField string   `json:"-"`
}

func adapterForDestinations(platform, provider string) destinationAdapter {
	if platform != "pinterest" {
		return destinationAdapter{}
	}
	out := destinationAdapter{Kind: "board", Label: "Board", Required: true, List: true, Create: true, SetDefault: true, Privacy: []string{"PUBLIC", "SECRET"}, ListTool: "list_boards", CreateTool: "create_board", ListItems: "items", CursorArg: "bookmark", CursorField: "bookmark"}
	if provider == zernioProviderSlug {
		out.ListTool = "list_account_pinterest_boards"
		out.CreateTool = "create_pinterest_board"
		out.DefaultTool = "set_default_pinterest_board"
		out.ListItems = "boards"
		out.CursorArg = ""
		out.CursorField = ""
		out.Privacy = []string{"PUBLIC", "PROTECTED", "SECRET"}
	} else if provider != "" && provider != "native" {
		return destinationAdapter{}
	}
	return out
}

type destinationAccount struct {
	ID                                                                               int64
	ConnID                                                                           int64
	Platform, Provider, ProviderID, ExternalID, DefaultID, DefaultName, Kind, Status string
}

func loadDestinationAccount(ctx *sdk.AppCtx, pid string, id int64) (destinationAccount, error) {
	out := destinationAccount{ID: id}
	err := ctx.AppDB().QueryRow(`SELECT platform,connection_id,COALESCE(provider_slug,'native'),COALESCE(provider_account_id,''),COALESCE(external_account_id,''),default_destination_id,default_destination_name,account_kind,status FROM social_accounts WHERE id=? AND project_id=?`, id, pid).Scan(&out.Platform, &out.ConnID, &out.Provider, &out.ProviderID, &out.ExternalID, &out.DefaultID, &out.DefaultName, &out.Kind, &out.Status)
	if err != nil {
		return out, errors.New("social account not found in this project")
	}
	if out.Status != "active" {
		return out, errors.New("social account is not active; reconnect it first")
	}
	// Old SDK/test fixtures can create legacy-shaped rows after migration. A new
	// account-level native Pinterest connection always has an empty external ID.
	if out.DefaultID == "" && out.Platform == "pinterest" && out.Provider == "native" {
		out.DefaultID = out.ExternalID
	}
	return out, nil
}

type publishingDestination struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Privacy     string `json:"privacy,omitempty"`
}
type destinationListResult struct {
	Status       string                  `json:"status"`
	Reason       string                  `json:"reason,omitempty"`
	Error        string                  `json:"error,omitempty"`
	Capabilities destinationAdapter      `json:"capabilities"`
	DefaultID    string                  `json:"default_destination_id"`
	DefaultName  string                  `json:"default_destination_name,omitempty"`
	Items        []publishingDestination `json:"items"`
	Cursor       string                  `json:"cursor,omitempty"`
}

func destinationInput(b destinationAccount) map[string]any {
	out := map[string]any{}
	if b.Provider == zernioProviderSlug {
		out["accountId"] = b.ProviderID
	}
	return out
}
func destinationUpstream(ctx *sdk.AppCtx, b destinationAccount, tool string, input map[string]any) (json.RawMessage, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(b.ConnID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("destination operation %s: %w", tool, err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("destination operation %s unavailable; check account permissions and the installed integration catalog: %w", tool, upstreamError(res))
	}
	return res.Data, nil
}
func fetchDestinationPage(ctx *sdk.AppCtx, b destinationAccount, cursor string, limit int) (destinationListResult, error) {
	adapter := adapterForDestinations(b.Platform, b.Provider)
	out := destinationListResult{Status: "ok", Capabilities: adapter, DefaultID: b.DefaultID, DefaultName: b.DefaultName, Items: []publishingDestination{}}
	if !adapter.List {
		out.Status = "unsupported"
		out.Reason = "This account does not expose publishing destinations."
		return out, nil
	}
	input := destinationInput(b)
	if adapter.CursorArg != "" {
		input["page_size"] = limit
		if cursor != "" {
			input[adapter.CursorArg] = cursor
		}
	} else if cursor != "" {
		return out, errors.New("this provider does not support destination cursors")
	}
	raw, err := destinationUpstream(ctx, b, adapter.ListTool, input)
	if err != nil {
		return out, err
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return out, errors.New("invalid destination response")
	}
	var items []publishingDestination
	if json.Unmarshal(payload[adapter.ListItems], &items) != nil || items == nil {
		return out, errors.New("destination list is missing; update the installed integration catalog if this operation is unavailable")
	}
	out.Items = items
	if adapter.CursorField != "" {
		_ = json.Unmarshal(payload[adapter.CursorField], &out.Cursor)
	}
	// Zernio may return its saved default in addition to the board list.
	if out.DefaultID == "" {
		_ = json.Unmarshal(payload["defaultBoardId"], &out.DefaultID)
		_ = json.Unmarshal(payload["defaultBoardName"], &out.DefaultName)
	}
	return out, nil
}
func findDestination(ctx *sdk.AppCtx, b destinationAccount, id string) (publishingDestination, error) {
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		result, err := fetchDestinationPage(ctx, b, cursor, 250)
		if err != nil {
			return publishingDestination{}, err
		}
		for _, item := range result.Items {
			if item.ID == id {
				return item, nil
			}
		}
		if result.Cursor == "" {
			break
		}
		if seen[result.Cursor] {
			return publishingDestination{}, errors.New("repeated destination cursor")
		}
		seen[result.Cursor] = true
		cursor = result.Cursor
	}
	return publishingDestination{}, errors.New("destination not found on this account; select an accessible destination")
}
func (a *App) toolDestinationList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	b, err := loadDestinationAccount(ctx, projectScope(ctx, args), int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 250 {
		return mcpError("limit must be 1–250"), nil
	}
	out, err := fetchDestinationPage(ctx, b, toString(args["cursor"]), limit)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
	}
	return out, nil
}
func (a *App) toolDestinationCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	b, err := loadDestinationAccount(ctx, projectScope(ctx, args), int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	adapter := adapterForDestinations(b.Platform, b.Provider)
	if !adapter.Create {
		return mcpError("destination creation is unsupported by this account"), nil
	}
	name := strings.TrimSpace(toString(args["name"]))
	description := toString(args["description"])
	privacy := strings.ToUpper(strings.TrimSpace(toString(args["privacy"])))
	if privacy == "" {
		privacy = "PUBLIC"
	}
	if name == "" || utf8.RuneCountInString(name) > 180 || utf8.RuneCountInString(description) > 500 {
		return mcpError("name must be 1–180 characters; description must be at most 500"), nil
	}
	if !containsFold(adapter.Privacy, privacy) {
		return mcpError("privacy is unsupported by this provider"), nil
	}
	input := destinationInput(b)
	input["name"] = name
	input["description"] = description
	input["privacy"] = privacy
	raw, err := destinationUpstream(ctx, b, adapter.CreateTool, input)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	var item publishingDestination
	if b.Provider == zernioProviderSlug {
		var wrapper struct {
			Board publishingDestination `json:"board"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		item = wrapper.Board
	} else {
		_ = json.Unmarshal(raw, &item)
	}
	if item.ID == "" {
		return mcpError("provider did not return the created destination ID; refresh the list before retrying creation"), nil
	}
	ctx.Emit("destinations.updated", map[string]any{"social_account_id": b.ID})
	return map[string]any{"status": "ok", "destination": item}, nil
}
func (a *App) toolDestinationDefault(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	pid := projectScope(ctx, args)
	unlock := lockSocialResource(ctx, int64(intArg(args, "social_account_id", 0)), "destination-default")
	defer unlock()
	b, err := loadDestinationAccount(ctx, pid, int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	adapter := adapterForDestinations(b.Platform, b.Provider)
	if !adapter.SetDefault {
		return mcpError("destination defaults are unsupported by this account"), nil
	}
	id := strings.TrimSpace(toString(args["destination_id"]))
	if id == "" {
		return mcpError("destination_id required"), nil
	}
	item, err := findDestination(ctx, b, id)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if b.Provider == zernioProviderSlug {
		// Older provider posts could rely on an implicit provider default, including
		// already mirrored schedules. Refuse to reroute those upstream posts.
		var unresolved int
		err = ctx.AppDB().QueryRow(`SELECT count(*) FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.social_account_id=? AND p.project_id=? AND t.status NOT IN ('published','cancelled') AND COALESCE(NULLIF(json_extract(CASE WHEN json_valid(t.options) THEN t.options ELSE '{}' END,'$.destination_id'),''),NULLIF(json_extract(CASE WHEN json_valid(t.options) THEN t.options ELSE '{}' END,'$.board_id'),''),NULLIF(json_extract(CASE WHEN json_valid(t.options) THEN t.options ELSE '{}' END,'$.boardId'),''),'')=''`, b.ID, pid).Scan(&unresolved)
		if err != nil {
			return nil, err
		}
		if unresolved > 0 {
			return mcpError("Existing posts rely on the provider default. Select their destinations explicitly before changing the default (including any mirrored provider schedules)."), nil
		}
	}
	if adapter.DefaultTool != "" {
		input := destinationInput(b)
		input["defaultBoardId"] = id
		input["defaultBoardName"] = item.Name
		if _, err = destinationUpstream(ctx, b, adapter.DefaultTool, input); err != nil {
			return mcpError(err.Error()), nil
		}
	}
	result, err := ctx.AppDB().Exec(`UPDATE social_accounts SET default_destination_id=?,default_destination_name=? WHERE id=? AND project_id=? AND status='active' AND connection_id=? AND COALESCE(provider_account_id,'')=?`, id, item.Name, b.ID, pid, b.ConnID, b.ProviderID)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return mcpError("account changed while setting default; reload it"), nil
	}
	_, _ = ctx.AppDB().Exec(`DELETE FROM account_metric_cache WHERE social_account_id=? AND project_id=?`, b.ID, pid)
	ctx.Emit("destinations.updated", map[string]any{"social_account_id": b.ID})
	return map[string]any{"status": "ok", "default_destination_id": id, "default_destination_name": item.Name}, nil
}
func destinationTools(a *App) []sdk.Tool {
	props := map[string]any{"social_account_id": map[string]any{"type": "integer"}}
	list := copyMap(props)
	list["cursor"] = map[string]any{"type": "string"}
	list["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 250}
	create := copyMap(props)
	create["name"] = map[string]any{"type": "string"}
	create["description"] = map[string]any{"type": "string"}
	create["privacy"] = map[string]any{"type": "string"}
	defaults := copyMap(props)
	defaults["destination_id"] = map[string]any{"type": "string"}
	return []sdk.Tool{
		{Name: "destination_list", Description: "Optional: discover publishing destinations (boards/collections) under a connected account. Returns provider capabilities, defaults, items and an optional cursor. Does not reconnect the account.", InputSchema: schemaObject(list, []string{"social_account_id"}), Handler: a.toolDestinationList, Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "destination_create", Description: "Create a destination under a connected account when its capabilities allow it. Does not change the default or publish a post.", InputSchema: schemaObject(create, []string{"social_account_id", "name"}), Handler: a.toolDestinationCreate},
		{Name: "account_set_default_destination", Description: "Set the default publishing destination. Existing posts retain their stored destination; new posts can override destination_id.", InputSchema: schemaObject(defaults, []string{"social_account_id", "destination_id"}), Handler: a.toolDestinationDefault},
	}
}
func (a *App) handleDestinationAccount(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/destinations/accounts/"), "/")
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid account", 400)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	args := projectArgsFromRequest(r)
	if r.Method == http.MethodGet {
		args = scopedQueryArgs(r, "cursor", "limit")
	} else {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		for k, v := range body {
			if k != "project_id" && k != "_project_id" {
				args[k] = v
			}
		}
	}
	args["social_account_id"] = id
	var out any
	switch r.Method {
	case http.MethodGet:
		out, err = a.toolDestinationList(globalCtx, args)
	case http.MethodPost:
		out, err = a.toolDestinationCreate(globalCtx, args)
	case http.MethodPut:
		out, err = a.toolDestinationDefault(globalCtx, args)
	default:
		http.Error(w, "method not allowed", 405)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// Freeze defaults into post target options before review/scheduling. Explicit
// board_id/boardId aliases remain accepted, but conflicting selectors fail.
func freezeTargetDestinations(ctx *sdk.AppCtx, pid string, targets []targetSpec, required bool) error {
	for i := range targets {
		b, err := loadDestinationAccount(ctx, pid, targets[i].SocialAccountID)
		if err != nil {
			return err
		}
		adapter := adapterForDestinations(b.Platform, b.Provider)
		if !adapter.Required {
			continue
		}
		opts := targets[i].Options
		if opts == nil {
			opts = map[string]any{}
			targets[i].Options = opts
		}
		selected := ""
		for _, key := range []string{"destination_id", "board_id", "boardId"} {
			if value := strings.TrimSpace(toString(opts[key])); value != "" {
				if selected != "" && selected != value {
					return errors.New("conflicting destination selectors")
				}
				selected = value
			}
		}
		if selected == "" {
			selected = b.DefaultID
		}
		if selected == "" && b.Provider == zernioProviderSlug {
			result, e := fetchDestinationPage(ctx, b, "", 100)
			if e != nil {
				if required {
					return e
				}
			} else {
				selected = result.DefaultID
			}
		}
		if selected == "" {
			if required {
				return fmt.Errorf("account %d requires a %s: set a default or pass destination_id", b.ID, adapter.Label)
			}
			continue
		}
		// Legacy presets and previously stored defaults were validated when linked.
		// Explicit per-post selections must be accessible through this exact account.
		if selected != b.DefaultID {
			if _, err = findDestination(ctx, b, selected); err != nil {
				return err
			}
		}
		opts["destination_id"] = selected
		delete(opts, "board_id")
		delete(opts, "boardId")
	}
	return nil
}
func resolvePinterestBoard(j publishJob) (string, error) {
	id := ""
	for _, key := range []string{"destination_id", "board_id", "boardId"} {
		if value := strOption(j.options, key); value != "" {
			if id != "" && id != value {
				return "", errors.New("conflicting destination selectors")
			}
			id = value
		}
	}
	if id == "" {
		id = j.extID
	} // compatibility for legacy jobs and external fixtures
	if id == "" {
		return "", errors.New("Pinterest requires destination_id; select a board before publishing")
	}
	return id, nil
}

func validateStoredDestinations(ctx *sdk.AppCtx, pid string, postID int64) error {
	rows, err := ctx.AppDB().Query(`SELECT a.platform,COALESCE(a.provider_slug,'native'),COALESCE(t.options,'{}') FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id JOIN posts p ON p.id=t.post_id WHERE t.post_id=? AND p.project_id=? AND a.project_id=?`, postID, pid, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var platform, provider, raw string
		if err := rows.Scan(&platform, &provider, &raw); err != nil {
			return err
		}
		if !adapterForDestinations(platform, provider).Required {
			continue
		}
		var opts map[string]any
		_ = json.Unmarshal([]byte(raw), &opts)
		if _, err := resolvePinterestBoard(publishJob{options: opts}); err != nil {
			return errors.New("select a valid publishing destination in the draft before submitting or publishing it: " + err.Error())
		}
	}
	return rows.Err()
}
