package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) projectHasAgent(app *sdk.AppCtx, projectID string, agentID int64) bool {
	if app == nil || agentID <= 0 {
		return false
	}
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), projectID)
	if err != nil {
		return false
	}
	for _, agent := range agents {
		if agent.ID == agentID {
			return true
		}
	}
	return false
}

func (a *App) handleTelegramIntake(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	app := a.appCtx(r)
	switch r.Method {
	case http.MethodGet:
		connections, err := a.boundTelegramConnections(app, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		policies := []TransportIntakePolicy{}
		for _, connection := range connections {
			policy, err := a.store.GetTransportIntakePolicy(telegramTransport, connection.ID)
			if err == nil && policy.ProjectID == projectID {
				policies = append(policies, *policy)
			}
		}
		writeJSON(w, map[string]any{"policies": policies})
	case http.MethodPost:
		var body struct {
			ConnectionID        int64  `json:"connection_id"`
			Mode                string `json:"mode"`
			DefaultAgentID      int64  `json:"default_agent_id"`
			DefaultTitle        string `json:"default_title"`
			RequireGroupMention bool   `json:"require_group_mention"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if _, err := a.boundTelegramConnection(app, projectID, body.ConnectionID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if _, err := a.store.GetTelegramConnection(body.ConnectionID); err != nil {
			http.Error(w, "activate the Telegram bot before configuring intake", http.StatusConflict)
			return
		}
		if existing, err := a.store.GetTransportIntakePolicy(telegramTransport, body.ConnectionID); err == nil && existing.ProjectID != projectID {
			http.Error(w, "this Telegram bot already has an intake project; use a separate bot connection for another project", http.StatusConflict)
			return
		}
		mode, err := normalizeIntakeMode(body.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !a.projectHasAgent(app, projectID, body.DefaultAgentID) {
			http.Error(w, "default agent is not available in this project", http.StatusNotFound)
			return
		}
		policy, err := a.store.UpsertTransportIntakePolicy(TransportIntakePolicy{
			Transport: telegramTransport, ConnectionID: body.ConnectionID, ProjectID: projectID,
			Mode: mode, DefaultAgentID: body.DefaultAgentID, DefaultTitle: body.DefaultTitle,
			RequireGroupMention: body.RequireGroupMention, CreatedByUserID: userID,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, policy)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTelegramAccess(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	app := a.appCtx(r)
	switch r.Method {
	case http.MethodGet:
		requests, err := a.store.ListTransportAccessRequests(telegramTransport, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		blocked, err := a.store.ListBlockedTransportAccess(telegramTransport, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"requests": requests, "blocked": blocked})
	case http.MethodPost:
		var body struct {
			ID             string `json:"id"`
			Action         string `json:"action"` // approve | dismiss | block
			ConversationID string `json:"conversation_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
			http.Error(w, "id and action are required", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(body.Action))
		access, err := a.store.GetTransportAccessRequest(body.ID)
		validState := err == nil && ((action == "unblock" && access.State == "blocked") || (action != "unblock" && access.State == "pending"))
		if err != nil || access.Transport != telegramTransport || access.ProjectID != projectID || !validState {
			http.Error(w, "access request not found", http.StatusNotFound)
			return
		}
		if _, err := a.boundTelegramConnection(app, projectID, access.ConnectionID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		switch action {
		case "unblock":
			if err := a.store.UnblockTransportAccessRequest(access.ID); err != nil {
				http.Error(w, "access request changed; refresh and try again", http.StatusConflict)
				return
			}
			writeJSON(w, map[string]any{"state": "dismissed"})
		case "dismiss", "block":
			state := strings.ToLower(strings.TrimSpace(body.Action))
			if state == "dismiss" {
				state = "dismissed"
			} else {
				state = "blocked"
			}
			if err := a.store.ResolveTransportAccessRequest(access.ID, state, ""); err != nil {
				http.Error(w, "access request changed; refresh and try again", http.StatusConflict)
				return
			}
			writeJSON(w, map[string]any{"state": state})
		case "approve":
			var existing *Conversation
			if strings.TrimSpace(body.ConversationID) != "" {
				existing, err = a.authorizeConversation(r, strings.TrimSpace(body.ConversationID))
				if err != nil || existing.ProjectID != projectID {
					http.Error(w, "conversation not found", http.StatusNotFound)
					return
				}
			}
			policy, err := a.store.GetTransportIntakePolicy(telegramTransport, access.ConnectionID)
			if err != nil || policy.ProjectID != projectID || policy.DefaultAgentID <= 0 {
				http.Error(w, "configure a default agent before approving requests", http.StatusConflict)
				return
			}
			binding, conv, err := a.approveTelegramAccess(access, policy, existing, userID)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					http.Error(w, "this Telegram chat is already connected", http.StatusConflict)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
			_ = a.sendTelegramSystem(app.WithProject(projectID), access.ConnectionID, access.ExternalChatID,
				"You’re connected to “"+conv.Title+"”. Send a message whenever you’re ready.\n\nUse /new to start a fresh conversation or /help for options.")
			writeJSON(w, map[string]any{"state": "approved", "binding": binding, "conversation": conv})
		default:
			http.Error(w, "action must be approve, dismiss, block, or unblock", http.StatusBadRequest)
		}
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) approveTelegramAccess(access *TransportAccessRequest, policy *TransportIntakePolicy, existing *Conversation, userID int64) (*TelegramBinding, *Conversation, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	conv := existing
	if conv == nil {
		title := telegramConversationTitle(policy.DefaultTitle, access.ChatTitle, access.DisplayName)
		conv = &Conversation{ID: newConversationID(), ProjectID: access.ProjectID, LeadAgentID: policy.DefaultAgentID,
			Title: title, Kind: "direct", Origin: telegramTransport, Audience: "operator", OwnerUserID: userID}
		if _, err := tx.Exec(`INSERT INTO conversations
			(id,project_id,lead_agent_id,title,kind,origin,conversation_key,audience,owner_user_id)
			VALUES (?,?,?,?,?,?,?,?,?)`, conv.ID, conv.ProjectID, conv.LeadAgentID, conv.Title, conv.Kind,
			conv.Origin, "", conv.Audience, conv.OwnerUserID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,agent_id) VALUES (?,?)`, conv.ID, conv.LeadAgentID); err != nil {
			return nil, nil, err
		}
		if userID > 0 {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,user_id) VALUES (?,?)`, conv.ID, userID); err != nil {
				return nil, nil, err
			}
		}
	}
	externalIdentity := "telegram:" + strconv.FormatInt(access.ConnectionID, 10) + ":" + access.ExternalUserID
	if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,external_identity,display_name) VALUES (?,?,?)`,
		conv.ID, externalIdentity, access.DisplayName); err != nil {
		return nil, nil, err
	}
	bindingID, err := randomTelegramSecret(12)
	if err != nil {
		return nil, nil, err
	}
	allowed := []int64{}
	if access.ChatType == "private" {
		allowedID, _ := strconv.ParseInt(access.ExternalUserID, 10, 64)
		allowed = normalizeTelegramUserIDs([]int64{allowedID})
	}
	requireMention := access.ChatType != "private" && policy.RequireGroupMention
	if _, err := tx.Exec(`INSERT INTO telegram_bindings
		(id,connection_id,project_id,conversation_id,chat_id,allowed_user_ids_json,created_by_user_id,
		 chat_type,chat_title,chat_username,require_mention,access_mode)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "tgb-"+bindingID, access.ConnectionID, access.ProjectID,
		conv.ID, access.ExternalChatID, encodeInt64s(allowed), userID, access.ChatType,
		access.ChatTitle, access.Username, requireMention, "pairing"); err != nil {
		return nil, nil, err
	}
	res, err := tx.Exec(`UPDATE transport_access_requests SET state='approved',conversation_id=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND state='pending'`, conv.ID, access.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	binding, err := a.store.GetTelegramBinding("tgb-" + bindingID)
	if err != nil {
		return nil, nil, err
	}
	conv, err = a.store.GetConversation(conv.ID)
	return binding, conv, err
}

func telegramConversationTitle(prefix, chatTitle, displayName string) string {
	name := strings.TrimSpace(chatTitle)
	if name == "" {
		name = strings.TrimSpace(displayName)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.EqualFold(prefix, "Telegram conversation") {
		prefix = "Telegram"
	}
	title := prefix
	if name != "" {
		title += " — " + name
	}
	runes := []rune(title)
	if len(runes) > 120 {
		title = string(runes[:120])
	}
	return title
}

func (a *App) handleTelegramInvites(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ConnectionID   int64  `json:"connection_id"`
		ConversationID string `json:"conversation_id"`
		ChatType       string `json:"chat_type"` // private | group
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.ChatType = strings.ToLower(strings.TrimSpace(body.ChatType))
	if body.ChatType == "" {
		body.ChatType = "private"
	}
	if body.ChatType != "private" && body.ChatType != "group" {
		http.Error(w, "chat_type must be private or group", http.StatusBadRequest)
		return
	}
	app := a.appCtx(r)
	if _, err := a.boundTelegramConnection(app, projectID, body.ConnectionID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	cfg, err := a.store.GetTelegramConnection(body.ConnectionID)
	if err != nil || cfg.BotUsername == "" {
		http.Error(w, "activate the Telegram bot before creating invites", http.StatusConflict)
		return
	}
	policy, err := a.store.GetTransportIntakePolicy(telegramTransport, body.ConnectionID)
	if err != nil || policy.ProjectID != projectID || policy.DefaultAgentID <= 0 {
		http.Error(w, "configure Telegram intake before creating invites", http.StatusConflict)
		return
	}
	invite := TransportInvite{Transport: telegramTransport, ConnectionID: body.ConnectionID,
		ProjectID: projectID, Audience: "operator", ChatType: body.ChatType,
		DefaultAgentID: policy.DefaultAgentID, CreatedByUserID: userID}
	if strings.TrimSpace(body.ConversationID) != "" {
		conv, err := a.authorizeConversation(r, strings.TrimSpace(body.ConversationID))
		if err != nil || conv.ProjectID != projectID {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
		invite.ConversationID = conv.ID
		invite.Audience = conv.Audience
		invite.DefaultAgentID = conv.LeadAgentID
	}
	raw, created, err := a.store.CreateTransportInvite(invite)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	query := "start=" + url.QueryEscape(raw)
	if body.ChatType == "group" {
		query = "startgroup=" + url.QueryEscape(raw)
	}
	writeJSON(w, map[string]any{
		"invite_url": "https://t.me/" + strings.TrimPrefix(cfg.BotUsername, "@") + "?" + query,
		"expires_at": created.ExpiresAt,
		"chat_type":  body.ChatType,
	})
}
