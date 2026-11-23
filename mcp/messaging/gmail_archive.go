package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// maxArchiveThreads bounds the Gmail calls one request can make. A CRM
// conversation that groups many automated emails can span hundreds of
// threads; the most recent ones are archived first and the rest reported.
const maxArchiveThreads = 50

const maxArchiveMessageIDs = 2000

type gmailArchiveThread struct {
	ConnectionID int64  `json:"connection_id"`
	ThreadID     string `json:"thread_id"`
	Error        string `json:"error,omitempty"`
}

type gmailArchiveResult struct {
	Action    string               `json:"action"`
	Archived  []gmailArchiveThread `json:"archived"`
	Failed    []gmailArchiveThread `json:"failed,omitempty"`
	Skipped   int                  `json:"skipped"`
	Remaining int                  `json:"remaining,omitempty"`
}

// Mailbox actions and the Gmail label each one removes from the thread.
// archive is Gmail's "Archive" (INBOX); mark_read is "Mark as read" (UNREAD).
var gmailThreadActions = map[string]string{"archive": "INBOX", "mark_read": "UNREAD"}

// toolMessageThreadArchive updates the provider mailbox threads holding the
// given messages: archive (default) or mark_read. Only Gmail supports it.
// Nothing is deleted, and a new reply returns an archived thread to the
// inbox. Messages from other providers, or from a Gmail connection this
// project no longer owns, are skipped.
func (a *App) toolMessageThreadArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ids, err := archiveMessageIDs(args["message_ids"])
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
	if action == "" {
		action = "archive"
	}
	if _, ok := gmailThreadActions[action]; !ok {
		return nil, fmt.Errorf("invalid action %q (archive|mark_read)", action)
	}
	return archiveGmailThreads(ctx, pid, ids, action)
}

func archiveMessageIDs(raw any) ([]int64, error) {
	list, ok := raw.([]any)
	if !ok {
		if typed, yes := raw.([]int64); yes {
			for _, id := range typed {
				list = append(list, id)
			}
		} else {
			return nil, errors.New("message_ids must be an array of message ids")
		}
	}
	if len(list) == 0 {
		return nil, errors.New("message_ids required")
	}
	if len(list) > maxArchiveMessageIDs {
		return nil, fmt.Errorf("at most %d message_ids per call", maxArchiveMessageIDs)
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(list))
	for _, item := range list {
		id := int64Arg(map[string]any{"id": item}, "id")
		if id <= 0 {
			return nil, errors.New("message_ids must be positive integers")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func archiveGmailThreads(ctx *sdk.AppCtx, pid string, ids []int64, action string) (*gmailArchiveResult, error) {
	out := &gmailArchiveResult{Action: action, Archived: []gmailArchiveThread{}}
	bound := map[int64]bool{}
	for _, b := range ctx.IntegrationsFor("email_provider") {
		if b.AppSlug == "gmail" {
			bound[b.ConnectionID] = true
		}
	}

	type threadKey struct {
		conn   int64
		thread string
	}
	latest := map[threadKey]string{}
	found := 0
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		chunk := ids[start:end]
		ph := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		q := []any{pid}
		for _, id := range chunk {
			q = append(q, id)
		}
		rows, err := ctx.AppDB().Query(`SELECT COALESCE(provider_slug,''),COALESCE(provider_connection_id,0),COALESCE(provider_thread_id,''),COALESCE(created_at,'')
			FROM messages WHERE project_id=? AND id IN (`+ph+`)`, q...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var slug, thread, at string
			var conn int64
			if err := rows.Scan(&slug, &conn, &thread, &at); err != nil {
				rows.Close()
				return nil, err
			}
			found++
			if slug != "gmail" || conn == 0 || strings.TrimSpace(thread) == "" {
				out.Skipped++
				continue
			}
			k := threadKey{conn, thread}
			if cur, ok := latest[k]; !ok || at > cur {
				latest[k] = at
			}
		}
		err = rows.Close()
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			return nil, err
		}
	}
	// Ids outside this project are not disclosed separately from other skips.
	out.Skipped += len(ids) - found

	owned := map[int64]bool{}
	keys := make([]threadKey, 0, len(latest))
	for k := range latest {
		if _, checked := owned[k.conn]; !checked {
			owned[k.conn] = bound[k.conn] && projectOwnsGmailConnection(ctx, pid, k.conn)
		}
		if !owned[k.conn] {
			out.Skipped++
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if latest[keys[i]] != latest[keys[j]] {
			return latest[keys[i]] > latest[keys[j]]
		}
		if keys[i].conn != keys[j].conn {
			return keys[i].conn < keys[j].conn
		}
		return keys[i].thread < keys[j].thread
	})
	if len(keys) > maxArchiveThreads {
		out.Remaining = len(keys) - maxArchiveThreads
		keys = keys[:maxArchiveThreads]
	}
	for _, k := range keys {
		item := gmailArchiveThread{ConnectionID: k.conn, ThreadID: k.thread}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(k.conn, "modify_thread", map[string]any{
			"threadId":       k.thread,
			"removeLabelIds": []string{gmailThreadActions[action]},
		})
		switch {
		case err != nil:
			item.Error = truncate(err.Error(), 300)
		case res == nil || !res.Success:
			item.Error = truncate(truncateResData(res), 300)
		}
		if item.Error != "" {
			out.Failed = append(out.Failed, item)
		} else {
			out.Archived = append(out.Archived, item)
		}
	}
	return out, nil
}

// The same ownership rule the mailbox sync uses: a live Gmail sender row in
// this project for this connection.
func projectOwnsGmailConnection(ctx *sdk.AppCtx, pid string, conn int64) bool {
	var n int
	err := ctx.AppDB().QueryRow(`SELECT count(*) FROM senders WHERE project_id=? AND provider='gmail' AND provider_connection_id=? AND channel='email' AND deleted_at IS NULL`, pid, conn).Scan(&n)
	return err == nil && n > 0
}
