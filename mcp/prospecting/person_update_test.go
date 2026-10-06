package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDecisionMakerRequiresNameRoleEvidence(t *testing.T) {
	for _, tt := range []struct{ name, text, want, role string }{
		{"observed cookie menu", "Interview Tips\nCookie Policy\nOur Story\nContact Us", "", ""},
		{"menu next to role", "Interview Tips\nCOO\nCookie Policy", "", ""},
		{"story next to founder", "Our Story\nFounder\nCompany Services", "", ""},
		{"narrative role", "Jane Smith\nWe are hiring a COO\nAlice Jones", "", ""},
		{"longer role word", "Jane Smith\nCOOwner\nAlice Jones", "", ""},
		{"doctor navigation", "Dr. Interview Tips\nCookie Policy", "", ""},
		{"inline name first", "Jane Smith, COO", "Jane Smith", "COO"},
		{"inline role first", "COO: Jane Smith", "Jane Smith", "COO"},
		{"adjacent role", "Jane Smith\nCOO", "Jane Smith", "COO"},
		{"adjacent name", "COO\nJane Smith", "Jane Smith", "COO"},
		{"accented name", "María García — Founder", "María García", "founder"},
		{"compound surname", "Anne O’Neill-Smith | COO", "Anne O’Neill-Smith", "COO"},
		{"name particle", "Ludwig van Beethoven, COO", "Ludwig van Beethoven", "COO"},
		{"dentist owner", "Dr. Ada Stone\nPractice Owner", "Ada Stone", "practice owner"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDecisionMaker([]webExtractPage{{Text: tt.text}}, []string{"COO"})
			if got.DisplayName != tt.want || got.JobTitle != tt.role {
				t.Fatalf("got %#v, want %q (%s)", got, tt.want, tt.role)
			}
		})
	}
}

func TestUnknownDecisionMakerDoesNotIncreaseScores(t *testing.T) {
	p := &TargetProfile{TargetTitles: []string{"COO"}}
	base := Candidate{CompanyName: "Example Staffing", CompanyDomain: "example.com", Website: "https://example.com"}
	fit, confidence, _ := scoreCandidate(p, &base, 1)
	for _, name := range []string{"", "Interview Tips", "Cookie Policy", "Our Story"} {
		c := base
		c.PersonDisplayName = name
		c.JobTitle = "COO"
		gotFit, gotConfidence, reasons := scoreCandidate(p, &c, 1)
		if gotFit != fit || gotConfidence != confidence {
			t.Fatalf("%q falsely increased scores: fit=%d confidence=%d reasons=%v", name, gotFit, gotConfidence, reasons)
		}
	}
	c := base
	c.PersonDisplayName = "Jane Smith"
	c.JobTitle = "COO"
	got, _, _ := scoreCandidate(p, &c, 1)
	if got != fit+15 {
		t.Fatalf("valid named role did not score: %d", got)
	}
	c.JobTitle = "Cookie Policy"
	got, _, reasons := scoreCandidate(p, &c, 1)
	if got != fit+5 || strings.Contains(strings.Join(reasons, " "), "target decision-maker role matches") {
		t.Fatalf("substring title scored: %v", reasons)
	}
}

func TestConcurrentCandidateEditsPreserveClearsAndScores(t *testing.T) {
	// Separate file-backed connections exercise SQLite snapshot conflicts as
	// well as interleaved API edits; production's single pool also stays safe.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "candidates.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	for _, file := range []string{"001_init.sql", "002_deterministic_qualification.sql"} {
		b, err := os.ReadFile(filepath.Join("migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := createProfile(db, "p", map[string]any{"name": "US staffing", "target_titles": []any{"COO"}})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 12; iteration++ {
		c, _, err := insertCandidate(db, "p", candidateInput{ProfileID: profile.ID, CompanyName: fmt.Sprintf("Staffing %d", iteration), Website: fmt.Sprintf("https://staff%d.example", iteration), PersonFirstName: "Jane", PersonLastName: "Smith", PersonDisplayName: "Jane Smith", JobTitle: "COO"}, profile)
		if err != nil {
			t.Fatal(err)
		}
		patches := []map[string]any{
			{"person_first_name": "", "person_last_name": "", "person_display_name": "", "job_title": ""},
			{"summary": "Observed staffing services; buyer need unverified."},
			{"email": "office@example.com"},
			{"phone": "+12147856700"},
			{"source_url": "https://example.com/contact"},
		}
		start := make(chan struct{})
		errors := make(chan error, len(patches))
		var wg sync.WaitGroup
		for _, patch := range patches {
			wg.Add(1)
			go func(args map[string]any) {
				defer wg.Done()
				<-start
				_, err := updateCandidate(db, "p", c.ID, args)
				errors <- err
			}(patch)
		}
		close(start)
		wg.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := getCandidate(db, "p", c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PersonFirstName != "" || got.PersonLastName != "" || got.PersonDisplayName != "" || got.JobTitle != "" {
			t.Fatalf("cleared fields restored: %+v", got)
		}
		if got.Summary != patches[1]["summary"] || got.Email != patches[2]["email"] || got.Phone != patches[3]["phone"] || got.SourceURL != patches[4]["source_url"] {
			t.Fatalf("concurrent patch lost: %+v", got)
		}
		fit, confidence, reasons := scoreCandidate(profile, got, 0)
		if got.FitScore != fit || got.ConfidenceScore != confidence || string(got.ScoreReasons) != mustJSON(reasons) {
			t.Fatalf("stale derived scores: %+v", got)
		}
	}
}

func TestQualificationRefreshRemovesInvalidIdentityClaim(t *testing.T) {
	profile := &TargetProfile{TargetTitles: []string{"COO"}}
	candidate := &Candidate{CompanyName: "Example Staffing", CompanyDomain: "example.com", Website: "https://example.com", PersonFirstName: "Interview", PersonLastName: "Tips", PersonDisplayName: "Interview Tips", JobTitle: "COO", EnrichedAt: "2026-10-01T00:00:00Z", Summary: "Interview Tips is listed as COO. General branch contact only; buyer need unverified."}
	applyDeterministicQualification(profile, candidate, []webExtractPage{{URL: "https://example.com", Title: "Example Staffing", Text: "Staffing and recruiting services.\nInterview Tips\nCookie Policy\nOur Story"}})
	if candidate.PersonFirstName != "" || candidate.PersonLastName != "" || candidate.PersonDisplayName != "" || candidate.JobTitle != "" {
		t.Fatalf("stale identity: %+v", candidate)
	}
	if strings.Contains(candidate.Summary, "Interview Tips") || !strings.Contains(candidate.Summary, "buyer need unverified") {
		t.Fatalf("stale claim or lost operator notes: %s", candidate.Summary)
	}
}
