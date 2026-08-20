package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func telegramCommand(text string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	command := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	arg := ""
	if len(fields) > 1 {
		arg = strings.TrimSpace(fields[1])
	}
	return command, arg
}

func telegramMessageAddressesBot(cfg *TelegramConnectionConfig, incoming telegramMessage) bool {
	if incoming.Chat.Type == "private" {
		return true
	}
	if command, _ := telegramCommand(incoming.Text); command != "" {
		return true
	}
	username := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cfg.BotUsername), "@"))
	if username != "" && strings.Contains(strings.ToLower(incoming.Text), "@"+username) {
		return true
	}
	if incoming.ReplyToMessage != nil && strconv.FormatInt(incoming.ReplyToMessage.From.ID, 10) == cfg.BotID {
		return true
	}
	return false
}

func (a *App) sendTelegramSystem(app *sdk.AppCtx, connectionID int64, chatID, text string) error {
	if app == nil {
		return errors.New("platform unavailable")
	}
	_, err := a.executeTelegram(app, connectionID, "send_message", map[string]any{
		"chat_id": chatID, "text": text,
	})
	return err
}

func (a *App) processUnknownTelegramMessage(cfg *TelegramConnectionConfig, updateID int64, incoming telegramMessage) error {
	policy, err := a.store.GetTransportIntakePolicy(telegramTransport, cfg.ConnectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	app := mountedCtx
	if app == nil {
		return nil
	}
	app = app.WithProject(policy.ProjectID)
	if _, err := a.boundTelegramConnection(app, policy.ProjectID, cfg.ConnectionID); err != nil {
		return nil
	}
	command, argument := telegramCommand(incoming.Text)
	if (command == "start" || command == "connect") && argument != "" {
		binding, conv, redeemErr := a.redeemTelegramInvite(cfg, policy, incoming, argument)
		if redeemErr == nil {
			a.syncTelegramBotNameBestEffort(app, cfg.ConnectionID)
			_ = a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
				"Connected to “"+conv.Title+"”. Send a message whenever you’re ready.\n\nUse /new to start fresh or /help for options.")
			return nil
		}
		if strings.Contains(redeemErr.Error(), "wrong chat type") {
			return a.sendTelegramSystem(app, cfg.ConnectionID, strconv.FormatInt(incoming.Chat.ID, 10),
				"This invite was created for a different kind of Telegram chat. Ask the operator for a new link.")
		}
		if errors.Is(redeemErr, sql.ErrNoRows) || strings.Contains(redeemErr.Error(), "expired") || strings.Contains(redeemErr.Error(), "used") {
			return a.sendTelegramSystem(app, cfg.ConnectionID, strconv.FormatInt(incoming.Chat.ID, 10),
				"This invite link is invalid, expired, or already used. Ask the operator for a new one.")
		}
		return redeemErr
	}
	if policy.Mode == "closed" {
		return nil
	}
	if policy.Mode == "public" && incoming.Chat.Type == "private" {
		if allowed, err := a.telegramPublicIntakeAllowed(cfg.ConnectionID); err != nil || !allowed {
			return err
		}
		binding, conv, err := a.ensurePublicTelegramConversation(cfg, policy, incoming)
		if err != nil {
			return err
		}
		a.syncTelegramBotNameBestEffort(app, cfg.ConnectionID)
		if command == "start" || command == "new" || strings.TrimSpace(incoming.Text) == "" {
			return a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
				"Welcome — your conversation “"+conv.Title+"” is ready. Send your message whenever you like.\n\nUse /new to start fresh or /help for options.")
		}
		if handled, commandErr := a.processTelegramCommand(cfg, binding, incoming); handled || commandErr != nil {
			return commandErr
		}
		return a.processBoundTelegramMessage(cfg, binding, updateID, incoming)
	}
	// Groups stay quiet unless someone explicitly requests connection; this
	// avoids a newly-added bot responding to every message before approval.
	if incoming.Chat.Type != "private" && command != "connect" && command != "start" {
		return nil
	}
	if allowed, err := a.telegramPairingRequestAllowed(cfg.ConnectionID); err != nil || !allowed {
		return err
	}
	access, notify, err := a.store.EnsureTransportAccessRequest(TransportAccessRequest{
		Transport: telegramTransport, ConnectionID: cfg.ConnectionID, ProjectID: policy.ProjectID,
		ExternalChatID: strconv.FormatInt(incoming.Chat.ID, 10), ExternalUserID: strconv.FormatInt(incoming.From.ID, 10),
		ChatType: incoming.Chat.Type, DisplayName: telegramDisplayName(incoming.From),
		Username: strings.TrimPrefix(incoming.From.Username, "@"), ChatTitle: telegramChatDisplayName(incoming.Chat, incoming.From),
	})
	if err != nil {
		return err
	}
	if access.State == "blocked" || !notify {
		return nil
	}
	message := "Access request “" + access.PairingCode + "” is waiting for approval in Conversations. You don’t need to share a Telegram ID.\n\nThis request expires in one hour."
	if err := a.sendTelegramSystem(app, cfg.ConnectionID, access.ExternalChatID, message); err != nil {
		return err
	}
	a.store.MarkTransportAccessNotified(access.ID)
	return nil
}

func (a *App) telegramPairingRequestAllowed(connectionID int64) (bool, error) {
	var count int
	err := a.store.db.QueryRow(`SELECT COUNT(*) FROM transport_access_requests
		WHERE transport=? AND connection_id=? AND created_at>=datetime('now','-1 minute')`,
		telegramTransport, connectionID).Scan(&count)
	return count < 30, err
}

func (a *App) telegramPublicIntakeAllowed(connectionID int64) (bool, error) {
	var count int
	err := a.store.db.QueryRow(`SELECT COUNT(*) FROM telegram_bindings
		WHERE connection_id=? AND access_mode='public' AND created_at>=datetime('now','-1 hour')`, connectionID).Scan(&count)
	return count < 120, err
}

func (a *App) ensurePublicTelegramConversation(cfg *TelegramConnectionConfig, policy *TransportIntakePolicy, incoming telegramMessage) (*TelegramBinding, *Conversation, error) {
	chatID := strconv.FormatInt(incoming.Chat.ID, 10)
	if existing, err := a.store.GetTelegramBindingByChat(cfg.ConnectionID, chatID); err == nil {
		conv, convErr := a.store.GetConversation(existing.ConversationID)
		return existing, conv, convErr
	}
	displayName := telegramDisplayName(incoming.From)
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID: policy.ProjectID, LeadAgentID: policy.DefaultAgentID,
		Title:  telegramConversationTitle(policy.DefaultTitle, "", displayName),
		Origin: telegramTransport, ConversationKey: fmt.Sprintf("telegram:%d:chat:%s", cfg.ConnectionID, chatID),
		Audience: "public", ExternalIdentity: fmt.Sprintf("telegram:%d:%d", cfg.ConnectionID, incoming.From.ID),
		ExternalName: displayName,
	})
	if err != nil {
		return nil, nil, err
	}
	id, err := randomTelegramSecret(12)
	if err != nil {
		return nil, nil, err
	}
	binding, err := a.store.CreateTelegramBinding(TelegramBinding{
		ID: "tgb-" + id, ConnectionID: cfg.ConnectionID, ProjectID: policy.ProjectID,
		ConversationID: conv.ID, ChatID: chatID, ChatType: incoming.Chat.Type,
		ChatTitle:    telegramChatDisplayName(incoming.Chat, incoming.From),
		ChatUsername: strings.TrimPrefix(incoming.Chat.Username, "@"), AccessMode: "public",
		RequireMention: false,
	})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		binding, err = a.store.GetTelegramBindingByChat(cfg.ConnectionID, chatID)
	}
	if err != nil {
		return nil, nil, err
	}
	return binding, conv, nil
}

func (a *App) redeemTelegramInvite(cfg *TelegramConnectionConfig, policy *TransportIntakePolicy, incoming telegramMessage, rawToken string) (*TelegramBinding, *Conversation, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	invite, err := scanTransportInvite(tx.QueryRow(`SELECT id,token_hash,transport,connection_id,
		project_id,conversation_id,audience,chat_type,default_agent_id,created_by_user_id,expires_at,used_at
		FROM transport_invites WHERE token_hash=? AND transport=? AND connection_id=?`,
		hashTransportToken(rawToken), telegramTransport, cfg.ConnectionID))
	if err != nil {
		return nil, nil, err
	}
	if invite.UsedAt != nil || !invite.ExpiresAt.After(time.Now().UTC()) {
		return nil, nil, errors.New("invite expired or already used")
	}
	if invite.ProjectID != policy.ProjectID {
		return nil, nil, errors.New("invite project does not match intake policy")
	}
	if (invite.ChatType == "group" && incoming.Chat.Type == "private") || (invite.ChatType != "group" && incoming.Chat.Type != "private") {
		return nil, nil, errors.New("invite was opened in the wrong chat type")
	}
	var conv *Conversation
	if invite.ConversationID != "" {
		conv, err = scanConversation(tx.QueryRow(`SELECT `+conversationCols+` FROM conversations
			WHERE id=? AND project_id=? AND archived_at IS NULL`, invite.ConversationID, invite.ProjectID))
		if err != nil {
			return nil, nil, err
		}
	} else {
		conv = &Conversation{ID: newConversationID(), ProjectID: invite.ProjectID, LeadAgentID: invite.DefaultAgentID,
			Title: telegramConversationTitle(policy.DefaultTitle, telegramChatDisplayName(incoming.Chat, incoming.From), telegramDisplayName(incoming.From)),
			Kind:  "direct", Origin: telegramTransport, Audience: invite.Audience, OwnerUserID: invite.CreatedByUserID}
		if _, err := tx.Exec(`INSERT INTO conversations
			(id,project_id,lead_agent_id,title,kind,origin,conversation_key,audience,owner_user_id)
			VALUES (?,?,?,?,?,?,?,?,?)`, conv.ID, conv.ProjectID, conv.LeadAgentID, conv.Title, conv.Kind,
			conv.Origin, "", conv.Audience, conv.OwnerUserID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,agent_id) VALUES (?,?)`, conv.ID, conv.LeadAgentID); err != nil {
			return nil, nil, err
		}
	}
	externalIdentity := fmt.Sprintf("telegram:%d:%d", cfg.ConnectionID, incoming.From.ID)
	if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,external_identity,display_name) VALUES (?,?,?)`,
		conv.ID, externalIdentity, telegramDisplayName(incoming.From)); err != nil {
		return nil, nil, err
	}
	bindingID, err := randomTelegramSecret(12)
	if err != nil {
		return nil, nil, err
	}
	allowed := []int64{}
	if conv.Audience == "operator" && incoming.Chat.Type == "private" {
		allowed = []int64{incoming.From.ID}
	}
	requireMention := incoming.Chat.Type != "private" && policy.RequireGroupMention
	if _, err := tx.Exec(`INSERT INTO telegram_bindings
		(id,connection_id,project_id,conversation_id,chat_id,allowed_user_ids_json,created_by_user_id,
		 chat_type,chat_title,chat_username,require_mention,access_mode)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "tgb-"+bindingID, cfg.ConnectionID, invite.ProjectID,
		conv.ID, strconv.FormatInt(incoming.Chat.ID, 10), encodeInt64s(allowed), invite.CreatedByUserID,
		incoming.Chat.Type, telegramChatDisplayName(incoming.Chat, incoming.From),
		strings.TrimPrefix(incoming.Chat.Username, "@"), requireMention, "invite"); err != nil {
		return nil, nil, err
	}
	res, err := tx.Exec(`UPDATE transport_invites SET used_at=CURRENT_TIMESTAMP WHERE id=? AND used_at IS NULL`, invite.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, errors.New("invite already used")
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

func (a *App) processTelegramCommand(cfg *TelegramConnectionConfig, binding *TelegramBinding, incoming telegramMessage) (bool, error) {
	command, _ := telegramCommand(incoming.Text)
	if command == "" {
		return false, nil
	}
	app := mountedCtx
	if app == nil {
		return true, nil
	}
	app = app.WithProject(binding.ProjectID)
	switch command {
	case "start", "help":
		return true, a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
			"This Telegram chat is connected to Conversations.\n\n/new — start a fresh conversation\n/status — show the current conversation\n/help — show this message")
	case "status":
		return true, a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
			"Connected to “"+binding.ConversationTitle+"” ("+binding.Audience+").")
	case "new":
		if incoming.Chat.Type != "private" {
			return true, a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
				"/new is available in private chats. Group rooms keep one shared conversation.")
		}
		conv, err := a.rotateTelegramConversation(binding, incoming)
		if err != nil {
			return true, err
		}
		return true, a.sendTelegramSystem(app, cfg.ConnectionID, binding.ChatID,
			"Started “"+conv.Title+"”. Your previous conversation remains available in the dashboard.")
	default:
		return false, nil
	}
}

func (a *App) rotateTelegramConversation(binding *TelegramBinding, incoming telegramMessage) (*Conversation, error) {
	previous, err := a.store.GetConversation(binding.ConversationID)
	if err != nil {
		return nil, err
	}
	title := telegramConversationTitle("Telegram", "", telegramDisplayName(incoming.From))
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID: previous.ProjectID, LeadAgentID: previous.LeadAgentID, Title: title,
		Origin:   telegramTransport,
		Audience: previous.Audience, ExternalIdentity: fmt.Sprintf("telegram:%d:%d", binding.ConnectionID, incoming.From.ID),
		ExternalName: telegramDisplayName(incoming.From),
	})
	if err != nil {
		return nil, err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if previous.ConversationKey != "" {
		if _, err := tx.Exec(`UPDATE conversations SET conversation_key='' WHERE id=?`, previous.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE conversations SET conversation_key=? WHERE id=?`, previous.ConversationKey, conv.ID); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(`UPDATE telegram_bindings SET conversation_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND conversation_id=?`, conv.ID, binding.ID, previous.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("Telegram route changed; retry /new")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	conv, _ = a.store.GetConversation(conv.ID)
	return conv, nil
}
