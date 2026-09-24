package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Publication belongs to one asset. Several rows can point to different posts
// on the same platform; no release plan is needed to record actual use.
type Publication struct {
	ID             string `json:"id"`
	AssetID        string `json:"asset_id"`
	Destination    string `json:"destination"`
	AccountRef     string `json:"account_ref"`
	Audience       string `json:"audience"`
	Status         string `json:"status"`
	PlannedAt      string `json:"planned_at"`
	ActualAt       string `json:"actual_at"`
	ExternalPostID string `json:"external_post_id"`
	ExternalURL    string `json:"external_url"`
	EvidenceSource string `json:"evidence_source"`
	FailureDetails string `json:"failure_details"`
	LegacyTargetID string `json:"legacy_target_id"`
}

const publicationColumns = `id,asset_id,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,legacy_target_id`

func scanPublication(rows *sql.Rows) (Publication, error) {
	var p Publication
	err := rows.Scan(&p.ID, &p.AssetID, &p.Destination, &p.AccountRef, &p.Audience, &p.Status,
		&p.PlannedAt, &p.ActualAt, &p.ExternalPostID, &p.ExternalURL, &p.EvidenceSource, &p.FailureDetails, &p.LegacyTargetID)
	return p, err
}

func publicationsForAsset(db *sql.DB, pid, assetID string) ([]Publication, error) {
	rows, err := db.Query(`SELECT `+publicationColumns+` FROM asset_publications WHERE project_id=? AND asset_id=? ORDER BY created_at DESC,id DESC`, pid, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Publication{}
	for rows.Next() {
		p, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) assetPublicationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id"); err != nil {
		return nil, err
	}
	if _, err = assetByID(ctx.AppDB(), pid, str(args, "asset_id")); err != nil {
		return nil, err
	}
	items, err := publicationsForAsset(ctx.AppDB(), pid, str(args, "asset_id"))
	return map[string]any{"publications": items}, err
}

// Record creates a direct asset publication or appends a new observed state to
// an existing one. All writes remain in Catalog; Social/Patreon are untouched.
func (a *App) assetPublicationRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id", "status"); err != nil {
		return nil, err
	}
	assetID := str(args, "asset_id")
	if _, err = assetByID(ctx.AppDB(), pid, assetID); err != nil {
		return nil, err
	}
	status := str(args, "status")
	if !oneOf(status, "planned", "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown") {
		return nil, errors.New("invalid publication status")
	}
	if (status == "verified_published" || status == "provider_reported_published") && str(args, "actual_at") == "" {
		return nil, errors.New("published status requires actual_at")
	}
	if status == "verified_published" && str(args, "external_post_id") == "" && str(args, "external_url") == "" {
		return nil, errors.New("verified publication requires a post URL or ID")
	}
	if status != "planned" && strings.TrimSpace(str(args, "evidence_source")) == "" {
		return nil, errors.New("observed status requires evidence_source")
	}
	for _, field := range []string{"planned_at", "actual_at"} {
		if value := str(args, field); value != "" {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return nil, errors.New(field + " must be RFC3339")
			}
		}
	}
	id := str(args, "publication_id")
	if id == "" {
		if err = required(args, "destination"); err != nil {
			return nil, err
		}
		id = newID()
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if str(args, "publication_id") == "" {
		_, err = tx.Exec(`INSERT INTO asset_publications(id,project_id,asset_id,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, assetID, strings.ToLower(strings.TrimSpace(str(args, "destination"))), str(args, "account_ref"), str(args, "audience"), status,
			str(args, "planned_at"), str(args, "actual_at"), str(args, "external_post_id"), str(args, "external_url"), str(args, "evidence_source"), str(args, "failure_details"))
	} else {
		res, updateErr := tx.Exec(`UPDATE asset_publications SET status=?,actual_at=?,external_post_id=?,external_url=?,evidence_source=?,failure_details=?,updated_at=?
			WHERE project_id=? AND asset_id=? AND id=?`, status, str(args, "actual_at"), str(args, "external_post_id"), str(args, "external_url"), str(args, "evidence_source"), str(args, "failure_details"), now(), pid, assetID, id)
		err = updateErr
		if err == nil {
			var n int64
			n, err = res.RowsAffected()
			if err == nil && n == 0 {
				err = errors.New("asset publication not found")
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if status != "planned" {
		_, err = tx.Exec(`INSERT INTO asset_publication_events(id,project_id,publication_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details)
			VALUES(?,?,?,?,?,?,?,?,?)`, newID(), pid, id, status, str(args, "external_post_id"), str(args, "external_url"), str(args, "actual_at"), str(args, "evidence_source"), str(args, "failure_details"))
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.publication.observed", pid, map[string]any{"publication_id": id, "asset_id": assetID, "status": status})
	return map[string]any{"publication_id": id, "status": status}, nil
}
