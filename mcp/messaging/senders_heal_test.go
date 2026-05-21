package main

// Tests for the v0.13.0 SES verification healing changes:
//   #1 — senders_create re-probes a stuck adopted DKIM identity
//   #3 — senders_refresh re-stamps inheritance mailboxes from the parent
//   ★  — the background poller's "projects with non-terminal rows" gate

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// #1: adopting an existing identity whose DKIM is TEMPORARY_FAILURE must
// delete + recreate it (forcing a fresh SES probe) rather than inheriting
// the broken status.
func TestSendersCreate_Domain_ReprobesStuckDkim(t *testing.T) {
	plat := &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": float64(1)}, // domains NOT bound → DNS publish skipped
		executeOverride: func(tool string, prior int) *sdk.ExecuteResult {
			switch tool {
			case "verify_domain":
				if prior == 0 {
					// first create → already exists
					return &sdk.ExecuteResult{Success: false, Status: 409, Data: json.RawMessage(`{"message":"already exists"}`)}
				}
				// re-create after delete → fresh, probing
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(
					`{"DkimAttributes":{"Tokens":["a","b","c"],"Status":"SUCCESS"}}`)}
			case "get_identity_verification":
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(
					`{"DkimAttributes":{"Tokens":["x","y","z"],"Status":"TEMPORARY_FAILURE"}}`)}
			case "delete_identity":
				return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
			}
			return nil
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendersCreate(ctx, map[string]any{"address": "stuck.com", "inbound": "false"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(*sendersCreateResp)

	if resp.DkimStatus != "SUCCESS" {
		t.Errorf("expected re-probed status SUCCESS, got %q", resp.DkimStatus)
	}
	// delete_identity must have run as part of the re-probe.
	deleted := false
	verifyDomainCalls := 0
	for _, c := range plat.executeCalls {
		switch c.Tool {
		case "delete_identity":
			deleted = true
		case "verify_domain":
			verifyDomainCalls++
		}
	}
	if !deleted {
		t.Errorf("expected delete_identity during re-probe")
	}
	if verifyDomainCalls != 2 {
		t.Errorf("expected verify_domain called twice (create + recreate), got %d", verifyDomainCalls)
	}
	// Step detail should note the re-probe.
	noted := false
	for _, s := range resp.Steps {
		if s.Step == "ses_verify_domain" && strings.Contains(s.Detail, "re-probed") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("ses_verify_domain step should note the re-probe, steps=%+v", resp.Steps)
	}
	// Identity persisted as verified (fresh SUCCESS).
	id, _ := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "stuck.com")
	if id == nil || !id.Verified {
		t.Errorf("expected re-probed identity persisted verified, got %+v", id)
	}
}

// #3: when a mailbox's per-address SES identity is gone but its parent
// domain is alive, refresh must re-stamp the mailbox from the parent
// instead of leaving a stale FAILED status.
func TestRefreshSES_RestampsInheritanceMailbox(t *testing.T) {
	plat := &stubPlatform{
		replyByTool: map[string]*sdk.ExecuteResult{
			// SES knows the domain (verified) but NOT the mailbox.
			"list_identities": {Success: true, Status: 200, Data: json.RawMessage(
				`{"EmailIdentities":[{"IdentityName":"acme.com","IdentityType":"DOMAIN","SendingEnabled":true,"VerificationStatus":"SUCCESS"}],"NextToken":""}`)},
		},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	parentID := preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "acme.com",
		Provider: "aws-ses", ProviderIdentityID: "acme.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})
	// Mailbox carries a stale FAILED status from when its per-address
	// identity still existed.
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "alice@acme.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "alice@acme.com",
		Verified: false, VerificationStatus: "failed", DkimStatus: "FAILED",
		ParentIdentityID: parentID,
	})

	if err := app.refreshSESIdentities(ctx, "test-proj", 1); err != nil {
		t.Fatal(err)
	}

	row, _ := dbFindSender(ctx.AppDB(), "test-proj", "email", "alice@acme.com")
	if row == nil {
		t.Fatalf("inheritance mailbox should not be soft-deleted")
	}
	if row.DeletedAt != nil {
		t.Fatalf("inheritance mailbox was soft-deleted; should have been re-stamped")
	}
	if !row.Verified || row.VerificationStatus != "verified" || row.DkimStatus != "SUCCESS" {
		t.Errorf("mailbox should be re-stamped from parent, got verified=%v status=%q dkim=%q",
			row.Verified, row.VerificationStatus, row.DkimStatus)
	}
}

// ★ poller gate: projects with non-terminal rows are returned; an
// all-terminal project is not.
func TestDbProjectsWithNonTerminalVerifications(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})

	// A pending identity + a verified sender in test-proj.
	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "pending.com",
		Provider: "aws-ses", ProviderIdentityID: "pending.com",
		Verified: false, VerificationStatus: "pending", DkimStatus: "PENDING",
	})
	preseedSender(t, ctx, senderUpsert{
		Channel: "email", Address: "ok@verified.com", Kind: "email_mailbox",
		Provider: "aws-ses", ProviderIdentityID: "ok@verified.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})

	pids, err := dbProjectsWithNonTerminalVerifications(ctx.AppDB(), pollVerifyMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 || pids[0] != "test-proj" {
		t.Fatalf("expected [test-proj] (pending identity present), got %v", pids)
	}

	// Flip the pending identity to verified → no non-terminal rows left.
	preseedIdentity(t, ctx, identityUpsert{
		Kind: "email_domain", Address: "pending.com",
		Provider: "aws-ses", ProviderIdentityID: "pending.com",
		Verified: true, VerificationStatus: "verified", DkimStatus: "SUCCESS",
	})
	pids, err = dbProjectsWithNonTerminalVerifications(ctx.AppDB(), pollVerifyMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Fatalf("expected no projects once all rows terminal, got %v", pids)
	}
}
