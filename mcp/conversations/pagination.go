package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type ConversationPage struct {
	Conversations []Conversation `json:"conversations"`
	NextCursor    string         `json:"next_cursor"`
}
type conversationCursor struct {
	Updated string `json:"updated"`
	ID      string `json:"id"`
}

func (s *store) ListConversationPage(project string, user, agent, lead int64, archived bool, query, cursor string, limit int) (ConversationPage, error) {
	out := ConversationPage{Conversations: []Conversation{}}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var cur conversationCursor
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &cur) != nil || cur.ID == "" || cur.Updated == "" {
			return out, fmt.Errorf("invalid conversation cursor")
		}
	}
	predicate := "c.archived_at IS NULL"
	if archived {
		predicate = "c.archived_at IS NOT NULL"
	}
	rows, err := s.db.Query(`SELECT `+prefixCols("c.", conversationCols)+` FROM conversations c
 WHERE c.project_id=? AND `+predicate+` AND (c.owner_user_id=0 OR c.owner_user_id=? OR EXISTS(SELECT 1 FROM participants p WHERE p.conversation_id=c.id AND p.user_id=?))
 AND (?=0 OR EXISTS(SELECT 1 FROM participants p WHERE p.conversation_id=c.id AND p.agent_id=?))
 AND (?=0 OR (c.lead_agent_id=? AND EXISTS(SELECT 1 FROM participants p WHERE p.conversation_id=c.id AND p.agent_id=?)))
 AND instr(lower(c.title),lower(?))>0
 AND (?='' OR julianday(c.updated_at)<julianday(?) OR (julianday(c.updated_at)=julianday(?) AND c.id<?))
 ORDER BY c.updated_at DESC,c.id DESC LIMIT ?`, project, user, user, agent, agent, lead, lead, lead, strings.TrimSpace(query), cursor, cur.Updated, cur.Updated, cur.ID, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return out, err
		}
		out.Conversations = append(out.Conversations, *c)
	}
	if len(out.Conversations) > limit {
		out.Conversations = out.Conversations[:limit]
		last := out.Conversations[limit-1]
		raw, _ := json.Marshal(conversationCursor{last.UpdatedAt.Format("2006-01-02 15:04:05"), last.ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, rows.Err()
}
