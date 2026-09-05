package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type ambiguousDeliveryError struct{ cause error }

func (e *ambiguousDeliveryError) Error() string {
	return "Provider acceptance is unknown: " + e.cause.Error()
}
func (e *ambiguousDeliveryError) Unwrap() error { return e.cause }

func telegramTextParts(text string) []string {
	var parts []string
	var b strings.Builder
	units := 0
	for _, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > 3500 {
			parts = append(parts, b.String())
			b.Reset()
			units = 0
		}
		b.WriteRune(r)
		units += width
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

// Long messages use plain text parts so no split can break an HTML entity/tag.
// Every confirmed part is recorded before proceeding to the next part.
func (t *telegramAdapter) deliverParts(app *sdk.AppCtx, binding *TelegramBinding, msg *Message, text string, markup map[string]any) error {
	parts := telegramTextParts(text)
	for index, part := range parts {
		input := map[string]any{"chat_id": binding.ChatID, "text": part}
		if len(parts) == 1 {
			input["text"] = telegramMarkdownToHTML(part)
			input["parse_mode"] = "HTML"
		}
		if index == 0 {
			if markup != nil {
				input["reply_markup"] = markup
			} else if msg.ComponentKind == kindApproval {
				input["reply_markup"] = map[string]any{"inline_keyboard": []any{}}
			}
		}
		var providerID int64
		var saved string
		err := t.app.store.db.QueryRow(`SELECT telegram_message_id,content FROM telegram_message_parts WHERE binding_id=? AND message_id=? AND part=?`, binding.ID, msg.ID, index).Scan(&providerID, &saved)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		// Upgrade existing single-message links in place.
		if providerID == 0 && index == 0 {
			if link, e := t.app.store.GetTelegramMessageLink(binding.ID, msg.ID); e == nil {
				providerID = link.TelegramMessageID
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		signature, _ := json.Marshal(input)
		if providerID != 0 && saved == string(signature) {
			continue
		}
		method := "send_message"
		if providerID != 0 {
			method = "edit_message_text"
			input["message_id"] = providerID
		}
		result, err := t.app.executeTelegram(app, binding.ConnectionID, method, input)
		if err != nil {
			if method == "edit_message_text" && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			} else {
				lower := strings.ToLower(err.Error())
				if method == "send_message" && (strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline") || strings.Contains(lower, "eof") || strings.Contains(lower, "connection reset") || strings.Contains(lower, "502") || strings.Contains(lower, "503") || strings.Contains(lower, "504")) {
					return &ambiguousDeliveryError{err}
				}
				return err
			}
		}
		if providerID == 0 {
			var response struct {
				Result struct {
					MessageID int64 `json:"message_id"`
				} `json:"result"`
			}
			if result == nil || json.Unmarshal(result.Data, &response) != nil || response.Result.MessageID <= 0 {
				return &ambiguousDeliveryError{errors.New("Telegram send_message returned no message id")}
			}
			providerID = response.Result.MessageID
		}
		if _, err := t.app.store.db.Exec(`INSERT INTO telegram_message_parts(binding_id,message_id,part,telegram_message_id,content) VALUES(?,?,?,?,?) ON CONFLICT(binding_id,message_id,part) DO UPDATE SET telegram_message_id=excluded.telegram_message_id,content=excluded.content`, binding.ID, msg.ID, index, providerID, string(signature)); err != nil {
			return &ambiguousDeliveryError{err}
		}
		if index == 0 {
			if err := t.app.store.SaveTelegramMessageLink(binding.ID, msg.ID, providerID); err != nil {
				return &ambiguousDeliveryError{err}
			}
		}
	}
	// Updated cards may become shorter. Remove only our known excess parts.
	rows, err := t.app.store.db.Query(`SELECT part,telegram_message_id FROM telegram_message_parts WHERE binding_id=? AND message_id=? AND part>=? ORDER BY part DESC`, binding.ID, msg.ID, len(parts))
	if err != nil {
		return err
	}
	type extraPart struct {
		index int
		id    int64
	}
	var extra []extraPart
	for rows.Next() {
		var p extraPart
		if err := rows.Scan(&p.index, &p.id); err != nil {
			rows.Close()
			return err
		}
		extra = append(extra, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range extra {
		if _, err := t.app.executeTelegram(app, binding.ConnectionID, "delete_message", map[string]any{"chat_id": binding.ChatID, "message_id": p.id}); err != nil {
			return fmt.Errorf("remove obsolete message part: %w", err)
		}
		if _, err := t.app.store.db.Exec(`DELETE FROM telegram_message_parts WHERE binding_id=? AND message_id=? AND part=?`, binding.ID, msg.ID, p.index); err != nil {
			return err
		}
	}
	return nil
}
