package main

import (
	"database/sql"
	"encoding/json"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

// orgChangeMeta records which organization fields an update changed. Policy
// changes keep the before/after JSON and, separately, whether email
// verification was switched on or off, so the trail answers "who turned
// verification off, and when".
func orgChangeMeta(ctx *sdk.AppCtx, before, after *Organization) map[string]any {
	if after == nil {
		return nil
	}
	meta := map[string]any{}
	if before == nil {
		return meta
	}
	if before.Name != after.Name {
		meta["name"] = map[string]any{"from": before.Name, "to": after.Name}
	}
	if before.Color != after.Color {
		meta["color"] = map[string]any{"from": before.Color, "to": after.Color}
	}
	if before.PolicyOverrides != after.PolicyOverrides {
		meta["policy_overrides"] = map[string]any{"from": before.PolicyOverrides, "to": after.PolicyOverrides}
		pb, eb := effectivePolicy(ctx, before)
		pa, ea := effectivePolicy(ctx, after)
		if eb == nil && ea == nil && pb.Verify != pa.Verify {
			meta["email_verification_required"] = map[string]any{"from": pb.Verify, "to": pa.Verify}
		}
		if eb == nil && ea == nil && (pb.FromEmail != pa.FromEmail || pb.FromName != pa.FromName) {
			meta["email_sender"] = map[string]any{
				"from": map[string]string{"from_email": pb.FromEmail, "from_name": pb.FromName},
				"to":   map[string]string{"from_email": pa.FromEmail, "from_name": pa.FromName},
			}
		}
	}
	return meta
}

type verificationOrgReport struct {
	OrganizationID            int64            `json:"organization_id"`
	Slug                      string           `json:"slug"`
	Status                    string           `json:"status"`
	EmailVerificationRequired *bool            `json:"email_verification_required"`
	PolicyError               string           `json:"policy_error,omitempty"`
	Source                    string           `json:"source"`
	Users                     map[string]int64 `json:"users_by_email_verification"`
}

// verificationReport lists every organization's effective verification policy
// with a count of accounts by how their address was established, plus the
// audited changes to the verification setting.
func verificationReport(ctx *sdk.AppCtx, pid string) (map[string]any, error) {
	orgs, err := dbListOrgs(ctx.AppDB(), pid, true)
	if err != nil {
		return nil, err
	}
	installDefault := true
	if p, err := effectivePolicy(ctx, nil); err == nil {
		installDefault = p.Verify
	}
	out := []verificationOrgReport{}
	off := []string{}
	for _, org := range orgs {
		org := org
		r := verificationOrgReport{OrganizationID: org.ID, Slug: org.Slug, Status: org.Status, Source: "install_default", Users: map[string]int64{}}
		if p, err := effectivePolicy(ctx, &org); err != nil {
			r.PolicyError = err.Error()
		} else {
			v := p.Verify
			r.EmailVerificationRequired = &v
			var overrides map[string]any
			if json.Unmarshal([]byte(org.PolicyOverrides), &overrides) == nil {
				if _, ok := overrides["email_verification_required"]; ok {
					r.Source = "policy_overrides"
				}
			}
			if !v {
				off = append(off, org.Slug)
			}
		}
		rows, err := ctx.AppDB().Query(`SELECT email_verification, COUNT(*) FROM users WHERE project_id=? AND organization_id=? AND kind='account' GROUP BY email_verification`, pid, org.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var state sql.NullString
			var n int64
			if err := rows.Scan(&state, &n); err != nil {
				rows.Close()
				return nil, err
			}
			r.Users[state.String] = n
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	changes, err := ctx.AppDB().Query(`SELECT organization_id, IFNULL(actor_type,''), IFNULL(actor_id,''), IFNULL(source_app,''), json_extract(metadata,'$.email_verification_required.to'), occurred_at
		FROM audit_log WHERE project_id=? AND event='org_updated' AND json_valid(metadata) AND json_type(metadata,'$.email_verification_required') IS NOT NULL
		ORDER BY occurred_at DESC LIMIT 200`, pid)
	if err != nil {
		return nil, err
	}
	defer changes.Close()
	history := []map[string]any{}
	for changes.Next() {
		var oid sql.NullInt64
		var actorType, actorID, app, at string
		var to sql.NullBool
		if err := changes.Scan(&oid, &actorType, &actorID, &app, &to, &at); err != nil {
			return nil, err
		}
		history = append(history, map[string]any{"organization_id": oid.Int64, "email_verification_required": to.Bool, "actor_type": actorType, "actor_id": actorID, "source_app": app, "occurred_at": at})
	}
	if err := changes.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"install_default_required": installDefault,
		"organizations":            out,
		"verification_off":         off,
		"changes":                  history,
		"note":                     "Changes made before Auth 0.14.0 were not attributed and do not appear in changes. not_required accounts signed up while verification was off; their mailbox was never proven.",
	}, nil
}

func (a *App) toolVerificationReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return verificationReport(ctx, pid)
}

func (a *App) handleAdminVerificationReport(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := verificationReport(getAppCtx(r), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "report failed")
		return
	}
	httpJSON(w, out)
}
