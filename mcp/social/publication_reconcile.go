package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type publicationRecordRef struct {
	App      string `json:"app"`
	ID       string `json:"record_id"`
	Revision int    `json:"expected_revision"`
}

func publicationReconcileTool(a *App) sdk.Tool {
	return sdk.Tool{Name: "post_publication_reconcile", Description: "Repair existing publication links and authoritative native times for one Social account/target, without publishing, retrying or creating assets. Defaults to dry-run; apply=true requires expected_post_revision and expected_publication_revision. refresh_native=true performs account-bound native inspection; missing times remain unknown. Optional records[] references existing Editorial/Content Catalog records with their expected revisions. correction_id resumes record-only propagation after interruption.", InputSchema: schemaObject(map[string]any{
		"post_id": map[string]any{"type": "integer"}, "target_id": map[string]any{"type": "integer"}, "social_account_id": map[string]any{"type": "integer"}, "expected_post_revision": map[string]any{"type": "integer", "minimum": 1}, "expected_publication_revision": map[string]any{"type": "integer", "minimum": 0}, "apply": map[string]any{"type": "boolean", "default": false}, "refresh_native": map[string]any{"type": "boolean", "default": false}, "correction_id": map[string]any{"type": "string"}, "records": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "object", "properties": map[string]any{"app": map[string]any{"type": "string", "enum": []string{"editorial", "content-catalog"}}, "record_id": map[string]any{"type": "string"}, "expected_revision": map[string]any{"type": "integer", "minimum": 1}}, "required": []string{"app", "record_id", "expected_revision"}}}}, []string{"post_id", "target_id", "social_account_id"}), Handler: a.toolPostPublicationReconcile}
}

func (a *App) toolPostPublicationReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	project := projectScope(ctx)
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	account := int64(intArg(args, "social_account_id", 0))
	if account <= 0 {
		return nil, errors.New("social_account_id required")
	}
	unlock := lockSocialPost(ctx, post)
	defer unlock()
	r, err := loadRecoveryTarget(ctx, post, target)
	if err != nil || r.AccountID != account || validateDeliveryAccount(args, r.AccountID) != nil {
		return mcpError("target/account not found in this project"), nil
	}
	if r.PublicID == "" {
		return mcpError("native identity is unknown; this operation never initializes or resolves an upload"), nil
	}
	apply := boolArg(args, "apply", false)
	if id := strOption(args, "correction_id"); id != "" {
		if !apply {
			return mcpError("apply=true required to resume record repair"), nil
		}
		return a.resumePublicationRecords(ctx, r, id)
	}
	f, err := loadPublicationFacts(ctx.AppDB(), project, target)
	if err != nil {
		return nil, err
	}
	before := f
	normalized, urlErr := normalizePublicationURL(r.Platform, valueOrEmpty(f.URL))
	f.URL = optionalText(normalized)
	evidence := map[string]any{"kind": "publication_record_correction", "native_inspection": false, "url_policy": r.Platform, "actor": workflowActor(args)}
	if urlErr != nil {
		evidence["url_rejection"] = urlErr.Error()
	}
	if boolArg(args, "refresh_native", false) {
		b, err := loadHistoryAccount(ctx, account)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		run := historyRun{Limitations: []string{}}
		observations, err := a.inspectNativeHistory(ctx, b, &run, r.PublicID)
		if err != nil {
			return mcpError("native inspection failed; no correction applied: " + redactDeliverySecrets(err.Error())), nil
		}
		if len(observations) != 1 || observations[0].ID != r.PublicID || run.Incomplete {
			return mcpError("native identity/account was not verified"), nil
		}
		n := normalizeNativeObservation(b, observations[0])
		n.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		evidence["native_inspection"], evidence["native_metadata"], evidence["verification_scope"] = true, n, nonEmpty(run.Scope, "account_bound_native_metadata")
		if n.URL != nil {
			f.URL = n.URL
		}
		if n.UploadedAt != nil {
			f.UploadedAt = n.UploadedAt
			if f.PublishedAt == nil {
				f.TimeSource = n.PublicationTimeSource
			}
		}
		if n.PublishedAt != nil && membershipPublicationTimeAllowed(r.Options, n) {
			f.PublishedAt = n.PublishedAt
			f.TimeSource = n.PublicationTimeSource
		}
		f.ObservedAt = &n.ObservedAt
	}
	var refs []publicationRecordRef
	if raw, ok := args["records"]; ok {
		bytes, _ := json.Marshal(raw)
		if json.Unmarshal(bytes, &refs) != nil {
			return mcpError("invalid records"), nil
		}
	}
	if len(refs) > 16 {
		return mcpError("at most 16 record references"), nil
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		key := ref.App + ":" + ref.ID
		if (ref.App != "editorial" && ref.App != "content-catalog") || strings.TrimSpace(ref.ID) == "" || ref.Revision <= 0 || seen[key] {
			return mcpError("records must be distinct existing Editorial/Catalog references with positive revisions"), nil
		}
		seen[key] = true
	}
	if !apply {
		return map[string]any{"status": "preview", "social_account_id": account, "post_id": post, "target_id": target, "expected_post_revision": r.Revision, "expected_publication_revision": before.Revision, "before": before, "after": f, "evidence": evidence, "records": refs, "publishing_actions": 0}, nil
	}
	if _, ok := args["expected_publication_revision"]; !ok || intArg(args, "expected_publication_revision", -1) != before.Revision || intArg(args, "expected_post_revision", -1) != r.Revision {
		return mcpError("revision conflict; preview again"), nil
	}
	// Observation freshness alone does not create repeated correction history.
	comparable := f
	comparable.ObservedAt = before.ObservedAt
	if reflect.DeepEqual(comparable, before) && len(refs) == 0 {
		return map[string]any{"status": "unchanged", "target_id": target, "facts": before, "publishing_actions": 0}, nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := loadRecoveryTargetFrom(tx, project, post, target)
	if err != nil {
		return nil, err
	}
	if current.fingerprint("") != r.fingerprint("") {
		return mcpError("target changed during inspection; no correction applied"), nil
	}
	id, err := correctPublicationFacts(tx, project, r, f, evidence)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	for _, ref := range refs {
		if _, err = tx.Exec(`INSERT INTO social_publication_record_repairs(correction_id,app,record_id,expected_revision) VALUES(?,?,?,?)`, id, ref.App, ref.ID, ref.Revision); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.rollupPostStatus(ctx, post)
	return a.resumePublicationRecords(ctx, r, id)
}

func (a *App) resumePublicationRecords(ctx *sdk.AppCtx, r recoveryTarget, id string) (any, error) {
	var after, before, evidence, identity, accountBinding, contentBinding string
	var postRevision, attempt int
	err := ctx.AppDB().QueryRow(`SELECT after_facts,before_facts,evidence,native_post_id,post_revision,source_attempt,account_binding,content_binding FROM social_publication_corrections WHERE id=? AND project_id=? AND target_id=? AND social_account_id=? AND post_id=?`, id, projectScope(ctx), r.TargetID, r.AccountID, r.PostID).Scan(&after, &before, &evidence, &identity, &postRevision, &attempt, &accountBinding, &contentBinding)
	if err != nil {
		return mcpError("correction not found in this account/target"), nil
	}
	var f, old publicationFacts
	if json.Unmarshal([]byte(after), &f) != nil || json.Unmarshal([]byte(before), &old) != nil {
		return nil, errors.New("invalid saved correction")
	}
	current, err := loadPublicationFacts(ctx.AppDB(), projectScope(ctx), r.TargetID)
	if err != nil {
		return nil, err
	}
	if accountBinding != uploadRecoveryAccountBinding(projectScope(ctx), r) || contentBinding != uploadRecoveryContentBinding(r) || identity != r.PublicID || postRevision != r.Revision || attempt != r.Attempts || current.Revision != f.Revision {
		return mcpError("correction is stale; inspect current facts before propagating records"), nil
	}
	rows, err := ctx.AppDB().Query(`SELECT app,record_id,expected_revision,state,result FROM social_publication_record_repairs WHERE correction_id=? ORDER BY app,record_id`, id)
	if err != nil {
		return nil, err
	}
	type item struct {
		ref        publicationRecordRef
		state, raw string
	}
	var items []item
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.ref.App, &x.ref.ID, &x.ref.Revision, &x.state, &x.raw); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	results := []map[string]any{}
	status := "complete"
	for _, x := range items {
		if x.state == "complete" {
			var result any
			_ = json.Unmarshal([]byte(x.raw), &result)
			results = append(results, map[string]any{"app": x.ref.App, "record_id": x.ref.ID, "status": "complete", "result": result, "idempotent": true})
			continue
		}
		result, repairErr := repairPublicationRecord(ctx, r, x.ref, id, old, f)
		state := "complete"
		if repairErr != nil {
			state = "pending"
			status = "records_pending"
			result = map[string]any{"error": redactDeliverySecrets(repairErr.Error())}
		}
		raw, _ := json.Marshal(result)
		if _, err = ctx.AppDB().Exec(`UPDATE social_publication_record_repairs SET state=?,result=? WHERE correction_id=? AND app=? AND record_id=?`, state, string(raw), id, x.ref.App, x.ref.ID); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"app": x.ref.App, "record_id": x.ref.ID, "status": state, "result": result})
	}
	return map[string]any{"status": status, "correction_id": id, "post_id": r.PostID, "target_id": r.TargetID, "social_account_id": r.AccountID, "facts": f, "records": results, "publishing_actions": 0}, nil
}

func repairPublicationRecord(ctx *sdk.AppCtx, r recoveryTarget, ref publicationRecordRef, id string, before, after publicationFacts) (map[string]any, error) {
	if ctx.IntegrationFor(ref.App) == nil {
		return nil, fmt.Errorf("optional %s binding is not configured; retry this correction after binding it", ref.App)
	}
	if ref.App == "content-catalog" {
		return repairCatalogPublication(ctx, r, ref, id, before, after)
	}
	return repairEditorialPublication(ctx, r, ref, id, before, after)
}

func currentSocialInstall(ctx *sdk.AppCtx) (int64, error) {
	who, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || who == nil || who.InstallID <= 0 {
		return 0, errors.New("Social install identity unavailable")
	}
	return who.InstallID, nil
}
func stringAccountReference(install, account int64) string {
	return "social:" + strconv.FormatInt(install, 10) + ":account:" + strconv.FormatInt(account, 10)
}
