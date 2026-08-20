package main

// telegram.go — Telegram is a Conversations transport, not a second chat
// store. Bot credentials stay in platform-managed integration connections;
// this app owns only webhook routing metadata, transcript rows, and the
// delivery/action ledger.

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const telegramIntegrationRole = "telegram_bot"

type TelegramConnectionConfig struct {
	ConnectionID  int64  `json:"connection_id"`
	WebhookKey    string `json:"-"`
	WebhookSecret string `json:"-"`
	WebhookURL    string `json:"webhook_url"`
	BotID         string `json:"bot_id"`
	BotUsername   string `json:"bot_username"`
	CreatedByUser int64  `json:"-"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type TelegramBinding struct {
	ID                string  `json:"id"`
	ConnectionID      int64   `json:"connection_id"`
	ProjectID         string  `json:"project_id"`
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle string  `json:"conversation_title,omitempty"`
	Audience          string  `json:"audience,omitempty"`
	ChatID            string  `json:"chat_id"`
	AllowedUserIDs    []int64 `json:"allowed_user_ids"`
	CreatedByUser     int64   `json:"-"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

type TelegramMessageLink struct {
	BindingID         string
	MessageID         int64
	TelegramMessageID int64
}

type TelegramActionToken struct {
	Token     string
	BindingID string
	MessageID int64
	ActionID  string
	ExpiresAt time.Time
}

func randomTelegramSecret(bytes int) (string, error) {
	if bytes <= 0 {
		bytes = 24
	}
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func encodeInt64s(values []int64) string {
	if values == nil {
		values = []int64{}
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func decodeInt64s(raw string) []int64 {
	out := []int64{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func normalizeTelegramUserIDs(values []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, id := range values {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *store) GetTelegramConnection(connectionID int64) (*TelegramConnectionConfig, error) {
	var cfg TelegramConnectionConfig
	err := s.db.QueryRow(`
		SELECT connection_id,webhook_key,webhook_secret,webhook_url,bot_id,bot_username,
		       created_by_user_id,created_at,updated_at
		FROM telegram_connections WHERE connection_id=?`, connectionID).Scan(
		&cfg.ConnectionID, &cfg.WebhookKey, &cfg.WebhookSecret, &cfg.WebhookURL,
		&cfg.BotID, &cfg.BotUsername, &cfg.CreatedByUser, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *store) GetTelegramConnectionByWebhookKey(key string) (*TelegramConnectionConfig, error) {
	var cfg TelegramConnectionConfig
	err := s.db.QueryRow(`
		SELECT connection_id,webhook_key,webhook_secret,webhook_url,bot_id,bot_username,
		       created_by_user_id,created_at,updated_at
		FROM telegram_connections WHERE webhook_key=?`, key).Scan(
		&cfg.ConnectionID, &cfg.WebhookKey, &cfg.WebhookSecret, &cfg.WebhookURL,
		&cfg.BotID, &cfg.BotUsername, &cfg.CreatedByUser, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *store) UpsertTelegramConnection(cfg TelegramConnectionConfig) error {
	_, err := s.db.Exec(`
		INSERT INTO telegram_connections
		(connection_id,webhook_key,webhook_secret,webhook_url,bot_id,bot_username,created_by_user_id,updated_at)
		VALUES (?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(connection_id) DO UPDATE SET
			webhook_key=excluded.webhook_key,
			webhook_secret=excluded.webhook_secret,
			webhook_url=excluded.webhook_url,
			bot_id=excluded.bot_id,
			bot_username=excluded.bot_username,
			updated_at=CURRENT_TIMESTAMP`,
		cfg.ConnectionID, cfg.WebhookKey, cfg.WebhookSecret, cfg.WebhookURL,
		cfg.BotID, cfg.BotUsername, cfg.CreatedByUser)
	return err
}

func (s *store) CreateTelegramBinding(binding TelegramBinding) (*TelegramBinding, error) {
	_, err := s.db.Exec(`
		INSERT INTO telegram_bindings
		(id,connection_id,project_id,conversation_id,chat_id,allowed_user_ids_json,created_by_user_id)
		VALUES (?,?,?,?,?,?,?)`, binding.ID, binding.ConnectionID, binding.ProjectID,
		binding.ConversationID, binding.ChatID, encodeInt64s(binding.AllowedUserIDs), binding.CreatedByUser)
	if err != nil {
		return nil, err
	}
	return s.GetTelegramBinding(binding.ID)
}

func scanTelegramBinding(scanner interface{ Scan(...any) error }) (*TelegramBinding, error) {
	var binding TelegramBinding
	var allowed string
	err := scanner.Scan(&binding.ID, &binding.ConnectionID, &binding.ProjectID,
		&binding.ConversationID, &binding.ConversationTitle, &binding.Audience,
		&binding.ChatID, &allowed, &binding.CreatedByUser, &binding.CreatedAt, &binding.UpdatedAt)
	if err != nil {
		return nil, err
	}
	binding.AllowedUserIDs = decodeInt64s(allowed)
	return &binding, nil
}

const telegramBindingColumns = `
	b.id,b.connection_id,b.project_id,b.conversation_id,c.title,c.audience,
	b.chat_id,b.allowed_user_ids_json,b.created_by_user_id,b.created_at,b.updated_at`

func (s *store) GetTelegramBinding(id string) (*TelegramBinding, error) {
	return scanTelegramBinding(s.db.QueryRow(`SELECT `+telegramBindingColumns+`
		FROM telegram_bindings b JOIN conversations c ON c.id=b.conversation_id
		WHERE b.id=?`, id))
}

func (s *store) GetTelegramBindingByChat(connectionID int64, chatID string) (*TelegramBinding, error) {
	return scanTelegramBinding(s.db.QueryRow(`SELECT `+telegramBindingColumns+`
		FROM telegram_bindings b JOIN conversations c ON c.id=b.conversation_id
		WHERE b.connection_id=? AND b.chat_id=?`, connectionID, chatID))
}

func (s *store) ListTelegramBindings(projectID string) ([]TelegramBinding, error) {
	rows, err := s.db.Query(`SELECT `+telegramBindingColumns+`
		FROM telegram_bindings b JOIN conversations c ON c.id=b.conversation_id
		WHERE b.project_id=? ORDER BY b.created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramBinding{}
	for rows.Next() {
		binding, err := scanTelegramBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *binding)
	}
	return out, rows.Err()
}

func (s *store) TelegramBindingsForConversation(conversationID string) ([]TelegramBinding, error) {
	rows, err := s.db.Query(`SELECT `+telegramBindingColumns+`
		FROM telegram_bindings b JOIN conversations c ON c.id=b.conversation_id
		WHERE b.conversation_id=? ORDER BY b.created_at`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramBinding{}
	for rows.Next() {
		binding, err := scanTelegramBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *binding)
	}
	return out, rows.Err()
}

func (s *store) DeleteTelegramBinding(projectID, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deliveries WHERE target=?`, "telegram:"+id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM telegram_bindings WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *store) ClaimTelegramUpdate(connectionID, updateID int64) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO telegram_updates(connection_id,update_id) VALUES (?,?)`, connectionID, updateID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *store) ReleaseTelegramUpdate(connectionID, updateID int64) {
	_, _ = s.db.Exec(`DELETE FROM telegram_updates WHERE connection_id=? AND update_id=?`, connectionID, updateID)
}

func (s *store) EnsureExternalParticipant(conversationID, identity, displayName string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO participants(conversation_id,external_identity,display_name)
		VALUES (?,?,?)`, conversationID, identity, displayName)
	return err
}

func (s *store) GetTelegramMessageLink(bindingID string, messageID int64) (*TelegramMessageLink, error) {
	var link TelegramMessageLink
	err := s.db.QueryRow(`SELECT binding_id,message_id,telegram_message_id
		FROM telegram_message_links WHERE binding_id=? AND message_id=?`, bindingID, messageID).
		Scan(&link.BindingID, &link.MessageID, &link.TelegramMessageID)
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (s *store) SaveTelegramMessageLink(bindingID string, messageID, telegramMessageID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO telegram_message_links(binding_id,message_id,telegram_message_id,updated_at)
		VALUES (?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(binding_id,message_id) DO UPDATE SET
			telegram_message_id=excluded.telegram_message_id,updated_at=CURRENT_TIMESTAMP`,
		bindingID, messageID, telegramMessageID)
	return err
}

func (s *store) EnsureTelegramActionToken(bindingID string, messageID int64, actionID string) (string, error) {
	var token string
	var expires time.Time
	err := s.db.QueryRow(`SELECT token,expires_at FROM telegram_action_tokens
		WHERE binding_id=? AND message_id=? AND action_id=?`, bindingID, messageID, actionID).Scan(&token, &expires)
	if err == nil && expires.After(time.Now().UTC()) {
		return token, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	_, _ = s.db.Exec(`DELETE FROM telegram_action_tokens
		WHERE binding_id=? AND message_id=? AND action_id=?`, bindingID, messageID, actionID)
	token, err = randomTelegramSecret(16)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO telegram_action_tokens
		(token,binding_id,message_id,action_id,expires_at) VALUES (?,?,?,?,?)`,
		token, bindingID, messageID, actionID, time.Now().UTC().Add(30*24*time.Hour))
	return token, err
}

func (s *store) GetTelegramActionToken(token string) (*TelegramActionToken, error) {
	var action TelegramActionToken
	var used sql.NullTime
	err := s.db.QueryRow(`SELECT token,binding_id,message_id,action_id,expires_at,used_at
		FROM telegram_action_tokens WHERE token=?`, token).Scan(
		&action.Token, &action.BindingID, &action.MessageID, &action.ActionID, &action.ExpiresAt, &used)
	if err != nil {
		return nil, err
	}
	if used.Valid {
		return nil, errors.New("Telegram action already used")
	}
	if !action.ExpiresAt.After(time.Now().UTC()) {
		return nil, errors.New("Telegram action expired")
	}
	return &action, nil
}

func (s *store) MarkTelegramActionUsed(token string) {
	_, _ = s.db.Exec(`UPDATE telegram_action_tokens SET used_at=CURRENT_TIMESTAMP WHERE token=? AND used_at IS NULL`, token)
}

func (s *store) PruneTelegramState() error {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	if _, err := s.db.Exec(`DELETE FROM telegram_updates WHERE received_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM telegram_action_tokens
		WHERE expires_at < ? OR (used_at IS NOT NULL AND used_at < ?)`, time.Now().UTC(), cutoff)
	return err
}

// ─── platform connection + setup HTTP ───────────────────────────────

type telegramConnectionView struct {
	ConnectionID int64  `json:"connection_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	ProjectID    string `json:"project_id"`
	Enabled      bool   `json:"enabled"`
	BotID        string `json:"bot_id,omitempty"`
	BotUsername  string `json:"bot_username,omitempty"`
	WebhookURL   string `json:"webhook_url,omitempty"`
}

func (a *App) boundTelegramConnections(app *sdk.AppCtx, projectID string) ([]sdk.PlatformConnection, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("platform unavailable")
	}
	out := []sdk.PlatformConnection{}
	seen := map[int64]bool{}
	for _, bound := range app.IntegrationsFor(telegramIntegrationRole) {
		if bound == nil || bound.ConnectionID <= 0 || seen[bound.ConnectionID] {
			continue
		}
		seen[bound.ConnectionID] = true
		conn, err := app.PlatformAPI().GetConnection(bound.ConnectionID)
		if err != nil || conn == nil {
			continue
		}
		if conn.AppSlug != "telegram" || conn.Status != "active" {
			continue
		}
		if conn.ProjectID != "" && conn.ProjectID != projectID {
			continue
		}
		out = append(out, *conn)
	}
	return out, nil
}

func (a *App) boundTelegramConnection(app *sdk.AppCtx, projectID string, connectionID int64) (*sdk.PlatformConnection, error) {
	connections, err := a.boundTelegramConnections(app, projectID)
	if err != nil {
		return nil, err
	}
	for i := range connections {
		if connections[i].ID == connectionID {
			return &connections[i], nil
		}
	}
	return nil, errors.New("Telegram connection is not active and bound to Conversations in this project")
}

func (a *App) executeTelegram(app *sdk.AppCtx, connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("Telegram integration is unavailable")
	}
	result, err := app.PlatformAPI().ExecuteIntegrationTool(connectionID, tool, input)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("Telegram integration returned no result")
	}
	if !result.Success || result.Status < 200 || result.Status >= 300 {
		message := strings.TrimSpace(string(result.Data))
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, fmt.Errorf("Telegram integration returned %d: %s", result.Status, message)
	}
	var envelope struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if len(result.Data) != 0 && json.Unmarshal(result.Data, &envelope) == nil && !envelope.OK {
		if envelope.Description == "" {
			envelope.Description = "Telegram rejected the request"
		}
		return nil, errors.New(envelope.Description)
	}
	return result, nil
}

func telegramPublicURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("a public server URL is required before enabling Telegram")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1") {
			return "", errors.New("Telegram requires a public HTTPS server URL")
		}
	}
	return raw, nil
}

func (a *App) handleTelegramConnections(w http.ResponseWriter, r *http.Request) {
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
		out := make([]telegramConnectionView, 0, len(connections))
		for _, conn := range connections {
			view := telegramConnectionView{ConnectionID: conn.ID, Name: conn.Name, Status: conn.Status, ProjectID: conn.ProjectID}
			if cfg, err := a.store.GetTelegramConnection(conn.ID); err == nil {
				view.Enabled = true
				view.BotID = cfg.BotID
				view.BotUsername = cfg.BotUsername
				view.WebhookURL = cfg.WebhookURL
			}
			out = append(out, view)
		}
		writeJSON(w, map[string]any{"connections": out})
	case http.MethodPost:
		var body struct {
			ConnectionID int64 `json:"connection_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil || body.ConnectionID <= 0 {
			http.Error(w, "connection_id required", http.StatusBadRequest)
			return
		}
		conn, err := a.boundTelegramConnection(app, projectID, body.ConnectionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		cfg, err := a.enableTelegramConnection(app.WithProject(projectID), conn.ID, userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, telegramConnectionView{ConnectionID: conn.ID, Name: conn.Name, Status: conn.Status,
			ProjectID: conn.ProjectID, Enabled: true, BotID: cfg.BotID, BotUsername: cfg.BotUsername, WebhookURL: cfg.WebhookURL})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) enableTelegramConnection(app *sdk.AppCtx, connectionID, userID int64) (*TelegramConnectionConfig, error) {
	identity, err := app.PlatformAPI().WhoAmI()
	if err != nil || identity == nil {
		return nil, errors.New("could not read the Conversations install identity")
	}
	publicURL, err := telegramPublicURL(identity.PublicURL)
	if err != nil {
		return nil, err
	}
	cfg, getErr := a.store.GetTelegramConnection(connectionID)
	if getErr != nil && !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	}
	if cfg == nil {
		key, err := randomTelegramSecret(18)
		if err != nil {
			return nil, err
		}
		secret, err := randomTelegramSecret(32)
		if err != nil {
			return nil, err
		}
		cfg = &TelegramConnectionConfig{ConnectionID: connectionID, WebhookKey: key, WebhookSecret: secret, CreatedByUser: userID}
	}
	cfg.WebhookURL = publicURL + "/api/apps/conversations/telegram-webhook/" + url.PathEscape(cfg.WebhookKey)
	profile, err := a.executeTelegram(app, connectionID, "get_me", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("verify Telegram bot: %w", err)
	}
	var me struct {
		Result struct {
			ID       json.Number `json:"id"`
			Username string      `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(profile.Data, &me); err != nil || me.Result.ID.String() == "" {
		return nil, errors.New("Telegram get_me returned an invalid bot profile")
	}
	cfg.BotID = me.Result.ID.String()
	cfg.BotUsername = strings.TrimSpace(me.Result.Username)
	if _, err := a.executeTelegram(app, connectionID, "set_webhook", map[string]any{
		"url": cfg.WebhookURL, "secret_token": cfg.WebhookSecret,
		"allowed_updates": []string{"message", "callback_query"},
	}); err != nil {
		return nil, fmt.Errorf("set Telegram webhook: %w", err)
	}
	if err := a.store.UpsertTelegramConnection(*cfg); err != nil {
		return nil, err
	}
	return a.store.GetTelegramConnection(connectionID)
}

func (a *App) handleTelegramBindings(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		bindings, err := a.store.ListTelegramBindings(projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"bindings": bindings})
	case http.MethodPost:
		var body struct {
			ConnectionID   int64   `json:"connection_id"`
			ConversationID string  `json:"conversation_id"`
			ChatID         string  `json:"chat_id"`
			AllowedUserIDs []int64 `json:"allowed_user_ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		chatID, err := strconv.ParseInt(strings.TrimSpace(body.ChatID), 10, 64)
		if body.ConnectionID <= 0 || body.ConversationID == "" || err != nil || chatID == 0 {
			http.Error(w, "connection_id, conversation_id, and numeric chat_id are required", http.StatusBadRequest)
			return
		}
		conv, err := a.authorizeConversation(r, body.ConversationID)
		if err != nil {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
		if _, err := a.boundTelegramConnection(a.appCtx(r), projectID, body.ConnectionID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if _, err := a.store.GetTelegramConnection(body.ConnectionID); err != nil {
			http.Error(w, "enable the Telegram bot webhook before binding a chat", http.StatusConflict)
			return
		}
		allowed := normalizeTelegramUserIDs(body.AllowedUserIDs)
		if conv.Audience == "operator" && len(allowed) == 0 {
			if chatID > 0 {
				allowed = []int64{chatID}
			} else {
				http.Error(w, "operator group chats require at least one allowed Telegram user id", http.StatusBadRequest)
				return
			}
		}
		id, err := randomTelegramSecret(12)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		binding, err := a.store.CreateTelegramBinding(TelegramBinding{
			ID: "tgb-" + id, ConnectionID: body.ConnectionID, ProjectID: projectID,
			ConversationID: conv.ID, ChatID: strconv.FormatInt(chatID, 10),
			AllowedUserIDs: allowed, CreatedByUser: userID,
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				http.Error(w, "this Telegram chat is already bound", http.StatusConflict)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, binding)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		binding, err := a.store.GetTelegramBinding(id)
		if err != nil || binding.ProjectID != projectID {
			http.Error(w, "Telegram binding not found", http.StatusNotFound)
			return
		}
		if _, err := a.authorizeConversation(r, binding.ConversationID); err != nil {
			http.Error(w, "Telegram binding not found", http.StatusNotFound)
			return
		}
		if err := a.store.DeleteTelegramBinding(projectID, id); err != nil {
			http.Error(w, "Telegram binding not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"deleted": true})
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

// ─── inbound webhook ────────────────────────────────────────────────

type telegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *telegramMessage       `json:"message"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      telegramUser `json:"from"`
	Chat      telegramChat `json:"chat"`
	Text      string       `json:"text"`
	Caption   string       `json:"caption"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func telegramDisplayName(user telegramUser) string {
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if name == "" && user.Username != "" {
		name = "@" + strings.TrimPrefix(user.Username, "@")
	}
	if name == "" {
		name = "Telegram user " + strconv.FormatInt(user.ID, 10)
	}
	return name
}

func telegramSenderAllowed(binding *TelegramBinding, userID int64) bool {
	if binding == nil || userID <= 0 {
		return false
	}
	if binding.Audience == "public" && len(binding.AllowedUserIDs) == 0 {
		return true
	}
	for _, allowed := range binding.AllowedUserIDs {
		if allowed == userID {
			return true
		}
	}
	return false
}

func (a *App) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/telegram-webhook/"), "/")
	if key == "" || strings.Contains(key, "/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	cfg, err := a.store.GetTelegramConnectionByWebhookKey(key)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	provided := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.WebhookSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var update telegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil || update.UpdateID <= 0 {
		http.Error(w, "invalid Telegram update", http.StatusBadRequest)
		return
	}
	claimed, err := a.store.ClaimTelegramUpdate(cfg.ConnectionID, update.UpdateID)
	if err != nil {
		http.Error(w, "could not claim Telegram update", http.StatusInternalServerError)
		return
	}
	if !claimed {
		writeJSON(w, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if err := a.processTelegramUpdate(cfg, update); err != nil {
		a.store.ReleaseTelegramUpdate(cfg.ConnectionID, update.UpdateID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) processTelegramUpdate(cfg *TelegramConnectionConfig, update telegramUpdate) error {
	switch {
	case update.CallbackQuery != nil:
		return a.processTelegramCallback(cfg, *update.CallbackQuery)
	case update.Message != nil:
		return a.processTelegramMessage(cfg, update.UpdateID, *update.Message)
	default:
		return nil
	}
}

func (a *App) processTelegramMessage(cfg *TelegramConnectionConfig, updateID int64, incoming telegramMessage) error {
	binding, err := a.store.GetTelegramBindingByChat(cfg.ConnectionID, strconv.FormatInt(incoming.Chat.ID, 10))
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Explicit routing: silently ignore chats the operator did not bind.
	}
	if err != nil {
		return err
	}
	if _, err := a.boundTelegramConnection(mountedCtx, binding.ProjectID, cfg.ConnectionID); err != nil {
		return nil
	}
	if !telegramSenderAllowed(binding, incoming.From.ID) {
		return nil
	}
	content := strings.TrimSpace(incoming.Text)
	if content == "" {
		content = strings.TrimSpace(incoming.Caption)
	}
	if content == "" {
		return nil
	}
	conv, err := a.store.GetConversation(binding.ConversationID)
	if err != nil || conv.ProjectID != binding.ProjectID {
		return errors.New("Telegram conversation binding is invalid")
	}
	externalID := fmt.Sprintf("telegram:%d:%d", cfg.ConnectionID, incoming.From.ID)
	if err := a.store.EnsureExternalParticipant(conv.ID, externalID, telegramDisplayName(incoming.From)); err != nil {
		return err
	}
	app := mountedCtx
	if app != nil {
		app = app.WithProject(binding.ProjectID)
	}
	msg, inserted, err := a.appendAndDeliver(app, conv, &Message{
		ConversationID: conv.ID,
		Role:           "user",
		Content:        content,
		ExternalSender: externalID,
		ClientID:       fmt.Sprintf("telegram:%d:%d", cfg.ConnectionID, updateID),
		Metadata: map[string]any{
			"transport": "telegram", "telegram_connection_id": cfg.ConnectionID,
			"telegram_chat_id": incoming.Chat.ID, "telegram_message_id": incoming.MessageID,
			"telegram_user_id": incoming.From.ID, "telegram_username": incoming.From.Username,
		},
	})
	if err != nil {
		return err
	}
	if inserted {
		a.forwardToAgents(app, conv, msg, nil)
	}
	return nil
}

func (a *App) processTelegramCallback(cfg *TelegramConnectionConfig, callback telegramCallbackQuery) error {
	if callback.Message == nil {
		return nil
	}
	binding, err := a.store.GetTelegramBindingByChat(cfg.ConnectionID, strconv.FormatInt(callback.Message.Chat.ID, 10))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := a.boundTelegramConnection(mountedCtx, binding.ProjectID, cfg.ConnectionID); err != nil {
		return nil
	}
	if !telegramSenderAllowed(binding, callback.From.ID) {
		return nil
	}
	tokenText, ok := strings.CutPrefix(strings.TrimSpace(callback.Data), "cv:")
	if !ok || tokenText == "" {
		return nil
	}
	action, err := a.store.GetTelegramActionToken(tokenText)
	if err != nil || action.BindingID != binding.ID {
		a.answerTelegramCallback(cfg.ConnectionID, callback.ID, "This action is no longer available")
		return nil
	}
	link, err := a.store.GetTelegramMessageLink(binding.ID, action.MessageID)
	if err != nil || link.TelegramMessageID != callback.Message.MessageID {
		return nil
	}
	msg, err := a.store.GetMessage(action.MessageID)
	if err != nil || msg.ConversationID != binding.ConversationID {
		return nil
	}
	app := mountedCtx
	if app != nil {
		app = app.WithProject(binding.ProjectID)
	}
	actor := fmt.Sprintf("telegram:%d:%d", cfg.ConnectionID, callback.From.ID)
	if _, err := a.resolveApprovalExternal(app, msg, action.ActionID, "", actor); err != nil {
		a.answerTelegramCallback(cfg.ConnectionID, callback.ID, err.Error())
		return nil
	}
	a.store.MarkTelegramActionUsed(tokenText)
	a.answerTelegramCallback(cfg.ConnectionID, callback.ID, "Recorded: "+action.ActionID)
	return nil
}

func (a *App) answerTelegramCallback(connectionID int64, callbackID, text string) {
	if strings.TrimSpace(callbackID) == "" || mountedCtx == nil {
		return
	}
	if len([]rune(text)) > 180 {
		text = string([]rune(text)[:180])
	}
	_, _ = a.executeTelegram(mountedCtx, connectionID, "answer_callback_query", map[string]any{
		"callback_query_id": callbackID, "text": text,
	})
}

// ─── outbound adapter ───────────────────────────────────────────────

type telegramAdapter struct{ app *App }

func (*telegramAdapter) ID() string { return "telegram" }

func (t *telegramAdapter) Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	if t == nil || t.app == nil {
		return errors.New("Telegram adapter unavailable")
	}
	binding, err := t.app.store.GetTelegramBinding(target)
	if err != nil {
		return fmt.Errorf("Telegram binding not found: %w", err)
	}
	if binding.ConversationID != conv.ID || binding.ProjectID != conv.ProjectID {
		return errors.New("Telegram binding does not match the conversation")
	}
	if strings.HasPrefix(msg.ExternalSender, "telegram:") {
		return nil
	}
	if len(msg.Attachments) != 0 {
		return errors.New("Telegram text transport does not yet support Conversations attachments")
	}
	if app == nil {
		return errors.New("platform unavailable")
	}
	app = app.WithProject(binding.ProjectID)
	if _, err := t.app.boundTelegramConnection(app, binding.ProjectID, binding.ConnectionID); err != nil {
		return err
	}
	text, markup, err := t.app.telegramRenderMessage(binding, msg)
	if err != nil {
		return err
	}
	input := map[string]any{"chat_id": binding.ChatID, "text": text}
	if markup != nil {
		input["reply_markup"] = markup
	}
	link, linkErr := t.app.store.GetTelegramMessageLink(binding.ID, msg.ID)
	if linkErr == nil {
		input["message_id"] = link.TelegramMessageID
		if msg.ComponentKind == kindApproval && markup == nil {
			input["reply_markup"] = map[string]any{"inline_keyboard": []any{}}
		}
		_, err := t.app.executeTelegram(app, binding.ConnectionID, "edit_message_text", input)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return err
		}
		return nil
	}
	if linkErr != nil && !errors.Is(linkErr, sql.ErrNoRows) {
		return linkErr
	}
	result, err := t.app.executeTelegram(app, binding.ConnectionID, "send_message", input)
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result.Data, &response); err != nil || response.Result.MessageID <= 0 {
		return errors.New("Telegram send_message returned no message id")
	}
	return t.app.store.SaveTelegramMessageLink(binding.ID, msg.ID, response.Result.MessageID)
}

func telegramTextLimit(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= 4096 {
		return string(runes)
	}
	return string(runes[:4093]) + "..."
}

func componentString(component Component, key string) string {
	value, _ := component.Props[key].(string)
	return strings.TrimSpace(value)
}

func approvalActions(component Component) []approvalAction {
	if typed, ok := component.Props["actions"].([]approvalAction); ok {
		return typed
	}
	raw, _ := component.Props["actions"].([]any)
	out := make([]approvalAction, 0, len(raw))
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		label, _ := entry["label"].(string)
		if id == "" {
			continue
		}
		if label == "" {
			label = id
		}
		out = append(out, approvalAction{ID: id, Label: label})
	}
	return out
}

func (a *App) telegramRenderMessage(binding *TelegramBinding, msg *Message) (string, map[string]any, error) {
	text := strings.TrimSpace(msg.Content)
	var markup map[string]any
	for _, component := range msg.Components {
		switch component.Name {
		case "approval-card":
			title, body, status := componentString(component, "title"), componentString(component, "body"), componentString(component, "status")
			if status == "" {
				status = cardStatus(msg)
			}
			parts := []string{"Approval required"}
			if status != "" && status != "pending" {
				parts[0] = "Approval resolved: " + status
			}
			if title != "" {
				parts = append(parts, title)
			}
			if body != "" && body != title {
				parts = append(parts, body)
			}
			text = strings.Join(parts, "\n\n")
			if status == "pending" {
				row := []map[string]string{}
				for _, action := range approvalActions(component) {
					token, err := a.store.EnsureTelegramActionToken(binding.ID, msg.ID, action.ID)
					if err != nil {
						return "", nil, err
					}
					row = append(row, map[string]string{"text": action.Label, "callback_data": "cv:" + token})
				}
				if len(row) != 0 {
					markup = map[string]any{"inline_keyboard": [][]map[string]string{row}}
				}
			}
		case "report-card":
			title, summary := componentString(component, "title"), componentString(component, "summary")
			text = strings.TrimSpace("Report: " + title + "\n\n" + summary)
		case "alert-card":
			severity, alertText := componentString(component, "severity"), componentString(component, "text")
			if severity == "" {
				severity = "info"
			}
			text = strings.TrimSpace("Alert (" + severity + "): " + alertText)
		}
	}
	if text == "" {
		text = "Conversation updated"
	}
	return telegramTextLimit(text), markup, nil
}
