package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMobileConversationLimit = 10
	minMobileConversationLimit     = 4
	maxMobileConversationLimit     = 20
)

type mobileConversation struct {
	ID       string `json:"id"`
	Peer     string `json:"peer"`
	Channel  string `json:"channel"`
	Preview  string `json:"preview"`
	LatestAt string `json:"latest_at"`
	Status   string `json:"status"`
}

type mobileConversationsResponse struct {
	Conversations []mobileConversation `json:"conversations"`
}

type mobileConversationRow struct {
	messageID int64
	channel   string
	direction string
	peer      string
	body      string
	latestAt  string
	status    string
}

func mobileConversationLimit(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMobileConversationLimit
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return defaultMobileConversationLimit
	}
	if limit < minMobileConversationLimit {
		return minMobileConversationLimit
	}
	if limit > maxMobileConversationLimit {
		return maxMobileConversationLimit
	}
	return limit
}

func mobileConversationChannel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case channelSMS:
		return channelSMS
	case channelWhatsApp:
		return channelWhatsApp
	default:
		return "all"
	}
}

func mobileConversationTimestamp(value string) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return value
}

func mobileConversationPreview(direction, body string) string {
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		body = "Message"
	}
	runes := []rune(body)
	if len(runes) > 240 {
		body = string(runes[:240])
	}
	if direction == "out" {
		return "You: " + body
	}
	return body
}

func mobileChannelLabel(channel string) string {
	if channel == channelWhatsApp {
		return "WhatsApp"
	}
	return "SMS"
}

func dbMobileConversations(db *sql.DB, projectID, channel string, limit int) ([]mobileConversation, error) {
	whereChannel := ""
	args := []any{projectID}
	if channel == channelSMS || channel == channelWhatsApp {
		whereChannel = " AND m.channel = ?"
		args = append(args, channel)
	}
	args = append(args, limit)

	// Only outbound rows join recipients: an inbound recipient is a local
	// sender, while its remote peer is the canonical From address.
	query := `
		WITH raw_candidates AS (
			SELECT m.id AS message_id,
			       m.channel,
			       m.direction,
			       CASE WHEN m.direction = 'in' THEN m.from_canonical ELSE mr.address END AS raw_peer,
			       substr(COALESCE(m.body_text, ''), 1, 1000) AS body_text,
			       COALESCE(
			         NULLIF(CASE WHEN m.direction = 'in' THEN m.received_at ELSE m.sent_at END, ''),
			         m.created_at
			       ) AS message_at,
			       m.status
			FROM messages m
			LEFT JOIN message_recipients mr
			  ON m.direction = 'out' AND mr.message_id = m.id
			WHERE m.project_id = ?
			  AND m.channel IN ('sms', 'whatsapp')` + whereChannel + `
		), candidates AS (
			SELECT message_id,
			       channel,
			       direction,
			       CASE
			         WHEN lower(trim(raw_peer)) LIKE 'whatsapp:%' THEN substr(trim(raw_peer), 10)
			         WHEN lower(trim(raw_peer)) LIKE 'tel:%' THEN substr(trim(raw_peer), 5)
			         WHEN lower(trim(raw_peer)) LIKE 'sms:%' THEN substr(trim(raw_peer), 5)
			         ELSE trim(raw_peer)
			       END AS peer,
			       body_text,
			       message_at,
			       status
			FROM raw_candidates
			WHERE trim(COALESCE(raw_peer, '')) != ''
		), ranked AS (
			SELECT *,
			       row_number() OVER (
			         PARTITION BY channel, peer
			         ORDER BY datetime(message_at) DESC, message_id DESC
			       ) AS conversation_rank
			FROM candidates
		)
		SELECT message_id, channel, direction, peer, body_text, message_at, status
		FROM ranked
		WHERE conversation_rank = 1
		ORDER BY datetime(message_at) DESC, message_id DESC
		LIMIT ?`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	selected := make([]mobileConversationRow, 0, limit)
	messageIDs := make([]int64, 0, limit)
	for rows.Next() {
		var row mobileConversationRow
		if err := rows.Scan(&row.messageID, &row.channel, &row.direction, &row.peer, &row.body, &row.latestAt, &row.status); err != nil {
			return nil, err
		}
		selected = append(selected, row)
		messageIDs = append(messageIDs, row.messageID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the cursor before the status query. Install databases may use a
	// single SQLite connection, so a nested query while rows is open can block.
	if err := rows.Close(); err != nil {
		return nil, err
	}

	eventCounts := dbDeliveryEventCountsBatch(db, messageIDs)
	conversations := make([]mobileConversation, 0, len(selected))
	for _, row := range selected {
		conversations = append(conversations, mobileConversation{
			ID:       row.channel + ":" + row.peer,
			Peer:     row.peer,
			Channel:  mobileChannelLabel(row.channel),
			Preview:  mobileConversationPreview(row.direction, row.body),
			LatestAt: mobileConversationTimestamp(row.latestAt),
			Status:   effectiveMessageStatus(row.status, eventCounts[row.messageID]),
		})
	}
	return conversations, nil
}

func (a *App) handleMobileConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	projectID := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if projectID == "" {
		httpErr(w, http.StatusBadRequest, "project context required")
		return
	}
	if globalCtx == nil || globalCtx.AppDB() == nil {
		httpErr(w, http.StatusInternalServerError, "messaging database unavailable")
		return
	}
	query := r.URL.Query()
	conversations, err := dbMobileConversations(
		globalCtx.AppDB(),
		projectID,
		mobileConversationChannel(query.Get("default_channel")),
		mobileConversationLimit(query.Get("max_conversations")),
	)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conversations == nil {
		conversations = []mobileConversation{}
	}
	httpJSON(w, mobileConversationsResponse{Conversations: conversations})
}
