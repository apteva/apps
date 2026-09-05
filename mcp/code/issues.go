package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	issueStateOpen        = "open"
	issueStateClosed      = "closed"
	issueReasonCompleted  = "completed"
	issueReasonNotPlanned = "not_planned"
	issueStatusTodo       = "todo"
	issueStatusTriage     = "triage"
	issueStatusPlanned    = "planned"
	issueStatusInProgress = "in_progress"
	issueStatusInReview   = "in_review"
	issueStatusBlocked    = "blocked"
	issueStatusDone       = "done"
)

type Issue struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id"`
	RepoID        int64  `json:"repo_id"`
	RepoSlug      string `json:"repo_slug,omitempty"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	State         string `json:"state"`
	StateReason   string `json:"state_reason,omitempty"`
	Priority      string `json:"priority"`
	Assignee      string `json:"assignee,omitempty"`
	ClaimOwner    string `json:"claim_owner,omitempty"`
	ClaimLabel    string `json:"claim_label,omitempty"`
	ClaimedAt     string `json:"claimed_at,omitempty"`
	CreatedBy     string `json:"created_by,omitempty"`
	ClosedAt      string `json:"closed_at,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	CommentsCount int    `json:"comments_count,omitempty"`
	LinksCount    int    `json:"links_count,omitempty"`
}

type IssueComment struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Author    string `json:"author,omitempty"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type IssueLink struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Title     string `json:"title,omitempty"`
	DataJSON  string `json:"data_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

type IssueEvent struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	EventType string `json:"event_type"`
	Actor     string `json:"actor,omitempty"`
	DataJSON  string `json:"data_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

type IssueDetail struct {
	HistoryOffset int             `json:"history_offset"`
	HistoryLimit  int             `json:"history_limit"`
	CommentsTotal int             `json:"comments_total"`
	EventsTotal   int             `json:"events_total"`
	LinksTotal    int             `json:"links_total"`
	Issue         *Issue          `json:"issue"`
	Comments      []*IssueComment `json:"comments"`
	Links         []*IssueLink    `json:"links"`
	Events        []*IssueEvent   `json:"events"`
}

type IssueListOptions struct {
	State    string
	Status   string
	Type     string
	Priority string
	Assignee string
	RepoSlug string
	Q        string
	Limit    int
	Offset   int
	Total    *int
}

type IssueCreateInput struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Type      string `json:"type"`
	Priority  string `json:"priority"`
	Assignee  string `json:"assignee"`
	CreatedBy string `json:"created_by"`
}

type IssuePatch struct {
	Title       *string `json:"title,omitempty"`
	Body        *string `json:"body,omitempty"`
	Type        *string `json:"type,omitempty"`
	Status      *string `json:"status,omitempty"`
	State       *string `json:"state,omitempty"`
	StateReason *string `json:"state_reason,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	Assignee    *string `json:"assignee,omitempty"`
	Actor       string  `json:"actor,omitempty"`
}

func validIssueType(s string) bool {
	switch s {
	case "bug", "feature", "task", "chore":
		return true
	}
	return false
}

func validIssueStatus(s string) bool {
	switch s {
	case issueStatusTodo, issueStatusTriage, issueStatusPlanned, issueStatusInProgress, issueStatusInReview, issueStatusBlocked, issueStatusDone:
		return true
	}
	return false
}

func validIssueState(s string) bool {
	switch s {
	case issueStateOpen, issueStateClosed:
		return true
	}
	return false
}

func validIssueStateReason(s string) bool {
	switch s {
	case "", issueReasonCompleted, issueReasonNotPlanned:
		return true
	}
	return false
}

func validIssuePriority(s string) bool {
	switch s {
	case "low", "medium", "high", "urgent":
		return true
	}
	return false
}

func normaliseIssueCreate(in IssueCreateInput) (IssueCreateInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return in, errors.New("title required")
	}
	if in.Type == "" {
		in.Type = "task"
	}
	if !validIssueType(in.Type) {
		return in, fmt.Errorf("invalid issue type %q", in.Type)
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	if !validIssuePriority(in.Priority) {
		return in, fmt.Errorf("invalid issue priority %q", in.Priority)
	}
	return in, nil
}

const issueCols = `i.id, i.project_id, i.repo_id, r.slug, i.number, i.title, i.body,
	i.type, i.status, COALESCE(i.state, 'open'), COALESCE(i.state_reason, ''), i.priority, i.assignee,
	COALESCE(i.claim_owner, ''), COALESCE(i.claim_label, ''), COALESCE(i.claimed_at, ''), i.created_by,
	COALESCE(i.closed_at, ''), i.created_at, i.updated_at,
	(SELECT COUNT(*) FROM repo_issue_comments c WHERE c.issue_id = i.id),
	(SELECT COUNT(*) FROM repo_issue_links l WHERE l.issue_id = i.id)`

func scanIssueRow(s rowScanner) (*Issue, error) {
	var iss Issue
	if err := s.Scan(
		&iss.ID, &iss.ProjectID, &iss.RepoID, &iss.RepoSlug, &iss.Number,
		&iss.Title, &iss.Body, &iss.Type, &iss.Status, &iss.State, &iss.StateReason, &iss.Priority,
		&iss.Assignee, &iss.ClaimOwner, &iss.ClaimLabel, &iss.ClaimedAt, &iss.CreatedBy,
		&iss.ClosedAt, &iss.CreatedAt, &iss.UpdatedAt,
		&iss.CommentsCount, &iss.LinksCount,
	); err != nil {
		return nil, err
	}
	return &iss, nil
}

func dbCreateIssue(db *sql.DB, projectID string, repo *Repo, in IssueCreateInput) (*Issue, error) {
	in, err := normaliseIssueCreate(in)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var next int
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(number), 0) + 1 FROM repo_issues
		 WHERE project_id = ? AND repo_id = ?
	`, projectID, repo.ID).Scan(&next); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`
		INSERT INTO repo_issues (
			project_id, repo_id, number, title, body, type, status, priority, assignee, created_by
		) VALUES (?, ?, ?, ?, ?, ?, 'todo', ?, ?, ?)
	`, projectID, repo.ID, next, in.Title, in.Body, in.Type, in.Priority, in.Assignee, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := insertIssueEventTx(tx, id, "created", in.CreatedBy, map[string]any{"title": in.Title}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetIssueByID(db, id)
}

func dbListIssues(db *sql.DB, projectID string, repoID int64, opt IssueListOptions) ([]*Issue, error) {
	query := `SELECT ` + issueCols + ` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id
		WHERE i.project_id = ? AND i.repo_id = ?`
	args := []any{projectID, repoID}
	return dbQueryIssues(db, query, args, opt)
}

func dbSearchIssues(db *sql.DB, projectID string, opt IssueListOptions) ([]*Issue, error) {
	query := `SELECT ` + issueCols + ` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id
		WHERE i.project_id = ?`
	args := []any{projectID}
	if opt.RepoSlug != "" {
		query += ` AND r.slug = ?`
		args = append(args, opt.RepoSlug)
	}
	return dbQueryIssues(db, query, args, opt)
}

func dbQueryIssues(db *sql.DB, query string, args []any, opt IssueListOptions) ([]*Issue, error) {
	if opt.Status == "active" {
		opt.State = issueStateOpen
		opt.Status = ""
	}
	if opt.Status == issueStateOpen || opt.Status == issueStateClosed {
		opt.State = opt.Status
		opt.Status = ""
	}
	if opt.State == "" {
		opt.State = issueStateOpen
	}
	if opt.State != "all" {
		query += ` AND COALESCE(i.state, 'open') = ?`
		args = append(args, opt.State)
	}
	if opt.Status != "" && opt.Status != "all" {
		query += ` AND i.status = ?`
		args = append(args, opt.Status)
	}
	if opt.Type != "" {
		query += ` AND i.type = ?`
		args = append(args, opt.Type)
	}
	if opt.Priority != "" {
		query += ` AND i.priority = ?`
		args = append(args, opt.Priority)
	}
	if opt.Assignee != "" {
		query += ` AND i.assignee = ?`
		args = append(args, opt.Assignee)
	}
	if opt.Q != "" {
		query += ` AND (i.title LIKE ? OR i.body LIKE ?)`
		like := "%" + opt.Q + "%"
		args = append(args, like, like)
	}
	if opt.Total != nil {
		from := strings.Index(query, " FROM repo_issues i JOIN repositories")
		if from < 0 {
			return nil, errors.New("invalid issue query")
		}
		if err := db.QueryRow("SELECT COUNT(*)"+query[from:], args...).Scan(opt.Total); err != nil {
			return nil, err
		}
	}
	query += ` ORDER BY CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, i.updated_at DESC, i.number DESC, i.id DESC`
	if opt.Limit <= 0 || opt.Limit > 200 {
		opt.Limit = 100
	}
	if opt.Offset < 0 {
		opt.Offset = 0
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, opt.Limit, opt.Offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Issue
	for rows.Next() {
		iss, err := scanIssueRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, iss)
	}
	return out, rows.Err()
}

func dbGetIssueByID(db *sql.DB, id int64) (*Issue, error) {
	row := db.QueryRow(`SELECT `+issueCols+` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id WHERE i.id = ?`, id)
	iss, err := scanIssueRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return iss, err
}

func dbGetIssueByNumber(db *sql.DB, projectID string, repoID int64, number int) (*Issue, error) {
	row := db.QueryRow(`SELECT `+issueCols+` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id
		WHERE i.project_id = ? AND i.repo_id = ? AND i.number = ?`, projectID, repoID, number)
	iss, err := scanIssueRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return iss, err
}

func dbGetIssueDetail(db *sql.DB, projectID string, repoID int64, number int) (*IssueDetail, error) {
	return dbGetIssueDetailPage(db, projectID, repoID, number, 0, 200)
}
func dbGetIssueDetailPage(db *sql.DB, projectID string, repoID int64, number, offset, limit int) (*IssueDetail, error) {
	offset = max(0, offset)
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	iss, err := dbGetIssueByNumber(db, projectID, repoID, number)
	if err != nil || iss == nil {
		return nil, err
	}
	comments, err := dbListIssueCommentsPage(db, iss.ID, offset, limit)
	if err != nil {
		return nil, err
	}
	links, err := dbListIssueLinksPage(db, iss.ID, offset, limit)
	if err != nil {
		return nil, err
	}
	events, err := dbListIssueEventsPage(db, iss.ID, offset, limit)
	if err != nil {
		return nil, err
	}
	detail := &IssueDetail{Issue: iss, Comments: comments, Links: links, Events: events, HistoryOffset: offset, HistoryLimit: limit}
	err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM repo_issue_comments WHERE issue_id=?),(SELECT COUNT(*) FROM repo_issue_events WHERE issue_id=?),(SELECT COUNT(*) FROM repo_issue_links WHERE issue_id=?)`, iss.ID, iss.ID, iss.ID).Scan(&detail.CommentsTotal, &detail.EventsTotal, &detail.LinksTotal)
	return detail, err
}

func dbUpdateIssue(db *sql.DB, iss *Issue, patch IssuePatch) (*Issue, error) {
	sets := []string{}
	args := []any{}
	eventData := map[string]any{}
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
		eventData[col] = v
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			return nil, errors.New("title required")
		}
		add("title", title)
	}
	if patch.Body != nil {
		add("body", *patch.Body)
	}
	if patch.Type != nil {
		if !validIssueType(*patch.Type) {
			return nil, fmt.Errorf("invalid issue type %q", *patch.Type)
		}
		add("type", *patch.Type)
	}
	if patch.Priority != nil {
		if !validIssuePriority(*patch.Priority) {
			return nil, fmt.Errorf("invalid issue priority %q", *patch.Priority)
		}
		add("priority", *patch.Priority)
	}
	if patch.Assignee != nil {
		add("assignee", *patch.Assignee)
	}
	if patch.Status != nil {
		if !validIssueStatus(*patch.Status) {
			return nil, fmt.Errorf("invalid issue status %q", *patch.Status)
		}
		add("status", *patch.Status)
		if *patch.Status == issueStatusDone && patch.State == nil {
			state := issueStateClosed
			patch.State = &state
		}
		if *patch.Status != issueStatusDone && patch.State == nil && iss.State == issueStateClosed {
			state := issueStateOpen
			patch.State = &state
		}
	}
	if patch.State != nil {
		if !validIssueState(*patch.State) {
			return nil, fmt.Errorf("invalid issue state %q", *patch.State)
		}
		add("state", *patch.State)
		if *patch.State == issueStateClosed {
			sets = append(sets, "closed_at = COALESCE(closed_at, CURRENT_TIMESTAMP)")
			if patch.StateReason == nil && iss.StateReason == "" {
				reason := issueReasonCompleted
				patch.StateReason = &reason
			}
			if patch.Status == nil && iss.Status != issueStatusDone {
				status := issueStatusDone
				patch.Status = &status
				add("status", status)
			}
		} else {
			sets = append(sets, "closed_at = NULL")
			if patch.StateReason == nil {
				reason := ""
				patch.StateReason = &reason
			}
			if patch.Status == nil && iss.Status == issueStatusDone {
				status := issueStatusTodo
				patch.Status = &status
				add("status", status)
			}
		}
	}
	if patch.StateReason != nil {
		if !validIssueStateReason(*patch.StateReason) {
			return nil, fmt.Errorf("invalid issue state_reason %q", *patch.StateReason)
		}
		add("state_reason", *patch.StateReason)
	}
	willClose := (patch.State != nil && *patch.State == issueStateClosed) ||
		(patch.Status != nil && *patch.Status == issueStatusDone)
	if willClose {
		sets = append(sets, "claim_owner = ''", "claim_label = ''", "claimed_at = NULL")
		if iss.ClaimOwner != "" {
			eventData["claim_released"] = iss.ClaimOwner
		}
	}
	if len(sets) == 0 {
		return iss, nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, iss.ID)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE repo_issues SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return nil, err
	}
	if err := insertIssueEventTx(tx, iss.ID, "updated", patch.Actor, eventData); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetIssueByID(db, iss.ID)
}

type IssueClaimOutcome struct {
	Issue    *Issue `json:"issue"`
	Success  bool   `json:"success"`
	Changed  bool   `json:"changed"`
	Conflict bool   `json:"conflict,omitempty"`
}

func normaliseClaimant(owner, label string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	label = strings.TrimSpace(label)
	if owner == "" {
		return "", "", errors.New("claim owner required")
	}
	if len(owner) > 255 {
		return "", "", errors.New("claim owner must be at most 255 characters")
	}
	if label == "" {
		label = owner
	}
	if len(label) > 255 {
		return "", "", errors.New("claim label must be at most 255 characters")
	}
	return owner, label, nil
}

func dbClaimIssue(db *sql.DB, issueID int64, owner, label string) (*IssueClaimOutcome, error) {
	owner, label, err := normaliseClaimant(owner, label)
	if err != nil {
		return nil, err
	}
	return retryIssueClaimDB(func() (*IssueClaimOutcome, error) {
		return dbClaimIssueOnce(db, issueID, owner, label)
	})
}

// dbClaimIssueOnce is a compare-and-set. The conditional UPDATE is the
// collision boundary: concurrent callers cannot both change an empty claim.
func dbClaimIssueOnce(db *sql.DB, issueID int64, owner, label string) (*IssueClaimOutcome, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE repo_issues
		   SET claim_owner = ?, claim_label = ?, claimed_at = CURRENT_TIMESTAMP,
		       status = 'in_progress', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND COALESCE(state, 'open') = 'open' AND claim_owner = ''
	`, owner, label, issueID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		if err := insertIssueEventTx(tx, issueID, "claimed", label, map[string]any{"claim_owner": owner, "claim_label": label}); err != nil {
			return nil, err
		}
		issue, err := scanIssueRow(tx.QueryRow(`SELECT `+issueCols+` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id WHERE i.id = ?`, issueID))
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &IssueClaimOutcome{Issue: issue, Success: true, Changed: true}, nil
	}
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	issue, err := dbGetIssueByID(db, issueID)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, errors.New("issue not found")
	}
	if issue.State != issueStateOpen {
		return nil, errors.New("closed issues cannot be claimed")
	}
	if issue.ClaimOwner == owner {
		return &IssueClaimOutcome{Issue: issue, Success: true, Changed: false}, nil
	}
	return &IssueClaimOutcome{Issue: issue, Success: false, Changed: false, Conflict: true}, nil
}

func dbReleaseIssueClaim(db *sql.DB, issueID int64, owner, label string) (*IssueClaimOutcome, error) {
	owner, label, err := normaliseClaimant(owner, label)
	if err != nil {
		return nil, err
	}
	return retryIssueClaimDB(func() (*IssueClaimOutcome, error) {
		return dbReleaseIssueClaimOnce(db, issueID, owner, label)
	})
}

func dbReleaseIssueClaimOnce(db *sql.DB, issueID int64, owner, label string) (*IssueClaimOutcome, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE repo_issues
		   SET claim_owner = '', claim_label = '', claimed_at = NULL,
		       status = CASE WHEN status = 'in_progress' THEN 'todo' ELSE status END,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND claim_owner = ?
	`, issueID, owner)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		if err := insertIssueEventTx(tx, issueID, "claim_released", label, map[string]any{"claim_owner": owner, "claim_label": label}); err != nil {
			return nil, err
		}
		issue, err := scanIssueRow(tx.QueryRow(`SELECT `+issueCols+` FROM repo_issues i JOIN repositories r ON r.id = i.repo_id WHERE i.id = ?`, issueID))
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &IssueClaimOutcome{Issue: issue, Success: true, Changed: true}, nil
	}
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	issue, err := dbGetIssueByID(db, issueID)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, errors.New("issue not found")
	}
	if issue.ClaimOwner == "" {
		return &IssueClaimOutcome{Issue: issue, Success: true, Changed: false}, nil
	}
	return &IssueClaimOutcome{Issue: issue, Success: false, Changed: false, Conflict: true}, nil
}

func retryIssueClaimDB(op func() (*IssueClaimOutcome, error)) (*IssueClaimOutcome, error) {
	const attempts = 20
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		outcome, err := op()
		if err == nil || !isSQLiteBusy(err) {
			return outcome, err
		}
		lastErr = err
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return nil, lastErr
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}

func dbAddIssueComment(db *sql.DB, issueID int64, author, body string) (*IssueComment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("comment body required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO repo_issue_comments (issue_id, author, body) VALUES (?, ?, ?)`, issueID, author, body)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE repo_issues SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, issueID); err != nil {
		return nil, err
	}
	if err := insertIssueEventTx(tx, issueID, "commented", author, map[string]any{"body": body}); err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, issue_id, author, body, created_at FROM repo_issue_comments WHERE id = ?`, id)
	var c IssueComment
	if err := row.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func dbListIssueComments(db *sql.DB, issueID int64) ([]*IssueComment, error) {
	return dbListIssueCommentsPage(db, issueID, 0, 200)
}
func dbListIssueCommentsPage(db *sql.DB, issueID int64, offset, limit int) ([]*IssueComment, error) {
	rows, err := db.Query(`SELECT id, issue_id, author, body, created_at FROM repo_issue_comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`, issueID, limit, max(0, offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IssueComment
	for rows.Next() {
		var c IssueComment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func dbAddIssueLink(db *sql.DB, issueID int64, kind, target, title string, data map[string]any, actor string) (*IssueLink, error) {
	kind = strings.TrimSpace(kind)
	target = strings.TrimSpace(target)
	if kind == "" || target == "" {
		return nil, errors.New("link kind and target required")
	}
	dataJSON, _ := json.Marshal(data)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		INSERT INTO repo_issue_links (issue_id, kind, target, title, data_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(issue_id, kind, target) DO UPDATE SET title=excluded.title, data_json=excluded.data_json
	`, issueID, kind, target, title, string(dataJSON))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE repo_issues SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, issueID); err != nil {
		return nil, err
	}
	if err := insertIssueEventTx(tx, issueID, "linked", actor, map[string]any{"kind": kind, "target": target}); err != nil {
		return nil, err
	}
	_ = res
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM repo_issue_links WHERE issue_id=? AND kind=? AND target=?`, issueID, kind, target).Scan(&id); err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, issue_id, kind, target, title, data_json, created_at FROM repo_issue_links WHERE id = ?`, id)
	var l IssueLink
	if err := row.Scan(&l.ID, &l.IssueID, &l.Kind, &l.Target, &l.Title, &l.DataJSON, &l.CreatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

func dbListIssueLinks(db *sql.DB, issueID int64) ([]*IssueLink, error) {
	return dbListIssueLinksPage(db, issueID, 0, 200)
}
func dbListIssueLinksPage(db *sql.DB, issueID int64, offset, limit int) ([]*IssueLink, error) {
	rows, err := db.Query(`SELECT id, issue_id, kind, target, title, data_json, created_at FROM repo_issue_links WHERE issue_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`, issueID, limit, max(0, offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IssueLink
	for rows.Next() {
		var l IssueLink
		if err := rows.Scan(&l.ID, &l.IssueID, &l.Kind, &l.Target, &l.Title, &l.DataJSON, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func insertIssueEventTx(tx *sql.Tx, issueID int64, typ, actor string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	body, _ := json.Marshal(data)
	_, err := tx.Exec(`INSERT INTO repo_issue_events (issue_id, event_type, actor, data_json) VALUES (?, ?, ?, ?)`,
		issueID, typ, actor, string(body))
	return err
}

func dbListIssueEvents(db *sql.DB, issueID int64) ([]*IssueEvent, error) {
	return dbListIssueEventsPage(db, issueID, 0, 200)
}
func dbListIssueEventsPage(db *sql.DB, issueID int64, offset, limit int) ([]*IssueEvent, error) {
	rows, err := db.Query(`SELECT id, issue_id, event_type, actor, data_json, created_at FROM repo_issue_events WHERE issue_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`, issueID, limit, max(0, offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IssueEvent
	for rows.Next() {
		var ev IssueEvent
		if err := rows.Scan(&ev.ID, &ev.IssueID, &ev.EventType, &ev.Actor, &ev.DataJSON, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func emitIssueEvent(ctx *sdk.AppCtx, topic string, repo *Repo, issue *Issue) {
	if ctx == nil || repo == nil || issue == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"slug": repo.Slug, "repo_id": repo.ID, "issue_id": issue.ID, "number": issue.Number,
		"title": issue.Title, "state": issue.State, "state_reason": issue.StateReason,
		"status": issue.Status, "type": issue.Type, "priority": issue.Priority,
		"claim_owner": issue.ClaimOwner, "claim_label": issue.ClaimLabel, "claimed_at": issue.ClaimedAt,
	})
}
