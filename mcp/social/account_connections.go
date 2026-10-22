package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"sort"
	"strings"
	"time"
)

type linkedConnectionAccount struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ProfileID   int64  `json:"profile_id"`
	ProfileName string `json:"profile_name"`
}
type socialConnectionChoice struct {
	ID       int64                     `json:"id"`
	Name     string                    `json:"name"`
	Accounts []linkedConnectionAccount `json:"accounts"`
}

// Supplied IDs select active project grants; they do not authorize a connection.
func accountConnectionChoices(ctx *sdk.AppCtx, pid string, def platformDef) ([]socialConnectionChoice, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, errors.New("project_id required")
	}
	connections, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: pid, AppSlug: def.IntegrationSlug})
	if err != nil {
		return nil, fmt.Errorf("list account connections: %w", err)
	}
	choices := []socialConnectionChoice{}
	seen := map[int64]bool{}
	for _, connection := range connections {
		if connection.ID <= 0 || seen[connection.ID] || connection.ProjectID != pid || connection.AppSlug != def.IntegrationSlug || connection.Status != "active" {
			continue
		}
		seen[connection.ID] = true
		choice := socialConnectionChoice{ID: connection.ID, Name: connection.Name, Accounts: []linkedConnectionAccount{}}
		rows, err := ctx.AppDB().Query(`SELECT a.id,a.display_name,COALESCE(a.profile_id,0),COALESCE(p.name,'') FROM social_accounts a LEFT JOIN profiles p ON p.id=a.profile_id AND p.project_id=a.project_id WHERE a.project_id=? AND a.platform=? AND a.connection_id=? AND COALESCE(a.provider_slug,'native')='native' AND a.status='active' ORDER BY a.id`, pid, def.Platform, connection.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var linked linkedConnectionAccount
			if err := rows.Scan(&linked.ID, &linked.Name, &linked.ProfileID, &linked.ProfileName); err != nil {
				rows.Close()
				return nil, err
			}
			choice.Accounts = append(choice.Accounts, linked)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if choice.Name == "" {
			choice.Name = def.DisplayName + " connection"
		}
		choices = append(choices, choice)
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].ID < choices[j].ID })
	return choices, nil
}
func (a *App) toolAccountListConnections(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	ctx = ctx.WithProject(pid)
	def, ok := platforms[toString(args["platform"])]
	if !ok {
		return mcpError("unsupported direct platform"), nil
	}
	choices, err := accountConnectionChoices(ctx, pid, def)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"platform": def.Platform, "connections": choices}, nil
}
func (a *App) handleAccountConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	out, err := a.toolAccountListConnections(globalCtx, scopedQueryArgs(r, "platform"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}
func pendingForExistingConnection(ctx *sdk.AppCtx, pid string, def platformDef, connectionID, profileID int64) (any, error) {
	res, err := ctx.AppDB().Exec(`INSERT INTO pending_accounts(project_id,platform,integration_slug,connection_id,status,expires_at,profile_id) VALUES(?,?,?,?,'ready',?,?)`, pid, def.Platform, def.IntegrationSlug, connectionID, pendingExpiry(time.Now().UTC().Add(10*time.Minute)), profileID)
	if err != nil {
		return nil, fmt.Errorf("create pending account: %w", err)
	}
	pendingID, _ := res.LastInsertId()
	return map[string]any{"pending_account_id": pendingID, "platform": def.Platform, "reused_connection": connectionID, "instructions": "Using the selected connection. Call account_list_pending_pages, then account_finalize."}, nil
}
