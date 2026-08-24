package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Types ──────────────────────────────────────────────────────────

type gig struct {
	ID                   int64               `json:"id"`
	ProjectID            string              `json:"project_id"`
	TemplateVersionID    int64               `json:"template_version_id,omitempty"`
	CreatedBy            string              `json:"created_by"`
	Title                string              `json:"title"`
	Vars                 map[string]any      `json:"vars,omitempty"`
	DerivedResultSchema  map[string]any      `json:"derived_result_schema"`
	DerivedMediaManifest []map[string]any    `json:"derived_media_manifest,omitempty"`
	DerivedChecklist     []map[string]any    `json:"derived_checklist,omitempty"`
	DerivedVariables     []map[string]any    `json:"derived_variables,omitempty"`
	BudgetCents          int64               `json:"budget_cents,omitempty"`
	ScheduledFor         string              `json:"scheduled_for,omitempty"`
	DueAt                string              `json:"due_at,omitempty"`
	DeadlineAt           string              `json:"deadline_at,omitempty"` // deprecated alias for due_at
	OverdueAt            string              `json:"overdue_at,omitempty"`
	Overdue              bool                `json:"overdue"`
	AccessExpiresAt      string              `json:"access_expires_at,omitempty"`
	AccessExpirySource   string              `json:"access_expiry_source,omitempty"`
	Priority             string              `json:"priority,omitempty"`
	Status               string              `json:"status"`
	Result               map[string]any      `json:"result,omitempty"`
	RejectionReason      string              `json:"rejection_reason,omitempty"`
	CreatedAt            string              `json:"created_at"`
	UpdatedAt            string              `json:"updated_at"`
	CompletedAt          string              `json:"completed_at,omitempty"`
	Composition          []gigInstructionRow `json:"composition,omitempty"`
	Assignments          []gigAssignmentView `json:"assignments,omitempty"`
	Submission           *submission         `json:"submission,omitempty"`
	Submissions          []*submission       `json:"submissions,omitempty"`
	Compensation         *gigCompensation    `json:"compensation,omitempty"`
}

type gigInstructionRow struct {
	ID                         int64          `json:"id"`
	SortOrder                  int            `json:"sort_order"`
	InstructionKind            string         `json:"instruction_kind"`
	InstructionName            string         `json:"instruction_name,omitempty"`
	RenderedBody               map[string]any `json:"rendered_body"`
	ResultKey                  string         `json:"result_key,omitempty"`
	SourceInstructionID        int64          `json:"source_instruction_id,omitempty"`
	SourceInstructionVersionID int64          `json:"source_instruction_version_id,omitempty"`
}

type gigAssignmentView struct {
	ID                 int64       `json:"id"`
	GigID              int64       `json:"gig_id"`
	WorkerID           int64       `json:"worker_id"`
	Status             string      `json:"status"`
	OfferedAt          string      `json:"offered_at"`
	RespondedAt        string      `json:"responded_at,omitempty"`
	SubmittedAt        string      `json:"submitted_at,omitempty"`
	CRMConversationID  int64       `json:"crm_conversation_id,omitempty"`
	WorkerURL          string      `json:"worker_url,omitempty"`
	PublicBaseURL      string      `json:"public_base_url,omitempty"`
	Worker             *worker     `json:"worker,omitempty"`
	Submission         *submission `json:"submission,omitempty"`
	Mode               string      `json:"mode,omitempty"`
	NotifyWorker       bool        `json:"notify_worker,omitempty"`
	AccessExpiresAt    string      `json:"access_expires_at,omitempty"`
	TokenExpiresAt     string      `json:"token_expires_at,omitempty"` // deprecated alias
	AccessExpirySource string      `json:"access_expiry_source,omitempty"`
}

type submission struct {
	ID                int64          `json:"id"`
	AssignmentID      int64          `json:"assignment_id"`
	Payload           map[string]any `json:"payload"`
	AttachmentFileIDs []int64        `json:"attachment_file_ids,omitempty"`
	Channel           string         `json:"channel,omitempty"`
	SubmittedAt       string         `json:"submitted_at"`
}

func (g *gig) assignmentAccessExpiry(assignmentID int64) string {
	for _, assignment := range g.Assignments {
		if assignment.ID == assignmentID {
			return assignment.AccessExpiresAt
		}
	}
	return g.AccessExpiresAt
}

// ─── Tool registry ──────────────────────────────────────────────────

func (a *App) gigTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "gigs_create_from_template",
			Description: "Primary dispatch path. Resolves the template's current active version, validates vars, renders the composition, snapshots it onto the gig, and optionally assigns to a worker. Scheduling: scheduled_for? is the intended work date, due_at? is a soft submission date, access_expires_at? is an optional hard worker-link expiry, and access_grace_days? derives hard expiry from due_at. deadline_at remains a deprecated alias for due_at. Returns {gig, assignment?}.",
			InputSchema: schemaObject(map[string]any{
				"template_id":       map[string]any{"type": "integer"},
				"template_slug":     map[string]any{"type": "string"},
				"vars":              map[string]any{"type": "object"},
				"worker_id":         map[string]any{"type": "integer"},
				"notify_worker":     map[string]any{"type": "boolean"},
				"public_domain_id":  map[string]any{"type": "integer"},
				"scheduled_for":     map[string]any{"type": "string", "format": "date-time"},
				"due_at":            map[string]any{"type": "string", "format": "date-time"},
				"deadline_at":       map[string]any{"type": "string"},
				"access_expires_at": map[string]any{"type": "string", "format": "date-time"},
				"access_grace_days": map[string]any{"type": "integer", "minimum": 1},
				"priority":          map[string]any{"type": "string"},
				"budget_cents":      map[string]any{"type": "integer"},
			}, []string{}),
			Handler: a.toolGigsCreateFromTemplate,
		},
		{
			Name:        "gigs_create_from_instructions",
			Description: "Ad-hoc dispatch from instruction refs. Scheduling supports scheduled_for?, soft due_at?, optional hard access_expires_at? or access_grace_days?, and deprecated deadline_at as a due_at alias. Returns {gig, assignment?}.",
			InputSchema: schemaObject(map[string]any{
				"title":             map[string]any{"type": "string"},
				"instructions":      map[string]any{"type": "array"},
				"vars":              map[string]any{"type": "object"},
				"worker_id":         map[string]any{"type": "integer"},
				"notify_worker":     map[string]any{"type": "boolean"},
				"public_domain_id":  map[string]any{"type": "integer"},
				"scheduled_for":     map[string]any{"type": "string", "format": "date-time"},
				"due_at":            map[string]any{"type": "string", "format": "date-time"},
				"deadline_at":       map[string]any{"type": "string"},
				"access_expires_at": map[string]any{"type": "string", "format": "date-time"},
				"access_grace_days": map[string]any{"type": "integer", "minimum": 1},
				"priority":          map[string]any{"type": "string"},
			}, []string{"title", "instructions"}),
			Handler: a.toolGigsCreateFromInstructions,
		},
		{
			Name:        "gigs_create",
			Description: "Fully inline gig (raw instruction bodies, no library references). Scheduling supports scheduled_for?, soft due_at?, optional hard access_expires_at? or access_grace_days?, and deprecated deadline_at as a due_at alias. Returns {gig, assignment?}.",
			InputSchema: schemaObject(map[string]any{
				"title":             map[string]any{"type": "string"},
				"instructions":      map[string]any{"type": "array"},
				"vars":              map[string]any{"type": "object"},
				"worker_id":         map[string]any{"type": "integer"},
				"notify_worker":     map[string]any{"type": "boolean"},
				"public_domain_id":  map[string]any{"type": "integer"},
				"scheduled_for":     map[string]any{"type": "string", "format": "date-time"},
				"due_at":            map[string]any{"type": "string", "format": "date-time"},
				"deadline_at":       map[string]any{"type": "string"},
				"access_expires_at": map[string]any{"type": "string", "format": "date-time"},
				"access_grace_days": map[string]any{"type": "integer", "minimum": 1},
				"priority":          map[string]any{"type": "string"},
			}, []string{"title", "instructions"}),
			Handler: a.toolGigsCreateInline,
		},
		{
			Name:        "gigs_assign",
			Description: "Assign or re-assign an open gig. Args: gig_id, worker_id, mode? (direct|broadcast|first-come; default direct), notify_worker? (default false), public_domain_id?. Returns {assignment}.",
			InputSchema: schemaObject(map[string]any{
				"gig_id":           map[string]any{"type": "integer"},
				"worker_id":        map[string]any{"type": "integer"},
				"mode":             map[string]any{"type": "string"},
				"notify_worker":    map[string]any{"type": "boolean"},
				"public_domain_id": map[string]any{"type": "integer"},
			}, []string{"gig_id", "worker_id"}),
			Handler: a.toolGigsAssign,
		},
		{
			Name:        "gigs_status",
			Description: "Pre-flight read for the agent — gig + composition + assignments + latest submission. Args: id. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolGigsStatus,
		},
		{
			Name:        "gigs_list_open",
			Description: "Filtered queue read returning lightweight gig summaries by default. Use gigs_status for one full record or include_details=true when full compositions and submissions are required. Args: status? (comma-separated; one or more of open, offered, accepted, submitted, reviewed, cancelled, expired; default 'open,offered,accepted'), worker_id?, template_id?, limit? (default 50), include_details? (default false). There is no 'completed' status — work that was accepted is 'reviewed', and 'completed' is accepted as an alias for it. An unrecognised status is an error, never an empty list. Returns {gigs}.",
			InputSchema: schemaObject(map[string]any{
				"status": map[string]any{
					"type": "string",
					"description": "Comma-separated subset of: open, offered, accepted, submitted, reviewed, cancelled, expired. " +
						"Defaults to 'open,offered,accepted'. Accepted work is 'reviewed'; 'completed'/'done' alias to it. " +
						"Terminal states are reviewed, cancelled and expired.",
				},
				"worker_id":       map[string]any{"type": "integer"},
				"template_id":     map[string]any{"type": "integer"},
				"limit":           map[string]any{"type": "integer"},
				"include_details": map[string]any{"type": "boolean"},
			}, []string{}),
			Handler: a.toolGigsListOpen,
		},
		{
			Name:        "gigs_cancel",
			Description: "Cancel an open gig and notify any offered workers via CRM. Args: id, reason?. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"reason": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolGigsCancel,
		},
		{
			Name:        "gigs_extend_deadline",
			Description: "Backward-compatible soft due-date update. Args: id, deadline_at (RFC3339). Relative-to-due worker access moves by the same interval; custom access expiry is preserved. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"deadline_at": map[string]any{"type": "string"},
			}, []string{"id", "deadline_at"}),
			Handler: a.toolGigsExtendDeadline,
		},
		{
			Name:        "gigs_update_schedule",
			Description: "Update a live gig's intended work date, soft due date, and optional hard worker-link expiry. Supplied fields are patched; access_expires_at:null removes automatic expiry. access_grace_days derives access expiry from the resulting due date. Args: id, scheduled_for?, due_at?, access_expires_at?, access_grace_days?. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id":                map[string]any{"type": "integer"},
				"scheduled_for":     map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
				"due_at":            map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
				"access_expires_at": map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
				"access_grace_days": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"id"}),
			Handler: a.toolGigsUpdateSchedule,
		},
		{
			Name:        "gigs_update",
			Description: "Correct a live gig's title and/or vars — use after gigs_extend_deadline so a reschedule does not leave the title and vars stating the original date. vars is a patch: supplied keys win, untouched keys survive, an explicit null drops a key. The title is shown to the worker on their gig page, so keep it worker-facing. Refused once the gig is reviewed, cancelled or expired. Does NOT re-render an already dispatched composition, which is frozen at dispatch. Args: id, title?, vars?. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
				"title": map[string]any{
					"type":        "string",
					"description": "Replacement title. Shown to the worker on their gig page — avoid internal codenames.",
				},
				"vars": map[string]any{
					"type":        "object",
					"description": "Patch merged over the existing vars. Explicit null removes a key.",
				},
			}, []string{"id"}),
			Handler: a.toolGigsUpdate,
		},
		{
			Name:        "gigs_delete",
			Description: "Permanently delete a gig and its assignments, submissions, events and upload sessions. Irreversible — there is no archive and no undo; prefer gigs_cancel unless the gig was created by mistake. Finished gigs (reviewed, cancelled, expired) delete directly; a live gig needs force=true, which also revokes the worker's magic link. Storage files are left in place because instruction media is shared with the instruction library. Args: id, force? (default false). Returns {deleted, gig} where gig is the record as it was.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Delete a gig that is still open, offered, accepted or submitted. This strands the assigned worker's link.",
				},
			}, []string{"id"}),
			Handler: a.toolGigsDelete,
		},
		{
			Name:        "gigs_accept_result",
			Description: "Accept a submitted result. Pass submission_id when a gig has multiple worker submissions; otherwise the latest active submission is used. Bumps that worker's accepted_count and logs to CRM. Args: id, submission_id?, notes?. Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"submission_id": map[string]any{"type": "integer"},
				"notes":         map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolGigsAccept,
		},
		{
			Name:        "gigs_reject_result",
			Description: "Reject a submitted result with a reason. Pass submission_id when a gig has multiple worker submissions. The gig reopens for another worker or stays with the same worker for a redo. Args: id, submission_id?, reason, reopen? (default true). Returns {gig}.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"submission_id": map[string]any{"type": "integer"},
				"reason":        map[string]any{"type": "string"},
				"reopen":        map[string]any{"type": "boolean"},
			}, []string{"id", "reason"}),
			Handler: a.toolGigsReject,
		},
	}
}

// ─── Dispatch ───────────────────────────────────────────────────────

func (a *App) toolGigsCreateFromTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tid := int64Arg(args, "template_id")
	if tid == 0 {
		if slug := strArg(args, "template_slug"); slug != "" {
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM templates WHERE project_id=? AND slug=? AND archived_at IS NULL`,
				pid, slug,
			).Scan(&tid)
		}
	}
	if tid == 0 {
		return nil, errors.New("template_id or template_slug required")
	}
	tpl, err := getTemplate(ctx.AppDB(), pid, tid)
	if err != nil {
		return nil, err
	}
	if tpl == nil {
		return nil, errors.New("template not found")
	}
	if tpl.CurrentVersion == nil {
		return nil, errors.New("template has no current version")
	}
	// Resolve the active version explicitly — current may be a draft
	// the operator is iterating on.
	activeVID, err := resolveActiveTemplateVersion(ctx.AppDB(), tid)
	if err != nil {
		return nil, err
	}
	if activeVID == 0 {
		return nil, errors.New("template has no active version (publish a draft first)")
	}
	tplv, err := getTemplateVersion(ctx.AppDB(), activeVID)
	if err != nil {
		return nil, err
	}
	composition, err := loadComposition(ctx.AppDB(), activeVID)
	if err != nil {
		return nil, err
	}

	vars := mapArg(args, "vars")
	// Apply template-level variable defaults under unset keys.
	if tplv != nil && tplv.VariableOverrides != nil {
		if vars == nil {
			vars = map[string]any{}
		}
		for k, v := range tplv.VariableOverrides {
			if _, set := vars[k]; set {
				continue
			}
			if m, ok := v.(map[string]any); ok {
				if def, ok := m["default"]; ok {
					vars[k] = def
				}
			}
		}
	}

	rendered := renderCompositionForGig(composition, vars)
	derived := deriveFromComposition(rendered)
	title := interpolate(tplv.TitleTemplate, vars)
	compensation, err := compensationQuoteForCreate(ctx, pid, tid, int64Arg(args, "worker_id"), args)
	if err != nil {
		return nil, err
	}
	accessGraceDays := intArg(args, "access_grace_days", 0)
	if _, graceSupplied := args["access_grace_days"]; !graceSupplied {
		if _, expirySupplied := args["access_expires_at"]; !expirySupplied {
			accessGraceDays = tplv.DefaultAccessGraceDays
		}
	}

	g, ass, err := createGig(ctx, pid, createOpts{
		TemplateVersionID:  activeVID,
		Title:              title,
		Vars:               vars,
		Rendered:           rendered,
		Derived:            derived,
		ScheduledFor:       strArg(args, "scheduled_for"),
		DueAt:              strArg(args, "due_at"),
		DeadlineAt:         strArg(args, "deadline_at"),
		AccessExpiresAt:    strArg(args, "access_expires_at"),
		AccessGraceDays:    accessGraceDays,
		Priority:           strArg(args, "priority"),
		BudgetCents:        int64Arg(args, "budget_cents"),
		WorkerID:           int64Arg(args, "worker_id"),
		NotifyWorker:       boolArg(args, "notify_worker", false),
		PublicDomainID:     int64Arg(args, "public_domain_id"),
		DefaultDeadlineHrs: tplv.DefaultDueHours,
		Compensation:       compensation,
		ContractID:         int64Arg(args, "_contract_id"),
		MilestoneID:        int64Arg(args, "_milestone_id"),
		OverrideReason:     strArg(args, "compensation_override_reason"),
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"gig": g}
	if ass != nil {
		out["assignment"] = ass
	}
	return out, nil
}

func (a *App) toolGigsCreateFromInstructions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	raw := sliceArg(args, "instructions")
	if len(raw) == 0 {
		return nil, errors.New("instructions required (non-empty)")
	}
	// Resolve refs into compositionItems by reading instruction_versions.
	items := make([]compositionItem, 0, len(raw))
	for i, r := range raw {
		ref, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("instructions[%d] not an object", i)
		}
		iid := int64Cast(ref["instruction_id"])
		if iid == 0 {
			return nil, fmt.Errorf("instructions[%d].instruction_id required", i)
		}
		var kind string
		var currentVID sql.NullInt64
		if err := ctx.AppDB().QueryRow(
			`SELECT kind, current_version_id FROM instructions
			 WHERE id=? AND project_id=? AND archived_at IS NULL`,
			iid, pid,
		).Scan(&kind, &currentVID); errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("instruction %d not in project", iid)
		} else if err != nil {
			return nil, err
		}
		ivid := int64Cast(ref["instruction_version_id"])
		if ivid == 0 {
			if !currentVID.Valid {
				return nil, fmt.Errorf("instruction %d has no current version", iid)
			}
			ivid = currentVID.Int64
		}
		v, err := getInstructionVersion(ctx.AppDB(), ivid)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("instruction_version %d not found", ivid)
		}
		// Apply per-row overrides.
		body := v.Body
		if ov, ok := ref["overrides"].(map[string]any); ok {
			body = mergeMaps(body, ov)
		}
		if err := validateBody(kind, body); err != nil {
			return nil, fmt.Errorf("instructions[%d] overrides: %w", i, err)
		}
		resultKey := strOf(ref["result_key"])
		if resultKey == "" {
			resultKey = v.DefaultResultKey
		}
		items = append(items, compositionItem{
			SortOrder:            i,
			InstructionID:        iid,
			InstructionVersionID: ivid,
			Kind:                 kind,
			Body:                 body,
			DeclaredVariables:    v.DeclaredVariables,
			ResultKey:            resultKey,
		})
	}
	vars := mapArg(args, "vars")
	rendered := renderCompositionForGig(items, vars)
	derived := deriveFromComposition(rendered)
	title = interpolate(title, vars)
	compensation, err := compensationQuoteForCreate(ctx, pid, 0, int64Arg(args, "worker_id"), args)
	if err != nil {
		return nil, err
	}
	g, ass, err := createGig(ctx, pid, createOpts{
		Title:           title,
		Vars:            vars,
		Rendered:        rendered,
		Derived:         derived,
		ScheduledFor:    strArg(args, "scheduled_for"),
		DueAt:           strArg(args, "due_at"),
		DeadlineAt:      strArg(args, "deadline_at"),
		AccessExpiresAt: strArg(args, "access_expires_at"),
		AccessGraceDays: intArg(args, "access_grace_days", 0),
		Priority:        strArg(args, "priority"),
		BudgetCents:     int64Arg(args, "budget_cents"),
		WorkerID:        int64Arg(args, "worker_id"),
		NotifyWorker:    boolArg(args, "notify_worker", false),
		PublicDomainID:  int64Arg(args, "public_domain_id"),
		Compensation:    compensation,
		ContractID:      int64Arg(args, "_contract_id"),
		MilestoneID:     int64Arg(args, "_milestone_id"),
		OverrideReason:  strArg(args, "compensation_override_reason"),
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"gig": g}
	if ass != nil {
		out["assignment"] = ass
	}
	return out, nil
}

func (a *App) toolGigsCreateInline(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	raw := sliceArg(args, "instructions")
	if len(raw) == 0 {
		return nil, errors.New("instructions required (non-empty)")
	}
	items := make([]compositionItem, 0, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("instructions[%d] not an object", i)
		}
		kind := strOf(m["kind"])
		body := mapArg(m, "body")
		if kind == "" || body == nil {
			return nil, fmt.Errorf("instructions[%d] needs kind + body", i)
		}
		if err := validateBody(kind, body); err != nil {
			return nil, fmt.Errorf("instructions[%d]: %w", i, err)
		}
		items = append(items, compositionItem{
			SortOrder:         i,
			Kind:              kind,
			Body:              body,
			DeclaredVariables: deriveDeclaredVariables(kind, body),
			ResultKey:         strOf(m["result_key"]),
		})
	}
	vars := mapArg(args, "vars")
	rendered := renderCompositionForGig(items, vars)
	derived := deriveFromComposition(rendered)
	title = interpolate(title, vars)
	compensation, err := compensationQuoteForCreate(ctx, pid, 0, int64Arg(args, "worker_id"), args)
	if err != nil {
		return nil, err
	}
	g, ass, err := createGig(ctx, pid, createOpts{
		Title:           title,
		Vars:            vars,
		Rendered:        rendered,
		Derived:         derived,
		ScheduledFor:    strArg(args, "scheduled_for"),
		DueAt:           strArg(args, "due_at"),
		DeadlineAt:      strArg(args, "deadline_at"),
		AccessExpiresAt: strArg(args, "access_expires_at"),
		AccessGraceDays: intArg(args, "access_grace_days", 0),
		Priority:        strArg(args, "priority"),
		BudgetCents:     int64Arg(args, "budget_cents"),
		WorkerID:        int64Arg(args, "worker_id"),
		NotifyWorker:    boolArg(args, "notify_worker", false),
		PublicDomainID:  int64Arg(args, "public_domain_id"),
		Compensation:    compensation,
		ContractID:      int64Arg(args, "_contract_id"),
		MilestoneID:     int64Arg(args, "_milestone_id"),
		OverrideReason:  strArg(args, "compensation_override_reason"),
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"gig": g}
	if ass != nil {
		out["assignment"] = ass
	}
	return out, nil
}

// createGig is the shared write path: insert the snapshot rows and mint
// an assignment if a worker was named.
type createOpts struct {
	TemplateVersionID  int64
	Title              string
	Vars               map[string]any
	Rendered           []compositionItem
	Derived            derivedComposition
	ScheduledFor       string
	DueAt              string
	DeadlineAt         string
	AccessExpiresAt    string
	AccessGraceDays    int
	Priority           string
	BudgetCents        int64
	WorkerID           int64
	NotifyWorker       bool
	PublicDomainID     int64
	DefaultDeadlineHrs int
	Compensation       *rateQuote
	ContractID         int64
	MilestoneID        int64
	OverrideReason     string
}

func compensationQuoteForCreate(ctx *sdk.AppCtx, pid string, templateID, workerID int64, args map[string]any) (*rateQuote, error) {
	if explicit, ok := args["_compensation_quote"].(*rateQuote); ok {
		return explicit, nil
	}
	if workerID == 0 {
		return nil, nil
	}
	quantity := floatArg(args, "quantity", 1)
	quote, err := resolveRate(ctx.AppDB(), pid, templateID, int64Arg(args, "_offer_package_id"), workerID, quantity, strArg(args, "currency"))
	if err != nil {
		return nil, err
	}
	if quote == nil || !quote.Configured {
		return nil, nil
	}
	return quote, nil
}

func normalizeRFC3339(name, value string) (string, time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s must be RFC3339", name)
	}
	return parsed.UTC().Format(time.RFC3339), parsed.UTC(), nil
}

func parseStoredTime(value string) (time.Time, error) {
	var lastErr error
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func resolveCreateSchedule(scheduledFor, dueAt, accessExpiresAt string, accessGraceDays int) (string, string, string, string, error) {
	scheduledFor, scheduledTime, err := normalizeRFC3339("scheduled_for", scheduledFor)
	if err != nil {
		return "", "", "", "", err
	}
	dueAt, dueTime, err := normalizeRFC3339("due_at", dueAt)
	if err != nil {
		return "", "", "", "", err
	}
	if !scheduledTime.IsZero() && !dueTime.IsZero() && dueTime.Before(scheduledTime) {
		return "", "", "", "", errors.New("due_at cannot be before scheduled_for")
	}
	if accessGraceDays > 0 && strings.TrimSpace(accessExpiresAt) != "" {
		return "", "", "", "", errors.New("use either access_expires_at or access_grace_days, not both")
	}
	accessSource := "none"
	var accessTime time.Time
	if accessGraceDays > 0 {
		if dueTime.IsZero() {
			return "", "", "", "", errors.New("access_grace_days requires due_at")
		}
		accessTime = dueTime.Add(time.Duration(accessGraceDays) * 24 * time.Hour)
		accessExpiresAt = accessTime.Format(time.RFC3339)
		accessSource = "due"
	} else if strings.TrimSpace(accessExpiresAt) != "" {
		accessExpiresAt, accessTime, err = normalizeRFC3339("access_expires_at", accessExpiresAt)
		if err != nil {
			return "", "", "", "", err
		}
		accessSource = "custom"
	}
	if !accessTime.IsZero() && !dueTime.IsZero() && accessTime.Before(dueTime) {
		return "", "", "", "", errors.New("access_expires_at cannot be before due_at")
	}
	if !accessTime.IsZero() && dueTime.IsZero() && !scheduledTime.IsZero() && accessTime.Before(scheduledTime) {
		return "", "", "", "", errors.New("access_expires_at cannot be before scheduled_for")
	}
	return scheduledFor, dueAt, accessExpiresAt, accessSource, nil
}

func createGig(ctx *sdk.AppCtx, pid string, o createOpts) (*gig, *gigAssignmentView, error) {
	dueAt := o.DueAt
	if o.DeadlineAt != "" {
		if dueAt != "" && dueAt != o.DeadlineAt {
			return nil, nil, errors.New("due_at and deprecated deadline_at must match when both are supplied")
		}
		dueAt = o.DeadlineAt
	}
	if dueAt == "" {
		hrs := o.DefaultDeadlineHrs
		if hrs <= 0 {
			hrs = atoi(ctx.Config().Get("default_due_hours"))
		}
		if hrs <= 0 {
			hrs = atoi(ctx.Config().Get("default_deadline_hours"))
		}
		if hrs > 0 {
			dueAt = time.Now().UTC().Add(time.Duration(hrs) * time.Hour).Format(time.RFC3339)
		}
	}
	scheduledFor, dueAt, accessExpiresAt, accessSource, err := resolveCreateSchedule(o.ScheduledFor, dueAt, o.AccessExpiresAt, o.AccessGraceDays)
	if err != nil {
		return nil, nil, err
	}
	var wk *worker
	var token string
	var publicBaseURL string
	if o.WorkerID > 0 {
		var err error
		wk, err = getWorker(ctx.AppDB(), pid, o.WorkerID)
		if err != nil {
			return nil, nil, err
		}
		if wk == nil || wk.Status != "active" {
			return nil, nil, errors.New("requested worker is not active in this project")
		}
		max := atoi(ctx.Config().Get("max_open_per_worker"))
		if open, _ := countOpenAssignments(ctx.AppDB(), o.WorkerID); max > 0 && open >= int64(max) {
			return nil, nil, fmt.Errorf("worker has %d open assignments (cap=%d)", open, max)
		}
		token = randomToken()
		publicBaseURL, err = resolveGigPublicBaseURL(ctx.AppDB(), pid, o.PublicDomainID)
		if err != nil {
			return nil, nil, err
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO gigs (
		    project_id, template_version_id, created_by, title, vars_json,
		    derived_result_schema_json, derived_media_manifest_json,
		    derived_checklist_json, derived_variables_json,
		    budget_cents, scheduled_for, due_at, deadline_at,
		    access_expires_at, access_expiry_source, priority, status
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid,
		nullInt64(o.TemplateVersionID),
		"agent", // TODO: pull caller id from ctx when available
		o.Title,
		mustJSON(o.Vars),
		mustJSON(o.Derived.ResultSchema),
		mustJSON(o.Derived.MediaManifest),
		mustJSON(o.Derived.Checklist),
		mustJSON(o.Derived.Variables),
		nullInt64(o.BudgetCents),
		nullStr(scheduledFor),
		nullStr(dueAt),
		nullStr(dueAt),
		nullStr(accessExpiresAt),
		accessSource,
		nullStr(o.Priority),
		"open",
	)
	if err != nil {
		return nil, nil, err
	}
	gigID, _ := res.LastInsertId()
	for _, it := range o.Rendered {
		if _, err := tx.Exec(
			`INSERT INTO gig_instructions
			   (gig_id, sort_order, instruction_kind, rendered_body_json,
			    result_key, source_instruction_id, source_instruction_version_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			gigID, it.SortOrder, it.Kind, mustJSON(it.Body),
			nullStr(it.ResultKey),
			nullInt64(it.InstructionID),
			nullInt64(it.InstructionVersionID),
		); err != nil {
			return nil, nil, err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
		 VALUES (?, ?, 'created', 'agent', ?)`,
		pid, gigID, o.Title,
	); err != nil {
		return nil, nil, err
	}
	var assignID int64
	if o.WorkerID > 0 {
		res, err := tx.Exec(`INSERT INTO gig_assignments
			(gig_id,worker_id,status,magic_token,mode,notify_worker,token_expires_at,public_base_url,access_expiry_source)
			VALUES (?,?,'offered',?,'direct',?,?,?,?)`, gigID, o.WorkerID, token, o.NotifyWorker, nullStr(accessExpiresAt), nullStr(publicBaseURL), accessSource)
		if err != nil {
			return nil, nil, err
		}
		assignID, _ = res.LastInsertId()
		if _, err := tx.Exec(`UPDATE gigs SET status='offered',updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, gigID, pid); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`INSERT INTO gig_events (project_id,gig_id,kind,actor,body)
			VALUES (?,?,'offered',?,'direct')`, pid, gigID, fmt.Sprintf("worker:%d", o.WorkerID)); err != nil {
			return nil, nil, err
		}
	}
	if o.Compensation != nil {
		o.Compensation.WorkerID = o.WorkerID
		if err := insertGigCompensationTx(tx, pid, gigID, o.Compensation, o.ContractID, o.MilestoneID, o.OverrideReason); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	ctx.EmitWithProject("gig.created", pid, map[string]any{
		"gig_id": gigID, "template_version_id": o.TemplateVersionID,
	})
	var ass *gigAssignmentView
	if assignID > 0 {
		workerURL, workerURLErr := buildWorkerURL(ctx, token, publicBaseURL)
		if workerURLErr != nil {
			ctx.Logger().Warn("build worker URL failed", "err", workerURLErr.Error(), "gig_id", gigID)
		}
		if o.NotifyWorker && workerURLErr == nil {
			body := fmt.Sprintf("%s\n\nOpen: %s", o.Title, workerURL)
			if conversationID, sendErr := crmSendMessage(ctx, pid, wk.ContactID, body, wk.DefaultChannel, o.Title); sendErr != nil {
				ctx.Logger().Warn("crm send failed", "err", sendErr.Error(), "gig_id", gigID)
			} else if conversationID > 0 {
				_, _ = ctx.AppDB().Exec(`UPDATE gig_assignments SET crm_conversation_id=? WHERE id=?`, conversationID, assignID)
			}
		}
		ctx.EmitWithProject("gig.offered", pid, map[string]any{"gig_id": gigID, "assignment_id": assignID, "worker_id": o.WorkerID,
			"mode": "direct", "worker_url": workerURL, "notify_worker": o.NotifyWorker})
		ass, err = loadAssignmentView(ctx, pid, assignID)
		if err != nil {
			return nil, nil, err
		}
	}
	g, err := loadGig(ctx, pid, gigID)
	if err != nil {
		return nil, nil, err
	}
	return g, ass, nil
}

// assignGig writes an assignment and optionally notifies via CRM. Returns the
// hydrated view so the caller can surface the magic URL.
func assignGig(ctx *sdk.AppCtx, pid string, gigID, workerID int64, mode string, notifyWorker bool, publicDomainID int64) (*gigAssignmentView, error) {
	if mode == "" {
		mode = "direct"
	}
	if mode != "direct" && mode != "broadcast" && mode != "first-come" {
		return nil, errors.New("mode must be direct|broadcast|first-come")
	}
	g, err := loadGig(ctx, pid, gigID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found in project")
	}
	if g.Status != "open" && g.Status != "offered" {
		return nil, fmt.Errorf("cannot assign gig in status %s", g.Status)
	}
	wk, err := getWorker(ctx.AppDB(), pid, workerID)
	if err != nil {
		return nil, err
	}
	if wk == nil {
		return nil, fmt.Errorf("worker %d not found", workerID)
	}
	if wk.Status != "active" {
		return nil, fmt.Errorf("worker %d is %s — only active workers can be offered gigs", workerID, wk.Status)
	}
	max := atoi(ctx.Config().Get("max_open_per_worker"))
	if max > 0 {
		open, _ := countOpenAssignments(ctx.AppDB(), workerID)
		if open >= int64(max) {
			return nil, fmt.Errorf("worker has %d open assignments (cap=%d) — pick someone else or pause new offers", open, max)
		}
	}
	token := randomToken()
	publicBaseURL, err := resolveGigPublicBaseURL(ctx.AppDB(), pid, publicDomainID)
	if err != nil {
		return nil, err
	}
	expiresAt := g.AccessExpiresAt
	accessSource := g.AccessExpirySource
	if accessSource == "" {
		accessSource = "none"
	}
	var assignmentQuote *rateQuote
	if g.Compensation == nil {
		var templateID int64
		if g.TemplateVersionID > 0 {
			_ = ctx.AppDB().QueryRow(`SELECT template_id FROM template_versions WHERE id=?`, g.TemplateVersionID).Scan(&templateID)
		}
		quote, qErr := resolveRate(ctx.AppDB(), pid, templateID, 0, workerID, 1, "")
		if qErr != nil {
			return nil, qErr
		}
		if quote != nil && quote.Configured {
			assignmentQuote = quote
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Assigning a previously-open gig freezes the then-current standard rate.
	// Existing snapshots are never replaced.
	if assignmentQuote != nil {
		if err := insertGigCompensationTx(tx, pid, gigID, assignmentQuote, 0, 0, ""); err != nil {
			return nil, err
		}
	}
	if mode == "direct" {
		if _, err := tx.Exec(`
			UPDATE gig_assignments
			   SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
			 WHERE gig_id=? AND status IN ('offered','accepted')`, gigID); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO gig_assignments
		   (gig_id, worker_id, status, magic_token, mode, notify_worker, token_expires_at, public_base_url, access_expiry_source)
		 VALUES (?, ?, 'offered', ?, ?, ?, ?, ?, ?)`,
		gigID, workerID, token, mode, notifyWorker, nullStr(expiresAt), nullStr(publicBaseURL), accessSource,
	)
	if err != nil {
		return nil, err
	}
	assignID, _ := res.LastInsertId()
	if _, err := tx.Exec(
		`UPDATE gigs SET status='offered', updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, gigID, pid,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
		 VALUES (?, ?, 'offered', ?, ?)`,
		pid, gigID, fmt.Sprintf("worker:%d", workerID), mode,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	workerURL, workerURLErr := buildWorkerURL(ctx, token, publicBaseURL)
	if workerURLErr != nil {
		ctx.Logger().Warn("build worker URL failed", "err", workerURLErr.Error(), "gig_id", gigID)
	}
	if notifyWorker && workerURLErr == nil {
		body := fmt.Sprintf("%s\n\nOpen: %s", g.Title, workerURL)
		subject := g.Title
		convoID, sendErr := crmSendMessage(ctx, pid, wk.ContactID, body, wk.DefaultChannel, subject)
		if sendErr != nil {
			ctx.Logger().Warn("crm send failed", "err", sendErr.Error(), "gig_id", gigID)
		}
		if convoID > 0 {
			_, _ = ctx.AppDB().Exec(
				`UPDATE gig_assignments SET crm_conversation_id=? WHERE id=?`,
				convoID, assignID,
			)
		}
	}
	ctx.EmitWithProject("gig.offered", pid, map[string]any{
		"gig_id":        gigID,
		"assignment_id": assignID,
		"worker_id":     workerID,
		"mode":          mode,
		"worker_url":    workerURL,
		"notify_worker": notifyWorker,
	})

	view, err := loadAssignmentView(ctx, pid, assignID)
	return view, err
}

func buildWorkerURL(ctx *sdk.AppCtx, token, publicBaseURL string) (string, error) {
	if base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"); base != "" {
		return base + "/worker/" + url.PathEscape(token), nil
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return "", errors.New("resolve gigs installation identity: platform client unavailable")
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return "", fmt.Errorf("resolve gigs installation identity: %w", err)
	}
	if identity == nil || identity.InstallID <= 0 {
		return "", errors.New("resolve gigs installation identity: platform returned no install id")
	}
	base := strings.TrimRight(strings.TrimSpace(identity.PublicURL), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/")
	}
	if base == "" {
		base = "http://localhost:5280"
	}
	return base + "/api/apps/gigs/_install/" + strconv.FormatInt(identity.InstallID, 10) +
		"/worker/" + url.PathEscape(token), nil
}

// ─── Lifecycle tools ────────────────────────────────────────────────

func (a *App) toolGigsAssign(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	gid := int64Arg(args, "gig_id")
	wid := int64Arg(args, "worker_id")
	if gid == 0 || wid == 0 {
		return nil, errors.New("gig_id and worker_id required")
	}
	mode := strArg(args, "mode")
	if mode == "" {
		mode = "direct"
	}
	notifyWorker := boolArg(args, "notify_worker", false)
	view, err := assignGig(ctx, pid, gid, wid, mode, notifyWorker, int64Arg(args, "public_domain_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"assignment": view}, nil
}

func (a *App) toolGigsStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	return map[string]any{"gig": g}, nil
}

func (a *App) toolGigsListOpen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if !boolArg(args, "include_details", false) {
		items, err := listGigSummaries(ctx.AppDB(), pid, strArg(args, "status"), int64Arg(args, "worker_id"), int64Arg(args, "template_id"), intArg(args, "limit", 50))
		if err != nil {
			return nil, err
		}
		return map[string]any{"gigs": items}, nil
	}
	statuses, err := parseGigStatusFilter(strArg(args, "status"))
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(statuses))
	qArgs := []any{pid}
	for i, s := range statuses {
		placeholders[i] = "?"
		qArgs = append(qArgs, s)
	}
	q := `SELECT id FROM gigs
	      WHERE project_id=? AND status IN (` + strings.Join(placeholders, ",") + `)`
	if wid := int64Arg(args, "worker_id"); wid > 0 {
		q = `SELECT g.id FROM gigs g
		     JOIN gig_assignments ga ON ga.gig_id=g.id
		     WHERE g.project_id=? AND g.status IN (` + strings.Join(placeholders, ",") + `) AND ga.worker_id=?`
		qArgs = append(qArgs, wid)
	}
	if tid := int64Arg(args, "template_id"); tid > 0 {
		q += ` AND template_version_id IN (SELECT id FROM template_versions WHERE template_id=?)`
		qArgs = append(qArgs, tid)
	}
	limit := intArg(args, "limit", 50)
	q += ` ORDER BY CASE WHEN due_at IS NULL THEN 1 ELSE 0 END, due_at ASC LIMIT ?`
	qArgs = append(qArgs, limit)

	rows, err := ctx.AppDB().Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := []*gig{}
	for _, id := range ids {
		g, err := loadGig(ctx, pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return map[string]any{"gigs": out}, nil
}

// ─── Gig status vocabulary ──────────────────────────────────────────

// gigStatuses is the complete set of values this app ever writes to gigs.status.
// Assignment rows and gig_events carry their own, wider vocabularies — see
// migrations/001_init.sql. Notably there is no "completed": the terminal
// accepted state is "reviewed" (gigs_accept_result). "expired" remains for
// backward compatibility and explicit terminal records; soft due dates now use
// overdue_at without changing workflow status.
var gigStatuses = []string{"open", "offered", "accepted", "submitted", "reviewed", "cancelled", "expired"}

// defaultGigStatuses is the live-queue view callers get when they pass no filter.
var defaultGigStatuses = []string{"open", "offered", "accepted"}

// gigTerminalGigStatuses are the states where the record is final. Editing a
// gig's title or vars after that would rewrite history rather than correct a
// live gig, so gigs_update refuses them.
var gigTerminalStatuses = []string{"reviewed", "cancelled", "expired"}

// gigStatusAliases maps words callers reach for onto the status actually stored.
// "completed" is the obvious term for work that was accepted, and asking for it
// used to return an empty list indistinguishable from "no such work exists".
var gigStatusAliases = map[string]string{
	"complete":  "reviewed",
	"completed": "reviewed",
	"done":      "reviewed",
}

// parseGigStatusFilter resolves a comma-separated status filter into concrete
// gigs.status values, applying aliases. Unknown values are rejected instead of
// being handed to SQL, where they match nothing and read as a truthful "no
// results" — the failure mode this guards against.
func parseGigStatusFilter(statusFilter string) ([]string, error) {
	statuses := make([]string, 0, len(gigStatuses))
	seen := make(map[string]bool, len(gigStatuses))
	for _, raw := range strings.Split(statusFilter, ",") {
		status := strings.ToLower(strings.TrimSpace(raw))
		if status == "" {
			continue
		}
		if canonical, ok := gigStatusAliases[status]; ok {
			status = canonical
		}
		if !slices.Contains(gigStatuses, status) {
			return nil, fmt.Errorf(
				"unknown gig status %q: valid values are %s (completed/complete/done are accepted as aliases for reviewed)",
				strings.TrimSpace(raw), strings.Join(gigStatuses, ", "))
		}
		if !seen[status] {
			seen[status] = true
			statuses = append(statuses, status)
		}
	}
	if len(statuses) == 0 {
		return slices.Clone(defaultGigStatuses), nil
	}
	return statuses, nil
}

func listGigSummaries(db *sql.DB, pid, statusFilter string, workerID, templateID int64, limit int) ([]*gig, error) {
	statuses, err := parseGigStatusFilter(statusFilter)
	if err != nil {
		return nil, err
	}
	marks := make([]string, 0, len(statuses))
	args := []any{pid}
	for _, status := range statuses {
		marks = append(marks, "?")
		args = append(args, status)
	}
	q := `SELECT g.id,g.project_id,COALESCE(g.template_version_id,0),g.created_by,g.title,
		COALESCE(g.scheduled_for,''),COALESCE(g.due_at,g.deadline_at,''),COALESCE(g.overdue_at,''),
		COALESCE(g.access_expires_at,''),COALESCE(g.access_expiry_source,'none'),
		COALESCE(g.priority,''),g.status,g.created_at,g.updated_at,
		COALESCE(g.completed_at,'') FROM gigs g WHERE g.project_id=? AND g.status IN (` + strings.Join(marks, ",") + `)`
	if workerID > 0 {
		q += ` AND EXISTS (SELECT 1 FROM gig_assignments a WHERE a.gig_id=g.id AND a.worker_id=?)`
		args = append(args, workerID)
	}
	if templateID > 0 {
		q += ` AND g.template_version_id IN (SELECT id FROM template_versions WHERE template_id=?)`
		args = append(args, templateID)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q += ` ORDER BY CASE WHEN g.due_at IS NULL THEN 1 ELSE 0 END,g.due_at,g.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*gig, 0)
	for rows.Next() {
		item := &gig{}
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.TemplateVersionID, &item.CreatedBy, &item.Title,
			&item.ScheduledFor, &item.DueAt, &item.OverdueAt, &item.AccessExpiresAt, &item.AccessExpirySource,
			&item.Priority, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		item.DeadlineAt = item.DueAt
		item.Overdue = item.OverdueAt != ""
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) toolGigsCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	reason := strArg(args, "reason")

	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	if g.Status == "submitted" || g.Status == "reviewed" {
		return nil, fmt.Errorf("cannot cancel gig in status %s", g.Status)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE gigs SET status='cancelled', updated_at=CURRENT_TIMESTAMP,
		    completed_at=CURRENT_TIMESTAMP, rejection_reason=?
		 WHERE id=? AND project_id=?`,
		nullStr(reason), id, pid,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		UPDATE gig_assignments
		   SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
		 WHERE gig_id=? AND status IN ('offered','accepted')`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body) VALUES (?, ?, 'cancelled', 'agent', ?)`,
		pid, id, reason,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := syncContractFromGig(ctx.AppDB(), pid, id, "cancelled"); err != nil {
		ctx.Logger().Warn("sync contract milestone after gig cancellation failed", "gig_id", id, "err", err.Error())
	}
	// Notify only workers who were explicitly notified about the offer.
	for _, ass := range g.Assignments {
		if ass.Status == "offered" || ass.Status == "accepted" {
			if ass.NotifyWorker {
				if wk, _ := getWorker(ctx.AppDB(), pid, ass.WorkerID); wk != nil {
					note := fmt.Sprintf("Gig cancelled: %s", g.Title)
					if reason != "" {
						note += fmt.Sprintf(" — %s", reason)
					}
					_, _ = crmSendMessage(ctx, pid, wk.ContactID, note, wk.DefaultChannel, "")
				}
			}
		}
	}
	ctx.EmitWithProject("gig.cancelled", pid, map[string]any{"gig_id": id, "reason": reason})
	g, _ = loadGig(ctx, pid, id)
	return map[string]any{"gig": g}, nil
}

func (a *App) toolGigsExtendDeadline(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	deadline := strArg(args, "deadline_at")
	if id == 0 || deadline == "" {
		return nil, errors.New("id and deadline_at required")
	}
	args["due_at"] = deadline
	return a.toolGigsUpdateSchedule(ctx, args)
}

func (a *App) toolGigsUpdateSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	if slices.Contains(gigTerminalStatuses, g.Status) {
		return nil, fmt.Errorf("cannot update schedule for gig in status %s", g.Status)
	}

	scheduledFor := g.ScheduledFor
	dueAt := g.DueAt
	accessExpiresAt := g.AccessExpiresAt
	accessSource := g.AccessExpirySource
	if accessSource == "" {
		accessSource = "none"
	}
	_, scheduledSupplied := args["scheduled_for"]
	_, dueSupplied := args["due_at"]
	_, accessSupplied := args["access_expires_at"]
	_, graceSupplied := args["access_grace_days"]
	if scheduledSupplied {
		scheduledFor = strArg(args, "scheduled_for")
	}
	if dueSupplied {
		dueAt = strArg(args, "due_at")
	}
	if accessSupplied && graceSupplied {
		return nil, errors.New("use either access_expires_at or access_grace_days, not both")
	}

	accessChanged := accessSupplied || graceSupplied
	accessGraceDays := 0
	if graceSupplied {
		accessGraceDays = intArg(args, "access_grace_days", 0)
		if accessGraceDays <= 0 {
			return nil, errors.New("access_grace_days must be positive")
		}
		accessExpiresAt = ""
	} else if accessSupplied {
		accessExpiresAt = strArg(args, "access_expires_at")
	}

	if !accessChanged && dueSupplied && accessSource == "due" && g.DueAt != "" && g.AccessExpiresAt != "" {
		oldDue, oldDueErr := parseStoredTime(g.DueAt)
		oldAccess, oldAccessErr := parseStoredTime(g.AccessExpiresAt)
		newDue, newDueErr := parseStoredTime(dueAt)
		if oldDueErr == nil && oldAccessErr == nil && newDueErr == nil {
			accessExpiresAt = newDue.Add(oldAccess.Sub(oldDue)).UTC().Format(time.RFC3339)
		}
	}
	if !accessChanged && dueSupplied && strings.TrimSpace(dueAt) == "" && accessSource == "due" {
		accessExpiresAt = ""
		accessSource = "none"
	}

	normalizedScheduled, normalizedDue, normalizedAccess, normalizedSource, err := resolveCreateSchedule(scheduledFor, dueAt, accessExpiresAt, accessGraceDays)
	if err != nil {
		return nil, err
	}
	if !accessChanged && accessSource == "due" {
		normalizedSource = "due"
	} else if !accessChanged && accessSource == "custom" {
		normalizedSource = "custom"
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE gigs
		SET scheduled_for=?,due_at=?,deadline_at=?,access_expires_at=?,access_expiry_source=?,
		    overdue_at=CASE WHEN ? IS NULL OR datetime(?)>datetime('now') THEN NULL ELSE overdue_at END,
		    updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`,
		nullStr(normalizedScheduled), nullStr(normalizedDue), nullStr(normalizedDue), nullStr(normalizedAccess), normalizedSource,
		nullStr(normalizedDue), nullStr(normalizedDue), pid, id); err != nil {
		return nil, err
	}
	if accessChanged {
		if _, err := tx.Exec(`UPDATE gig_assignments
			SET token_expires_at=?, access_expiry_source=?
			WHERE gig_id=? AND token_revoked_at IS NULL`, nullStr(normalizedAccess), normalizedSource, id); err != nil {
			return nil, err
		}
	} else if dueSupplied && normalizedSource == "due" {
		if _, err := tx.Exec(`UPDATE gig_assignments
			SET token_expires_at=?
			WHERE gig_id=? AND access_expiry_source='due' AND token_revoked_at IS NULL`, nullStr(normalizedAccess), id); err != nil {
			return nil, err
		}
	}
	body := mustJSON(map[string]any{
		"scheduled_for":        normalizedScheduled,
		"due_at":               normalizedDue,
		"access_expires_at":    normalizedAccess,
		"access_expiry_source": normalizedSource,
	})
	if _, err := tx.Exec(`INSERT INTO gig_events(project_id,gig_id,kind,actor,body)
		VALUES (?,?,'schedule_updated','agent',?)`, pid, id, body); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("gig.schedule_updated", pid, map[string]any{"gig_id": id})
	g, _ = loadGig(ctx, pid, id)
	return map[string]any{"gig": g}, nil
}

// toolGigsUpdate corrects a live gig's title and vars. Rescheduling used to
// move deadline_at only, leaving the title and vars.recording_date stating the
// original date — a self-contradictory record whose title is served to the
// worker by handleWorkerGigJSON.
//
// Note this edits the gig's own fields only. A gig's composition is a frozen
// snapshot taken at dispatch, so updating vars does NOT re-render already
// dispatched instruction bodies.
func (a *App) toolGigsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	rawTitle, hasTitle := args["title"]
	rawVars, hasVars := args["vars"]
	if !hasTitle && !hasVars {
		return nil, errors.New("nothing to update: pass title and/or vars")
	}

	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	if slices.Contains(gigTerminalStatuses, g.Status) {
		return nil, fmt.Errorf("cannot update gig in status %s", g.Status)
	}

	sets := []string{"updated_at=CURRENT_TIMESTAMP"}
	qArgs := []any{}
	if hasTitle {
		title, ok := rawTitle.(string)
		if !ok || strings.TrimSpace(title) == "" {
			return nil, errors.New("title must be a non-empty string")
		}
		sets = append(sets, "title=?")
		qArgs = append(qArgs, strings.TrimSpace(title))
	}
	if hasVars {
		patch, ok := rawVars.(map[string]any)
		if !ok {
			return nil, errors.New("vars must be an object")
		}
		// Patch semantics: supplied keys win, untouched keys survive, and an
		// explicit null drops a key. Replacing wholesale would silently lose
		// the dispatch-time context a caller did not think to resend.
		merged := map[string]any{}
		for k, v := range g.Vars {
			merged[k] = v
		}
		for k, v := range patch {
			if v == nil {
				delete(merged, k)
				continue
			}
			merged[k] = v
		}
		sets = append(sets, "vars_json=?")
		qArgs = append(qArgs, mustJSON(merged))
	}
	qArgs = append(qArgs, pid, id)

	if _, err := ctx.AppDB().Exec(
		`UPDATE gigs SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, qArgs...,
	); err != nil {
		return nil, err
	}
	out, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"gig": out}, nil
}

// toolGigsDelete permanently removes a gig and everything hanging off it.
//
// Children are deleted explicitly rather than leaning on ON DELETE CASCADE.
// The cascade should fire — app-sdk opens the writer with foreign_keys(on) —
// but that same file records pragmas silently no-opping in prod for months,
// and a half-deleted gig would leave orphan rows pointing at a missing parent.
// Explicit deletes also give the caller a count of what actually went.
//
// Storage files are deliberately left alone: instruction media belongs to the
// instruction library and outlives any one gig, and storage deduplicates, so a
// file this gig referenced may still be referenced elsewhere.
func (a *App) toolGigsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}

	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	// Deleting live work strands whichever worker is holding its magic link,
	// so that needs saying out loud rather than happening by typo.
	if !slices.Contains(gigTerminalStatuses, g.Status) && !boolArg(args, "force", false) {
		return nil, fmt.Errorf(
			"gig %d is %s, not finished — cancel it first, or pass force=true to delete live work and revoke the worker's link",
			id, g.Status)
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deleted := map[string]int64{}
	del := func(label, q string, qArgs ...any) error {
		res, err := tx.Exec(q, qArgs...)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		n, _ := res.RowsAffected()
		deleted[label] = n
		return nil
	}

	// Grandchildren first: these hang off gig_assignments, not off gigs.
	for _, step := range []struct{ label, query string }{
		{"upload_sessions", `DELETE FROM gig_upload_sessions WHERE assignment_id IN (SELECT id FROM gig_assignments WHERE gig_id=?)`},
		{"submissions", `DELETE FROM gig_submissions WHERE assignment_id IN (SELECT id FROM gig_assignments WHERE gig_id=?)`},
		{"assignments", `DELETE FROM gig_assignments WHERE gig_id=?`},
		{"events", `DELETE FROM gig_events WHERE gig_id=?`},
		{"instructions", `DELETE FROM gig_instructions WHERE gig_id=?`},
	} {
		if err := del(step.label, step.query, id); err != nil {
			return nil, err
		}
	}
	if err := del("gig", `DELETE FROM gigs WHERE id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	if deleted["gig"] == 0 {
		return nil, errors.New("gig not found")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The caller loses the record entirely, so hand back what was destroyed.
	return map[string]any{"deleted": deleted, "gig": g}, nil
}

func (a *App) toolGigsAccept(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	if g.Status != "submitted" {
		return nil, fmt.Errorf("gig is %s, expected submitted", g.Status)
	}
	requestedSubmissionID := int64Arg(args, "submission_id")
	var subID, workerID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT s.id, a.worker_id
		 FROM gig_submissions s
		 JOIN gig_assignments a ON a.id = s.assignment_id
		 JOIN gigs g ON g.id=a.gig_id
		 WHERE a.gig_id=? AND g.project_id=? AND a.status='submitted'
		   AND (?=0 OR s.id=?) ORDER BY s.id DESC LIMIT 1`,
		id, pid, requestedSubmissionID, requestedSubmissionID,
	).Scan(&subID, &workerID); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("no submission to accept")
	} else if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE gigs SET status='reviewed', completed_at=CURRENT_TIMESTAMP,
		    updated_at=CURRENT_TIMESTAMP,
		    result_json=(SELECT payload_json FROM gig_submissions WHERE id=?)
		 WHERE id=?`,
		subID, id,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE gig_assignments
		    SET status='reviewed', reviewed_at=CURRENT_TIMESTAMP, token_revoked_at=CURRENT_TIMESTAMP
		  WHERE id=(SELECT assignment_id FROM gig_submissions WHERE id=?)`, subID,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE gig_assignments
		    SET status='withdrawn', token_revoked_at=CURRENT_TIMESTAMP
		  WHERE gig_id=? AND id<>(SELECT assignment_id FROM gig_submissions WHERE id=?)
		    AND status IN ('offered','accepted','submitted')`, id, subID,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE workers SET accepted_count = accepted_count + 1,
		    updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		workerID,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
		 VALUES (?, ?, 'reviewed', 'agent', ?)`,
		pid, id, strArg(args, "notes"),
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := syncContractFromGig(ctx.AppDB(), pid, id, "reviewed"); err != nil {
		ctx.Logger().Warn("sync contract milestone after gig review failed", "gig_id", id, "err", err.Error())
	}
	// Log to the worker's CRM timeline.
	if wk, _ := getWorker(ctx.AppDB(), pid, workerID); wk != nil {
		_ = crmLogActivity(ctx, pid, wk.ContactID, "note",
			fmt.Sprintf("Gig accepted: %s", g.Title), "gigs")
	}
	ctx.EmitWithProject("gig.reviewed", pid, map[string]any{
		"gig_id":        id,
		"submission_id": subID,
		"worker_id":     workerID,
	})
	if boolArg(map[string]any{"enabled": ctx.Config().Get("auto_create_worker_payables")}, "enabled", false) {
		if _, _, payableErr := createGigPayable(ctx, pid, id); payableErr != nil {
			ctx.Logger().Warn("automatic gig payable failed", "gig_id", id, "err", payableErr.Error())
		}
	}
	g, _ = loadGig(ctx, pid, id)
	return map[string]any{"gig": g}, nil
}

func (a *App) toolGigsReject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	reason := strArg(args, "reason")
	if id == 0 || reason == "" {
		return nil, errors.New("id and reason required")
	}
	reopen := boolArg(args, "reopen", true)
	g, err := loadGig(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("gig not found")
	}
	if g.Status != "submitted" {
		return nil, fmt.Errorf("gig is %s, expected submitted", g.Status)
	}
	requestedSubmissionID := int64Arg(args, "submission_id")
	var submissionID, assignmentID, workerID int64
	var assignmentMode string
	if err := ctx.AppDB().QueryRow(
		`SELECT s.id, a.id, a.worker_id, COALESCE(a.mode,'direct') FROM gig_submissions s
		 JOIN gig_assignments a ON a.id = s.assignment_id
		 JOIN gigs g ON g.id=a.gig_id
		 WHERE a.gig_id=? AND g.project_id=? AND a.status='submitted'
		   AND (?=0 OR s.id=?) ORDER BY s.id DESC LIMIT 1`,
		id, pid, requestedSubmissionID, requestedSubmissionID,
	).Scan(&submissionID, &assignmentID, &workerID, &assignmentMode); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("submission not found or already reviewed")
	} else if err != nil {
		return nil, err
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if workerID > 0 {
		_, _ = tx.Exec(
			`UPDATE workers SET rejected_count = rejected_count + 1,
			    updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			workerID,
		)
	}
	if assignmentID > 0 {
		if reopen {
			_, err = tx.Exec(`UPDATE gig_assignments
				SET status='rejected', token_revoked_at=CURRENT_TIMESTAMP WHERE id=?`, assignmentID)
		} else {
			_, err = tx.Exec(`UPDATE gig_assignments
				SET status='accepted', submitted_at=NULL WHERE id=?`, assignmentID)
		}
		if err != nil {
			return nil, err
		}
	}
	if reopen && assignmentMode == "first-come" {
		if _, err := tx.Exec(`UPDATE gig_assignments SET status='offered', token_revoked_at=NULL
			WHERE gig_id=? AND id<>? AND mode='first-come' AND status='withdrawn'
			  AND (token_expires_at IS NULL OR datetime(token_expires_at)>datetime('now'))`, id, assignmentID); err != nil {
			return nil, err
		}
	}
	newStatus := "accepted"
	if reopen {
		if err := tx.QueryRow(`SELECT CASE
			WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='submitted') THEN 'submitted'
			WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='accepted') THEN 'accepted'
			WHEN EXISTS (SELECT 1 FROM gig_assignments WHERE gig_id=? AND status='offered') THEN 'offered'
			ELSE 'open' END`, id, id, id).Scan(&newStatus); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(
		`UPDATE gigs SET status=?, rejection_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`,
		newStatus, reason, id, pid,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO gig_events (project_id, gig_id, kind, actor, body)
		 VALUES (?, ?, 'rejected', 'agent', ?)`,
		pid, id, reason,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := syncContractFromGig(ctx.AppDB(), pid, id, "rejected"); err != nil {
		ctx.Logger().Warn("sync contract milestone after gig rejection failed", "gig_id", id, "err", err.Error())
	}
	ctx.EmitWithProject("gig.rejected", pid, map[string]any{
		"gig_id": id, "submission_id": submissionID, "worker_id": workerID, "reason": reason,
	})
	g, _ = loadGig(ctx, pid, id)
	return map[string]any{"gig": g}, nil
}

// ─── DB helpers ─────────────────────────────────────────────────────

func resolveActiveTemplateVersion(db *sql.DB, templateID int64) (int64, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM template_versions WHERE template_id=? AND status='active'
		 ORDER BY version DESC LIMIT 1`,
		templateID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func loadGig(ctx *sdk.AppCtx, pid string, id int64) (*gig, error) {
	db := ctx.AppDB()
	g := &gig{ProjectID: pid}
	var tplVID sql.NullInt64
	var vars, schema, media, checklist, vars2, result, priority, scheduledFor, dueAt, overdueAt, accessExpiresAt, accessSource, completedAt, rejection sql.NullString
	var budget sql.NullInt64
	err := db.QueryRow(
		`SELECT id, template_version_id, created_by, title, vars_json,
		        derived_result_schema_json, derived_media_manifest_json,
		        derived_checklist_json, derived_variables_json,
		        budget_cents, scheduled_for, COALESCE(due_at,deadline_at), overdue_at,
		        access_expires_at, access_expiry_source, priority, status, result_json,
		        rejection_reason, created_at, updated_at, completed_at
		 FROM gigs WHERE project_id=? AND id=?`,
		pid, id,
	).Scan(
		&g.ID, &tplVID, &g.CreatedBy, &g.Title, &vars,
		&schema, &media, &checklist, &vars2,
		&budget, &scheduledFor, &dueAt, &overdueAt, &accessExpiresAt, &accessSource, &priority, &g.Status, &result,
		&rejection, &g.CreatedAt, &g.UpdatedAt, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.TemplateVersionID = tplVID.Int64
	g.BudgetCents = budget.Int64
	g.ScheduledFor = scheduledFor.String
	g.DueAt = dueAt.String
	g.DeadlineAt = dueAt.String
	g.OverdueAt = overdueAt.String
	g.Overdue = overdueAt.Valid
	g.AccessExpiresAt = accessExpiresAt.String
	g.AccessExpirySource = accessSource.String
	g.Priority = priority.String
	g.RejectionReason = rejection.String
	g.CompletedAt = completedAt.String
	_ = parseJSON(vars.String, &g.Vars)
	_ = parseJSON(schema.String, &g.DerivedResultSchema)
	_ = parseJSON(media.String, &g.DerivedMediaManifest)
	_ = parseJSON(checklist.String, &g.DerivedChecklist)
	_ = parseJSON(vars2.String, &g.DerivedVariables)
	_ = parseJSON(result.String, &g.Result)
	g.Compensation, _ = loadGigCompensation(db, pid, id)
	// Composition.
	rows, err := db.Query(
		`SELECT gi.id, gi.sort_order, gi.instruction_kind, gi.rendered_body_json, gi.result_key,
		        gi.source_instruction_id, gi.source_instruction_version_id, COALESCE(i.name, '')
		   FROM gig_instructions gi
		   LEFT JOIN instructions i ON i.id = gi.source_instruction_id
		  WHERE gi.gig_id=?
		  ORDER BY gi.sort_order`,
		id,
	)
	if err == nil {
		for rows.Next() {
			row := gigInstructionRow{}
			var rk sql.NullString
			var sid, svid sql.NullInt64
			var bodyJSON string
			if err := rows.Scan(&row.ID, &row.SortOrder, &row.InstructionKind, &bodyJSON, &rk, &sid, &svid, &row.InstructionName); err != nil {
				return nil, err
			}
			_ = parseJSON(bodyJSON, &row.RenderedBody)
			row.ResultKey = rk.String
			row.SourceInstructionID = sid.Int64
			row.SourceInstructionVersionID = svid.Int64
			g.Composition = append(g.Composition, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	// Assignments.
	aRows, err := db.Query(
		`SELECT id, worker_id, status, offered_at, responded_at, submitted_at,
		        crm_conversation_id, magic_token, COALESCE(mode,'direct'),
		        COALESCE(notify_worker,0), COALESCE(token_expires_at,''), COALESCE(public_base_url,''),
		        COALESCE(access_expiry_source,'none')
		 FROM gig_assignments WHERE gig_id=? ORDER BY offered_at`,
		id,
	)
	if err == nil {
		for aRows.Next() {
			v := gigAssignmentView{GigID: id}
			var responded, submitted sql.NullString
			var convID sql.NullInt64
			var token string
			if err := aRows.Scan(&v.ID, &v.WorkerID, &v.Status, &v.OfferedAt, &responded, &submitted, &convID, &token, &v.Mode, &v.NotifyWorker, &v.AccessExpiresAt, &v.PublicBaseURL, &v.AccessExpirySource); err != nil {
				return nil, err
			}
			v.TokenExpiresAt = v.AccessExpiresAt
			v.RespondedAt = responded.String
			v.SubmittedAt = submitted.String
			v.CRMConversationID = convID.Int64
			v.WorkerURL, _ = buildWorkerURL(ctx, token, v.PublicBaseURL)
			g.Assignments = append(g.Assignments, v)
		}
		if err := aRows.Err(); err != nil {
			_ = aRows.Close()
			return nil, err
		}
		if err := aRows.Close(); err != nil {
			return nil, err
		}
	}
	if g.Result == nil {
		var payload sql.NullString
		if err := db.QueryRow(
			`SELECT s.payload_json
			 FROM gig_submissions s
			 JOIN gig_assignments a ON a.id = s.assignment_id
			 WHERE a.gig_id=?
			 ORDER BY s.id DESC LIMIT 1`,
			id,
		).Scan(&payload); err == nil && payload.Valid {
			_ = parseJSON(payload.String, &g.Result)
		}
	}
	subs, err := loadSubmissions(db, id)
	if err != nil {
		return nil, err
	}
	g.Submissions = subs
	if len(subs) > 0 {
		sub := subs[0]
		g.Submission = sub
		for i := range g.Assignments {
			if g.Assignments[i].ID == sub.AssignmentID {
				g.Assignments[i].Submission = sub
				break
			}
		}
	}
	return g, nil
}

func loadLatestSubmission(db *sql.DB, gigID int64) (*submission, error) {
	subs, err := loadSubmissions(db, gigID)
	if err != nil || len(subs) == 0 {
		return nil, err
	}
	return subs[0], nil
}

func loadSubmissions(db *sql.DB, gigID int64) ([]*submission, error) {
	rows, err := db.Query(
		`SELECT s.id, s.assignment_id, s.payload_json,
		        COALESCE(s.attachment_file_ids_json, '[]'),
		        COALESCE(s.channel, ''), COALESCE(s.submitted_at, '')
		   FROM gig_submissions s
		   JOIN gig_assignments a ON a.id = s.assignment_id
		  WHERE a.gig_id = ?
		  ORDER BY s.id DESC`, gigID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*submission
	for rows.Next() {
		var payloadJSON, attachmentIDsJSON string
		sub := &submission{}
		if err := rows.Scan(&sub.ID, &sub.AssignmentID, &payloadJSON, &attachmentIDsJSON, &sub.Channel, &sub.SubmittedAt); err != nil {
			return nil, err
		}
		_ = parseJSON(payloadJSON, &sub.Payload)
		_ = parseJSON(attachmentIDsJSON, &sub.AttachmentFileIDs)
		if sub.Payload == nil {
			sub.Payload = map[string]any{}
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func loadAssignmentView(ctx *sdk.AppCtx, pid string, assignID int64) (*gigAssignmentView, error) {
	v := &gigAssignmentView{}
	var responded, submitted sql.NullString
	var convID sql.NullInt64
	var token string
	if err := ctx.AppDB().QueryRow(
		`SELECT id, gig_id, worker_id, status, offered_at, responded_at, submitted_at,
		        crm_conversation_id, magic_token, COALESCE(mode,'direct'),
		        COALESCE(notify_worker,0), COALESCE(token_expires_at,''), COALESCE(public_base_url,''),
		        COALESCE(access_expiry_source,'none')
		 FROM gig_assignments WHERE id=?`,
		assignID,
	).Scan(&v.ID, &v.GigID, &v.WorkerID, &v.Status, &v.OfferedAt, &responded, &submitted, &convID, &token, &v.Mode, &v.NotifyWorker, &v.AccessExpiresAt, &v.PublicBaseURL, &v.AccessExpirySource); err != nil {
		return nil, err
	}
	v.TokenExpiresAt = v.AccessExpiresAt
	v.RespondedAt = responded.String
	v.SubmittedAt = submitted.String
	v.CRMConversationID = convID.Int64
	v.WorkerURL, _ = buildWorkerURL(ctx, token, v.PublicBaseURL)
	if wk, err := getWorker(ctx.AppDB(), pid, v.WorkerID); err == nil {
		if c, _ := crmGetContact(ctx, pid, wk.ContactID); c != nil {
			wk.Contact = c
		}
		v.Worker = wk
	}
	return v, nil
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPGigsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("summary") == "true" {
			items, err := listGigSummaries(ctx.AppDB(), pid, r.URL.Query().Get("status"), parseQueryInt(r, "worker_id"), parseQueryInt(r, "template_id"), parseQueryIntDefault(r, "limit", 50))
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"gigs": items})
			return
		}
		out, err := a.toolGigsListOpen(ctx, map[string]any{
			"_project_id": pid,
			"status":      r.URL.Query().Get("status"),
			"worker_id":   parseQueryInt(r, "worker_id"),
			"template_id": parseQueryInt(r, "template_id"),
			"limit":       parseQueryIntDefault(r, "limit", 50),
		})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := httpDecode(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		// Body must declare which create path: template_id|template_slug
		// → from_template; instructions present and items have kind →
		// inline; instructions with instruction_id → from_instructions.
		var out any
		if body["offer_id"] != nil || body["offer_slug"] != nil {
			out, err = a.toolGigsCreateFromOffer(ctx, body)
		} else if body["template_id"] != nil || body["template_slug"] != nil {
			out, err = a.toolGigsCreateFromTemplate(ctx, body)
		} else if items, ok := body["instructions"].([]any); ok && len(items) > 0 {
			if first, ok := items[0].(map[string]any); ok && first["kind"] != nil {
				out, err = a.toolGigsCreateInline(ctx, body)
			} else {
				out, err = a.toolGigsCreateFromInstructions(ctx, body)
			}
		} else {
			httpErr(w, http.StatusBadRequest, "need template_id, template_slug, or instructions")
			return
		}
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPGigItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/gigs/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		switch parts[1] {
		case "schedule":
			var body map[string]any
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["id"] = id
			out, err := a.toolGigsUpdateSchedule(ctx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "cancel":
			var body map[string]any
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			out, err := a.toolGigsCancel(ctx, map[string]any{
				"_project_id": pid,
				"id":          id,
				"reason":      strOf(body["reason"]),
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "accept":
			var body map[string]any
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			out, err := a.toolGigsAccept(ctx, map[string]any{
				"_project_id":   pid,
				"id":            id,
				"submission_id": body["submission_id"],
				"notes":         strOf(body["notes"]),
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "reject":
			var body map[string]any
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			out, err := a.toolGigsReject(ctx, map[string]any{
				"_project_id":   pid,
				"id":            id,
				"submission_id": body["submission_id"],
				"reason":        strOf(body["reason"]),
				"reopen":        body["reopen"],
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "assign":
			var body map[string]any
			if err := httpDecode(r, &body); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			out, err := a.toolGigsAssign(ctx, map[string]any{
				"_project_id":      pid,
				"gig_id":           id,
				"worker_id":        body["worker_id"],
				"mode":             strOf(body["mode"]),
				"notify_worker":    body["notify_worker"],
				"public_domain_id": body["public_domain_id"],
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolGigsStatus(ctx, map[string]any{"_project_id": pid, "id": id})
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
