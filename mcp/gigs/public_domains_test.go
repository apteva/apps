package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPublicDomainMigrationAndDefaultResolution(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	columns, err := ctx.AppDB().Query(`PRAGMA table_info(gig_assignments)`)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	foundSnapshot := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "public_base_url" {
			foundSnapshot = true
		}
	}
	if !foundSnapshot {
		t.Fatal("gig_assignments.public_base_url migration was not applied")
	}

	res, err := ctx.AppDB().Exec(`INSERT INTO gig_public_domains
		(project_id,hostname,apex_domain,dns_name,dns_type,dns_value,ingress_target,is_default,status)
		VALUES ('project-a','work.example.com','example.com','work','A','203.0.113.10','http://127.0.0.1:8080',1,'active')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, selectedID := range []int64{0, id} {
		base, err := resolveGigPublicBaseURL(ctx.AppDB(), "project-a", selectedID)
		if err != nil || base != "https://work.example.com" {
			t.Fatalf("selected=%d base=%q err=%v", selectedID, base, err)
		}
	}
}

func TestWorkerURLUsesCustomDomain(t *testing.T) {
	got, err := buildWorkerURL(nil, "worker token", "https://work.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://work.example.com/worker/worker%20token"; got != want {
		t.Fatalf("worker URL=%q want=%q", got, want)
	}
	if html := workerPageHTML("worker-token"); !strings.Contains(html, `const API = window.location.pathname.replace(/\/+$/, "");`) {
		t.Fatal("worker page does not preserve its custom or exact-install path")
	}
}

func TestAssignmentSnapshotsSelectedPublicDomain(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 22)
	gigID := seedGig(t, ctx, "project-a", "open", `{"type":"object","properties":{}}`)
	res, err := ctx.AppDB().Exec(`INSERT INTO gig_public_domains
		(project_id,hostname,apex_domain,dns_name,dns_type,dns_value,ingress_target,is_default,status)
		VALUES ('project-a','crew.example.com','example.com','crew','A','203.0.113.10','http://127.0.0.1:8080',0,'active')`)
	if err != nil {
		t.Fatal(err)
	}
	domainID, _ := res.LastInsertId()
	assignment, err := assignGig(ctx, "project-a", gigID, workerID, "direct", false, domainID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.PublicBaseURL != "https://crew.example.com" {
		t.Fatalf("public base=%q", assignment.PublicBaseURL)
	}
	if !strings.HasPrefix(assignment.WorkerURL, "https://crew.example.com/worker/") {
		t.Fatalf("worker URL=%q", assignment.WorkerURL)
	}
	if assignment.NotifyWorker {
		t.Fatal("assignment unexpectedly enabled worker notification")
	}
}

func TestPublicDomainNormalizationAndIngressTarget(t *testing.T) {
	if got, err := normalizePublicSubdomain(" Team.Work "); err != nil || got != "team.work" {
		t.Fatalf("subdomain=%q err=%v", got, err)
	}
	if _, err := normalizePublicSubdomain("../bad"); err == nil {
		t.Fatal("invalid subdomain was accepted")
	}
	if value, kind, err := normalizeDNSTarget("203.0.113.10"); err != nil || value != "203.0.113.10" || kind != "A" {
		t.Fatalf("DNS target=%q %q err=%v", value, kind, err)
	}
	if value, kind, err := normalizeDNSTarget("edge.example.com."); err != nil || value != "edge.example.com" || kind != "CNAME" {
		t.Fatalf("DNS target=%q %q err=%v", value, kind, err)
	}
	t.Setenv("APTEVA_APP_PORT", "8123")
	if target, err := gigsIngressTarget(); err != nil || target != "http://127.0.0.1:8123" {
		t.Fatalf("ingress target=%q err=%v", target, err)
	}
}
