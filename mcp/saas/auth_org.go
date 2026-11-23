package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SaaS v0.13.0 -- per-plan Auth org mode.
//
// metadata.auth_org on a plan:
//   - "per_account" (default): provisioning creates one Auth org per account,
//     as before.
//   - "none": no org is created and auth_org_id stays empty, for products
//     whose users sign in to a shared org of their own. An owner user can
//     still be created when the caller passes that org as auth_org_id.

const (
	authOrgPerAccount = "per_account"
	authOrgNone       = "none"
)

func planAuthOrgMode(plan *Plan) string {
	if plan == nil {
		return authOrgPerAccount
	}
	return authOrgModeFromMetadata(mapFromAny(plan.Metadata))
}

func authOrgModeFromMetadata(meta map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(strArg(meta, "auth_org")), authOrgNone) {
		return authOrgNone
	}
	return authOrgPerAccount
}

// validatePlanAuthOrg runs on plan upsert. A plan without per-account orgs
// cannot keep fulfillment actions that read the account's org id.
func validatePlanAuthOrg(db *sql.DB, pid, planKey string, metadata map[string]any) error {
	raw := strings.TrimSpace(strArg(metadata, "auth_org"))
	switch strings.ToLower(raw) {
	case "", authOrgPerAccount, authOrgNone:
	default:
		return fmt.Errorf("metadata.auth_org must be %q or %q", authOrgPerAccount, authOrgNone)
	}
	if authOrgModeFromMetadata(metadata) != authOrgNone {
		return nil
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM saas_plan_actions WHERE project_id=? AND plan_key=? AND args_json LIKE '%auth_org%'`, pid, planKey).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("metadata.auth_org none: %d fulfillment action(s) on this plan reference auth_org", n)
	}
	return nil
}

// validateActionAuthOrg rejects an action that reads auth_org on a plan that
// creates no per-account org.
func validateActionAuthOrg(db *sql.DB, pid, planKey string, actionArgs any) error {
	if !strings.Contains(jsonOrEmpty(actionArgs, "{}"), "auth_org") {
		return nil
	}
	plan, err := dbPlanGet(db, pid, planKey)
	if err != nil {
		return err
	}
	if planAuthOrgMode(plan) == authOrgNone {
		return errors.New("this plan creates no per-account Auth org (metadata.auth_org none); action args cannot reference auth_org")
	}
	return nil
}

// validateAccountAuthOrg rejects an owner user with no org to join.
func validateAccountAuthOrg(plan *Plan, args map[string]any) error {
	if planAuthOrgMode(plan) == authOrgNone && boolArg(args, "create_owner_user") && int64Arg(args, "auth_org_id") == 0 {
		return errors.New("plan creates no per-account Auth org (metadata.auth_org none): create_owner_user needs auth_org_id of the shared org")
	}
	return nil
}
