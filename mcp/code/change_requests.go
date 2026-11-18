package main

// Native change requests (Code 0.17): ask to land a source branch on a target
// branch, discuss and review it, and merge it. Merging goes through
// nativeVCS.mergeRefs, so it never needs the target checked out and is the
// only way a protected branch moves.
//
// Approval rule: a reviewer's latest review counts. An approval counts only
// if it was given on the source branch's current head, so new revisions need
// fresh approval; a "request_changes" blocks until that reviewer approves.
// Authors cannot approve their own request.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ChangeRequest struct {
	ID            int64  `json:"id"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Source        string `json:"source_branch"`
	Target        string `json:"target_branch"`
	State         string `json:"state"`
	Author        string `json:"author"`
	Issues        []int  `json:"issues"`
	MergeMode     string `json:"merge_mode,omitempty"`
	MergeRevision string `json:"merge_revision,omitempty"`
	MergedBy      string `json:"merged_by,omitempty"`
	MergedAt      string `json:"merged_at,omitempty"`
	ClosedAt      string `json:"closed_at,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type ChangeRequestNote struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Author    string `json:"author"`
	Body      string `json:"body,omitempty"`
	Verdict   string `json:"verdict,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Revision  string `json:"revision,omitempty"`
	CreatedAt string `json:"created_at"`
}

type changeRequestReviews struct {
	Required         int      `json:"required_approvals"`
	Approvals        []string `json:"approved_by"`
	StaleApprovals   []string `json:"stale_approvals,omitempty"`
	ChangesRequested []string `json:"changes_requested_by,omitempty"`
}

type ChangeRequestDetail struct {
	*ChangeRequest
	Reviews   changeRequestReviews `json:"reviews"`
	Compare   *nativeCompare       `json:"compare,omitempty"`
	Mergeable string               `json:"mergeable,omitempty"` // mergeable, conflicts, up_to_date
	Conflicts []string             `json:"conflicts,omitempty"`
	CanMerge  bool                 `json:"can_merge"`
	Blockers  []string             `json:"blockers,omitempty"`
	Files     []nativeFileDiff     `json:"files,omitempty"`
	Notes     []ChangeRequestNote  `json:"notes"`
}

const crCols = `id, number, title, body, source_branch, target_branch, state, author, issues,
	merge_mode, merge_revision, merged_by, COALESCE(merged_at, ''), COALESCE(closed_at, ''),
	COALESCE(created_at, ''), COALESCE(updated_at, '')`

func scanChangeRequest(s rowScanner) (*ChangeRequest, error) {
	var cr ChangeRequest
	var issues string
	if err := s.Scan(&cr.ID, &cr.Number, &cr.Title, &cr.Body, &cr.Source, &cr.Target, &cr.State, &cr.Author, &issues,
		&cr.MergeMode, &cr.MergeRevision, &cr.MergedBy, &cr.MergedAt, &cr.ClosedAt, &cr.CreatedAt, &cr.UpdatedAt); err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(issues), &cr.Issues) != nil || cr.Issues == nil {
		cr.Issues = []int{}
	}
	return &cr, nil
}

type changeRequestService struct {
	db *sql.DB
	n  *nativeVCS
}

func (s *changeRequestService) get(repo *Repo, number int) (*ChangeRequest, error) {
	if number <= 0 {
		return nil, errors.New("number is required")
	}
	cr, err := scanChangeRequest(s.db.QueryRow(`SELECT `+crCols+` FROM repo_change_requests WHERE project_id = ? AND repo_id = ? AND number = ?`, repo.ProjectID, repo.ID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("change request #%d not found", number)
	}
	return cr, err
}

func (s *changeRequestService) note(crID int64, kind, author, body, verdict, path string, line int, revision string) error {
	_, err := s.db.Exec(`INSERT INTO repo_change_request_notes (change_request_id, kind, author, body, verdict, path, line, revision) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		crID, kind, author, body, verdict, path, line, revision)
	if err == nil {
		_, err = s.db.Exec(`UPDATE repo_change_requests SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, crID)
	}
	return err
}

type changeRequestCreate struct {
	Title  string
	Body   string
	Source string
	Target string
	Issues []int
	Author string
}

func (s *changeRequestService) create(repo *Repo, in changeRequestCreate) (*ChangeRequest, error) {
	in.Title, in.Source, in.Target = strings.TrimSpace(in.Title), strings.TrimSpace(in.Source), strings.TrimSpace(in.Target)
	if in.Title == "" {
		return nil, errors.New("create needs title")
	}
	if in.Source == "" {
		return nil, errors.New("create needs source: the branch with the changes")
	}
	branches, err := s.n.branches(repo)
	if err != nil {
		return nil, err
	}
	if in.Target == "" {
		in.Target = "main"
	}
	if branches[in.Source] == "" {
		return nil, fmt.Errorf("source branch %s not found", in.Source)
	}
	if branches[in.Target] == "" {
		return nil, fmt.Errorf("target branch %s not found", in.Target)
	}
	if in.Source == in.Target {
		return nil, errors.New("source and target must be different branches")
	}
	var existing int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(number), 0) FROM repo_change_requests WHERE project_id = ? AND repo_id = ? AND source_branch = ? AND target_branch = ? AND state = 'open'`,
		repo.ProjectID, repo.ID, in.Source, in.Target).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("change request #%d is already open for %s into %s", existing, in.Source, in.Target)
	}
	issues := in.Issues
	if issues == nil {
		issues = []int{}
	}
	issuesJSON, _ := json.Marshal(issues)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(number), 0) + 1 FROM repo_change_requests WHERE project_id = ? AND repo_id = ?`, repo.ProjectID, repo.ID).Scan(&next); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO repo_change_requests (project_id, repo_id, number, title, body, source_branch, target_branch, author, issues) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repo.ProjectID, repo.ID, next, in.Title, strings.TrimSpace(in.Body), in.Source, in.Target, strings.TrimSpace(in.Author), string(issuesJSON))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO repo_change_request_notes (change_request_id, kind, author, body, revision) VALUES (?, 'event', ?, 'opened', ?)`, id, strings.TrimSpace(in.Author), branches[in.Source]); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.get(repo, next)
}

func (s *changeRequestService) list(repo *Repo, state string, limit int) ([]*ChangeRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + crCols + ` FROM repo_change_requests WHERE project_id = ? AND repo_id = ?`
	args := []any{repo.ProjectID, repo.ID}
	switch state = strings.TrimSpace(state); state {
	case "", "open":
		query += ` AND state = 'open'`
	case "merged", "closed":
		query += ` AND state = ?`
		args = append(args, state)
	case "all":
	default:
		return nil, errors.New("state must be open, merged, closed or all")
	}
	query += ` ORDER BY number DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ChangeRequest{}
	for rows.Next() {
		cr, err := scanChangeRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

func (s *changeRequestService) notes(crID int64) ([]ChangeRequestNote, error) {
	rows, err := s.db.Query(`SELECT id, kind, author, body, verdict, path, line, revision, COALESCE(created_at, '') FROM repo_change_request_notes WHERE change_request_id = ? ORDER BY id`, crID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRequestNote{}
	for rows.Next() {
		var n ChangeRequestNote
		if err := rows.Scan(&n.ID, &n.Kind, &n.Author, &n.Body, &n.Verdict, &n.Path, &n.Line, &n.Revision, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func summariseReviews(cr *ChangeRequest, notes []ChangeRequestNote, head string, required int) changeRequestReviews {
	latest := map[string]ChangeRequestNote{}
	order := []string{}
	for _, n := range notes {
		if n.Kind != "review" || n.Verdict == "comment" || n.Author == cr.Author {
			continue
		}
		if _, ok := latest[n.Author]; !ok {
			order = append(order, n.Author)
		}
		latest[n.Author] = n
	}
	sort.Strings(order)
	out := changeRequestReviews{Required: required, Approvals: []string{}}
	for _, who := range order {
		n := latest[who]
		switch {
		case n.Verdict == "request_changes":
			out.ChangesRequested = append(out.ChangesRequested, who)
		case n.Verdict == "approve" && n.Revision == head:
			out.Approvals = append(out.Approvals, who)
		case n.Verdict == "approve":
			out.StaleApprovals = append(out.StaleApprovals, who)
		}
	}
	return out
}

func (s *changeRequestService) detail(repo *Repo, number int, withPatches bool) (*ChangeRequestDetail, error) {
	cr, err := s.get(repo, number)
	if err != nil {
		return nil, err
	}
	notes, err := s.notes(cr.ID)
	if err != nil {
		return nil, err
	}
	d := &ChangeRequestDetail{ChangeRequest: cr, Notes: notes}
	required := 0
	if p, err := s.n.protection(repo, cr.Target); err != nil {
		return nil, err
	} else if p != nil {
		required = p.RequiredApprovals
	}
	branches, err := s.n.branches(repo)
	if err != nil {
		return nil, err
	}
	head := branches[cr.Source]
	d.Reviews = summariseReviews(cr, notes, head, required)
	if cr.State != "open" {
		return d, nil
	}
	if head == "" || branches[cr.Target] == "" {
		d.Blockers = append(d.Blockers, "source or target branch no longer exists")
		return d, nil
	}
	if d.Compare, err = s.n.compareRefs(repo, cr.Source, cr.Target); err != nil {
		return nil, err
	}
	probe, err := s.n.mergeRefs(repo, cr.Target, cr.Source, "merge", "", "", true, "")
	if err != nil {
		return nil, err
	}
	d.Mergeable, d.Conflicts = probe.Status, probe.Conflicts
	if withPatches && d.Compare.MergeBase != "" {
		if _, _, d.Files, err = s.n.lineDiff(repo, d.Compare.MergeBase, cr.Source, nil); err != nil {
			return nil, err
		}
	}
	d.Blockers = mergeBlockers(d)
	d.CanMerge = len(d.Blockers) == 0
	return d, nil
}

func mergeBlockers(d *ChangeRequestDetail) []string {
	out := []string{}
	switch d.Mergeable {
	case "conflicts":
		out = append(out, fmt.Sprintf("conflicts with %s in %s: merge %s into %s with repos_merge, resolve, continue, then retry", d.Target, strings.Join(d.Conflicts, ", "), d.Target, d.Source))
	case "up_to_date":
		out = append(out, fmt.Sprintf("%s has no changes that %s lacks", d.Source, d.Target))
	}
	if len(d.Reviews.ChangesRequested) > 0 {
		out = append(out, "changes requested by "+strings.Join(d.Reviews.ChangesRequested, ", "))
	}
	if have := len(d.Reviews.Approvals); have < d.Reviews.Required {
		out = append(out, fmt.Sprintf("needs %d approval(s) on the current head, has %d", d.Reviews.Required, have))
	}
	return out
}

func (s *changeRequestService) requireOpen(repo *Repo, number int) (*ChangeRequest, error) {
	cr, err := s.get(repo, number)
	if err != nil {
		return nil, err
	}
	if cr.State != "open" {
		return nil, fmt.Errorf("change request #%d is %s", number, cr.State)
	}
	return cr, nil
}

func (s *changeRequestService) comment(repo *Repo, number int, author, body, path string, line int) (*ChangeRequestDetail, error) {
	cr, err := s.get(repo, number)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("comment needs body")
	}
	if path != "" {
		if path, err = normalisePath(path); err != nil {
			return nil, err
		}
	}
	branches, _ := s.n.branches(repo)
	if err := s.note(cr.ID, "comment", strings.TrimSpace(author), strings.TrimSpace(body), "", path, line, branches[cr.Source]); err != nil {
		return nil, err
	}
	return s.detail(repo, number, false)
}

func (s *changeRequestService) review(repo *Repo, number int, author, verdict, body string) (*ChangeRequestDetail, error) {
	cr, err := s.requireOpen(repo, number)
	if err != nil {
		return nil, err
	}
	switch verdict = strings.TrimSpace(verdict); verdict {
	case "approve", "request_changes", "comment":
	default:
		return nil, errors.New("review needs verdict: approve, request_changes or comment")
	}
	author = strings.TrimSpace(author)
	if verdict == "approve" && author == cr.Author {
		return nil, errors.New("authors cannot approve their own change request")
	}
	branches, err := s.n.branches(repo)
	if err != nil {
		return nil, err
	}
	if err := s.note(cr.ID, "review", author, strings.TrimSpace(body), verdict, "", 0, branches[cr.Source]); err != nil {
		return nil, err
	}
	return s.detail(repo, number, false)
}

// merge lands an open change request. The approvals are evaluated on the
// source head that is then merged, so a push between check and merge fails.
func (s *changeRequestService) merge(repo *Repo, number int, actor, mode, message string, deleteSource bool) (*ChangeRequestDetail, *nativeRefMerge, error) {
	if _, err := s.requireOpen(repo, number); err != nil {
		return nil, nil, err
	}
	d, err := s.detail(repo, number, false)
	if err != nil {
		return nil, nil, err
	}
	if !d.CanMerge {
		return d, nil, fmt.Errorf("change request #%d cannot be merged: %s", number, strings.Join(d.Blockers, "; "))
	}
	if mode == "" {
		mode = "merge"
	}
	if strings.TrimSpace(message) == "" {
		message = fmt.Sprintf("Merge change request #%d: %s\n\n%s into %s", d.Number, d.Title, d.Source, d.Target)
	}
	res, err := s.n.mergeRefs(repo, d.Target, d.Source, mode, message, actor, false, d.Compare.FromRevision)
	if err != nil {
		return d, nil, err
	}
	if res.Status != "merged" {
		return d, res, fmt.Errorf("change request #%d did not merge: %s %s", number, res.Status, strings.Join(res.Conflicts, ", "))
	}
	if _, err := s.db.Exec(`UPDATE repo_change_requests SET state = 'merged', merge_mode = ?, merge_revision = ?, merged_by = ?, merged_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		mode, res.Revision.ID, strings.TrimSpace(actor), d.ID); err != nil {
		return d, res, err
	}
	_ = s.note(d.ID, "event", strings.TrimSpace(actor), "merged", "", "", 0, res.Revision.ID)
	if deleteSource {
		if _, err := s.n.deleteBranch(repo, d.Source, true, actor); err != nil {
			_ = s.note(d.ID, "event", "system", "source branch kept: "+err.Error(), "", "", 0, "")
		}
	}
	out, err := s.detail(repo, number, false)
	return out, res, err
}

func (s *changeRequestService) setState(repo *Repo, number int, actor, state string) (*ChangeRequestDetail, error) {
	cr, err := s.get(repo, number)
	if err != nil {
		return nil, err
	}
	switch {
	case state == "closed" && cr.State == "open":
		_, err = s.db.Exec(`UPDATE repo_change_requests SET state = 'closed', closed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, cr.ID)
	case state == "open" && cr.State == "closed":
		_, err = s.db.Exec(`UPDATE repo_change_requests SET state = 'open', closed_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, cr.ID)
	default:
		return nil, fmt.Errorf("change request #%d is %s", number, cr.State)
	}
	if err != nil {
		return nil, err
	}
	verb := map[string]string{"closed": "closed", "open": "reopened"}[state]
	if err := s.note(cr.ID, "event", strings.TrimSpace(actor), verb, "", "", 0, ""); err != nil {
		return nil, err
	}
	return s.detail(repo, number, false)
}
