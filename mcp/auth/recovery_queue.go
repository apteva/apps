package main

import (
	"context"
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"time"
)

// The same SQL statement handles known and unknown mailboxes. Delivery occurs
// outside the public request, avoiding a synchronous mail-provider timing leak.
func enqueueRecovery(ctx *sdk.AppCtx, pid string, org *Organization, c *Client, email, kind, continueURL string) error {
	now := time.Now().Unix()
	_, err := ctx.AppDB().Exec(`INSERT INTO auth_recovery_jobs(project_id,organization_id,user_id,client_id,kind,continue_url,next_attempt,created_at)
 SELECT project_id,organization_id,id,?,?,?,?,? FROM users WHERE project_id=? AND organization_id=? AND email=? AND status='active' AND (?<>'verify_email' OR email_verified_at IS NULL)
 ON CONFLICT(project_id,organization_id,user_id,client_id,kind) DO NOTHING`, c.ClientID, kind, continueURL, now, now, pid, org.ID, email, kind)
	return err
}
func (a *App) deliverRecoveryJobs(runCtx context.Context, ctx *sdk.AppCtx) error {
	for i := 0; i < 20; i++ {
		if err := runCtx.Err(); err != nil {
			return err
		}
		var id, oid, uid int64
		var pid, cid, kind, continuation string
		// Atomic lease supports multiple workers without holding a DB lock during mail.
		err := ctx.AppDB().QueryRow(`UPDATE auth_recovery_jobs SET attempts=attempts+1,next_attempt=? WHERE id=(SELECT id FROM auth_recovery_jobs WHERE next_attempt<=? AND attempts<5 ORDER BY id LIMIT 1) RETURNING id,project_id,organization_id,user_id,client_id,kind,continue_url`, time.Now().Add(5*time.Minute).Unix(), time.Now().Unix()).Scan(&id, &pid, &oid, &uid, &cid, &kind, &continuation)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		org, orgErr := dbGetOrgByID(ctx.AppDB(), pid, oid)
		user, userErr := dbGetUserByID(ctx.AppDB(), pid, oid, uid)
		client, clientErr := dbGetClientByClientID(ctx.AppDB(), pid, cid)
		valid := orgErr == nil && userErr == nil && clientErr == nil && org.Status == "active" && user.Status == "active" && client.DisabledAt == "" && (client.OrganizationID == 0 || client.OrganizationID == oid)
		if valid {
			err = issueRecoveryToken(ctx, pid, org, uid, user.Email, kind, recoveryLinkOptions{ClientID: cid, ContinueURL: continuation})
		}
		if !valid || err == nil {
			if _, err = ctx.AppDB().Exec(`DELETE FROM auth_recovery_jobs WHERE id=?`, id); err != nil {
				return err
			}
		} else {
			ctx.Logger().Warn("recovery delivery queued for retry", "job_id", id)
		}
	}
	return nil
}
