package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const orderSchema = "apteva-pcb-order/v1"

// Order tracks one fabrication order for a revision. PCB Studio prepares and
// verifies the package; placing the order happens at the fab (no fab in the
// integration catalog publishes a quote or order API yet), and the result
// is recorded back with pcb_order_update so the release history stays
// complete.
type Order struct {
	ID                int64           `json:"id"`
	DesignID          int64           `json:"design_id"`
	RevisionID        int64           `json:"revision_id"`
	Fabricator        string          `json:"fabricator"`
	Quantity          int             `json:"quantity"`
	Assembly          bool            `json:"assembly"`
	Options           json.RawMessage `json:"options"`
	Status            string          `json:"status"`
	PackageArtifactID *int64          `json:"package_artifact_id,omitempty"`
	ExternalOrderID   string          `json:"external_order_id,omitempty"`
	QuoteAmount       *float64        `json:"quote_amount,omitempty"`
	QuoteCurrency     string          `json:"quote_currency,omitempty"`
	Tracking          string          `json:"tracking,omitempty"`
	Note              string          `json:"note,omitempty"`
	History           json.RawMessage `json:"history"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type OrderOptions struct {
	Fabricator     string `json:"fabricator"`
	Quantity       int    `json:"quantity"`
	Assembly       bool   `json:"assembly,omitempty"`
	SurfaceFinish  string `json:"surface_finish,omitempty"`
	SolderMask     string `json:"solder_mask,omitempty"`
	CopperWeightOz int    `json:"copper_weight_oz,omitempty"`
	Note           string `json:"note,omitempty"`
}

type OrderUpdate struct {
	Status          string   `json:"status,omitempty"`
	ExternalOrderID string   `json:"external_order_id,omitempty"`
	QuoteAmount     *float64 `json:"quote_amount,omitempty"`
	QuoteCurrency   string   `json:"quote_currency,omitempty"`
	Tracking        string   `json:"tracking,omitempty"`
	Note            string   `json:"note,omitempty"`
}

var orderTransitions = map[string][]string{
	"prepared":      {"quoted", "placed", "cancelled"},
	"quoted":        {"quoted", "placed", "cancelled"},
	"placed":        {"in_production", "shipped", "cancelled"},
	"in_production": {"shipped", "cancelled"},
	"shipped":       {"delivered"},
	"delivered":     {},
	"cancelled":     {},
}

var fabricatorUploadPages = map[string]string{
	"jlcpcb":  "https://cart.jlcpcb.com/quote",
	"pcbway":  "https://www.pcbway.com/orderonline.aspx",
	"oshpark": "https://oshpark.com/",
}

// orderPackageFiles builds the files a fab needs for one order: Gerbers and
// drills, plus centroid and assembly BOM when the order includes assembly,
// and an order.json describing the requested options.
func orderPackageFiles(def *Definition, design *Design, revision *Revision, options OrderOptions) (map[string][]byte, error) {
	files := map[string][]byte{}
	gerbers := manufacturingFiles(def)
	verification := verifyManufacturingFiles(def, gerbers)
	if verification.Status == "failed" {
		return nil, fmt.Errorf("independent fabrication verification failed with %d errors; order package refused", verification.Errors)
	}
	for name, body := range gerbers {
		files["fabrication/"+name] = body
	}
	if options.Assembly {
		assembly := assemblyFiles(def)
		if checks := verifyPlacementCSV(def, assembly["assembly/pick-and-place.csv"]); len(checks) > 0 {
			return nil, fmt.Errorf("pick-and-place verification failed: %s", checks[0].Message)
		}
		for name, body := range assembly {
			files[name] = body
		}
	}
	profile := ""
	if p, ok := activeFabProfile(def); ok {
		profile = p.ID
	}
	spec := map[string]any{
		"schema": orderSchema, "design_id": design.ID, "design_name": design.Name, "revision_id": revision.ID, "revision_number": revision.Number, "source_sha256": revision.SourceSHA256,
		"fabricator": options.Fabricator, "fab_profile_id": profile, "quantity": options.Quantity, "assembly": options.Assembly,
		"board":          map[string]any{"width_mm": float64(def.Board.WidthNM) / 1e6, "height_mm": float64(def.Board.HeightNM) / 1e6, "thickness_mm": float64(def.Board.ThicknessNM) / 1e6, "copper_layers": copperLayerCount(def)},
		"surface_finish": options.SurfaceFinish, "solder_mask": options.SolderMask, "copper_weight_oz": options.CopperWeightOz, "note": strings.TrimSpace(options.Note),
	}
	body, _ := json.MarshalIndent(spec, "", "  ")
	files["order.json"] = append(body, '\n')
	return files, nil
}

func normalizeOrderOptions(options OrderOptions) (OrderOptions, error) {
	options.Fabricator = strings.ToLower(strings.TrimSpace(options.Fabricator))
	if options.Fabricator == "" {
		return options, errors.New("fabricator is required (for example jlcpcb, pcbway, oshpark)")
	}
	if !idValid(options.Fabricator) {
		return options, fmt.Errorf("fabricator %q is not a valid identifier", options.Fabricator)
	}
	if options.Quantity <= 0 || options.Quantity > 100_000 {
		return options, errors.New("quantity must be between 1 and 100000")
	}
	if options.CopperWeightOz < 0 || options.CopperWeightOz > 6 {
		return options, errors.New("copper_weight_oz must be between 0 and 6")
	}
	if len(options.Note) > 2_000 || len(options.SurfaceFinish) > 60 || len(options.SolderMask) > 60 {
		return options, errors.New("order text fields exceed limits")
	}
	return options, nil
}

// ---- service ----

func (s *Service) OrderPrepare(designID, revisionID int64, options OrderOptions) (*Order, *Artifact, error) {
	options, err := normalizeOrderOptions(options)
	if err != nil {
		return nil, nil, err
	}
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	report := validateDefinition(def)
	if report.Status == "failed" {
		return nil, nil, fmt.Errorf("revision failed validation with %d errors; order package refused", report.Errors)
	}
	files, err := orderPackageFiles(def, design, revision, options)
	if err != nil {
		return nil, nil, err
	}
	body, err := deterministicZip(files)
	if err != nil {
		return nil, nil, err
	}
	artifact, err := s.persistArtifact(design, revision, "order-package", "zip", "application/zip", body, map[string]any{"schema": orderSchema, "fabricator": options.Fabricator, "quantity": options.Quantity, "assembly": options.Assembly})
	if artifact == nil {
		return nil, nil, err
	}
	optionsJSON, _ := json.Marshal(options)
	order, createErr := s.store.CreateOrder(s.project, design.ID, revision.ID, options, optionsJSON, artifact.ID)
	if createErr != nil {
		return nil, artifact, createErr
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.order.updated", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "order_id": order.ID, "status": order.Status, "fabricator": order.Fabricator})
	}
	return order, artifact, err
}

func (s *Service) OrderUpdate(orderID int64, update OrderUpdate, author string) (*Order, error) {
	order, err := s.store.GetOrder(s.project, orderID)
	if err != nil {
		return nil, err
	}
	next := strings.TrimSpace(update.Status)
	if next != "" && next != order.Status {
		allowed := false
		for _, candidate := range orderTransitions[order.Status] {
			allowed = allowed || candidate == next
		}
		if !allowed {
			return nil, fmt.Errorf("order %d cannot move from %s to %s", order.ID, order.Status, next)
		}
	}
	if next == "" {
		next = order.Status
	}
	if update.QuoteAmount != nil && *update.QuoteAmount < 0 {
		return nil, errors.New("quote_amount cannot be negative")
	}
	if len(update.Note) > 2_000 || len(update.Tracking) > 500 || len(update.ExternalOrderID) > 200 || len(update.QuoteCurrency) > 8 {
		return nil, errors.New("order update fields exceed limits")
	}
	updated, err := s.store.UpdateOrder(s.project, order, next, update, author)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.order.updated", map[string]any{"design_id": updated.DesignID, "revision_id": updated.RevisionID, "order_id": updated.ID, "status": updated.Status, "fabricator": updated.Fabricator})
	}
	return updated, err
}

// ---- store ----

const orderColumns = `id,design_id,revision_id,fabricator,quantity,assembly,options_json,status,package_artifact_id,external_order_id,quote_amount,quote_currency,tracking,note,history_json,created_at,updated_at`

func scanOrder(scan func(...any) error) (*Order, error) {
	var o Order
	var assembly int
	var artifact sql.NullInt64
	var quote sql.NullFloat64
	if err := scan(&o.ID, &o.DesignID, &o.RevisionID, &o.Fabricator, &o.Quantity, &assembly, rawJSONScanner{&o.Options}, &o.Status, &artifact, &o.ExternalOrderID, &quote, &o.QuoteCurrency, &o.Tracking, &o.Note, rawJSONScanner{&o.History}, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.Assembly = assembly != 0
	if artifact.Valid {
		v := artifact.Int64
		o.PackageArtifactID = &v
	}
	if quote.Valid {
		v := quote.Float64
		o.QuoteAmount = &v
	}
	return &o, nil
}

func (s *Store) CreateOrder(project string, designID, revisionID int64, options OrderOptions, optionsJSON []byte, artifactID int64) (*Order, error) {
	history, _ := json.Marshal([]map[string]any{{"status": "prepared", "at": time.Now().UTC().Format(time.RFC3339)}})
	res, err := s.db.Exec(`INSERT INTO pcb_orders(project_id,design_id,revision_id,fabricator,quantity,assembly,options_json,status,package_artifact_id,note,history_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, project, designID, revisionID, options.Fabricator, options.Quantity, boolInt(options.Assembly), string(optionsJSON), "prepared", artifactID, strings.TrimSpace(options.Note), string(history))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetOrder(project, id)
}

func (s *Store) GetOrder(project string, id int64) (*Order, error) {
	row := s.db.QueryRow(`SELECT `+orderColumns+` FROM pcb_orders WHERE project_id=? AND id=?`, project, id)
	o, err := scanOrder(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("order %d: %w", id, errNotFound)
	}
	return o, err
}

func (s *Store) ListOrders(project string, designID int64) ([]Order, error) {
	rows, err := s.db.Query(`SELECT `+orderColumns+` FROM pcb_orders WHERE project_id=? AND design_id=? ORDER BY id DESC`, project, designID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Store) UpdateOrder(project string, order *Order, status string, update OrderUpdate, author string) (*Order, error) {
	var history []map[string]any
	_ = json.Unmarshal(order.History, &history)
	entry := map[string]any{"status": status, "at": time.Now().UTC().Format(time.RFC3339)}
	if author != "" {
		entry["by"] = author
	}
	if note := strings.TrimSpace(update.Note); note != "" {
		entry["note"] = note
	}
	history = append(history, entry)
	historyJSON, _ := json.Marshal(history)
	external := order.ExternalOrderID
	if v := strings.TrimSpace(update.ExternalOrderID); v != "" {
		external = v
	}
	var quote any
	if order.QuoteAmount != nil {
		quote = *order.QuoteAmount
	}
	if update.QuoteAmount != nil {
		quote = *update.QuoteAmount
	}
	currency := order.QuoteCurrency
	if v := strings.ToUpper(strings.TrimSpace(update.QuoteCurrency)); v != "" {
		currency = v
	}
	tracking := order.Tracking
	if v := strings.TrimSpace(update.Tracking); v != "" {
		tracking = v
	}
	if _, err := s.db.Exec(`UPDATE pcb_orders SET status=?,external_order_id=?,quote_amount=?,quote_currency=?,tracking=?,history_json=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, external, quote, currency, tracking, string(historyJSON), project, order.ID); err != nil {
		return nil, err
	}
	return s.GetOrder(project, order.ID)
}
