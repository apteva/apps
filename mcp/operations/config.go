package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ─── configuration types ────────────────────────────────────────────

var phaseOrder = []string{"intake", "quote", "schedule", "execute", "bill", "closed"}

var validModes = map[string]bool{"at_customer": true, "at_site": true, "fulfilment": true, "production": true, "internal": true}
var validResourceKinds = map[string]bool{"person": true, "crew": true, "space": true, "equipment": true, "vehicle": true}
var validPricingModes = map[string]bool{"flat": true, "per_unit": true, "hourly": true, "tiered": true, "formula": true, "quote_only": true}
var validFieldTypes = map[string]bool{"text": true, "number": true, "bool": true, "choice": true, "multi_choice": true, "photo": true, "signature": true, "date": true}

// Requirement says which resources a piece of work needs at the same
// time: "1 person with the plumber qualification", "1 space in zone
// dining with capacity >= party size", "1 equipment of zone lift".
type Requirement struct {
	Kind         string   `json:"kind"`
	Count        int      `json:"count,omitempty"`
	Quals        []string `json:"quals,omitempty"`
	Zone         string   `json:"zone,omitempty"`
	CapacityFrom string   `json:"capacity_from,omitempty"` // "party_size"
	Role         string   `json:"role,omitempty"`
}

type StatusDef struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Phase string `json:"phase"`
	Hold  bool   `json:"hold,omitempty"`
}

type PriorityRule struct {
	Keywords []string `json:"keywords"`
	Priority string   `json:"priority"`
}

type QuotePolicy struct {
	AutoApproveMaxCents int64 `json:"auto_approve_max_cents,omitempty"`
}

type WorkType struct {
	ID                 int64               `json:"id,omitempty"`
	PackKey            string              `json:"pack_key,omitempty"`
	Key                string              `json:"key"`
	Name               string              `json:"name"`
	Mode               string              `json:"mode"`
	Phases             []string            `json:"phases"`
	Statuses           []StatusDef         `json:"statuses,omitempty"`
	RequiredForms      map[string][]string `json:"required_forms,omitempty"`
	IntakeFormKey      string              `json:"intake_form_key,omitempty"`
	DefaultDurationMin int                 `json:"default_duration_min,omitempty"`
	BufferMin          int                 `json:"buffer_min,omitempty"`
	Requirements       []Requirement       `json:"requirements,omitempty"`
	PriorityRules      []PriorityRule      `json:"priority_rules,omitempty"`
	SLA                map[string]int      `json:"sla,omitempty"`
	QuotePolicy        QuotePolicy         `json:"quote_policy,omitempty"`
	Playbook           string              `json:"playbook,omitempty"`
	Active             bool                `json:"active"`
	UpdatedAt          string              `json:"updated_at,omitempty"`
}

func (wt *WorkType) usesPhase(p string) bool { return contains(wt.Phases, p) }

type FormField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}

type FormSchema struct {
	Fields []FormField `json:"fields"`
}

type Form struct {
	ID        int64      `json:"id,omitempty"`
	PackKey   string     `json:"pack_key,omitempty"`
	Key       string     `json:"key"`
	Version   int        `json:"version"`
	Name      string     `json:"name"`
	Purpose   string     `json:"purpose"`
	Schema    FormSchema `json:"schema"`
	UpdatedAt string     `json:"updated_at,omitempty"`
}

type Tier struct {
	UpTo      float64 `json:"up_to,omitempty"` // 0 = no upper bound
	UnitCents int64   `json:"unit_cents"`
}

// Pricing is evaluated by the pricing engine only; agents never type a
// price. Modes: flat, per_unit, hourly, tiered, formula, quote_only.
type Pricing struct {
	Mode           string `json:"mode"`
	Unit           string `json:"unit,omitempty"`
	UnitCents      int64  `json:"unit_cents,omitempty"`
	MinChargeCents int64  `json:"min_charge_cents,omitempty"`
	Tiers          []Tier `json:"tiers,omitempty"`
	Formula        string `json:"formula,omitempty"`  // result in cents, over intake answers + qty
	QtyFrom        string `json:"qty_from,omitempty"` // intake answer key that sets qty
}

type Duration struct {
	FixedMin   int     `json:"fixed_min,omitempty"`
	PerUnitMin float64 `json:"per_unit_min,omitempty"`
}

type Material struct {
	SKU        string  `json:"sku"`
	QtyPerUnit float64 `json:"qty_per_unit"`
}

type RateItem struct {
	ID             int64         `json:"id,omitempty"`
	PackKey        string        `json:"pack_key,omitempty"`
	Code           string        `json:"code"`
	Name           string        `json:"name"`
	CatalogPriceID *int64        `json:"catalog_price_id,omitempty"`
	Pricing        Pricing       `json:"pricing"`
	CostCents      int64         `json:"cost_cents,omitempty"`
	Duration       Duration      `json:"duration,omitempty"`
	Requirements   []Requirement `json:"requirements,omitempty"`
	Materials      []Material    `json:"materials,omitempty"`
	Active         bool          `json:"active"`
	UpdatedAt      string        `json:"updated_at,omitempty"`
}

type ModifierWhen struct {
	Priorities   []string `json:"priorities,omitempty"`
	Weekdays     []int    `json:"weekdays,omitempty"` // 1=Mon … 7=Sun, site local time
	HourFrom     *int     `json:"hour_from,omitempty"`
	HourTo       *int     `json:"hour_to,omitempty"` // wraps past midnight when < hour_from
	PartySizeGTE int      `json:"party_size_gte,omitempty"`
	Flags        []string `json:"flags,omitempty"` // all must be present in the pricing context
	WorkTypes    []string `json:"work_types,omitempty"`
}

type Amount struct {
	Pct   float64 `json:"pct,omitempty"`
	Cents int64   `json:"cents,omitempty"`
}

type Modifier struct {
	ID        int64        `json:"id,omitempty"`
	PackKey   string       `json:"pack_key,omitempty"`
	Key       string       `json:"key"`
	Name      string       `json:"name"`
	Kind      string       `json:"kind"`
	When      ModifierWhen `json:"when"`
	Amount    Amount       `json:"amount"`
	Active    bool         `json:"active"`
	UpdatedAt string       `json:"updated_at,omitempty"`
}

// Pack is a business profile: everything specific to one kind of
// business, installed as data.
type Pack struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	WorkTypes   []WorkType     `json:"work_types"`
	Forms       []Form         `json:"forms"`
	RateItems   []RateItem     `json:"rate_items"`
	Modifiers   []Modifier     `json:"modifiers"`
	Policies    map[string]any `json:"policies"`
	Setup       string         `json:"setup,omitempty"` // what the owner still has to configure
}

//go:embed packs/*.json
var packFS embed.FS

func builtinPacks() (map[string]Pack, error) {
	entries, err := packFS.ReadDir("packs")
	if err != nil {
		return nil, err
	}
	out := map[string]Pack{}
	for _, e := range entries {
		b, err := packFS.ReadFile("packs/" + e.Name())
		if err != nil {
			return nil, err
		}
		var p Pack
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("pack %s: %w", e.Name(), err)
		}
		out[p.Key] = p
	}
	return out, nil
}

// ─── validation ─────────────────────────────────────────────────────

func validateWorkType(wt *WorkType) error {
	if wt.Key == "" || wt.Name == "" {
		return errors.New("work type needs key and name")
	}
	if wt.Mode == "" {
		wt.Mode = "at_site"
	}
	if !validModes[wt.Mode] {
		return fmt.Errorf("work type %s: invalid mode %q", wt.Key, wt.Mode)
	}
	if len(wt.Phases) == 0 {
		wt.Phases = []string{"intake", "schedule", "execute", "closed"}
	}
	last := -1
	for _, p := range wt.Phases {
		i := phaseIndex(p)
		if i < 0 {
			return fmt.Errorf("work type %s: unknown phase %q", wt.Key, p)
		}
		if i <= last {
			return fmt.Errorf("work type %s: phases must be in order %v", wt.Key, phaseOrder)
		}
		last = i
	}
	if wt.Phases[len(wt.Phases)-1] != "closed" {
		wt.Phases = append(wt.Phases, "closed")
	}
	for _, s := range wt.Statuses {
		if s.Key == "" || !wt.usesPhase(s.Phase) {
			return fmt.Errorf("work type %s: status %q must name a phase the type uses", wt.Key, s.Key)
		}
	}
	for p := range wt.RequiredForms {
		if !wt.usesPhase(p) {
			return fmt.Errorf("work type %s: required_forms phase %q not used", wt.Key, p)
		}
	}
	for _, r := range wt.Requirements {
		if err := validateRequirement(r); err != nil {
			return fmt.Errorf("work type %s: %w", wt.Key, err)
		}
	}
	if wt.DefaultDurationMin <= 0 {
		wt.DefaultDurationMin = 60
	}
	return nil
}

func validateRequirement(r Requirement) error {
	if !validResourceKinds[r.Kind] {
		return fmt.Errorf("requirement kind %q invalid", r.Kind)
	}
	if r.CapacityFrom != "" && r.CapacityFrom != "party_size" {
		return fmt.Errorf("requirement capacity_from %q invalid (party_size)", r.CapacityFrom)
	}
	return nil
}

func validateForm(f *Form) error {
	if f.Key == "" || f.Name == "" {
		return errors.New("form needs key and name")
	}
	if f.Purpose == "" {
		f.Purpose = "checklist"
	}
	if f.Version <= 0 {
		f.Version = 1
	}
	seen := map[string]bool{}
	for _, fld := range f.Schema.Fields {
		if fld.Key == "" || seen[fld.Key] {
			return fmt.Errorf("form %s: field keys must be unique and non-empty", f.Key)
		}
		seen[fld.Key] = true
		if !validFieldTypes[fld.Type] {
			return fmt.Errorf("form %s: field %s has invalid type %q", f.Key, fld.Key, fld.Type)
		}
		if (fld.Type == "choice" || fld.Type == "multi_choice") && len(fld.Options) == 0 {
			return fmt.Errorf("form %s: field %s needs options", f.Key, fld.Key)
		}
	}
	return nil
}

func validateRateItem(ri *RateItem) error {
	if ri.Code == "" || ri.Name == "" {
		return errors.New("rate item needs code and name")
	}
	if !validPricingModes[ri.Pricing.Mode] {
		return fmt.Errorf("rate item %s: invalid pricing mode %q", ri.Code, ri.Pricing.Mode)
	}
	switch ri.Pricing.Mode {
	case "tiered":
		if len(ri.Pricing.Tiers) == 0 {
			return fmt.Errorf("rate item %s: tiered pricing needs tiers", ri.Code)
		}
	case "formula":
		if _, err := parseFormula(ri.Pricing.Formula); err != nil {
			return fmt.Errorf("rate item %s: formula: %w", ri.Code, err)
		}
	}
	for _, r := range ri.Requirements {
		if err := validateRequirement(r); err != nil {
			return fmt.Errorf("rate item %s: %w", ri.Code, err)
		}
	}
	return nil
}

func validateModifier(m *Modifier) error {
	if m.Key == "" || m.Name == "" {
		return errors.New("modifier needs key and name")
	}
	if m.Kind != "surcharge" && m.Kind != "discount" && m.Kind != "fee" {
		return fmt.Errorf("modifier %s: kind must be surcharge, discount or fee", m.Key)
	}
	if m.Amount.Pct == 0 && m.Amount.Cents == 0 {
		return fmt.Errorf("modifier %s: amount needs pct or cents", m.Key)
	}
	return nil
}

// validatePack checks a pack on its own and that every form it
// references is either in the pack or already installed.
func validatePack(db *sql.DB, pid string, p *Pack) []string {
	var errs []string
	if p.Key == "" || p.Name == "" {
		errs = append(errs, "pack needs key and name")
	}
	formKeys := map[string]bool{}
	for i := range p.Forms {
		if err := validateForm(&p.Forms[i]); err != nil {
			errs = append(errs, err.Error())
		}
		formKeys[p.Forms[i].Key] = true
	}
	formExists := func(k string) bool {
		if formKeys[k] {
			return true
		}
		if db == nil {
			return false
		}
		_, err := getForm(db, pid, k)
		return err == nil
	}
	for i := range p.WorkTypes {
		wt := &p.WorkTypes[i]
		wt.Active = true
		if err := validateWorkType(wt); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if wt.IntakeFormKey != "" && !formExists(wt.IntakeFormKey) {
			errs = append(errs, fmt.Sprintf("work type %s: intake form %q not found", wt.Key, wt.IntakeFormKey))
		}
		for _, keys := range wt.RequiredForms {
			for _, k := range keys {
				if !formExists(k) {
					errs = append(errs, fmt.Sprintf("work type %s: required form %q not found", wt.Key, k))
				}
			}
		}
	}
	for i := range p.RateItems {
		p.RateItems[i].Active = true
		if err := validateRateItem(&p.RateItems[i]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for i := range p.Modifiers {
		p.Modifiers[i].Active = true
		if err := validateModifier(&p.Modifiers[i]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}

// ─── store ──────────────────────────────────────────────────────────

func installPack(db *sql.DB, pid string, p Pack) (map[string]int, error) {
	if errs := validatePack(db, pid, &p); len(errs) > 0 {
		return nil, fmt.Errorf("pack %s is invalid: %s", p.Key, strings.Join(errs, "; "))
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUTC()
	for _, f := range p.Forms {
		f.PackKey = p.Key
		if err := upsertForm(tx, pid, f); err != nil {
			return nil, err
		}
	}
	for _, wt := range p.WorkTypes {
		wt.PackKey = p.Key
		if err := upsertWorkType(tx, pid, wt); err != nil {
			return nil, err
		}
	}
	for _, ri := range p.RateItems {
		ri.PackKey = p.Key
		if err := upsertRateItem(tx, pid, ri); err != nil {
			return nil, err
		}
	}
	for _, m := range p.Modifiers {
		m.PackKey = p.Key
		if err := upsertModifier(tx, pid, m); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(p.Policies))
	for k := range p.Policies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Pack policies are defaults: never overwrite an owner's choice.
		if _, err := tx.Exec(`INSERT INTO policies(project_id,key,value_json,updated_at) VALUES(?,?,?,?)
			ON CONFLICT(project_id,key) DO NOTHING`, pid, k, mustJSON(p.Policies[k]), now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO packs(project_id,key,name,version,manifest_json,installed_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(project_id,key) DO UPDATE SET name=excluded.name, version=excluded.version,
		manifest_json=excluded.manifest_json, installed_at=excluded.installed_at`,
		pid, p.Key, p.Name, p.Version, mustJSON(p), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]int{"work_types": len(p.WorkTypes), "forms": len(p.Forms), "rate_items": len(p.RateItems),
		"modifiers": len(p.Modifiers), "policies": len(p.Policies)}, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertWorkType(db execer, pid string, wt WorkType) error {
	_, err := db.Exec(`INSERT INTO work_types(project_id,pack_key,key,name,mode,phases_json,statuses_json,required_forms_json,
		intake_form_key,default_duration_min,buffer_min,requirements_json,priority_rules_json,sla_json,quote_policy_json,playbook,active,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,key) DO UPDATE SET pack_key=excluded.pack_key,name=excluded.name,mode=excluded.mode,
		phases_json=excluded.phases_json,statuses_json=excluded.statuses_json,required_forms_json=excluded.required_forms_json,
		intake_form_key=excluded.intake_form_key,default_duration_min=excluded.default_duration_min,buffer_min=excluded.buffer_min,
		requirements_json=excluded.requirements_json,priority_rules_json=excluded.priority_rules_json,sla_json=excluded.sla_json,
		quote_policy_json=excluded.quote_policy_json,playbook=excluded.playbook,active=excluded.active,updated_at=excluded.updated_at`,
		pid, wt.PackKey, wt.Key, wt.Name, wt.Mode, mustJSON(wt.Phases), jsonOr(wt.Statuses, "[]"), jsonOr(wt.RequiredForms, "{}"),
		wt.IntakeFormKey, wt.DefaultDurationMin, wt.BufferMin, jsonOr(wt.Requirements, "[]"), jsonOr(wt.PriorityRules, "[]"),
		jsonOr(wt.SLA, "{}"), mustJSON(wt.QuotePolicy), wt.Playbook, boolInt(wt.Active), nowUTC())
	return err
}

func upsertForm(db execer, pid string, f Form) error {
	_, err := db.Exec(`INSERT INTO forms(project_id,pack_key,key,version,name,purpose,schema_json,updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,key) DO UPDATE SET pack_key=excluded.pack_key,
		version=CASE WHEN forms.schema_json<>excluded.schema_json THEN MAX(forms.version+1, excluded.version) ELSE forms.version END,
		name=excluded.name,purpose=excluded.purpose,schema_json=excluded.schema_json,updated_at=excluded.updated_at`,
		pid, f.PackKey, f.Key, f.Version, f.Name, f.Purpose, mustJSON(f.Schema), nowUTC())
	return err
}

func upsertRateItem(db execer, pid string, ri RateItem) error {
	var catalog any
	if ri.CatalogPriceID != nil {
		catalog = *ri.CatalogPriceID
	}
	_, err := db.Exec(`INSERT INTO rate_items(project_id,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,
		requirements_json,materials_json,active,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,code) DO UPDATE SET pack_key=excluded.pack_key,name=excluded.name,catalog_price_id=excluded.catalog_price_id,
		pricing_json=excluded.pricing_json,cost_cents=excluded.cost_cents,duration_json=excluded.duration_json,
		requirements_json=excluded.requirements_json,materials_json=excluded.materials_json,active=excluded.active,updated_at=excluded.updated_at`,
		pid, ri.PackKey, ri.Code, ri.Name, catalog, mustJSON(ri.Pricing), ri.CostCents, mustJSON(ri.Duration),
		jsonOr(ri.Requirements, "[]"), jsonOr(ri.Materials, "[]"), boolInt(ri.Active), nowUTC())
	return err
}

func upsertModifier(db execer, pid string, m Modifier) error {
	_, err := db.Exec(`INSERT INTO modifiers(project_id,pack_key,key,name,kind,when_json,amount_json,active,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,key) DO UPDATE SET pack_key=excluded.pack_key,name=excluded.name,kind=excluded.kind,
		when_json=excluded.when_json,amount_json=excluded.amount_json,active=excluded.active,updated_at=excluded.updated_at`,
		pid, m.PackKey, m.Key, m.Name, m.Kind, mustJSON(m.When), mustJSON(m.Amount), boolInt(m.Active), nowUTC())
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const workTypeCols = `id,pack_key,key,name,mode,phases_json,statuses_json,required_forms_json,intake_form_key,default_duration_min,
	buffer_min,requirements_json,priority_rules_json,sla_json,quote_policy_json,playbook,active,updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanWorkType(s scanner) (*WorkType, error) {
	var wt WorkType
	var phases, statuses, forms, reqs, rules, sla, qp string
	var active int
	if err := s.Scan(&wt.ID, &wt.PackKey, &wt.Key, &wt.Name, &wt.Mode, &phases, &statuses, &forms, &wt.IntakeFormKey,
		&wt.DefaultDurationMin, &wt.BufferMin, &reqs, &rules, &sla, &qp, &wt.Playbook, &active, &wt.UpdatedAt); err != nil {
		return nil, err
	}
	decodeInto(phases, &wt.Phases)
	decodeInto(statuses, &wt.Statuses)
	decodeInto(forms, &wt.RequiredForms)
	decodeInto(reqs, &wt.Requirements)
	decodeInto(rules, &wt.PriorityRules)
	decodeInto(sla, &wt.SLA)
	decodeInto(qp, &wt.QuotePolicy)
	wt.Active = active == 1
	return &wt, nil
}

func getWorkType(db *sql.DB, pid string, id int64, key string) (*WorkType, error) {
	if id > 0 {
		return scanWorkType(db.QueryRow(`SELECT `+workTypeCols+` FROM work_types WHERE project_id=? AND id=?`, pid, id))
	}
	return scanWorkType(db.QueryRow(`SELECT `+workTypeCols+` FROM work_types WHERE project_id=? AND key=?`, pid, key))
}

func listWorkTypes(db *sql.DB, pid string) ([]*WorkType, error) {
	rows, err := db.Query(`SELECT `+workTypeCols+` FROM work_types WHERE project_id=? ORDER BY pack_key, key`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorkType{}
	for rows.Next() {
		wt, err := scanWorkType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wt)
	}
	return out, rows.Err()
}

func scanForm(s scanner) (*Form, error) {
	var f Form
	var schema string
	if err := s.Scan(&f.ID, &f.PackKey, &f.Key, &f.Version, &f.Name, &f.Purpose, &schema, &f.UpdatedAt); err != nil {
		return nil, err
	}
	decodeInto(schema, &f.Schema)
	return &f, nil
}

func getForm(db *sql.DB, pid, key string) (*Form, error) {
	return scanForm(db.QueryRow(`SELECT id,pack_key,key,version,name,purpose,schema_json,updated_at FROM forms WHERE project_id=? AND key=?`, pid, key))
}

func listForms(db *sql.DB, pid string) ([]*Form, error) {
	rows, err := db.Query(`SELECT id,pack_key,key,version,name,purpose,schema_json,updated_at FROM forms WHERE project_id=? ORDER BY pack_key,key`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Form{}
	for rows.Next() {
		f, err := scanForm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

const rateItemCols = `id,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,requirements_json,materials_json,active,updated_at`

func scanRateItem(s scanner) (*RateItem, error) {
	var ri RateItem
	var catalog sql.NullInt64
	var pricing, dur, reqs, mats string
	var active int
	if err := s.Scan(&ri.ID, &ri.PackKey, &ri.Code, &ri.Name, &catalog, &pricing, &ri.CostCents, &dur, &reqs, &mats, &active, &ri.UpdatedAt); err != nil {
		return nil, err
	}
	if catalog.Valid {
		ri.CatalogPriceID = &catalog.Int64
	}
	decodeInto(pricing, &ri.Pricing)
	decodeInto(dur, &ri.Duration)
	decodeInto(reqs, &ri.Requirements)
	decodeInto(mats, &ri.Materials)
	ri.Active = active == 1
	return &ri, nil
}

func getRateItem(db *sql.DB, pid string, id int64, code string) (*RateItem, error) {
	if id > 0 {
		return scanRateItem(db.QueryRow(`SELECT `+rateItemCols+` FROM rate_items WHERE project_id=? AND id=?`, pid, id))
	}
	return scanRateItem(db.QueryRow(`SELECT `+rateItemCols+` FROM rate_items WHERE project_id=? AND code=?`, pid, code))
}

func listRateItems(db *sql.DB, pid, q, pack string) ([]*RateItem, error) {
	query := `SELECT ` + rateItemCols + ` FROM rate_items WHERE project_id=? AND active=1`
	args := []any{pid}
	if q != "" {
		query += ` AND (lower(name) LIKE ? OR lower(code) LIKE ?)`
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	if pack != "" {
		query += ` AND pack_key=?`
		args = append(args, pack)
	}
	rows, err := db.Query(query+` ORDER BY pack_key, code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RateItem{}
	for rows.Next() {
		ri, err := scanRateItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

func listModifiers(db *sql.DB, pid string) ([]*Modifier, error) {
	rows, err := db.Query(`SELECT id,pack_key,key,name,kind,when_json,amount_json,active,updated_at FROM modifiers WHERE project_id=? ORDER BY key`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Modifier{}
	for rows.Next() {
		var m Modifier
		var when, amount string
		var active int
		if err := rows.Scan(&m.ID, &m.PackKey, &m.Key, &m.Name, &m.Kind, &when, &amount, &active, &m.UpdatedAt); err != nil {
			return nil, err
		}
		decodeInto(when, &m.When)
		decodeInto(amount, &m.Amount)
		m.Active = active == 1
		out = append(out, &m)
	}
	return out, rows.Err()
}

func listPolicies(db *sql.DB, pid string) (map[string]any, error) {
	rows, err := db.Query(`SELECT key,value_json FROM policies WHERE project_id=?`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var val any
		decodeInto(v, &val)
		out[k] = val
	}
	return out, rows.Err()
}

func policyInt(db *sql.DB, pid, key string, def int64) int64 {
	var raw string
	if err := db.QueryRow(`SELECT value_json FROM policies WHERE project_id=? AND key=?`, pid, key).Scan(&raw); err != nil {
		return def
	}
	var f float64
	if json.Unmarshal([]byte(raw), &f) != nil {
		return def
	}
	return int64(f)
}

func setPolicy(db *sql.DB, pid, key string, value any) error {
	_, err := db.Exec(`INSERT INTO policies(project_id,key,value_json,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(project_id,key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at`,
		pid, key, mustJSON(value), nowUTC())
	return err
}

func listInstalledPacks(db *sql.DB, pid string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT key,name,version,installed_at FROM packs WHERE project_id=? ORDER BY key`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k, n, v, at string
		if err := rows.Scan(&k, &n, &v, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"key": k, "name": n, "version": v, "installed_at": at})
	}
	return out, rows.Err()
}
