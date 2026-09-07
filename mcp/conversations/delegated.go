package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type delegatedContextKey struct{}
type delegatedPrincipal struct {
	UserID    int64
	Agents    map[int64]bool
	Directive string
}
type delegatedScope struct {
	Type      string   `json:"type"`
	App       string   `json:"app"`
	Actions   []string `json:"actions"`
	AgentIDs  []int64  `json:"agent_ids"`
	Directive string   `json:"directive"`
}

func delegatedFrom(r *http.Request) *delegatedPrincipal {
	p, _ := r.Context().Value(delegatedContextKey{}).(*delegatedPrincipal)
	return p
}

// The platform authenticates the token and stamps subject headers. Its UserID
// belongs to the issuer's installer, so it must never become a visitor's owner.
func (a *App) delegatedHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-ID"))
		issuer := strings.TrimSpace(r.Header.Get("X-Apteva-Issuer-App"))
		if subject == "" && issuer == "" && r.Header.Get("X-Apteva-Subject-Type") == "" && r.Header.Get("X-Apteva-Issuer-Install-ID") == "" {
			next(w, r)
			return
		}
		project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
		kind := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Type"))
		install := strings.TrimSpace(r.Header.Get("X-Apteva-Issuer-Install-ID"))
		if subject == "" || issuer == "" || project == "" || kind == "" || install == "" {
			http.Error(w, "incomplete application-user identity", 401)
			return
		}
		action := delegatedAction(r.Method, r.URL.Path)
		p := &delegatedPrincipal{Agents: map[int64]bool{}}
		var scopes []delegatedScope
		if json.Unmarshal([]byte(r.Header.Get("X-Apteva-Scopes")), &scopes) != nil {
			http.Error(w, "invalid application-user scope", 403)
			return
		}
		for _, scope := range scopes {
			if scope.Type != "app_user" || scope.App != "conversations" {
				continue
			}
			allowed := false
			for _, name := range scope.Actions {
				if name == action && action != "" {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
			for _, id := range scope.AgentIDs {
				if id > 0 {
					p.Agents[id] = true
				}
			}
			p.Directive = scope.Directive
		}
		if len(p.Agents) == 0 {
			http.Error(w, "application-user action not allowed", 403)
			return
		}
		// Aggregate reads require a permitted agent projection. A one-agent token
		// can omit it; multi-agent hosts choose explicitly. No installation discovery.
		q := r.URL.Query()
		if (r.URL.Path == "/chats" && r.Method == "GET" && q.Get("id") == "") || r.URL.Path == "/inbox" || r.URL.Path == "/unread-summary" {
			key := "agent_id"
			if q.Get("lead_agent_id") != "" {
				key = "lead_agent_id"
			}
			id, _ := strconv.ParseInt(q.Get(key), 10, 64)
			if id == 0 && len(p.Agents) == 1 {
				for only := range p.Agents {
					id = only
				}
				q.Set(key, strconv.FormatInt(id, 10))
				r.URL.RawQuery = q.Encode()
			}
			if !p.Agents[id] {
				http.Error(w, "permitted agent projection required", 403)
				return
			}
		}
		if r.URL.Path == "/stream" && q.Get("chat_id") == "" {
			http.Error(w, "conversation stream required", 403)
			return
		}
		var id int64
		identity := []any{project, issuer, install, kind, subject, r.Header.Get("X-Apteva-Organization-ID")}
		lookup := `SELECT id FROM external_principals WHERE project_id=? AND issuer_app=? AND issuer_install_id=? AND subject_type=? AND subject_id=? AND organization_id=?`
		err := a.store.db.QueryRow(lookup, identity...).Scan(&id)
		if err == sql.ErrNoRows {
			_, err = a.store.db.Exec(`INSERT INTO external_principals(project_id,issuer_app,issuer_install_id,subject_type,subject_id,organization_id) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, identity...)
			if err == nil {
				err = a.store.db.QueryRow(lookup, identity...).Scan(&id)
			}
		}
		if err != nil {
			http.Error(w, "subject identity unavailable", 500)
			return
		}
		p.UserID = -id
		next(w, r.WithContext(context.WithValue(r.Context(), delegatedContextKey{}, p)))
	}
}
func delegatedAction(method, path string) string {
	switch method + " " + path {
	case "GET /chats":
		return "chat.read"
	case "POST /chats":
		return "chat.create"
	case "PATCH /chats":
		return "chat.update"
	case "DELETE /chats":
		return "chat.delete"
	case "GET /messages", "GET /changes":
		return "message.read"
	case "POST /messages":
		return "message.send"
	case "GET /stream":
		return "stream.read"
	case "POST /seen":
		return "chat.seen"
	case "GET /unread-summary":
		return "chat.read"
	case "GET /participants":
		return "chat.read"
	case "GET /agents":
		return "chat.read"
	case "GET /inbox":
		return "inbox.read"
	case "POST /message-action":
		return "approval.act"
	case "POST /message-dismiss":
		return "inbox.dismiss"
	case "GET /deliveries":
		return "delivery.read"
	case "POST /delivery-failures":
		return "delivery.retry"
	}
	return ""
}
func delegatedConversationAllowed(r *http.Request, c *Conversation) error {
	if p := delegatedFrom(r); p != nil && (c.Audience != "public" || !p.Agents[c.LeadAgentID]) {
		return fmt.Errorf("conversation not found")
	}
	return nil
}

// Apply the same audience/roster rule to aggregate reads before pagination and
// counts. IDs are trusted integers, rendered canonically (never request text).
func allowedConversationSQL(agents []int64) string {
	if len(agents) == 0 {
		return ""
	}
	ids := make([]string, 0, len(agents))
	for _, id := range agents {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	sort.Strings(ids)
	allowed := strings.Join(ids, ",")
	return " AND c.audience='public' AND c.lead_agent_id IN (" + allowed + ") AND NOT EXISTS(SELECT 1 FROM participants scope_agent WHERE scope_agent.conversation_id=c.id AND scope_agent.agent_id>0 AND scope_agent.agent_id NOT IN (" + allowed + "))"
}
func requestAllowedAgents(r *http.Request) []int64 {
	p := delegatedFrom(r)
	if p == nil {
		return nil
	}
	ids := make([]int64, 0, len(p.Agents))
	for id := range p.Agents {
		ids = append(ids, id)
	}
	return ids
}
