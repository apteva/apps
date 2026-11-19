package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestPullRequestReview_PersonApprovesAgentMerges(t *testing.T) {
	f := newBranchDeleteFixture(t)
	db, repoID := f.ctx.AppDB(), f.repo.ID
	target := githubRepo{Owner: "apteva", Name: "apps"}
	gh := &fakeGitHub{replies: map[string]*sdk.ExecuteResult{
		"get_pull":   ghReply(200, samplePull),
		"list_pulls": ghReply(200, []any{samplePull}),
		"merge_pull": ghReply(200, map[string]any{"merged": true, "sha": "merge1"}),
	}}
	run := func(in PullRequestInput, required bool) (*PullRequestResult, error) {
		return reviewedPullRequest(db, repoID, gh, 134, target, in, nil, required)
	}
	called := func(tool string) bool {
		for _, c := range gh.calls {
			if c == tool {
				return true
			}
		}
		return false
	}

	// An agent cannot merge an unapproved pull request, and nothing reaches GitHub's merge.
	_, err := run(PullRequestInput{Action: "merge", Number: 42, Actor: "agent:7"}, true)
	wantErr(t, err, "needs a person's approval of its current head abc123")
	if called("merge_pull") {
		t.Fatal("unapproved merge reached GitHub")
	}
	// Agents and unidentified callers cannot approve.
	for _, actor := range []string{"agent:7", "unknown", ""} {
		_, err = run(PullRequestInput{Action: "approve", Number: 42, Actor: actor}, true)
		wantErr(t, err, "only a person can approve")
	}
	// A person approving an outdated head is told to look again.
	_, err = run(PullRequestInput{Action: "approve", Number: 42, Actor: "user:1", ExpectedHeadSHA: "old999"}, true)
	wantErr(t, err, "review the new commits before approving")

	got, err := run(PullRequestInput{Action: "approve", Number: 42, Actor: "user:1", ExpectedHeadSHA: "abc123"}, true)
	if err != nil || len(got.PullRequest.ApprovedBy) != 1 || got.PullRequest.ApprovedBy[0] != "user:1" {
		t.Fatalf("approve=%+v err=%v", got, err)
	}
	// Approving twice is harmless.
	if _, err := run(PullRequestInput{Action: "approve", Number: 42, Actor: "user:1"}, true); err != nil {
		t.Fatal(err)
	}
	listed, err := run(PullRequestInput{Action: "list"}, true)
	if err != nil || len(listed.PullRequests[0].ApprovedBy) != 1 {
		t.Fatalf("list approvals=%+v err=%v", listed, err)
	}

	// Now the agent merges, pinned to the approved head.
	got, err = run(PullRequestInput{Action: "merge", Number: 42, Actor: "agent:7", Method: "squash"}, true)
	if err != nil || !got.Merged || got.MergeSHA != "merge1" {
		t.Fatalf("approved merge=%+v err=%v", got, err)
	}
	mergeIn := gh.inputs[len(gh.inputs)-2]
	if mergeIn["sha"] != "abc123" || mergeIn["pull_number"] != 42 {
		t.Fatalf("merge not pinned to approved head: %v", mergeIn)
	}

	// A new push makes the approval stale: the agent is refused again.
	moved := map[string]any{}
	for k, v := range samplePull {
		moved[k] = v
	}
	moved["head"] = map[string]any{"ref": "feat/places", "sha": "def456"}
	gh.replies["get_pull"] = ghReply(200, moved)
	before := len(gh.calls)
	_, err = run(PullRequestInput{Action: "merge", Number: 42, Actor: "agent:7"}, true)
	wantErr(t, err, "current head def456")
	for _, c := range gh.calls[before:] {
		if c == "merge_pull" {
			t.Fatal("stale approval let the agent merge")
		}
	}
	// A caller expecting a head that is no longer current is refused too.
	_, err = run(PullRequestInput{Action: "merge", Number: 42, Actor: "user:1", ExpectedHeadSHA: "abc123"}, true)
	wantErr(t, err, "review the new commits before merging")

	// A person merging is their own review, and is recorded as an approval.
	if _, err := run(PullRequestInput{Action: "merge", Number: 42, Actor: "user:2"}, true); err != nil {
		t.Fatal(err)
	}
	approvals, _ := dbPullApprovals(db, repoID, []int{42})
	if len(approvals[42]["def456"]) != 1 || approvals[42]["def456"][0] != "user:2" {
		t.Fatalf("person merge approval=%v", approvals)
	}

	// With the gate off (git_merge_approval=off) an agent merges directly.
	if _, err := run(PullRequestInput{Action: "merge", Number: 43, Actor: "agent:7"}, false); err != nil {
		t.Fatal(err)
	}

	// The operation log has who approved and merged, including the refusals.
	ops, err := dbGitOperations(db, repoID, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, op := range ops {
		counts[op.Operation+"/"+op.Status]++
		if op.Operation == "pr_approve" && op.Actor != "user:1" {
			t.Fatalf("approve recorded for %s", op.Actor)
		}
	}
	if counts["pr_approve/ok"] != 2 || counts["pr_merge/ok"] != 3 || counts["pr_merge/error"] != 2 {
		t.Fatalf("operation log=%v", counts)
	}
}

func TestCommandRuns_RecordedAndCapped(t *testing.T) {
	f := newBranchDeleteFixture(t)
	big := strings.Repeat("x", commandRunOutputCap+100) + "END"
	recordCommandRun(f.ctx, f.repo, "workspace", "go test ./...", "agent:7",
		&repoCommandResult{Status: "failed", ExitCode: 1, DurationMS: 1234, WorkspaceID: "ws1", LogTail: big}, nil)
	recordCommandRun(f.ctx, f.repo, "local", "bun test", "user:1", nil, errString("runner unavailable"))
	runs, err := dbCommandRuns(f.ctx.AppDB(), f.repo.ID, 10, 0)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if runs[0].Command != "bun test" || runs[0].Status != "error" || runs[0].Error != "runner unavailable" {
		t.Fatalf("newest run=%+v", runs[0])
	}
	if r := runs[1]; r.ExitCode != 1 || r.DurationMS != 1234 || r.WorkspaceID != "ws1" || r.Actor != "agent:7" || len(r.OutputTail) != 2000 || !strings.HasSuffix(r.OutputTail, "END") {
		t.Fatalf("listed run=%+v (tail %d)", r, len(r.OutputTail))
	}
	one, err := dbCommandRuns(f.ctx.AppDB(), f.repo.ID, 1, runs[1].ID)
	if err != nil || len(one) != 1 || len(one[0].OutputTail) != commandRunOutputCap || !strings.HasSuffix(one[0].OutputTail, "END") {
		t.Fatalf("full run tail=%d err=%v", len(one[0].OutputTail), err)
	}
	for i := 0; i < commandRunsKept+5; i++ {
		recordCommandRun(f.ctx, f.repo, "local", "true", "", &repoCommandResult{Status: "ok"}, nil)
	}
	var n int
	if err := f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM repo_command_runs WHERE repo_id = ?`, f.repo.ID).Scan(&n); err != nil || n != commandRunsKept {
		t.Fatalf("kept=%d err=%v", n, err)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
