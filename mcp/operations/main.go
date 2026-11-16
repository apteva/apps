package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: operations
display_name: Operations
version: 0.1.0
description: Generic operations core for any physical business — work, scheduling, resources, pricing, forms, recurring work.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/operations
icon: /ui/icon.svg
icon_style: monochrome
tags: [operations, scheduling, field-service, appointments, reservations, work-orders]
scopes: [project]
requires:
  permissions: [db.write.app, platform.apps.call]
  integrations:
    - role: crm
      kind: app
      compatible_app_names: [crm]
      required: false
      label: "CRM"
    - role: billing
      kind: app
      compatible_app_names: [billing]
      required: false
      label: "Billing"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: packs_list,          description: "List built-in and installed business packs." }
    - { name: packs_install,       description: "Install a built-in or custom business pack." }
    - { name: packs_validate,      description: "Validate a custom pack without installing it." }
    - { name: config_get,          description: "Read work types, forms, rate items, modifiers and policies." }
    - { name: config_upsert,       description: "Create or update one work type, form, rate item or modifier." }
    - { name: policies_set,        description: "Set an owner policy." }
    - { name: sites_upsert,        description: "Create or update a site (own premises or customer location)." }
    - { name: sites_list,          description: "List sites." }
    - { name: resources_upsert,    description: "Create or update a resource (person, crew, space, equipment, vehicle)." }
    - { name: resources_list,      description: "List resources." }
    - { name: resources_set_blocks, description: "Add shifts, time off or blocked periods to a resource." }
    - { name: resources_blocks,    description: "List a resource's shifts and blocks." }
    - { name: objects_upsert,      description: "Create or update a service object at a site." }
    - { name: objects_list,        description: "List service objects." }
    - { name: work_intake,         description: "Create a work item from a request." }
    - { name: work_get,            description: "Read a work item with lines, slots and forms." }
    - { name: work_search,         description: "Search work items." }
    - { name: work_transition,     description: "Change a work item's status through the lifecycle guards." }
    - { name: work_timeline,       description: "Read a work item's event timeline." }
    - { name: work_note,           description: "Add a note to a work item's timeline." }
    - { name: price_preview,       description: "Price rate items without saving." }
    - { name: quote_build,         description: "Price a work item and store its estimate." }
    - { name: quote_decide,        description: "Approve, send, accept or decline a quote." }
    - { name: schedule_suggest,    description: "Suggest slots with resources for a work item." }
    - { name: schedule_book,       description: "Book a slot for a work item." }
    - { name: slots_update,        description: "Reschedule, cancel, mark missed, start, set ETA or note a slot." }
    - { name: slots_list,          description: "Agenda of slots by time, resource or site." }
    - { name: work_complete,       description: "Complete a work item's visit and record actual lines." }
    - { name: forms_submit,        description: "Submit a form (checklist, inspection, compliance log)." }
    - { name: forms_responses,     description: "List form responses." }
    - { name: work_invoice,        description: "Invoice a completed work item through Billing." }
    - { name: work_mark_paid,      description: "Record payment and close a work item." }
    - { name: agreements_create,   description: "Create recurring work." }
    - { name: agreements_list,     description: "List recurring work agreements." }
    - { name: agreements_update,   description: "Pause, resume or end an agreement." }
    - { name: agreements_generate, description: "Generate (and book) recurring work due up to a date." }
    - { name: metrics,             description: "Operations metrics for a period." }
  publishes:
    - { name: work.created,        description: "A work item was created." }
    - { name: work.status_changed, description: "A work item changed status within its phase." }
    - { name: work.phase_changed,  description: "A work item entered a new phase." }
    - { name: work.needs_owner,    description: "A work item waits on an owner decision." }
    - { name: work.closed,         description: "A work item was closed." }
    - { name: quote.accepted,      description: "A quote was accepted." }
    - { name: slot.booked,         description: "A slot was booked." }
    - { name: slot.updated,        description: "A slot was rescheduled, cancelled, missed or started." }
    - { name: form.submitted,      description: "A form was submitted." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/operations }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/operations.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("operations requires a db block")
	}
	if _, err := builtinPacks(); err != nil {
		return err
	}
	globalCtx = ctx
	ctx.Logger().Info("operations mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/call/", Handler: a.handleCall}}
}

func callerFor(ctx *sdk.AppCtx) appCaller {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	return func(app, tool string, input map[string]any, out any) error {
		return ctx.WithProject(ctx.CurrentProject()).PlatformAPI().CallAppResult(app, tool, input, out)
	}
}

func emit(ctx *sdk.AppCtx, topic string, data map[string]any) {
	if ctx == nil || topic == "" {
		return
	}
	ctx.EmitWithProject(topic, ctx.CurrentProject(), data)
}

func workEvent(w *Work) map[string]any {
	if w == nil {
		return nil
	}
	return map[string]any{"work_id": w.ID, "number": w.Number, "work_type": w.WorkType, "mode": w.Mode, "phase": w.Phase,
		"status": w.Status, "priority": w.Priority, "customer_name": w.CustomerName, "site_id": w.SiteID, "summary": w.Summary}
}

// ─── tools ──────────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	customer := sObject("{name, email, phone, crm_contact_id}")
	items := sArray("Rate items: [\"CODE\"] or [{code, qty}]. qty defaults to the item's qty_from answer, else 1.")
	return []sdk.Tool{
		{Name: "packs_list", Description: "List business packs: built-in ones (plumber, salon, restaurant, …) and what is installed in this project.",
			InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolPacksList},
		{Name: "packs_install", Description: "Install a business pack: work types, forms, rate items, modifiers and default policies. Give key for a built-in pack or pack for a custom one (same JSON shape as packs_list returns). Re-installing updates in place; owner policies are kept.",
			InputSchema: schemaObject(map[string]any{"key": sString(), "pack": sObject("custom pack JSON")}, nil), Handler: a.toolPacksInstall},
		{Name: "packs_validate", Description: "Validate a custom pack (the Setup agent drafts one by interviewing the owner). Returns errors without installing.",
			InputSchema: schemaObject(map[string]any{"pack": sObject("pack JSON")}, []string{"pack"}), Handler: a.toolPacksValidate},
		{Name: "config_get", Description: "Read this business's configuration: work types (phases, statuses, requirements, playbook), forms, rate items, modifiers, policies. Use section to read one part; q filters rate items.",
			InputSchema: schemaObject(map[string]any{"section": sStringDesc("work_types | forms | rate_items | modifiers | policies"), "q": sString(), "work_type": sString()}, nil), Handler: a.toolConfigGet},
		{Name: "config_upsert", Description: "Create or update one configuration record by key/code. kind: work_type | form | rate_item | modifier; value: the record JSON as config_get returns it.",
			InputSchema: schemaObject(map[string]any{"kind": sString(), "value": sObject("record")}, []string{"kind", "value"}), Handler: a.toolConfigUpsert},
		{Name: "policies_set", Description: "Set an owner policy. Common keys: currency (\"EUR\"), timezone, auto_quote_max_cents, max_discount_pct, min_margin_pct. Owner-only: agents should ask before changing.",
			InputSchema: schemaObject(map[string]any{"key": sString(), "value": map[string]any{}}, []string{"key", "value"}), Handler: a.toolPoliciesSet},

		{Name: "sites_upsert", Description: "Create or update a site. kind own = the business's premises (with opening hours and timezone); customer = where field work happens. hours: {\"mon\":[[\"09:00\",\"18:00\"]], …}. Args: id?, kind, name, address, lat, lng, timezone, hours, access, custom, crm_contact_id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "kind": sString(), "name": sString(), "address": sString(), "lat": sNumber(), "lng": sNumber(),
				"timezone": sString(), "hours": sObject("opening hours"), "access": sObject("access notes, codes"), "custom": sObject(""), "crm_contact_id": sInteger()}, nil),
			Handler: a.toolSitesUpsert},
		{Name: "sites_list", Description: "List sites. Args: kind?, q?.", InputSchema: schemaObject(map[string]any{"kind": sString(), "q": sString()}, nil), Handler: a.toolSitesList},
		{Name: "resources_upsert", Description: "Create or update a resource. kind: person | crew | space (chair, room, table, bay) | equipment (oven, lift) | vehicle. quals = qualifications or skills (e.g. gas_safe, colour). zone groups spaces (dining, terrace). capacity = seats or crew size. People only work inside their shifts (needs_shift) unless set false.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "kind": sString(), "name": sString(), "site_id": sInteger(), "zone": sString(), "capacity": sInteger(),
				"quals": sArray("strings"), "crm_contact_id": sInteger(), "gigs_worker_id": sInteger(), "home_lat": sNumber(), "home_lng": sNumber(),
				"cost_rate_cents": sIntDesc("cost per hour"), "needs_shift": sBool(), "active": sBool(), "custom": sObject("")}, nil),
			Handler: a.toolResourcesUpsert},
		{Name: "resources_list", Description: "List resources. Args: kind?, site_id?, include_inactive?.",
			InputSchema: schemaObject(map[string]any{"kind": sString(), "site_id": sInteger(), "include_inactive": sBool()}, nil), Handler: a.toolResourcesList},
		{Name: "resources_set_blocks", Description: "Add shifts / time off / blocked periods to a resource. blocks: [{kind: shift|off|blocked, starts_at, ends_at, note}]. replace_from + replace_to first delete blocks starting in that window (use to rewrite a roster week).",
			InputSchema: schemaObject(map[string]any{"resource_id": sInteger(), "blocks": sArray("blocks"), "replace_from": sString(), "replace_to": sString()}, []string{"resource_id", "blocks"}),
			Handler:     a.toolResourcesSetBlocks},
		{Name: "resources_blocks", Description: "List a resource's shifts and blocks between from and to.",
			InputSchema: schemaObject(map[string]any{"resource_id": sInteger(), "from": sString(), "to": sString()}, []string{"resource_id"}), Handler: a.toolResourcesBlocks},
		{Name: "objects_upsert", Description: "Create or update a service object at a site: a boiler, a pool, an oven, a vehicle under service. Args: id?, site_id, kind, name, attributes (brand, model, serial…), installed_on, warranty_until.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "site_id": sInteger(), "kind": sString(), "name": sString(), "attributes": sObject(""),
				"installed_on": sString(), "warranty_until": sString(), "last_serviced_at": sString()}, nil), Handler: a.toolObjectsUpsert},
		{Name: "objects_list", Description: "List service objects. Args: site_id?, kind?.", InputSchema: schemaObject(map[string]any{"site_id": sInteger(), "kind": sString()}, nil), Handler: a.toolObjectsList},

		{Name: "work_intake", Description: "Create a work item from a customer request or an internal need. work_type = a configured type key (config_get). answers = intake form answers (missing required ones are returned so you can keep asking). For field work give address (creates a customer site) or site_id. party_size for reservations/groups. items prices it immediately. Priority comes from the type's rules unless given. Returns the work, its price if items were given, and the type's playbook.",
			InputSchema: schemaObject(map[string]any{"work_type": sString(), "customer": customer, "summary": sString(), "answers": sObject("intake answers"),
				"address": sString(), "lat": sNumber(), "lng": sNumber(), "site_id": sInteger(), "object_ids": sArray("ids"), "party_size": sInteger(),
				"priority": sStringDesc("p1 (urgent) … p4"), "source": sStringDesc("call, sms, web, walk_in, agreement…"), "items": items, "currency": sString(), "custom": sObject("")},
				[]string{"work_type"}), Handler: a.toolWorkIntake},
		{Name: "work_get", Description: "Read a work item with estimate and actual lines, slots, form responses and the statuses it can move to. Args: work_id or number.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString()}, nil), Handler: a.toolWorkGet},
		{Name: "work_search", Description: "Search work. Filters: phase (or 'open'), status, work_type, mode, priority, site_id, crm_contact_id, q (summary, customer, number), sla_breached (no slot and past SLA). Urgent first.",
			InputSchema: schemaObject(map[string]any{"phase": sString(), "status": sString(), "work_type": sString(), "mode": sString(), "priority": sString(),
				"site_id": sInteger(), "crm_contact_id": sInteger(), "q": sString(), "sla_breached": sBool(), "limit": sInteger()}, nil), Handler: a.toolWorkSearch},
		{Name: "work_transition", Description: "Move a work item to a status (or a phase name). Guards are enforced (quote accepted before scheduling, a slot before execute, required forms, invoice before close). Special: on_hold, needs_owner (escalate to the owner), resume, cancelled. Returns the reasons when blocked.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "to": sString(), "reason": sString(), "actor": sString()}, []string{"to"}), Handler: a.toolWorkTransition},
		{Name: "work_timeline", Description: "Read a work item's append-only timeline (status changes, quotes, bookings, forms, notes).",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString()}, nil), Handler: a.toolWorkTimeline},
		{Name: "work_note", Description: "Add a note to a work item's timeline. Args: work_id, text, actor.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "text": sString(), "actor": sString()}, []string{"text"}), Handler: a.toolWorkNote},

		{Name: "price_preview", Description: "Price rate items without saving: lines, total, margin, duration, the resources needed and policy flags. Args: items, answers, work_type?, priority?, party_size?, starts_at?, discount_pct?, flags?.",
			InputSchema: schemaObject(map[string]any{"items": items, "answers": sObject(""), "work_type": sString(), "priority": sString(), "party_size": sInteger(),
				"starts_at": sString(), "discount_pct": sNumber(), "flags": sArray("strings")}, []string{"items"}), Handler: a.toolPricePreview},
		{Name: "quote_build", Description: "Price a work item from rate items and store the estimate. Prices only come from the rate book: you choose items and quantities, never amounts. If a policy is hit (above auto-approve, low margin, big discount, quote-only item) the quote is needs_owner and the work waits for the owner.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "items": items, "answers": sObject("extra answers"), "discount_pct": sNumber(),
				"flags": sArray("strings"), "actor": sString()}, []string{"items"}), Handler: a.toolQuoteBuild},
		{Name: "quote_decide", Description: "decision: approve (owner clears a policy flag) | send (customer was sent the quote) | accept (customer said yes; moves to scheduling) | decline.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "decision": sString(), "reason": sString(), "actor": sString()}, []string{"decision"}), Handler: a.toolQuoteDecide},

		{Name: "schedule_suggest", Description: "Suggest bookable slots: every requirement (people with the right skills and shifts, chairs, tables with enough seats, equipment, vehicles) free at the same time, inside opening hours. Ranked by wait, travel, seat waste, preferred resources and SLA. Args: work_id, from?, to?, step_min?, limit?, preferred_resource_ids?.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "from": sString(), "to": sString(), "step_min": sInteger(), "limit": sInteger(),
				"preferred_resource_ids": sArray("ids"), "duration_min": sInteger()}, nil), Handler: a.toolScheduleSuggest},
		{Name: "schedule_book", Description: "Book a work item at starts_at. Give resource_ids to choose them (e.g. the stylist the customer asked for), otherwise the engine picks. Moves the work into execute/scheduled.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "starts_at": sString(), "resource_ids": sArray("ids"),
				"preferred_resource_ids": sArray("ids"), "duration_min": sInteger(), "notes": sString(), "actor": sString()}, []string{"starts_at"}), Handler: a.toolScheduleBook},
		{Name: "slots_update", Description: "action: reschedule (starts_at, resource_ids?) | cancel | missed (no-show) | start | eta (eta) | note (notes). Cancelling the last slot sends the work back to scheduling.",
			InputSchema: schemaObject(map[string]any{"slot_id": sInteger(), "action": sString(), "starts_at": sString(), "resource_ids": sArray("ids"), "eta": sString(),
				"notes": sString(), "actor": sString()}, []string{"slot_id", "action"}), Handler: a.toolSlotsUpdate},
		{Name: "slots_list", Description: "Agenda: slots between from and to, optionally for one resource or site. Use for a technician's day, a salon's book or tonight's reservations.",
			InputSchema: schemaObject(map[string]any{"from": sString(), "to": sString(), "resource_id": sInteger(), "site_id": sInteger(), "include_cancelled": sBool()}, nil), Handler: a.toolSlotsList},
		{Name: "work_complete", Description: "Complete the work: closes open slots, records actual lines (the estimate unless items are given), stamps serviced objects, and moves to bill or closed. Required forms must be submitted first.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "items": items, "answers": sObject(""), "notes": sString(), "actor": sString()}, nil), Handler: a.toolWorkComplete},
		{Name: "forms_submit", Description: "Submit a form: a checklist or sign-off for a work item (work_id or slot_id) or a site log such as a temperature check (site_id). Validated against the form schema.",
			InputSchema: schemaObject(map[string]any{"form_key": sString(), "data": sObject("field values"), "work_id": sInteger(), "slot_id": sInteger(), "site_id": sInteger(),
				"object_id": sInteger(), "responder": sString()}, []string{"form_key", "data"}), Handler: a.toolFormsSubmit},
		{Name: "forms_responses", Description: "List form responses by work_id, site_id, object_id, form_key, since.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "site_id": sInteger(), "object_id": sInteger(), "form_key": sString(), "since": sString(), "limit": sInteger()}, nil),
			Handler:     a.toolFormsResponses},
		{Name: "work_invoice", Description: "Invoice a work item in the bill phase through the Billing app (needs the customer's email), or record an invoice made elsewhere with external_ref. Idempotent.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "finalize": sBool(), "external_ref": sString(), "notes": sString(), "actor": sString()}, nil), Handler: a.toolWorkInvoice},
		{Name: "work_mark_paid", Description: "Record that a work item is paid and close it.",
			InputSchema: schemaObject(map[string]any{"work_id": sInteger(), "number": sString(), "actor": sString()}, nil), Handler: a.toolWorkMarkPaid},

		{Name: "agreements_create", Description: "Create recurring work: a weekly clean, an annual service, a daily prep run, an opening checklist. recurrence: {every, unit: day|week|month, weekdays: [1..7], time: \"HH:MM\"}. auto_book books each occurrence, preferring preferred_resource_ids.",
			InputSchema: schemaObject(map[string]any{"name": sString(), "work_type": sString(), "customer": customer, "site_id": sInteger(), "recurrence": sObject("recurrence"),
				"items": items, "answers": sObject(""), "preferred_resource_ids": sArray("ids"), "auto_book": sBool(), "starts_on": sString(), "ends_on": sString()},
				[]string{"work_type", "recurrence"}), Handler: a.toolAgreementsCreate},
		{Name: "agreements_list", Description: "List agreements. Args: status?.", InputSchema: schemaObject(map[string]any{"status": sString()}, nil), Handler: a.toolAgreementsList},
		{Name: "agreements_update", Description: "Change an agreement's status (active | paused | ended), ends_on or preferred_resource_ids.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "status": sString(), "ends_on": sString(), "preferred_resource_ids": sArray("ids")}, []string{"id"}), Handler: a.toolAgreementsUpdate},
		{Name: "agreements_generate", Description: "Create the work for every occurrence due up to `until` (YYYY-MM-DD) and book it when auto_book. Safe to re-run; run daily.",
			InputSchema: schemaObject(map[string]any{"until": sString(), "agreement_id": sInteger()}, []string{"until"}), Handler: a.toolAgreementsGenerate},
		{Name: "metrics", Description: "Operations metrics between from and to (default last 30 days): open work by phase, created/closed, waiting on owner, quote acceptance, invoiced and margin, median minutes to first slot, utilisation per person.",
			InputSchema: schemaObject(map[string]any{"from": sString(), "to": sString()}, nil), Handler: a.toolMetrics},
	}
}

// ─── handlers ───────────────────────────────────────────────────────

func (a *App) toolPacksList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	packs, err := builtinPacks()
	if err != nil {
		return nil, err
	}
	installed, err := listInstalledPacks(ctx.AppDB(), ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(packs))
	for k := range packs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	builtin := []map[string]any{}
	for _, k := range keys {
		p := packs[k]
		wts := []string{}
		for _, wt := range p.WorkTypes {
			wts = append(wts, wt.Key)
		}
		builtin = append(builtin, map[string]any{"key": p.Key, "name": p.Name, "version": p.Version, "description": p.Description, "work_types": wts, "setup": p.Setup})
	}
	return map[string]any{"builtin": builtin, "installed": installed}, nil
}

func (a *App) toolPacksInstall(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var p Pack
	if raw := objectArg(args, "pack"); raw != nil {
		if err := remarshal(raw, &p); err != nil {
			return nil, err
		}
	} else {
		packs, err := builtinPacks()
		if err != nil {
			return nil, err
		}
		var ok bool
		if p, ok = packs[stringArg(args, "key")]; !ok {
			return nil, errors.New("unknown pack key; see packs_list")
		}
	}
	counts, err := installPack(ctx.AppDB(), ctx.CurrentProject(), p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"installed": p.Key, "counts": counts, "setup": p.Setup}, nil
}

func (a *App) toolPacksValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var p Pack
	if err := remarshal(objectArg(args, "pack"), &p); err != nil {
		return nil, err
	}
	errs := validatePack(ctx.AppDB(), ctx.CurrentProject(), &p)
	return map[string]any{"valid": len(errs) == 0, "errors": errs}, nil
}

func (a *App) toolConfigGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	section := stringArg(args, "section")
	out := map[string]any{}
	want := func(s string) bool { return section == "" || section == s }
	if want("work_types") {
		wts, err := listWorkTypes(db, pid)
		if err != nil {
			return nil, err
		}
		if k := stringArg(args, "work_type"); k != "" {
			filtered := []*WorkType{}
			for _, wt := range wts {
				if wt.Key == k {
					filtered = append(filtered, wt)
				}
			}
			wts = filtered
		}
		out["work_types"] = wts
	}
	if want("forms") {
		f, err := listForms(db, pid)
		if err != nil {
			return nil, err
		}
		out["forms"] = f
	}
	if want("rate_items") {
		r, err := listRateItems(db, pid, stringArg(args, "q"), "")
		if err != nil {
			return nil, err
		}
		out["rate_items"] = r
	}
	if want("modifiers") {
		m, err := listModifiers(db, pid)
		if err != nil {
			return nil, err
		}
		out["modifiers"] = m
	}
	if want("policies") {
		p, err := listPolicies(db, pid)
		if err != nil {
			return nil, err
		}
		out["policies"] = p
	}
	return out, nil
}

func (a *App) toolConfigUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	val := objectArg(args, "value")
	if val == nil {
		return nil, errors.New("value required")
	}
	switch stringArg(args, "kind") {
	case "work_type":
		var wt WorkType
		if err := remarshal(val, &wt); err != nil {
			return nil, err
		}
		if _, ok := val["active"]; !ok {
			wt.Active = true
		}
		if err := validateWorkType(&wt); err != nil {
			return nil, err
		}
		if err := upsertWorkType(db, pid, wt); err != nil {
			return nil, err
		}
		return getWorkType(db, pid, 0, wt.Key)
	case "form":
		var f Form
		if err := remarshal(val, &f); err != nil {
			return nil, err
		}
		if err := validateForm(&f); err != nil {
			return nil, err
		}
		if err := upsertForm(db, pid, f); err != nil {
			return nil, err
		}
		return getForm(db, pid, f.Key)
	case "rate_item":
		var ri RateItem
		if err := remarshal(val, &ri); err != nil {
			return nil, err
		}
		if _, ok := val["active"]; !ok {
			ri.Active = true
		}
		if err := validateRateItem(&ri); err != nil {
			return nil, err
		}
		if err := upsertRateItem(db, pid, ri); err != nil {
			return nil, err
		}
		return getRateItem(db, pid, 0, ri.Code)
	case "modifier":
		var m Modifier
		if err := remarshal(val, &m); err != nil {
			return nil, err
		}
		if _, ok := val["active"]; !ok {
			m.Active = true
		}
		if err := validateModifier(&m); err != nil {
			return nil, err
		}
		if err := upsertModifier(db, pid, m); err != nil {
			return nil, err
		}
		return map[string]any{"modifier": m}, nil
	}
	return nil, errors.New("kind must be work_type, form, rate_item or modifier")
}

func (a *App) toolPoliciesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	key := stringArg(args, "key")
	if key == "" {
		return nil, errors.New("key required")
	}
	if err := setPolicy(ctx.AppDB(), ctx.CurrentProject(), key, args["value"]); err != nil {
		return nil, err
	}
	return listPolicies(ctx.AppDB(), ctx.CurrentProject())
}

func (a *App) toolSitesUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := upsertSite(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"site": s}, nil
}

func (a *App) toolSitesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := listSites(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "kind"), stringArg(args, "q"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"sites": s}, nil
}

func (a *App) toolResourcesUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := upsertResource(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"resource": r}, nil
}

func (a *App) toolResourcesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := listResources(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "kind"), int64Arg(args, "site_id"), !boolArg(args, "include_inactive", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{"resources": r}, nil
}

func (a *App) toolResourcesSetBlocks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	b, err := addBlocks(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "resource_id"), arrayArg(args, "blocks"),
		stringArg(args, "replace_from"), stringArg(args, "replace_to"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"added": b}, nil
}

func (a *App) toolResourcesBlocks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	id := int64Arg(args, "resource_id")
	if _, err := getResource(db, pid, id); err != nil {
		return nil, notFound(err, "resource")
	}
	from, to := rangeArgs(args, 0, 14)
	b, err := listBlocks(db, id, fmtTime(from), fmtTime(to))
	if err != nil {
		return nil, err
	}
	return map[string]any{"blocks": b}, nil
}

func rangeArgs(args map[string]any, backDays, fwdDays int) (time.Time, time.Time) {
	now := clock()
	from, err := parseTime(stringArg(args, "from"))
	if err != nil {
		from = now.AddDate(0, 0, -backDays)
	}
	to, err := parseTime(stringArg(args, "to"))
	if err != nil {
		to = from.AddDate(0, 0, backDays+fwdDays)
	}
	return from, to
}

func (a *App) toolObjectsUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := upsertObject(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"object": o}, nil
}

func (a *App) toolObjectsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := listObjects(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "site_id"), stringArg(args, "kind"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"objects": o}, nil
}

func (a *App) toolWorkIntake(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	res, err := intakeWork(ctx.AppDB(), ctx.CurrentProject(), callerFor(ctx), args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "work.created", workEvent(res.Work))
	return res, nil
}

func (a *App) toolWorkGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	return workDetail(db, pid, w)
}

func workDetail(db *sql.DB, pid string, w *Work) (map[string]any, error) {
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, err
	}
	est, _ := listLines(db, w.ID, "estimate")
	act, _ := listLines(db, w.ID, "actual")
	slots, err := listSlots(db, pid, w.ID, map[string]any{"include_cancelled": true})
	if err != nil {
		return nil, err
	}
	forms, _ := listFormResponses(db, pid, map[string]any{"work_id": float64(w.ID)})
	next := []string{}
	if w.Phase != "closed" && w.Phase != statusCancelled {
		allowed := allowedStatuses(wt)
		next = append(next, allowed[w.Phase]...)
		if np := nextPhase(wt, w.Phase); np != "" {
			next = append(next, allowed[np]...)
		}
	}
	return map[string]any{"work": w, "phases": wt.Phases, "can_move_to": next, "required_forms": wt.RequiredForms,
		"estimate_lines": est, "actual_lines": act, "slots": slots, "forms": forms, "playbook": wt.Playbook}, nil
}

func (a *App) toolWorkSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ws, err := searchWork(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"work": ws, "count": len(ws)}, nil
}

func (a *App) toolWorkTransition(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	tr, err := transition(db, pid, w, stringArg(args, "to"), stringArg(args, "actor"), stringArg(args, "reason"), nil)
	if err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return map[string]any{"ok": false, "blocked": ge}, nil
		}
		return nil, err
	}
	emit(ctx, tr.Event, workEvent(tr.Work))
	return map[string]any{"ok": true, "transition": tr}, nil
}

func (a *App) toolWorkTimeline(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	ev, err := listEvents(db, pid, w.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"work": w.Number, "events": ev}, nil
}

func (a *App) toolWorkNote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	if err := logEvent(db, pid, w.ID, stringArg(args, "actor"), "note", "", "", map[string]any{"text": stringArg(args, "text")}); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`UPDATE work SET updated_at=? WHERE id=?`, nowUTC(), w.ID)
	return map[string]any{"ok": true}, nil
}

func (a *App) toolPricePreview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	pc := PriceContext{Priority: firstNonEmpty(stringArg(args, "priority"), "p3"), PartySize: intArg(args, "party_size", 0),
		Answers: objectArg(args, "answers"), DiscountPct: floatArg(args, "discount_pct", 0), Location: loadLocation(policyString(db, pid, "timezone", "UTC"))}
	if pc.Answers == nil {
		pc.Answers = map[string]any{}
	}
	if k := stringArg(args, "work_type"); k != "" {
		wt, err := getWorkType(db, pid, 0, k)
		if err != nil {
			return nil, notFound(err, "work type")
		}
		pc.WorkTypeKey, pc.DefaultDur, pc.TypeReqs, pc.AutoMax = wt.Key, wt.DefaultDurationMin, wt.Requirements, wt.QuotePolicy.AutoApproveMaxCents
	}
	if t, err := parseTime(stringArg(args, "starts_at")); err == nil {
		pc.StartsAt = &t
	}
	for _, f := range arrayArg(args, "flags") {
		if s, ok := f.(string); ok {
			pc.Flags = append(pc.Flags, s)
		}
	}
	return priceItems(db, pid, parseItems(args["items"]), pc)
}

func (a *App) toolQuoteBuild(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	if w.Phase == "closed" || w.Phase == statusCancelled || w.Phase == "bill" {
		return nil, errors.New("work is " + w.Status)
	}
	if w.QuoteStatus == "accepted" {
		return nil, errors.New("quote already accepted; record changes on completion with work_complete items")
	}
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, err
	}
	items := parseItems(args["items"])
	if len(items) == 0 {
		return nil, errors.New("items required")
	}
	pr, err := buildEstimate(db, pid, w, wt, items, args)
	if err != nil {
		return nil, err
	}
	w, _ = getWork(db, pid, w.ID)
	if wt.usesPhase("quote") && w.Phase == "intake" {
		if tr, err := transition(db, pid, w, "quoting", stringArg(args, "actor"), "", nil); err == nil {
			emit(ctx, tr.Event, workEvent(tr.Work))
		}
	}
	if pr.NeedsOwner && w.Status != statusNeedsOwner {
		if tr, err := transition(db, pid, w, statusNeedsOwner, stringArg(args, "actor"), strings.Join(pr.Flags, ", "), map[string]any{"flags": pr.Flags}); err == nil {
			emit(ctx, tr.Event, workEvent(tr.Work))
		}
	}
	w, _ = getWork(db, pid, w.ID)
	return map[string]any{"work": w, "price": pr}, nil
}

func (a *App) toolQuoteDecide(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	nw, ev, err := quoteDecision(db, pid, w, stringArg(args, "decision"), stringArg(args, "actor"), stringArg(args, "reason"))
	if err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return map[string]any{"ok": false, "blocked": ge}, nil
		}
		return nil, err
	}
	emit(ctx, ev, workEvent(nw))
	return map[string]any{"ok": true, "work": nw}, nil
}

func (a *App) toolScheduleSuggest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, err
	}
	p := suggestParams{StepMin: intArg(args, "step_min", 0), Limit: intArg(args, "limit", 5), Preferred: int64sArg(args, "preferred_resource_ids"),
		DurationMin: intArg(args, "duration_min", 0)}
	if t, err := parseTime(stringArg(args, "from")); err == nil {
		p.From = t
	}
	if t, err := parseTime(stringArg(args, "to")); err == nil {
		p.To = t
	}
	opts, reqs, err := suggestSlots(db, pid, w, wt, p)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"options": opts, "requirements": reqs, "duration_min": firstPositive(p.DurationMin, w.DurationMin, wt.DefaultDurationMin)}
	if w.SLADueAt != "" {
		out["sla_due_at"] = w.SLADueAt
	}
	if len(opts) == 0 {
		out["hint"] = "no slot in the window: widen from/to, add shifts (resources_set_blocks) or check opening hours"
	}
	return out, nil
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func (a *App) toolScheduleBook(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	br, err := bookSlot(db, pid, w, args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "slot.booked", map[string]any{"slot_id": br.Slot.ID, "starts_at": br.Slot.StartsAt, "resources": br.Slot.Resources, "work": workEvent(br.Work)})
	return br, nil
}

func (a *App) toolSlotsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, w, err := updateSlot(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return map[string]any{"ok": false, "blocked": ge}, nil
		}
		return nil, err
	}
	emit(ctx, "slot.updated", map[string]any{"slot_id": s.ID, "action": stringArg(args, "action"), "status": s.Status, "starts_at": s.StartsAt,
		"eta": s.ETA, "work": workEvent(w)})
	return map[string]any{"ok": true, "slot": s, "work": w}, nil
}

func (a *App) toolSlotsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if stringArg(args, "from") == "" {
		args["from"] = fmtTime(clock().Truncate(24 * time.Hour))
	}
	if stringArg(args, "to") == "" {
		f, _ := parseTime(stringArg(args, "from"))
		args["to"] = fmtTime(f.Add(24 * time.Hour))
	}
	s, err := listSlots(ctx.AppDB(), ctx.CurrentProject(), 0, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"slots": s, "from": args["from"], "to": args["to"]}, nil
}

func (a *App) toolWorkComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	res, err := completeWork(db, pid, w, args)
	if err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return map[string]any{"ok": false, "blocked": ge}, nil
		}
		return nil, err
	}
	ev := "work.phase_changed"
	if res.Work.Phase == "closed" {
		ev = "work.closed"
	}
	emit(ctx, ev, workEvent(res.Work))
	return res, nil
}

func (a *App) toolFormsSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := submitForm(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "form.submitted", map[string]any{"response_id": r.ID, "form_key": r.FormKey, "work_id": r.WorkID, "site_id": r.SiteID})
	return map[string]any{"response": r}, nil
}

func (a *App) toolFormsResponses(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := listFormResponses(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"responses": r}, nil
}

func (a *App) toolWorkInvoice(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	nw, info, err := invoiceWork(db, pid, callerFor(ctx), w, args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "work.status_changed", workEvent(nw))
	return map[string]any{"work": nw, "invoice": info}, nil
}

func (a *App) toolWorkMarkPaid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	w, err := getWorkByRef(db, pid, args)
	if err != nil {
		return nil, err
	}
	if w.PaidAt == "" {
		if _, err := db.Exec(`UPDATE work SET paid_at=?, updated_at=? WHERE id=?`, nowUTC(), nowUTC(), w.ID); err != nil {
			return nil, err
		}
		w.PaidAt = nowUTC()
		_ = logEvent(db, pid, w.ID, stringArg(args, "actor"), "paid", "", "", nil)
	}
	if w.Phase == "closed" {
		return map[string]any{"ok": true, "work": w}, nil
	}
	tr, err := transition(db, pid, w, "closed", stringArg(args, "actor"), "paid", nil)
	if err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return map[string]any{"ok": false, "blocked": ge}, nil
		}
		return nil, err
	}
	emit(ctx, tr.Event, workEvent(tr.Work))
	return map[string]any{"ok": true, "work": tr.Work}, nil
}

func (a *App) toolAgreementsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ag, err := createAgreement(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agreement": ag}, nil
}

func (a *App) toolAgreementsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	l, err := listAgreements(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "status"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"agreements": l}, nil
}

func (a *App) toolAgreementsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	ag, err := getAgreement(db, pid, int64Arg(args, "id"))
	if err != nil {
		return nil, notFound(err, "agreement")
	}
	if s := stringArg(args, "status"); s != "" {
		if s != "active" && s != "paused" && s != "ended" {
			return nil, errors.New("status must be active, paused or ended")
		}
		ag.Status = s
	}
	if hasArg(args, "ends_on") {
		ag.EndsOn = stringArg(args, "ends_on")
	}
	if hasArg(args, "preferred_resource_ids") {
		ag.PreferredResourceIDs = int64sArg(args, "preferred_resource_ids")
	}
	if _, err := db.Exec(`UPDATE agreements SET status=?, ends_on=?, preferred_resource_ids_json=?, updated_at=? WHERE id=?`,
		ag.Status, ag.EndsOn, jsonOr(ag.PreferredResourceIDs, "[]"), nowUTC(), ag.ID); err != nil {
		return nil, err
	}
	return getAgreement(db, pid, ag.ID)
}

func (a *App) toolAgreementsGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	occ, err := generateAgreements(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "until"), int64Arg(args, "agreement_id"))
	if err != nil {
		return map[string]any{"generated": occ, "error": err.Error()}, nil
	}
	for _, o := range occ {
		emit(ctx, "work.created", map[string]any{"work_id": o.WorkID, "number": o.Number, "agreement_id": o.AgreementID, "slot_id": o.SlotID})
	}
	return map[string]any{"generated": occ, "count": len(occ)}, nil
}

func (a *App) toolMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	from, to := rangeArgs(args, 30, 0)
	if !to.After(from) {
		to = clock()
	}
	return metrics(ctx.AppDB(), ctx.CurrentProject(), from, to)
}

// ─── HTTP: every tool as POST /call/<tool> for panels ───────────────

func (a *App) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx := globalCtx
	if ctx == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		ctx = ctx.WithProject(pid)
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/call/"), "/")
	var handler sdk.ToolHandler
	for _, t := range a.MCPTools() {
		if t.Name == name {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	args := map[string]any{}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5<<20)).Decode(&args); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	out, err := handler(ctx, args)
	if err != nil {
		status := http.StatusBadRequest
		if strings.HasSuffix(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
