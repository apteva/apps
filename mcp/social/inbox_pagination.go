package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Each stream has a durable continuation. Commit only after the adapter has
// persisted this batch. A bounded batch lets subsequent ticks finish backfill.
func collectInboxPages(ctx *sdk.AppCtx, accountID, connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	keyJSON, _ := json.Marshal(struct {
		Conn  int64
		Tool  string
		Input map[string]any
	}{connID, tool, input})
	key := fmt.Sprintf("pages:%x", sha256.Sum256(keyJSON))
	var cursor string
	_ = ctx.AppDB().QueryRow(`SELECT COALESCE(cursor,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, key).Scan(&cursor)
	args := map[string]any{}
	for k, v := range input {
		args[k] = v
	}
	cursorArg := "after"
	if strings.HasPrefix(tool, "get_user_") || tool == "get_dm_events" {
		cursorArg = "pagination_token"
	}
	if tool == "list_commented_posts" || tool == "get_post_comments" || tool == "list_conversation_messages" && input["access_token"] == nil || tool == "list_conversations" && input["accountId"] != nil {
		cursorArg = "cursor"
	}
	conversation := tool == "get_conversation" || tool == "facebook_get_conversation"
	original := tool
	replies := tool == "get_comment" && input["_social_reply_fields"] != nil
	replyFields, _ := input["_social_reply_fields"].(string)
	delete(args, "_social_reply_fields")
	if replies {
		args["fields"] = "id,replies.limit(100){" + replyFields + "}"
	}
	if cursor != "" {
		if replies {
			if !safeInboxCursor.MatchString(cursor) {
				return nil, fmt.Errorf("invalid reply cursor")
			}
			args["fields"] = "id,replies.limit(100).after(" + cursor + "){" + replyFields + "}"
		} else {
			args[cursorArg] = cursor
		}
		if conversation {
			tool = "list_conversation_messages"
		}
	}
	var first map[string]any
	var all []any
	var users []any
	var last *sdk.ExecuteResult
	warning := ""
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		if !consumeInboxBudget(ctx, accountID) {
			if last == nil {
				return nil, fmt.Errorf("inbox request budget or provider backoff reached")
			}
			warning = "more inbox history queued after request budget"
			break
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, args)
		if res != nil && res.Status == 429 {
			deferInboxPolling(ctx, accountID)
		}
		if err != nil || res == nil || !res.Success {
			if last == nil {
				return res, err
			}
			warning = "provider pagination incomplete"
			break
		}
		var obj map[string]any
		if err = json.Unmarshal(res.Data, &obj); err != nil {
			return nil, err
		}
		if obj == nil {
			obj = map[string]any{}
		}
		if first == nil {
			first = obj
		}
		last = res
		holder := obj
		if replies {
			if nested, ok := obj["replies"].(map[string]any); ok {
				holder = nested
			}
		}
		if conversation {
			if nested, ok := obj["messages"].(map[string]any); ok {
				holder = nested
			}
		}
		_, items := inboxPageItems(holder)
		all = append(all, items...)
		if inc, ok := obj["includes"].(map[string]any); ok {
			if u, ok := inc["users"].([]any); ok {
				users = append(users, u...)
			}
		}
		next := inboxNextCursor(holder)
		cursor = next
		if next == "" {
			break
		}
		if seen[next] {
			warning = "provider repeated a pagination cursor"
			break
		}
		seen[next] = true
		if replies {
			if !safeInboxCursor.MatchString(next) {
				warning = "invalid reply cursor"
				break
			}
			args["fields"] = "id,replies.limit(100).after(" + next + "){" + replyFields + "}"
		} else {
			args[cursorArg] = next
		}
		if conversation {
			tool = "list_conversation_messages"
		}
		if page == 9 {
			warning = "more inbox history queued for the next sync"
		}
	}
	if first == nil {
		return nil, fmt.Errorf("%s returned no page", original)
	}
	if replies {
		first["replies"] = map[string]any{"data": all}
	} else if conversation {
		first["messages"] = map[string]any{"data": all}
	} else {
		key, _ := inboxPageItems(first)
		first[key] = all
	}
	if len(users) > 0 {
		first["includes"] = map[string]any{"users": users}
	}
	if original == "list_media_comments" || original == "get_media_comments" {
		children := []any{}
		for _, value := range all {
			comment, ok := value.(map[string]any)
			if !ok {
				continue
			}
			reply, ok := comment["replies"].(map[string]any)
			if !ok || inboxNextCursor(reply) == "" {
				continue
			}
			fields := "id,text,username,timestamp,like_count"
			if strings.Contains(fmt.Sprint(input["fields"]), "message") {
				fields = "id,message,from{id,name,picture},created_time,like_count"
			}
			childInput := map[string]any{"commentId": firstString(comment, "id"), "_social_reply_fields": fields}
			if token := input["access_token"]; token != nil {
				childInput["access_token"] = token
			}
			child, err := collectInboxPages(ctx, accountID, connID, "get_comment", childInput)
			if err != nil || child == nil || !child.Success {
				warning = "reply history collection incomplete"
				continue
			}
			var childObj map[string]any
			if json.Unmarshal(child.Data, &childObj) != nil {
				warning = "invalid reply history"
				continue
			}
			comment["replies"] = childObj["replies"]
			children = append(children, childObj)
		}
		first["_social_child_pages"] = children
	}
	first["_social_page_key"] = key
	first["_social_next_cursor"] = cursor
	first["_social_page_warning"] = warning
	data, err := json.Marshal(first)
	if err != nil {
		return nil, err
	}
	copy := *last
	copy.Data = data
	return &copy, nil
}
func inboxPageItems(obj map[string]any) (string, []any) {
	for _, key := range []string{"data", "items", "messages", "conversations", "posts", "comments", "results"} {
		if items, ok := obj[key].([]any); ok {
			return key, items
		}
	}
	return "data", nil
}
func inboxNextCursor(obj map[string]any) string {
	if paging, ok := obj["paging"].(map[string]any); ok {
		if next := firstString(paging, "next"); next != "" {
			if u, err := url.Parse(next); err == nil && u.Query().Get("after") != "" {
				return u.Query().Get("after")
			}
			if c, ok := paging["cursors"].(map[string]any); ok {
				return firstString(c, "after")
			}
		}
	}
	if meta, ok := obj["meta"].(map[string]any); ok {
		if next := firstString(meta, "next_token", "nextCursor", "next_cursor"); next != "" {
			return next
		}
	}
	if p, ok := obj["pagination"].(map[string]any); ok {
		if next := firstString(p, "nextCursor", "next_cursor"); next != "" {
			return next
		}
	}
	return firstString(obj, "nextCursor", "next_cursor")
}
func finishInboxPages(ctx *sdk.AppCtx, accountID int64, res *sdk.ExecuteResult) string {
	if res == nil || !res.Success {
		return "inbox page unavailable"
	}
	if err := inboxWriteError(ctx.AppDB(), accountID); err != nil {
		return "inbox persistence incomplete: " + err.Error()
	}
	var obj map[string]any
	if json.Unmarshal(res.Data, &obj) != nil {
		return "invalid inbox page"
	}
	if children, ok := obj["_social_child_pages"].([]any); ok {
		for _, child := range children {
			raw, _ := json.Marshal(child)
			if w := finishInboxPages(ctx, accountID, &sdk.ExecuteResult{Success: true, Data: raw}); w != "" {
				return w
			}
		}
	}
	key := firstString(obj, "_social_page_key")
	if key == "" {
		return ""
	}
	warning := firstString(obj, "_social_page_warning")
	_, err := ctx.AppDB().Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at,last_error) VALUES (?,?,?,CURRENT_TIMESTAMP,?) ON CONFLICT(social_account_id,kind) DO UPDATE SET cursor=excluded.cursor,last_sync_at=excluded.last_sync_at,last_error=excluded.last_error`, accountID, key, firstString(obj, "_social_next_cursor"), nullable(warning))
	if err != nil {
		return "persist inbox cursor: " + err.Error()
	}
	return warning
}

var inboxWrites = struct {
	sync.Mutex
	errors    map[postLockKey]error
	remaining map[postLockKey]int
}{errors: map[postLockKey]error{}, remaining: map[postLockKey]int{}}

func beginInboxSync(ctx *sdk.AppCtx, id int64) func() {
	unlock := lockSocialResource(ctx, id, "inbox")
	key := postLockKey{ctx.AppDB(), id, "inbox"}
	inboxWrites.Lock()
	inboxWrites.errors[key] = nil
	inboxWrites.remaining[key] = 200
	var blocked string
	_ = ctx.AppDB().QueryRow(`SELECT COALESCE(cursor,'') FROM inbox_cursors WHERE social_account_id=? AND kind='poll_backoff'`, id).Scan(&blocked)
	if until, err := time.Parse(time.RFC3339, blocked); err == nil && until.After(time.Now()) {
		inboxWrites.remaining[key] = 0
	}
	inboxWrites.Unlock()
	return func() {
		inboxWrites.Lock()
		delete(inboxWrites.errors, key)
		delete(inboxWrites.remaining, key)
		inboxWrites.Unlock()
		unlock()
	}
}
func recordInboxWriteError(db *sql.DB, id int64, err error) {
	inboxWrites.Lock()
	defer inboxWrites.Unlock()
	key := postLockKey{db, id, "inbox"}
	if _, ok := inboxWrites.errors[key]; ok {
		inboxWrites.errors[key] = err
	}
}
func inboxWriteError(db *sql.DB, id int64) error {
	inboxWrites.Lock()
	defer inboxWrites.Unlock()
	return inboxWrites.errors[postLockKey{db, id, "inbox"}]
}

var safeInboxCursor = regexp.MustCompile(`^[A-Za-z0-9_=-]+$`)

func consumeInboxBudget(ctx *sdk.AppCtx, id int64) bool {
	inboxWrites.Lock()
	defer inboxWrites.Unlock()
	key := postLockKey{ctx.AppDB(), id, "inbox"}
	remaining, ok := inboxWrites.remaining[key]
	if !ok {
		return true
	}
	if remaining <= 0 {
		return false
	}
	inboxWrites.remaining[key] = remaining - 1
	return true
}
func deferInboxPolling(ctx *sdk.AppCtx, id int64) {
	_, _ = ctx.AppDB().Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_error) VALUES (?,'poll_backoff',?,'provider rate limit') ON CONFLICT(social_account_id,kind) DO UPDATE SET cursor=excluded.cursor,last_error=excluded.last_error`, id, time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339))
	inboxWrites.Lock()
	inboxWrites.remaining[postLockKey{ctx.AppDB(), id, "inbox"}] = 0
	inboxWrites.Unlock()
}
