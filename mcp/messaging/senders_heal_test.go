package main

// Tests for the v0.13.0 SES verification healing changes:
//   #1 — senders_create re-probes a stuck adopted DKIM identity
//   #3 — senders_refresh re-stamps inheritance mailboxes from the parent
//   ★  — the background poller's "projects with non-terminal rows" gate

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// Adopting an unhealthy identity must preserve its provider resource and status.
func TestSendersCreate_Domain_PreservesStuckDkimIdentity(t *testing.T) {
	plat := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{
		"verify_domain":             {Success: false, Status: 409, Data: json.RawMessage(`{"message":"already exists"}`)},
		"get_identity_verification": {Success: true, Data: json.RawMessage(`{"DkimAttributes":{"Tokens":["x","y","z"],"Status":"TEMPORARY_FAILURE"}}`)},
	}}
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "stuck.com", "inbound": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(*sendersCreateResp).DkimStatus != "TEMPORARY_FAILURE" {
		t.Fatalf("status=%+v", out)
	}
	verify := 0
	for _, call := range plat.executeCalls {
		if call.Tool == "delete_identity" {
			t.Fatal("adoption deleted existing identity")
		}
		if call.Tool == "verify_domain" {
			verify++
		}
	}
	if verify != 1 {
		t.Fatalf("verify calls=%d", verify)
	}
	row, err := dbFindIdentity(ctx.AppDB(), "test-proj", "email_domain", "stuck.com")
	if err != nil || row == nil || row.Verified {
		t.Fatalf("identity=%+v error=%v", row, err)
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
