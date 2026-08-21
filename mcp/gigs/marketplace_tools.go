package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) marketplaceTools() []sdk.Tool {
	obj := func(props map[string]any, required ...string) map[string]any { return schemaObject(props, required) }
	integer := map[string]any{"type": "integer"}
	stringP := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	number := map[string]any{"type": "number"}
	object := map[string]any{"type": "object"}
	array := map[string]any{"type": "array"}
	return []sdk.Tool{
		{Name: "pay_grades_create", Description: "Create a configurable worker pay grade. Args: name, slug?, rank?, description?, default_pricing_model?, default_amount_minor?, currency?. Returns {pay_grade}.", InputSchema: obj(map[string]any{"name": stringP, "slug": stringP, "rank": integer, "description": stringP, "default_pricing_model": stringP, "default_amount_minor": integer, "currency": stringP}, "name"), Handler: a.toolPayGradesCreate},
		{Name: "pay_grades_list", Description: "List worker pay grades ordered by rank. Args: include_inactive?. Returns {pay_grades}.", InputSchema: obj(map[string]any{"include_inactive": boolean}), Handler: a.toolPayGradesList},
		{Name: "pay_grades_update", Description: "Patch a pay grade. Args: id, patch {name,rank,description,default_pricing_model,default_amount_minor,currency,active}. Returns {pay_grade}.", InputSchema: obj(map[string]any{"id": integer, "patch": object}, "id", "patch"), Handler: a.toolPayGradesUpdate},
		{Name: "workers_set_pay_grade", Description: "Assign a commercial pay grade to a worker, separately from skill proficiency. Args: worker_id, pay_grade_id, currency?, metadata?. Returns {worker,pay_profile}.", InputSchema: obj(map[string]any{"worker_id": integer, "pay_grade_id": integer, "currency": stringP, "metadata": object}, "worker_id", "pay_grade_id"), Handler: a.toolWorkersSetPayGrade},

		{Name: "rates_set", Description: "Create an immutable effective worker-pay rate. A new matching rate archives the previous active rate. Scope with worker_id or pay_grade_id plus optional template_id or offer_package_id. Args: pricing_model, amount_minor, currency, worker_id?, pay_grade_id?, template_id?, offer_package_id?, unit?, minimum_quantity?, maximum_quantity?, effective_from?, effective_until?, notes?. Returns {rate}.", InputSchema: obj(map[string]any{"pricing_model": stringP, "amount_minor": integer, "currency": stringP, "worker_id": integer, "pay_grade_id": integer, "template_id": integer, "offer_package_id": integer, "unit": stringP, "minimum_quantity": number, "maximum_quantity": number, "effective_from": stringP, "effective_until": stringP, "notes": stringP}, "pricing_model", "amount_minor", "currency"), Handler: a.toolRatesSet},
		{Name: "rates_list", Description: "List rate-card history. Filter by worker_id, pay_grade_id, template_id, offer_package_id, include_archived?. Returns {rates}.", InputSchema: obj(map[string]any{"worker_id": integer, "pay_grade_id": integer, "template_id": integer, "offer_package_id": integer, "include_archived": boolean}), Handler: a.toolRatesList},
		{Name: "rates_resolve", Description: "Explain the standard worker compensation for a worker/template/package/quantity. Never invents a missing rate. Returns {quote} with configured=false and an explanation when configuration is incomplete.", InputSchema: obj(map[string]any{"worker_id": integer, "template_id": integer, "offer_package_id": integer, "quantity": number, "currency": stringP}, "worker_id"), Handler: a.toolRatesResolve},

		{Name: "offers_create", Description: "Create a draft standard service offer around a published Gigs template. Args: template_id, name, slug?, description?, category?, visibility? (private|unlisted|public). Returns {offer}.", InputSchema: obj(map[string]any{"template_id": integer, "name": stringP, "slug": stringP, "description": stringP, "category": stringP, "visibility": stringP}, "template_id", "name"), Handler: a.toolOffersCreate},
		{Name: "offers_list", Description: "List standard offers with packages. Args: status?, q?, limit?. Returns {offers}.", InputSchema: obj(map[string]any{"status": stringP, "q": stringP, "limit": integer}), Handler: a.toolOffersList},
		{Name: "offers_get", Description: "Get a standard offer and its packages by id or slug. Returns {offer}.", InputSchema: obj(map[string]any{"id": integer, "slug": stringP}), Handler: a.toolOffersGet},
		{Name: "offers_update", Description: "Patch draft offer metadata. Args: id, patch {name,description,category,visibility}. Increments offer version. Returns {offer}.", InputSchema: obj(map[string]any{"id": integer, "patch": object}, "id", "patch"), Handler: a.toolOffersUpdate},
		{Name: "offer_packages_set", Description: "Replace an offer's package definitions. Packages contain slug,name,tier,scope,pricing_model,quantity,unit,delivery_days,revisions,customer_amount_minor,currency,sort_order. Existing package IDs are preserved by slug and removed packages are deactivated. Returns {offer}.", InputSchema: obj(map[string]any{"offer_id": integer, "packages": array}, "offer_id", "packages"), Handler: a.toolOfferPackagesSet},
		{Name: "offers_publish", Description: "Publish an offer and synchronize its sell-side service product/prices to Catalog. Returns {offer,catalog_sync}.", InputSchema: obj(map[string]any{"id": integer}, "id"), Handler: a.toolOffersPublish},
		{Name: "offers_recommend", Description: "Recommend a configured standard offer/package and explain customer price plus worker compensation. Args: offer_id?, offer_slug?, template_id?, q?, package_slug?, worker_id?, quantity?, currency?. Never invents missing rates. Returns {recommendation}.", InputSchema: obj(map[string]any{"offer_id": integer, "offer_slug": stringP, "template_id": integer, "q": stringP, "package_slug": stringP, "worker_id": integer, "quantity": number, "currency": stringP}), Handler: a.toolOffersRecommend},
		{Name: "gigs_create_from_offer", Description: "Create and optionally assign a gig from a published standard offer package. Resolves and snapshots worker compensation and customer price. Args: offer_id|offer_slug, package_id|package_slug, worker_id?, quantity?, vars?, notify_worker?, public_domain_id?, deadline_at?, priority?. Returns {gig,assignment?,recommendation}.", InputSchema: obj(map[string]any{"offer_id": integer, "offer_slug": stringP, "package_id": integer, "package_slug": stringP, "worker_id": integer, "quantity": number, "vars": object, "notify_worker": boolean, "public_domain_id": integer, "deadline_at": stringP, "priority": stringP}), Handler: a.toolGigsCreateFromOffer},

		{Name: "job_posts_create", Description: "Create a marketplace job post. Args: title, description?, template_id?, customer_contact_id?, scope?, pricing_models?, budget_min_minor?, budget_max_minor?, currency?, deadline_at?, visibility?, publish?. Returns {job_post}.", InputSchema: obj(map[string]any{"title": stringP, "description": stringP, "template_id": integer, "customer_contact_id": integer, "scope": object, "pricing_models": array, "budget_min_minor": integer, "budget_max_minor": integer, "currency": stringP, "deadline_at": stringP, "visibility": stringP, "publish": boolean}, "title"), Handler: a.toolJobPostsCreate},
		{Name: "job_posts_list", Description: "List marketplace job posts. Args: status?, limit?. Returns {job_posts}.", InputSchema: obj(map[string]any{"status": stringP, "limit": integer}), Handler: a.toolJobPostsList},
		{Name: "proposals_submit", Description: "Submit or replace a worker proposal on an open job post. Args: job_post_id, worker_id, pricing_model, amount_minor, currency, offer_package_id?, estimated_days?, message?, milestones?. Returns {proposal}.", InputSchema: obj(map[string]any{"job_post_id": integer, "worker_id": integer, "pricing_model": stringP, "amount_minor": integer, "currency": stringP, "offer_package_id": integer, "estimated_days": integer, "message": stringP, "milestones": array}, "job_post_id", "worker_id", "pricing_model", "amount_minor", "currency"), Handler: a.toolProposalsSubmit},
		{Name: "proposals_list", Description: "List proposals for a job post. Args: job_post_id, status?. Returns {proposals}.", InputSchema: obj(map[string]any{"job_post_id": integer, "status": stringP}, "job_post_id"), Handler: a.toolProposalsList},
		{Name: "proposals_accept", Description: "Accept a submitted proposal, reject competing proposals, award the job post, and create an active contract with snapshotted terms and milestones. Args: id. Returns {proposal,contract}.", InputSchema: obj(map[string]any{"id": integer}, "id"), Handler: a.toolProposalsAccept},

		{Name: "contracts_create", Description: "Create a direct, package, order, or subscription contract with immutable commercial terms. Args: source_type, title, pricing_model, currency, source_id?, customer_contact_id?, worker_id?, template_id?, offer_id?, offer_package_id?, customer_amount_minor?, worker_amount_minor?, quantity?, unit?, revision_limit?, order_id?, billing_invoice_id?, terms?, activate?. Returns {contract}.", InputSchema: obj(map[string]any{"source_type": stringP, "source_id": integer, "title": stringP, "pricing_model": stringP, "currency": stringP, "customer_contact_id": integer, "worker_id": integer, "template_id": integer, "offer_id": integer, "offer_package_id": integer, "customer_amount_minor": integer, "worker_amount_minor": integer, "quantity": number, "unit": stringP, "revision_limit": integer, "order_id": integer, "billing_invoice_id": integer, "terms": object, "activate": boolean}, "source_type", "title", "pricing_model", "currency"), Handler: a.toolContractsCreate},
		{Name: "contracts_get", Description: "Fetch a contract with milestones. Args: id. Returns {contract}.", InputSchema: obj(map[string]any{"id": integer}, "id"), Handler: a.toolContractsGet},
		{Name: "contracts_list", Description: "List contracts. Args: status?, worker_id?, limit?. Returns {contracts}.", InputSchema: obj(map[string]any{"status": stringP, "worker_id": integer, "limit": integer}), Handler: a.toolContractsList},
		{Name: "contracts_add_milestone", Description: "Append a priced deliverable milestone to a contract. Args: contract_id,title,description?,due_at?,customer_amount_minor?,worker_amount_minor?,currency?,sort_order?. Returns {contract,milestone}.", InputSchema: obj(map[string]any{"contract_id": integer, "title": stringP, "description": stringP, "due_at": stringP, "customer_amount_minor": integer, "worker_amount_minor": integer, "currency": stringP, "sort_order": integer}, "contract_id", "title"), Handler: a.toolContractsAddMilestone},
		{Name: "contracts_dispatch_milestone", Description: "Dispatch a pending contract milestone as a worker gig with the contract compensation snapshotted. The milestone then follows gig submission and review automatically. Args: contract_id,milestone_id,vars?,notify_worker?,public_domain_id?,deadline_at?,priority?. Returns {contract,milestone,gig,assignment?}.", InputSchema: obj(map[string]any{"contract_id": integer, "milestone_id": integer, "vars": object, "notify_worker": boolean, "public_domain_id": integer, "deadline_at": stringP, "priority": stringP}, "contract_id", "milestone_id"), Handler: a.toolContractsDispatchMilestone},
		{Name: "gigs_create_payable", Description: "Create or retry the Bills accounts-payable record for an approved gig's compensation snapshot. Idempotent once a bill is linked. Args: gig_id. Returns {compensation,bill?}.", InputSchema: obj(map[string]any{"gig_id": integer}, "gig_id"), Handler: a.toolGigsCreatePayable},
	}
}

func (a *App) toolPayGradesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	slug := slugify(strArg(args, "slug"))
	if slug == "" {
		slug = slugify(name)
	}
	model := strArg(args, "default_pricing_model")
	amount := int64Arg(args, "default_amount_minor")
	currency := strArg(args, "currency")
	if model != "" {
		if model, err = normalisePricingModel(model); err != nil {
			return nil, err
		}
	}
	if amount < 0 {
		return nil, errors.New("default_amount_minor must be >= 0")
	}
	if amount > 0 || currency != "" {
		if currency, err = normaliseCurrency(currency); err != nil {
			return nil, err
		}
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO pay_grades(project_id,slug,name,rank,description,default_pricing_model,default_amount_minor,currency)
        VALUES(?,?,?,?,?,?,?,?)`, pid, slug, name, intArg(args, "rank", 0), nullStr(strArg(args, "description")), nullStr(model), nullInt64(amount), nullStr(currency))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	g, err := getPayGrade(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("pay_grade.created", pid, map[string]any{"pay_grade_id": id})
	}
	return map[string]any{"pay_grade": g}, err
}

func (a *App) toolPayGradesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := listPayGrades(ctx.AppDB(), pid, boolArg(args, "include_inactive", false))
	return map[string]any{"pay_grades": items}, err
}

func (a *App) toolPayGradesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch := mapArg(args, "patch")
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	if g, _ := getPayGrade(ctx.AppDB(), pid, id); g == nil {
		return nil, errors.New("pay grade not found")
	}
	sets := []string{"updated_at=CURRENT_TIMESTAMP"}
	vals := []any{}
	allowed := map[string]bool{"name": true, "rank": true, "description": true, "default_pricing_model": true, "default_amount_minor": true, "currency": true, "active": true}
	for key, value := range patch {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported pay grade field %q", key)
		}
		switch key {
		case "default_pricing_model":
			if value == nil || strOf(value) == "" {
				value = nil
			} else {
				value, err = normalisePricingModel(strOf(value))
				if err != nil {
					return nil, err
				}
			}
		case "default_amount_minor":
			if int64Cast(value) < 0 {
				return nil, errors.New("default_amount_minor must be >= 0")
			}
		case "currency":
			if value == nil || strOf(value) == "" {
				value = nil
			} else {
				value, err = normaliseCurrency(strOf(value))
				if err != nil {
					return nil, err
				}
			}
		case "active":
			if b, ok := value.(bool); ok {
				if b {
					value = 1
				} else {
					value = 0
				}
			}
		}
		sets = append(sets, key+"=?")
		vals = append(vals, value)
	}
	vals = append(vals, pid, id)
	if _, err = ctx.AppDB().Exec(`UPDATE pay_grades SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	g, err := getPayGrade(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("pay_grade.updated", pid, map[string]any{"pay_grade_id": id})
	}
	return map[string]any{"pay_grade": g}, err
}

func (a *App) toolWorkersSetPayGrade(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wid, gid := int64Arg(args, "worker_id"), int64Arg(args, "pay_grade_id")
	if wid == 0 || gid == 0 {
		return nil, errors.New("worker_id and pay_grade_id required")
	}
	w, err := getWorker(ctx.AppDB(), pid, wid)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("worker not found")
	}
	g, err := getPayGrade(ctx.AppDB(), pid, gid)
	if err != nil {
		return nil, err
	}
	if g == nil || !g.Active {
		return nil, errors.New("active pay grade not found")
	}
	currency := strArg(args, "currency")
	if currency != "" {
		if currency, err = normaliseCurrency(currency); err != nil {
			return nil, err
		}
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO worker_pay_profiles(worker_id,project_id,pay_grade_id,currency,metadata_json)
      VALUES(?,?,?,?,?) ON CONFLICT(worker_id) DO UPDATE SET pay_grade_id=excluded.pay_grade_id,currency=excluded.currency,
      metadata_json=excluded.metadata_json,updated_at=CURRENT_TIMESTAMP`, wid, pid, gid, nullStr(currency), nullStr(mustJSON(mapArg(args, "metadata"))))
	if err != nil {
		return nil, err
	}
	profile, err := getWorkerPayProfile(ctx.AppDB(), pid, wid)
	if err == nil {
		w, _ = getWorker(ctx.AppDB(), pid, wid)
		ctx.EmitWithProject("worker.pay_grade_updated", pid, map[string]any{"worker_id": wid, "pay_grade_id": gid})
	}
	return map[string]any{"worker": w, "pay_profile": profile}, err
}

func (a *App) toolRatesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	model, err := normalisePricingModel(strArg(args, "pricing_model"))
	if err != nil {
		return nil, err
	}
	amount := int64Arg(args, "amount_minor")
	if amount < 0 {
		return nil, errors.New("amount_minor must be >= 0")
	}
	currency, err := normaliseCurrency(strArg(args, "currency"))
	if err != nil {
		return nil, err
	}
	wid, gid, tid, pkgID := int64Arg(args, "worker_id"), int64Arg(args, "pay_grade_id"), int64Arg(args, "template_id"), int64Arg(args, "offer_package_id")
	if wid == 0 && gid == 0 {
		return nil, errors.New("worker_id or pay_grade_id required")
	}
	if wid > 0 && gid > 0 {
		return nil, errors.New("scope by worker_id or pay_grade_id, not both")
	}
	if tid > 0 && pkgID > 0 {
		return nil, errors.New("scope by template_id or offer_package_id, not both")
	}
	minQ, maxQ := floatArg(args, "minimum_quantity", 0), floatArg(args, "maximum_quantity", 0)
	if minQ < 0 || maxQ < 0 || (maxQ > 0 && minQ > maxQ) {
		return nil, errors.New("invalid quantity range")
	}
	from, until := strArg(args, "effective_from"), strArg(args, "effective_until")
	if err = validDateTimeOrEmpty(from); err != nil {
		return nil, err
	}
	if err = validDateTimeOrEmpty(until); err != nil {
		return nil, err
	}
	if from == "" {
		from = "CURRENT_TIMESTAMP"
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE rate_cards SET status='archived',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND status='active'
      AND COALESCE(worker_id,0)=? AND COALESCE(pay_grade_id,0)=? AND COALESCE(template_id,0)=? AND COALESCE(offer_package_id,0)=?
      AND pricing_model=? AND currency=? AND COALESCE(unit,'')=?`, pid, wid, gid, tid, pkgID, model, currency, strArg(args, "unit"))
	if err != nil {
		return nil, err
	}
	var res sql.Result
	if from == "CURRENT_TIMESTAMP" {
		res, err = tx.Exec(`INSERT INTO rate_cards(project_id,template_id,offer_package_id,pay_grade_id,worker_id,pricing_model,amount_minor,currency,unit,minimum_quantity,maximum_quantity,effective_until,notes)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, nullInt64(tid), nullInt64(pkgID), nullInt64(gid), nullInt64(wid), model, amount, currency, nullStr(strArg(args, "unit")), nullableFloat(minQ), nullableFloat(maxQ), nullStr(until), nullStr(strArg(args, "notes")))
	} else {
		res, err = tx.Exec(`INSERT INTO rate_cards(project_id,template_id,offer_package_id,pay_grade_id,worker_id,pricing_model,amount_minor,currency,unit,minimum_quantity,maximum_quantity,effective_from,effective_until,notes)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, nullInt64(tid), nullInt64(pkgID), nullInt64(gid), nullInt64(wid), model, amount, currency, nullStr(strArg(args, "unit")), nullableFloat(minQ), nullableFloat(maxQ), from, nullStr(until), nullStr(strArg(args, "notes")))
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	rates, err := listRateCards(ctx.AppDB(), pid, tid, pkgID, gid, wid, true)
	if err != nil {
		return nil, err
	}
	var created *rateCard
	for _, r := range rates {
		if r.ID == id {
			created = r
			break
		}
	}
	ctx.EmitWithProject("rate.created", pid, map[string]any{"rate_id": id})
	return map[string]any{"rate": created}, nil
}

func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (a *App) toolRatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := listRateCards(ctx.AppDB(), pid, int64Arg(args, "template_id"), int64Arg(args, "offer_package_id"), int64Arg(args, "pay_grade_id"), int64Arg(args, "worker_id"), boolArg(args, "include_archived", false))
	return map[string]any{"rates": items}, err
}

func (a *App) toolRatesResolve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wid := int64Arg(args, "worker_id")
	if wid == 0 {
		return nil, errors.New("worker_id required")
	}
	q, err := resolveRate(ctx.AppDB(), pid, int64Arg(args, "template_id"), int64Arg(args, "offer_package_id"), wid, floatArg(args, "quantity", 1), strArg(args, "currency"))
	return map[string]any{"quote": q}, err
}

func (a *App) toolOffersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tid := int64Arg(args, "template_id")
	name := strings.TrimSpace(strArg(args, "name"))
	if tid == 0 || name == "" {
		return nil, errors.New("template_id and name required")
	}
	t, err := getTemplate(ctx.AppDB(), pid, tid)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errors.New("template not found")
	}
	if activeVersionID, e := resolveActiveTemplateVersion(ctx.AppDB(), tid); e != nil {
		return nil, e
	} else if activeVersionID == 0 {
		return nil, errors.New("template must have an active published version")
	}
	slug := slugify(strArg(args, "slug"))
	if slug == "" {
		slug = slugify(name)
	}
	visibility := strArg(args, "visibility")
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "unlisted" && visibility != "public" {
		return nil, errors.New("visibility must be private, unlisted, or public")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO standard_offers(project_id,template_id,slug,name,description,category,visibility) VALUES(?,?,?,?,?,?,?)`, pid, tid, slug, name, nullStr(strArg(args, "description")), nullStr(strArg(args, "category")), visibility)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	o, err := loadOffer(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("offer.created", pid, map[string]any{"offer_id": id})
	}
	return map[string]any{"offer": o}, err
}

func (a *App) toolOffersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := listOffers(ctx.AppDB(), pid, strArg(args, "status"), strArg(args, "q"), intArg(args, "limit", 50))
	return map[string]any{"offers": items}, err
}

func (a *App) toolOffersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var o *standardOffer
	if id := int64Arg(args, "id"); id > 0 {
		o, err = loadOffer(ctx.AppDB(), pid, id)
	} else if slug := strArg(args, "slug"); slug != "" {
		o, err = loadOfferBySlug(ctx.AppDB(), pid, slug)
	} else {
		return nil, errors.New("id or slug required")
	}
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("offer not found")
	}
	return map[string]any{"offer": o}, nil
}

func (a *App) toolOffersUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch := mapArg(args, "patch")
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	o, err := loadOffer(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("offer not found")
	}
	if o.Status == "archived" {
		return nil, errors.New("archived offer cannot be updated")
	}
	allowed := map[string]bool{"name": true, "description": true, "category": true, "visibility": true}
	sets := []string{"version=version+1", "status=CASE WHEN status='active' THEN 'draft' ELSE status END", "updated_at=CURRENT_TIMESTAMP"}
	vals := []any{}
	for k, v := range patch {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported offer field %q", k)
		}
		if k == "visibility" {
			s := strOf(v)
			if s != "private" && s != "unlisted" && s != "public" {
				return nil, errors.New("visibility must be private, unlisted, or public")
			}
		}
		sets = append(sets, k+"=?")
		vals = append(vals, v)
	}
	vals = append(vals, pid, id)
	_, err = ctx.AppDB().Exec(`UPDATE standard_offers SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, vals...)
	if err != nil {
		return nil, err
	}
	o, err = loadOffer(ctx.AppDB(), pid, id)
	return map[string]any{"offer": o}, err
}

func (a *App) toolOfferPackagesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	offerID := int64Arg(args, "offer_id")
	raw := sliceArg(args, "packages")
	if offerID == 0 {
		return nil, errors.New("offer_id required")
	}
	o, err := loadOffer(ctx.AppDB(), pid, offerID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("offer not found")
	}
	if len(raw) == 0 {
		return nil, errors.New("packages must be non-empty")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("packages[%d] must be an object", i)
		}
		name := strings.TrimSpace(strOf(m["name"]))
		slug := slugify(strOf(m["slug"]))
		if slug == "" {
			slug = slugify(name)
		}
		if name == "" || slug == "" {
			return nil, fmt.Errorf("packages[%d] name required", i)
		}
		if seen[slug] {
			return nil, fmt.Errorf("duplicate package slug %q", slug)
		}
		seen[slug] = true
		model, err := normalisePricingModel(strOf(m["pricing_model"]))
		if err != nil {
			return nil, fmt.Errorf("packages[%d]: %w", i, err)
		}
		amount := int64Cast(m["customer_amount_minor"])
		if amount < 0 {
			return nil, fmt.Errorf("packages[%d].customer_amount_minor must be >= 0", i)
		}
		currency := strOf(m["currency"])
		if amount > 0 || currency != "" {
			currency, err = normaliseCurrency(currency)
			if err != nil {
				return nil, fmt.Errorf("packages[%d]: %w", i, err)
			}
		}
		qty := floatArg(m, "quantity", 0)
		if qty < 0 {
			return nil, fmt.Errorf("packages[%d].quantity must be > 0", i)
		}
		_, err = tx.Exec(`INSERT INTO offer_packages(project_id,offer_id,slug,name,tier,description,scope_json,pricing_model,quantity,unit,delivery_days,revisions,customer_amount_minor,currency,active,sort_order)
          VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?) ON CONFLICT(offer_id,slug) DO UPDATE SET name=excluded.name,tier=excluded.tier,description=excluded.description,scope_json=excluded.scope_json,
          pricing_model=excluded.pricing_model,quantity=excluded.quantity,unit=excluded.unit,delivery_days=excluded.delivery_days,revisions=excluded.revisions,
          customer_amount_minor=excluded.customer_amount_minor,currency=excluded.currency,active=1,sort_order=excluded.sort_order,updated_at=CURRENT_TIMESTAMP`, pid, offerID, slug, name, nullStr(strOf(m["tier"])), nullStr(strOf(m["description"])), nullStr(mustJSON(m["scope"])), model, nullableFloat(qty), nullStr(strOf(m["unit"])), nullInt64(int64Cast(m["delivery_days"])), nullInt64(int64Cast(m["revisions"])), nullInt64(amount), nullStr(currency), intArg(m, "sort_order", i))
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(`SELECT id,slug FROM offer_packages WHERE project_id=? AND offer_id=?`, pid, offerID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var slug string
		if err = rows.Scan(&id, &slug); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !seen[slug] {
			if _, err = tx.Exec(`UPDATE offer_packages SET active=0,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
	}
	_ = rows.Close()
	_, err = tx.Exec(`UPDATE standard_offers SET version=version+1,status=CASE WHEN status='active' THEN 'draft' ELSE status END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, offerID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	o, err = loadOffer(ctx.AppDB(), pid, offerID)
	return map[string]any{"offer": o}, err
}

func (a *App) toolOffersPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	o, err := loadOffer(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("offer not found")
	}
	active := 0
	for _, p := range o.Packages {
		if p.Active {
			active++
		}
	}
	if active == 0 {
		return nil, errors.New("offer needs at least one active package")
	}
	syncResult, err := syncOfferToCatalog(ctx, pid, o)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE standard_offers SET status='active',published_at=COALESCE(published_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return nil, err
	}
	o, err = loadOffer(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("offer.published", pid, map[string]any{"offer_id": id})
	}
	return map[string]any{"offer": o, "catalog_sync": syncResult}, err
}

func selectRecommendedPackage(o *standardOffer, slug string, quantity float64) (*offerPackage, error) {
	var candidates []*offerPackage
	for _, p := range o.Packages {
		if !p.Active {
			continue
		}
		if slug != "" && p.Slug == slug {
			return p, nil
		}
		candidates = append(candidates, p)
	}
	if slug != "" {
		return nil, errors.New("active package not found")
	}
	if len(candidates) == 0 {
		return nil, errors.New("offer has no active package")
	}
	if quantity > 0 {
		for _, p := range candidates {
			if p.Quantity >= quantity {
				return p, nil
			}
		}
	}
	return candidates[0], nil
}

func (a *App) recommendOffer(ctx *sdk.AppCtx, pid string, args map[string]any) (map[string]any, error) {
	var o *standardOffer
	var err error
	if id := int64Arg(args, "offer_id"); id > 0 {
		o, err = loadOffer(ctx.AppDB(), pid, id)
	} else if slug := strArg(args, "offer_slug"); slug != "" {
		o, err = loadOfferBySlug(ctx.AppDB(), pid, slug)
	} else {
		items, e := listOffers(ctx.AppDB(), pid, "active", strArg(args, "q"), 20)
		if e != nil {
			return nil, e
		}
		tid := int64Arg(args, "template_id")
		for _, candidate := range items {
			if tid == 0 || candidate.TemplateID == tid {
				o = candidate
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if o == nil || o.Status != "active" {
		return nil, errors.New("matching active offer not found")
	}
	qty := floatArg(args, "quantity", 0)
	var p *offerPackage
	if packageID := int64Arg(args, "package_id"); packageID > 0 {
		for _, candidate := range o.Packages {
			if candidate.ID == packageID && candidate.Active {
				p = candidate
				break
			}
		}
		if p == nil {
			return nil, errors.New("active package not found")
		}
	} else {
		p, err = selectRecommendedPackage(o, strArg(args, "package_slug"), qty)
	}
	if err != nil {
		return nil, err
	}
	if qty <= 0 {
		qty = p.Quantity
	}
	if qty <= 0 {
		qty = 1
	}
	currency := strArg(args, "currency")
	if currency == "" {
		currency = p.Currency
	}
	quote, err := resolveRate(ctx.AppDB(), pid, o.TemplateID, p.ID, int64Arg(args, "worker_id"), qty, currency)
	if err != nil {
		return nil, err
	}
	quote.CustomerAmountMinor = calculateRateTotal(p.PricingModel, p.CustomerAmountMinor, func() float64 {
		if p.Quantity > 0 {
			return qty / p.Quantity
		}
		return qty
	}())
	margin := int64(0)
	if quote.Configured && quote.Currency == p.Currency {
		margin = quote.CustomerAmountMinor - quote.WorkerAmountMinor
	}
	return map[string]any{"offer": o, "package": p, "worker_compensation": quote, "customer_price": map[string]any{"pricing_model": p.PricingModel, "amount_minor": quote.CustomerAmountMinor, "currency": p.Currency}, "estimated_margin_minor": margin, "currency": p.Currency}, nil
}

func (a *App) toolOffersRecommend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rec, err := a.recommendOffer(ctx, pid, args)
	return map[string]any{"recommendation": rec}, err
}

func (a *App) toolGigsCreateFromOffer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rec, err := a.recommendOffer(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	o := rec["offer"].(*standardOffer)
	p := rec["package"].(*offerPackage)
	quote := rec["worker_compensation"].(*rateQuote)
	if int64Arg(args, "worker_id") > 0 && !quote.Configured {
		return nil, errors.New("worker compensation is not configured: " + strings.Join(quote.Explanation, "; "))
	}
	createArgs := map[string]any{"_project_id": pid, "template_id": o.TemplateID, "vars": mapArg(args, "vars"), "worker_id": int64Arg(args, "worker_id"), "notify_worker": boolArg(args, "notify_worker", false), "public_domain_id": int64Arg(args, "public_domain_id"), "deadline_at": strArg(args, "deadline_at"), "priority": strArg(args, "priority"), "_compensation_quote": quote, "_offer_id": o.ID, "_offer_package_id": p.ID}
	out, err := a.toolGigsCreateFromTemplate(ctx, createArgs)
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	m["recommendation"] = rec
	return m, nil
}
