package main

// Human review for Git pull request merges (Code 0.21). A person approves a
// pull request at its current head commit, from the Code panel or with
// repos_git_pull_request action=approve. Agents cannot approve. An agent can
// merge only a pull request a person approved at its current head, and the
// merge is pinned to that head, so commits pushed after the approval make
// GitHub refuse it. A person merging is their own review. Every approve,
// create, merge and close is written to the repository's Git operation log.
// The gate is on unless the install sets git_merge_approval to "off".

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func mergeApprovalRequired() bool {
	if globalCtx != nil && globalCtx.Config() != nil {
		return !strings.EqualFold(strings.TrimSpace(globalCtx.Config().Get("git_merge_approval")), "off")
	}
	return true
}

// isPerson reports whether a verified actor is a person rather than an
// agent or an unidentified caller.
func isPerson(actor string) bool { return strings.HasPrefix(actor, "user:") }

func shortSHA(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

func dbApprovePull(db *sql.DB, repoID int64, number int, headSHA, actor string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO repo_git_pr_approvals(repo_id, number, head_sha, approved_by) VALUES (?, ?, ?, ?)`,
		repoID, number, headSHA, actor)
	return err
}

// dbPullApprovals returns number -> head SHA -> approvers.
func dbPullApprovals(db *sql.DB, repoID int64, numbers []int) (map[int]map[string][]string, error) {
	out := map[int]map[string][]string{}
	if len(numbers) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(numbers)), ",")
	args := []any{repoID}
	for _, n := range numbers {
		args = append(args, n)
	}
	rows, err := db.Query(`SELECT number, head_sha, approved_by FROM repo_git_pr_approvals WHERE repo_id = ? AND number IN (`+marks+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var sha, by string
		if err := rows.Scan(&n, &sha, &by); err != nil {
			return nil, err
		}
		if out[n] == nil {
			out[n] = map[string][]string{}
		}
		out[n][sha] = append(out[n][sha], by)
	}
	return out, rows.Err()
}

func annotateApprovals(db *sql.DB, repoID int64, pulls ...*GitPullRequest) {
	numbers := []int{}
	for _, pr := range pulls {
		if pr != nil {
			numbers = append(numbers, pr.Number)
		}
	}
	approvals, err := dbPullApprovals(db, repoID, numbers)
	if err != nil {
		return
	}
	for _, pr := range pulls {
		if pr != nil && pr.HeadSHA != "" {
			pr.ApprovedBy = approvals[pr.Number][pr.HeadSHA]
		}
	}
}

// reviewedPullRequest wraps runPullRequest with approvals, the merge gate and
// the operation log.
func reviewedPullRequest(db *sql.DB, repoID int64, exec pullRequestExecutor, conn int64, target githubRepo, in PullRequestInput,
	branchState func(string) (string, string), requireApproval bool) (*PullRequestResult, error) {
	action := strings.ToLower(strings.TrimSpace(in.Action))
	actor := strings.TrimSpace(in.Actor)
	current := func() (*GitPullRequest, error) {
		if in.Number <= 0 {
			return nil, errors.New("number required")
		}
		got, err := runPullRequest(exec, conn, target, PullRequestInput{Action: "get", Number: in.Number}, nil)
		if err != nil {
			return nil, err
		}
		return got.PullRequest, nil
	}
	record := func(op, from, to string, err error) {
		dbRecordGitOperation(db, repoID, op, actor, from, to, err)
	}

	switch action {
	case "approve":
		if !isPerson(actor) {
			return nil, fmt.Errorf("only a person can approve a pull request (caller is %s); ask a person to approve it in the Code panel", firstNonEmpty(actor, "unknown"))
		}
		pr, err := current()
		if err != nil {
			return nil, err
		}
		if pr.State != "open" {
			return nil, fmt.Errorf("pull request #%d is %s; only open pull requests can be approved", pr.Number, pr.State)
		}
		if want := strings.TrimSpace(in.ExpectedHeadSHA); want != "" && want != pr.HeadSHA {
			return nil, fmt.Errorf("pull request #%d head is now %s, not %s; review the new commits before approving", pr.Number, shortSHA(pr.HeadSHA), shortSHA(want))
		}
		err = dbApprovePull(db, repoID, pr.Number, pr.HeadSHA, actor)
		record("pr_approve", "", pr.HeadSHA, err)
		if err != nil {
			return nil, err
		}
		annotateApprovals(db, repoID, pr)
		return &PullRequestResult{Action: "approve", Repository: target.String(), PullRequest: pr}, nil

	case "merge":
		if requireApproval {
			pr, err := current()
			if err != nil {
				return nil, err
			}
			if want := strings.TrimSpace(in.ExpectedHeadSHA); want != "" && want != pr.HeadSHA {
				return nil, fmt.Errorf("pull request #%d head is now %s, not %s; review the new commits before merging", pr.Number, shortSHA(pr.HeadSHA), shortSHA(want))
			}
			if isPerson(actor) {
				// A person merging is their own review of this head.
				_ = dbApprovePull(db, repoID, pr.Number, pr.HeadSHA, actor)
			} else {
				approvals, err := dbPullApprovals(db, repoID, []int{pr.Number})
				if err != nil {
					return nil, err
				}
				if len(approvals[pr.Number][pr.HeadSHA]) == 0 {
					err := fmt.Errorf("pull request #%d needs a person's approval of its current head %s before an agent can merge it; ask a person to approve it in the Code panel", pr.Number, shortSHA(pr.HeadSHA))
					record("pr_merge", pr.HeadSHA, "", err)
					return nil, err
				}
			}
			// Pin the merge to the approved head: GitHub refuses if it moved.
			in.ExpectedHeadSHA = pr.HeadSHA
		}
	}

	result, err := runPullRequest(exec, conn, target, in, branchState)
	switch action {
	case "create":
		to := ""
		if result != nil && result.PullRequest != nil {
			to = result.PullRequest.HeadSHA
		}
		if result == nil || !result.Existing {
			record("pr_create", "", to, err)
		}
	case "merge":
		record("pr_merge", strings.TrimSpace(in.ExpectedHeadSHA), func() string {
			if result != nil {
				return result.MergeSHA
			}
			return ""
		}(), err)
	case "close":
		record("pr_close", "", "", err)
	}
	if err != nil {
		return nil, err
	}
	pulls := []*GitPullRequest{result.PullRequest}
	for i := range result.PullRequests {
		pulls = append(pulls, &result.PullRequests[i])
	}
	annotateApprovals(db, repoID, pulls...)
	return result, nil
}
