package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepoCardDataDoesNotSerializeRuntimeSecrets(t *testing.T) {
	body, err := json.Marshal(RepoCardData{
		Slug: "repo",
		Name: "Repo",
		DevRun: &RepoCardDevRun{
			Status: "live", Framework: "node", Port: 3000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, forbidden := range []string{"env_json", "run_cmd", "log_path", "stream_url"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("repository card serialized %q: %s", forbidden, encoded)
		}
	}
}

func TestIssueCardDataBoundsBody(t *testing.T) {
	issue := &Issue{
		RepoSlug: "repo",
		Number:   7,
		Title:    "Large issue",
		Body:     strings.Repeat("\u00e9", 1200),
		Type:     "bug",
		Status:   "todo",
		State:    "open",
		Priority: "medium",
	}
	card := issueCardData(issue)
	if got := len([]rune(card.Body)); got != 1000 {
		t.Fatalf("body rune count=%d, want 1000", got)
	}
	if !strings.HasSuffix(card.Body, "\u2026") {
		t.Fatalf("truncated body has no ellipsis: %q", card.Body[len(card.Body)-8:])
	}
}
