// Inventory is a reusable stock ledger for commerce, orders, and
// fulfillment apps. It owns quantities and reservations, while Catalog
// owns product/price identity and Orders owns fulfillment state.
package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("inventory requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("inventory mounted", "project_id", projectScope(ctx))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "reservation-expiration",
		Schedule: "@every 1m",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			return expireReservations(ctx)
		},
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/summary", Handler: a.handleSummary},
		{Pattern: "/locations", Handler: a.handleLocations},
		{Pattern: "/locations/", Handler: a.handleLocation},
		{Pattern: "/items", Handler: a.handleItems},
		{Pattern: "/items/", Handler: a.handleItem},
		{Pattern: "/levels", Handler: a.handleLevels},
		{Pattern: "/availability", Handler: a.handleAvailability},
		{Pattern: "/adjust", Handler: a.handleAdjust},
		{Pattern: "/receive", Handler: a.handleReceive},
		{Pattern: "/transfer", Handler: a.handleTransfer},
		{Pattern: "/reserve", Handler: a.handleReserve},
		{Pattern: "/reservations", Handler: a.handleReservations},
		{Pattern: "/reservations/", Handler: a.handleReservationAction},
		{Pattern: "/movements", Handler: a.handleMovements},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "inventory_locations_create", Description: "Create a stock location. Args: name, code?, type?, address?, active?, metadata?.", InputSchema: schemaObject(locationProps(), []string{"name"}), Handler: a.toolLocationsCreate},
		{Name: "inventory_locations_list", Description: "List stock locations. Args: active?.", InputSchema: schemaObject(map[string]any{"active": typ("boolean")}, nil), Handler: a.toolLocationsList},
		{Name: "inventory_locations_get", Description: "Fetch one location. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolLocationsGet},
		{Name: "inventory_locations_update", Description: "Patch a location. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolLocationsUpdate},
		{Name: "inventory_items_create", Description: "Create an inventory item/SKU. Args: sku, name, catalog_product_id?, catalog_price_id?, barcode?, unit?, track_quantity?, allow_backorder?, metadata?.", InputSchema: schemaObject(itemProps(), []string{"sku", "name"}), Handler: a.toolItemsCreate},
		{Name: "inventory_items_list", Description: "List inventory items with aggregate stock. Args: q?, include_archived?, limit?.", InputSchema: schemaObject(map[string]any{"q": typ("string"), "include_archived": typ("boolean"), "limit": typ("integer")}, nil), Handler: a.toolItemsList},
		{Name: "inventory_items_get", Description: "Fetch one item with levels and active reservations. Args: id? or sku?.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "sku": typ("string")}, nil), Handler: a.toolItemsGet},
		{Name: "inventory_items_update", Description: "Patch an inventory item. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolItemsUpdate},
		{Name: "inventory_levels_get", Description: "List stock levels. Args: item_id?, sku?, location_id?, low_stock_only?.", InputSchema: schemaObject(levelFilterProps(), nil), Handler: a.toolLevelsGet},
		{Name: "inventory_availability_check", Description: "Check available quantity for an item. Args: item_id? or sku, quantity?, location_id?.", InputSchema: schemaObject(availabilityProps(), nil), Handler: a.toolAvailabilityCheck},
		{Name: "inventory_adjust", Description: "Adjust on-hand stock. Args: item_id? or sku, location_id, quantity_delta, reason?, actor?, reference_*?, metadata?.", InputSchema: schemaObject(adjustProps(), []string{"location_id", "quantity_delta"}), Handler: a.toolAdjust},
		{Name: "inventory_receive", Description: "Receive incoming stock. Args: item_id? or sku, location_id, quantity, reason?, actor?, reference_*?, metadata?.", InputSchema: schemaObject(receiveProps(), []string{"location_id", "quantity"}), Handler: a.toolReceive},
		{Name: "inventory_transfer", Description: "Move on-hand stock between locations. Args: item_id? or sku, from_location_id, to_location_id, quantity, reason?, actor?, reference_*?, metadata?.", InputSchema: schemaObject(transferProps(), []string{"from_location_id", "to_location_id", "quantity"}), Handler: a.toolTransfer},
		{Name: "inventory_reserve", Description: "Reserve available stock. Args: item_id? or sku, quantity, location_id?, reference_app?, reference_type?, reference_id?, expires_at?, metadata?.", InputSchema: schemaObject(reserveProps(), []string{"quantity"}), Handler: a.toolReserve},
		{Name: "inventory_release_reservation", Description: "Release one active reservation. Args: reservation_id? or reference_app/reference_type/reference_id.", InputSchema: schemaObject(reservationRefProps(), nil), Handler: a.toolReleaseReservation},
		{Name: "inventory_commit_reservation", Description: "Commit one active reservation, reducing on-hand stock. Args: reservation_id? or reference_app/reference_type/reference_id.", InputSchema: schemaObject(reservationRefProps(), nil), Handler: a.toolCommitReservation},
		{Name: "inventory_reservations_list", Description: "List reservations. Args: item_id?, location_id?, status?, reference_app?, reference_type?, reference_id?, limit?.", InputSchema: schemaObject(reservationFilterProps(), nil), Handler: a.toolReservationsList},
		{Name: "inventory_movements_list", Description: "List movement audit rows. Args: item_id?, location_id?, reference_app?, reference_type?, reference_id?, limit?.", InputSchema: schemaObject(movementFilterProps(), nil), Handler: a.toolMovementsList},
	}
}

type Location struct {
	ID        int64          `json:"id"`
	Code      string         `json:"code"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Address   map[string]any `json:"address,omitempty"`
	Active    bool           `json:"active"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

type Item struct {
	ID               int64          `json:"id"`
	SKU              string         `json:"sku"`
	Name             string         `json:"name"`
	CatalogProductID *int64         `json:"catalog_product_id,omitempty"`
	CatalogPriceID   *int64         `json:"catalog_price_id,omitempty"`
	Barcode          string         `json:"barcode"`
	Unit             string         `json:"unit"`
	TrackQuantity    bool           `json:"track_quantity"`
	AllowBackorder   bool           `json:"allow_backorder"`
	Archived         bool           `json:"archived"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	OnHand           float64        `json:"on_hand"`
	Reserved         float64        `json:"reserved"`
	Available        float64        `json:"available"`
	LocationCount    int            `json:"location_count"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type Level struct {
	ID            int64   `json:"id"`
	ItemID        int64   `json:"item_id"`
	SKU           string  `json:"sku"`
	ItemName      string  `json:"item_name"`
	LocationID    int64   `json:"location_id"`
	LocationCode  string  `json:"location_code"`
	LocationName  string  `json:"location_name"`
	OnHand        float64 `json:"on_hand"`
	Reserved      float64 `json:"reserved"`
	Available     float64 `json:"available"`
	Incoming      float64 `json:"incoming"`
	SafetyStock   float64 `json:"safety_stock"`
	AvailableOver float64 `json:"available_over_safety_stock"`
	UpdatedAt     string  `json:"updated_at"`
}

type Reservation struct {
	ID            int64          `json:"id"`
	ItemID        int64          `json:"item_id"`
	SKU           string         `json:"sku"`
	ItemName      string         `json:"item_name"`
	LocationID    int64          `json:"location_id"`
	LocationCode  string         `json:"location_code"`
	LocationName  string         `json:"location_name"`
	Quantity      float64        `json:"quantity"`
	Status        string         `json:"status"`
	ReferenceApp  string         `json:"reference_app"`
	ReferenceType string         `json:"reference_type"`
	ReferenceID   string         `json:"reference_id"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	ExpiresAt     string         `json:"expires_at,omitempty"`
	CommittedAt   string         `json:"committed_at,omitempty"`
	ReleasedAt    string         `json:"released_at,omitempty"`
}

type Movement struct {
	ID            int64          `json:"id"`
	ItemID        int64          `json:"item_id"`
	SKU           string         `json:"sku"`
	ItemName      string         `json:"item_name"`
	LocationID    int64          `json:"location_id"`
	LocationCode  string         `json:"location_code"`
	LocationName  string         `json:"location_name"`
	Type          string         `json:"type"`
	QuantityDelta float64        `json:"quantity_delta"`
	OnHandAfter   float64        `json:"on_hand_after"`
	ReservedAfter float64        `json:"reserved_after"`
	ReferenceApp  string         `json:"reference_app"`
	ReferenceType string         `json:"reference_type"`
	ReferenceID   string         `json:"reference_id"`
	Reason        string         `json:"reason"`
	Actor         string         `json:"actor"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

func (a *App) toolLocationsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx)
	loc, err := createLocation(ctx.AppDB(), pid, args)
	return map[string]any{"location": loc}, err
}

func (a *App) toolLocationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	locs, err := listLocations(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"locations": locs, "count": len(locs)}, err
}

func (a *App) toolLocationsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	loc, err := getLocation(ctx.AppDB(), projectScope(ctx), intArg(args, "id"))
	if err != nil || loc == nil {
		return nil, firstErr(err, errors.New("location not found"))
	}
	return map[string]any{"location": loc}, nil
}

func (a *App) toolLocationsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	loc, err := updateLocation(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"location": loc}, err
}

func (a *App) toolItemsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := createItem(ctx.AppDB(), projectScope(ctx), args)
	if err == nil {
		ctx.Emit("inventory.item.created", map[string]any{"id": item.ID, "sku": item.SKU, "name": item.Name})
	}
	return map[string]any{"item": item}, err
}

func (a *App) toolItemsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	items, err := listItems(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"items": items, "count": len(items)}, err
}

func (a *App) toolItemsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := resolveItem(ctx.AppDB(), projectScope(ctx), args)
	if err != nil {
		return nil, err
	}
	levels, err := listLevels(ctx.AppDB(), projectScope(ctx), map[string]any{"item_id": item.ID})
	if err != nil {
		return nil, err
	}
	res, err := listReservations(ctx.AppDB(), projectScope(ctx), map[string]any{"item_id": item.ID, "status": "active"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": item, "levels": levels, "reservations": res}, nil
}

func (a *App) toolItemsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := updateItem(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"item": item}, err
}

func (a *App) toolLevelsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	levels, err := listLevels(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"levels": levels, "count": len(levels)}, err
}

func (a *App) toolAvailabilityCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := availabilityCheck(ctx.AppDB(), projectScope(ctx), args)
	return out, err
}

func (a *App) toolAdjust(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := adjustStock(ctx.AppDB(), projectScope(ctx), "adjust", args)
	emitLevel(ctx, out)
	return out, err
}

func (a *App) toolReceive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cp := copyMap(args)
	cp["quantity_delta"] = numArg(args, "quantity")
	if strArg(cp, "reason") == "" {
		cp["reason"] = "received"
	}
	out, err := adjustStock(ctx.AppDB(), projectScope(ctx), "receive", cp)
	emitLevel(ctx, out)
	return out, err
}

func (a *App) toolTransfer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := transferStock(ctx.AppDB(), projectScope(ctx), args)
	emitLevel(ctx, out)
	return out, err
}

func (a *App) toolReserve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	input := copyMap(args)
	if strings.TrimSpace(strArg(input, "expires_at")) == "" {
		ttlMinutes := inventoryConfigInt(ctx, "reservation_ttl_minutes", 60)
		input["expires_at"] = time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute).Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, strArg(input, "expires_at")); err != nil {
		return nil, errors.New("expires_at must be RFC3339")
	}
	out, err := reserveStock(ctx.AppDB(), projectScope(ctx), input)
	if err == nil {
		if r, ok := out["reservation"].(*Reservation); ok {
			ctx.Emit("inventory.reservation.created", map[string]any{"reservation_id": r.ID, "item_id": r.ItemID, "location_id": r.LocationID, "quantity": r.Quantity})
		}
	}
	return out, err
}

func (a *App) toolReleaseReservation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := finishReservation(ctx.AppDB(), projectScope(ctx), args, "released")
	if err == nil {
		if r, ok := out["reservation"].(*Reservation); ok {
			ctx.Emit("inventory.reservation.released", map[string]any{"reservation_id": r.ID, "item_id": r.ItemID, "location_id": r.LocationID, "quantity": r.Quantity})
		}
	}
	return out, err
}

func (a *App) toolCommitReservation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := finishReservation(ctx.AppDB(), projectScope(ctx), args, "committed")
	if err == nil {
		if r, ok := out["reservation"].(*Reservation); ok {
			ctx.Emit("inventory.reservation.committed", map[string]any{"reservation_id": r.ID, "item_id": r.ItemID, "location_id": r.LocationID, "quantity": r.Quantity})
		}
	}
	return out, err
}

func (a *App) toolReservationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	res, err := listReservations(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"reservations": res, "count": len(res)}, err
}

func (a *App) toolMovementsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	mov, err := listMovements(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"movements": mov, "count": len(mov)}, err
}

func createLocation(db *sql.DB, pid string, args map[string]any) (*Location, error) {
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	code := normalizeCode(firstNonEmpty(strArg(args, "code"), name))
	typ := firstNonEmpty(strArg(args, "type"), "warehouse")
	active := true
	if _, ok := args["active"]; ok {
		active = boolArg(args, "active")
	}
	res, err := db.Exec(`INSERT INTO inventory_locations
		(project_id, code, name, type, address_json, active, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, code, name, typ, jsonText(args["address"], "{}"), boolToInt(active), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getLocation(db, pid, id)
}

func listLocations(db *sql.DB, pid string, args map[string]any) ([]*Location, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	if _, ok := args["active"]; ok {
		where = append(where, "active=?")
		vals = append(vals, boolToInt(boolArg(args, "active")))
	}
	rows, err := db.Query(`SELECT id, code, name, type, address_json, active, metadata_json, created_at, updated_at
		FROM inventory_locations WHERE `+strings.Join(where, " AND ")+` ORDER BY active DESC, name`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Location
	for rows.Next() {
		loc, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, loc)
	}
	return out, rows.Err()
}

func getLocation(db *sql.DB, pid string, id int64) (*Location, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	row := db.QueryRow(`SELECT id, code, name, type, address_json, active, metadata_json, created_at, updated_at
		FROM inventory_locations WHERE project_id=? AND id=?`, pid, id)
	loc, err := scanLocation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return loc, err
}

func updateLocation(db *sql.DB, pid string, id int64, patch map[string]any) (*Location, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	sets := []string{}
	vals := []any{}
	for _, key := range []string{"name", "type"} {
		if v := strArg(patch, key); v != "" {
			sets = append(sets, key+"=?")
			vals = append(vals, v)
		}
	}
	if v := strArg(patch, "code"); v != "" {
		sets = append(sets, "code=?")
		vals = append(vals, normalizeCode(v))
	}
	if _, ok := patch["active"]; ok {
		sets = append(sets, "active=?")
		vals = append(vals, boolToInt(boolArg(patch, "active")))
	}
	if _, ok := patch["address"]; ok {
		sets = append(sets, "address_json=?")
		vals = append(vals, jsonText(patch["address"], "{}"))
	}
	if _, ok := patch["metadata"]; ok {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return getLocation(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE inventory_locations SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return getLocation(db, pid, id)
}

func createItem(db *sql.DB, pid string, args map[string]any) (*Item, error) {
	sku := normalizeSKU(strArg(args, "sku"))
	name := strings.TrimSpace(strArg(args, "name"))
	if sku == "" || name == "" {
		return nil, errors.New("sku and name required")
	}
	track := true
	if _, ok := args["track_quantity"]; ok {
		track = boolArg(args, "track_quantity")
	}
	res, err := db.Exec(`INSERT INTO inventory_items
		(project_id, sku, name, catalog_product_id, catalog_price_id, barcode, unit, track_quantity, allow_backorder, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, sku, name, nullableInt(intArg(args, "catalog_product_id")), nullableInt(intArg(args, "catalog_price_id")),
		strArg(args, "barcode"), firstNonEmpty(strArg(args, "unit"), "each"), boolToInt(track), boolToInt(boolArg(args, "allow_backorder")),
		jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getItemByID(db, pid, id)
}

func listItems(db *sql.DB, pid string, args map[string]any) ([]*Item, error) {
	where := []string{"i.project_id=?"}
	vals := []any{pid}
	if !boolArg(args, "include_archived") {
		where = append(where, "i.archived=0")
	}
	if q := strings.TrimSpace(strArg(args, "q")); q != "" {
		like := "%" + q + "%"
		where = append(where, "(i.sku LIKE ? OR i.name LIKE ? OR i.barcode LIKE ?)")
		vals = append(vals, like, like, like)
	}
	limit := clamp(intArg(args, "limit"), 1, 500, 100)
	vals = append(vals, limit)
	rows, err := db.Query(itemSelect()+` WHERE `+strings.Join(where, " AND ")+`
		GROUP BY i.id ORDER BY i.archived ASC, i.name ASC LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func resolveItem(db *sql.DB, pid string, args map[string]any) (*Item, error) {
	if id := intArg(args, "item_id"); id != 0 {
		return requireItem(db, pid, id)
	}
	if id := intArg(args, "id"); id != 0 {
		return requireItem(db, pid, id)
	}
	if sku := strArg(args, "sku"); sku != "" {
		it, err := getItemBySKU(db, pid, sku)
		if err != nil || it == nil {
			return nil, firstErr(err, fmt.Errorf("item sku %q not found", sku))
		}
		return it, nil
	}
	return nil, errors.New("item_id, id, or sku required")
}

func requireItem(db *sql.DB, pid string, id int64) (*Item, error) {
	it, err := getItemByID(db, pid, id)
	if err != nil || it == nil {
		return nil, firstErr(err, errors.New("item not found"))
	}
	return it, nil
}

func getItemByID(db *sql.DB, pid string, id int64) (*Item, error) {
	row := db.QueryRow(itemSelect()+` WHERE i.project_id=? AND i.id=? GROUP BY i.id`, pid, id)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

func getItemBySKU(db *sql.DB, pid, sku string) (*Item, error) {
	row := db.QueryRow(itemSelect()+` WHERE i.project_id=? AND i.sku=? GROUP BY i.id`, pid, normalizeSKU(sku))
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

func updateItem(db *sql.DB, pid string, id int64, patch map[string]any) (*Item, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	sets := []string{}
	vals := []any{}
	for _, key := range []string{"name", "barcode", "unit"} {
		if v := strArg(patch, key); v != "" {
			sets = append(sets, key+"=?")
			vals = append(vals, v)
		}
	}
	if v := strArg(patch, "sku"); v != "" {
		sets = append(sets, "sku=?")
		vals = append(vals, normalizeSKU(v))
	}
	for _, key := range []string{"catalog_product_id", "catalog_price_id"} {
		if _, ok := patch[key]; ok {
			sets = append(sets, key+"=?")
			vals = append(vals, nullableInt(intArg(patch, key)))
		}
	}
	for _, key := range []string{"track_quantity", "allow_backorder", "archived"} {
		if _, ok := patch[key]; ok {
			sets = append(sets, key+"=?")
			vals = append(vals, boolToInt(boolArg(patch, key)))
		}
	}
	if _, ok := patch["metadata"]; ok {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return getItemByID(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE inventory_items SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return getItemByID(db, pid, id)
}

func listLevels(db *sql.DB, pid string, args map[string]any) ([]*Level, error) {
	where := []string{"l.project_id=?"}
	vals := []any{pid}
	if id := firstNonZero(intArg(args, "item_id"), intArg(args, "id")); id != 0 {
		where = append(where, "l.item_id=?")
		vals = append(vals, id)
	} else if sku := strArg(args, "sku"); sku != "" {
		where = append(where, "i.sku=?")
		vals = append(vals, normalizeSKU(sku))
	}
	if loc := intArg(args, "location_id"); loc != 0 {
		where = append(where, "l.location_id=?")
		vals = append(vals, loc)
	}
	if boolArg(args, "low_stock_only") {
		where = append(where, "(l.on_hand - l.reserved) <= l.safety_stock")
	}
	rows, err := db.Query(levelSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY i.name, loc.name`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Level
	for rows.Next() {
		l, err := scanLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func availabilityCheck(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	item, err := resolveItem(db, pid, args)
	if err != nil {
		return nil, err
	}
	levels, err := listLevels(db, pid, map[string]any{"item_id": item.ID, "location_id": intArg(args, "location_id")})
	if err != nil {
		return nil, err
	}
	available := 0.0
	for _, l := range levels {
		if l.AvailableOver > 0 {
			available += l.AvailableOver
		}
	}
	quantity := numArg(args, "quantity")
	return map[string]any{
		"item":        item,
		"levels":      levels,
		"available":   round4(available),
		"quantity":    quantity,
		"can_reserve": item.AllowBackorder || quantity <= 0 || available+1e-9 >= quantity,
	}, nil
}

func adjustStock(db *sql.DB, pid, typ string, args map[string]any) (map[string]any, error) {
	item, err := resolveItem(db, pid, args)
	if err != nil {
		return nil, err
	}
	locID := intArg(args, "location_id")
	if locID == 0 {
		return nil, errors.New("location_id required")
	}
	delta := numArg(args, "quantity_delta")
	if delta == 0 {
		return nil, errors.New("quantity_delta must be non-zero")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureLevelTx(tx, pid, item.ID, locID); err != nil {
		return nil, err
	}
	lvl, err := getLevelForUpdate(tx, pid, item.ID, locID)
	if err != nil {
		return nil, err
	}
	nextOnHand := lvl.OnHand + delta
	if nextOnHand < -1e-9 {
		return nil, fmt.Errorf("adjustment would make on_hand negative: %.4f", nextOnHand)
	}
	if _, err := tx.Exec(`UPDATE inventory_levels SET on_hand=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND item_id=? AND location_id=?`, round4(nextOnHand), pid, item.ID, locID); err != nil {
		return nil, err
	}
	if err := insertMovementTx(tx, pid, item.ID, locID, typ, delta, nextOnHand, lvl.Reserved, args); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	level, err := getOneLevel(db, pid, item.ID, locID)
	return map[string]any{"item": item, "level": level}, err
}

func transferStock(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	item, err := resolveItem(db, pid, args)
	if err != nil {
		return nil, err
	}
	fromID, toID := intArg(args, "from_location_id"), intArg(args, "to_location_id")
	qty := numArg(args, "quantity")
	if fromID == 0 || toID == 0 || fromID == toID {
		return nil, errors.New("from_location_id and to_location_id must be different")
	}
	if qty <= 0 {
		return nil, errors.New("quantity must be positive")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureLevelTx(tx, pid, item.ID, fromID); err != nil {
		return nil, err
	}
	if err := ensureLevelTx(tx, pid, item.ID, toID); err != nil {
		return nil, err
	}
	from, err := getLevelForUpdate(tx, pid, item.ID, fromID)
	if err != nil {
		return nil, err
	}
	if from.Available+1e-9 < qty && !item.AllowBackorder {
		return nil, fmt.Errorf("insufficient available stock at source: available %.4f, requested %.4f", from.Available, qty)
	}
	to, err := getLevelForUpdate(tx, pid, item.ID, toID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE inventory_levels SET on_hand=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND item_id=? AND location_id=?`, round4(from.OnHand-qty), pid, item.ID, fromID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE inventory_levels SET on_hand=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND item_id=? AND location_id=?`, round4(to.OnHand+qty), pid, item.ID, toID); err != nil {
		return nil, err
	}
	cp := copyMap(args)
	if strArg(cp, "reason") == "" {
		cp["reason"] = "transfer"
	}
	if err := insertMovementTx(tx, pid, item.ID, fromID, "transfer_out", -qty, from.OnHand-qty, from.Reserved, cp); err != nil {
		return nil, err
	}
	if err := insertMovementTx(tx, pid, item.ID, toID, "transfer_in", qty, to.OnHand+qty, to.Reserved, cp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	fromLevel, _ := getOneLevel(db, pid, item.ID, fromID)
	toLevel, _ := getOneLevel(db, pid, item.ID, toID)
	return map[string]any{"item": item, "from_level": fromLevel, "to_level": toLevel}, nil
}

func reserveStock(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	item, err := resolveItem(db, pid, args)
	if err != nil {
		return nil, err
	}
	qty := numArg(args, "quantity")
	if qty <= 0 {
		return nil, errors.New("quantity must be positive")
	}
	locID := intArg(args, "location_id")
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if locID == 0 {
		locID, err = chooseReservationLocationTx(tx, pid, item.ID, qty)
		if err != nil {
			return nil, err
		}
	}
	if err := ensureLevelTx(tx, pid, item.ID, locID); err != nil {
		return nil, err
	}
	lvl, err := getLevelForUpdate(tx, pid, item.ID, locID)
	if err != nil {
		return nil, err
	}
	availableOverSafety := lvl.AvailableOver
	if availableOverSafety < 0 {
		availableOverSafety = 0
	}
	if availableOverSafety+1e-9 < qty && !item.AllowBackorder {
		return nil, fmt.Errorf("insufficient available stock above safety stock: available %.4f, requested %.4f", availableOverSafety, qty)
	}
	res, err := tx.Exec(`INSERT INTO inventory_reservations
		(project_id, item_id, location_id, quantity, reference_app, reference_type, reference_id, metadata_json, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, item.ID, locID, qty, strArg(args, "reference_app"), strArg(args, "reference_type"), strArg(args, "reference_id"),
		jsonText(args["metadata"], "{}"), nullStr(strArg(args, "expires_at")))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	nextReserved := lvl.Reserved + qty
	if _, err := tx.Exec(`UPDATE inventory_levels SET reserved=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND item_id=? AND location_id=?`, round4(nextReserved), pid, item.ID, locID); err != nil {
		return nil, err
	}
	if err := insertMovementTx(tx, pid, item.ID, locID, "reserve", 0, lvl.OnHand, nextReserved, args); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r, err := getReservation(db, pid, id)
	level, _ := getOneLevel(db, pid, item.ID, locID)
	return map[string]any{"reservation": r, "item": item, "level": level}, err
}

func finishReservation(db *sql.DB, pid string, args map[string]any, status string) (map[string]any, error) {
	if reservationID := intArg(args, "reservation_id"); reservationID != 0 {
		existing, err := getReservation(db, pid, reservationID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, errors.New("reservation not found")
		}
		if existing.Status == status {
			level, err := getOneLevel(db, pid, existing.ItemID, existing.LocationID)
			return map[string]any{"reservation": existing, "level": level}, err
		}
		if existing.Status != "active" {
			return nil, fmt.Errorf("reservation is already %s", existing.Status)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := findActiveReservationTx(tx, pid, args)
	if err != nil {
		return nil, err
	}
	lvl, err := getLevelForUpdate(tx, pid, r.ItemID, r.LocationID)
	if err != nil {
		return nil, err
	}
	nextReserved := lvl.Reserved - r.Quantity
	if nextReserved < 0 && nextReserved > -1e-9 {
		nextReserved = 0
	}
	if nextReserved < -1e-9 {
		return nil, fmt.Errorf("reservation would make reserved negative: %.4f", nextReserved)
	}
	nextOnHand := lvl.OnHand
	movementType := "release"
	if status == "committed" {
		nextOnHand = lvl.OnHand - r.Quantity
		if nextOnHand < -1e-9 {
			return nil, fmt.Errorf("reservation commit would make on_hand negative: %.4f", nextOnHand)
		}
		movementType = "commit"
	} else if status == "expired" {
		movementType = "expire"
	}
	if _, err := tx.Exec(`UPDATE inventory_levels SET on_hand=?, reserved=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND item_id=? AND location_id=?`, round4(nextOnHand), round4(nextReserved), pid, r.ItemID, r.LocationID); err != nil {
		return nil, err
	}
	tsCol := "released_at"
	if status == "committed" {
		tsCol = "committed_at"
	}
	if _, err := tx.Exec(`UPDATE inventory_reservations SET status=?, updated_at=CURRENT_TIMESTAMP, `+tsCol+`=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, status, pid, r.ID); err != nil {
		return nil, err
	}
	cp := copyMap(args)
	if cp["reference_app"] == nil {
		cp["reference_app"] = r.ReferenceApp
		cp["reference_type"] = r.ReferenceType
		cp["reference_id"] = r.ReferenceID
	}
	delta := 0.0
	if status == "committed" {
		delta = -r.Quantity
	}
	if err := insertMovementTx(tx, pid, r.ItemID, r.LocationID, movementType, delta, nextOnHand, nextReserved, cp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	done, err := getReservation(db, pid, r.ID)
	level, _ := getOneLevel(db, pid, r.ItemID, r.LocationID)
	return map[string]any{"reservation": done, "level": level}, err
}

func listReservations(db *sql.DB, pid string, args map[string]any) ([]*Reservation, error) {
	where := []string{"r.project_id=?"}
	vals := []any{pid}
	for _, f := range []string{"reference_app", "reference_type", "reference_id", "status"} {
		if v := strArg(args, f); v != "" {
			where = append(where, "r."+f+"=?")
			vals = append(vals, v)
		}
	}
	if id := intArg(args, "item_id"); id != 0 {
		where = append(where, "r.item_id=?")
		vals = append(vals, id)
	}
	if id := intArg(args, "location_id"); id != 0 {
		where = append(where, "r.location_id=?")
		vals = append(vals, id)
	}
	limit := clamp(intArg(args, "limit"), 1, 500, 100)
	vals = append(vals, limit)
	rows, err := db.Query(reservationSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY r.id DESC LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Reservation
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getReservation(db *sql.DB, pid string, id int64) (*Reservation, error) {
	row := db.QueryRow(reservationSelect()+` WHERE r.project_id=? AND r.id=?`, pid, id)
	r, err := scanReservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func listMovements(db *sql.DB, pid string, args map[string]any) ([]*Movement, error) {
	where := []string{"m.project_id=?"}
	vals := []any{pid}
	for _, f := range []string{"reference_app", "reference_type", "reference_id", "type"} {
		if v := strArg(args, f); v != "" {
			where = append(where, "m."+f+"=?")
			vals = append(vals, v)
		}
	}
	if id := intArg(args, "item_id"); id != 0 {
		where = append(where, "m.item_id=?")
		vals = append(vals, id)
	}
	if id := intArg(args, "location_id"); id != 0 {
		where = append(where, "m.location_id=?")
		vals = append(vals, id)
	}
	limit := clamp(intArg(args, "limit"), 1, 500, 100)
	vals = append(vals, limit)
	rows, err := db.Query(movementSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY m.id DESC LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Movement
	for rows.Next() {
		m, err := scanMovement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func ensureLevelTx(tx *sql.Tx, pid string, itemID, locID int64) error {
	_, err := tx.Exec(`INSERT INTO inventory_levels (project_id, item_id, location_id)
		VALUES (?, ?, ?) ON CONFLICT(project_id, item_id, location_id) DO NOTHING`, pid, itemID, locID)
	return err
}

func getLevelForUpdate(tx *sql.Tx, pid string, itemID, locID int64) (*Level, error) {
	row := tx.QueryRow(levelSelect()+` WHERE l.project_id=? AND l.item_id=? AND l.location_id=?`, pid, itemID, locID)
	return scanLevel(row)
}

func getOneLevel(db *sql.DB, pid string, itemID, locID int64) (*Level, error) {
	row := db.QueryRow(levelSelect()+` WHERE l.project_id=? AND l.item_id=? AND l.location_id=?`, pid, itemID, locID)
	l, err := scanLevel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

func chooseReservationLocationTx(tx *sql.Tx, pid string, itemID int64, qty float64) (int64, error) {
	rows, err := tx.Query(`SELECT location_id, on_hand - reserved - safety_stock AS available
		FROM inventory_levels WHERE project_id=? AND item_id=? ORDER BY available DESC`, pid, itemID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var bestID int64
	for rows.Next() {
		var id int64
		var avail float64
		if err := rows.Scan(&id, &avail); err != nil {
			return 0, err
		}
		if bestID == 0 {
			bestID = id
		}
		if avail+1e-9 >= qty {
			return id, nil
		}
	}
	if bestID == 0 {
		return 0, errors.New("no stock level exists for item; pass location_id or receive stock first")
	}
	return bestID, nil
}

func findActiveReservationTx(tx *sql.Tx, pid string, args map[string]any) (*Reservation, error) {
	if id := intArg(args, "reservation_id"); id != 0 {
		row := tx.QueryRow(reservationSelect()+` WHERE r.project_id=? AND r.id=? AND r.status='active'`, pid, id)
		r, err := scanReservation(row)
		if err != nil {
			return nil, firstErr(skipNoRows(err), errors.New("active reservation not found"))
		}
		return r, nil
	}
	app, typ, id := strArg(args, "reference_app"), strArg(args, "reference_type"), strArg(args, "reference_id")
	if app == "" || typ == "" || id == "" {
		return nil, errors.New("reservation_id or reference_app/reference_type/reference_id required")
	}
	row := tx.QueryRow(reservationSelect()+` WHERE r.project_id=? AND r.reference_app=? AND r.reference_type=? AND r.reference_id=? AND r.status='active' ORDER BY r.id DESC LIMIT 1`, pid, app, typ, id)
	r, err := scanReservation(row)
	if err != nil {
		return nil, firstErr(skipNoRows(err), errors.New("active reservation not found"))
	}
	return r, nil
}

func insertMovementTx(tx *sql.Tx, pid string, itemID, locID int64, typ string, delta, onHandAfter, reservedAfter float64, args map[string]any) error {
	_, err := tx.Exec(`INSERT INTO inventory_movements
		(project_id, item_id, location_id, type, quantity_delta, on_hand_after, reserved_after,
		 reference_app, reference_type, reference_id, reason, actor, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, itemID, locID, typ, round4(delta), round4(onHandAfter), round4(reservedAfter),
		strArg(args, "reference_app"), strArg(args, "reference_type"), strArg(args, "reference_id"),
		strArg(args, "reason"), firstNonEmpty(strArg(args, "actor"), "system"), jsonText(args["metadata"], "{}"))
	return err
}

func itemSelect() string {
	return `SELECT i.id, i.sku, i.name, i.catalog_product_id, i.catalog_price_id,
		COALESCE(i.barcode,''), i.unit, i.track_quantity, i.allow_backorder, i.archived,
		i.metadata_json, COALESCE(SUM(l.on_hand),0), COALESCE(SUM(l.reserved),0),
		COALESCE(SUM(l.on_hand-l.reserved),0), COUNT(l.id), i.created_at, i.updated_at
		FROM inventory_items i LEFT JOIN inventory_levels l ON l.item_id=i.id AND l.project_id=i.project_id`
}

func levelSelect() string {
	return `SELECT l.id, l.item_id, i.sku, i.name, l.location_id, loc.code, loc.name,
		l.on_hand, l.reserved, l.on_hand-l.reserved, l.incoming, l.safety_stock,
		(l.on_hand-l.reserved)-l.safety_stock, l.updated_at
		FROM inventory_levels l
		JOIN inventory_items i ON i.id=l.item_id AND i.project_id=l.project_id
		JOIN inventory_locations loc ON loc.id=l.location_id AND loc.project_id=l.project_id`
}

func reservationSelect() string {
	return `SELECT r.id, r.item_id, i.sku, i.name, r.location_id, loc.code, loc.name,
		r.quantity, r.status, r.reference_app, r.reference_type, r.reference_id,
		r.metadata_json, r.created_at, r.updated_at, COALESCE(r.expires_at,''), COALESCE(r.committed_at,''), COALESCE(r.released_at,'')
		FROM inventory_reservations r
		JOIN inventory_items i ON i.id=r.item_id AND i.project_id=r.project_id
		JOIN inventory_locations loc ON loc.id=r.location_id AND loc.project_id=r.project_id`
}

func movementSelect() string {
	return `SELECT m.id, m.item_id, i.sku, i.name, m.location_id, loc.code, loc.name,
		m.type, m.quantity_delta, m.on_hand_after, m.reserved_after,
		m.reference_app, m.reference_type, m.reference_id, m.reason, m.actor, m.metadata_json, m.created_at
		FROM inventory_movements m
		JOIN inventory_items i ON i.id=m.item_id AND i.project_id=m.project_id
		JOIN inventory_locations loc ON loc.id=m.location_id AND loc.project_id=m.project_id`
}

type scanner interface{ Scan(dest ...any) error }

func scanLocation(s scanner) (*Location, error) {
	var loc Location
	var active int
	var address, meta string
	if err := s.Scan(&loc.ID, &loc.Code, &loc.Name, &loc.Type, &address, &active, &meta, &loc.CreatedAt, &loc.UpdatedAt); err != nil {
		return nil, err
	}
	loc.Active = active != 0
	loc.Address = jsonMap(address)
	loc.Metadata = jsonMap(meta)
	return &loc, nil
}

func scanItem(s scanner) (*Item, error) {
	var it Item
	var productID, priceID sql.NullInt64
	var track, backorder, archived int
	var meta string
	if err := s.Scan(&it.ID, &it.SKU, &it.Name, &productID, &priceID, &it.Barcode, &it.Unit, &track, &backorder, &archived,
		&meta, &it.OnHand, &it.Reserved, &it.Available, &it.LocationCount, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	it.CatalogProductID = ptrIfValid(productID)
	it.CatalogPriceID = ptrIfValid(priceID)
	it.TrackQuantity = track != 0
	it.AllowBackorder = backorder != 0
	it.Archived = archived != 0
	it.Metadata = jsonMap(meta)
	it.OnHand, it.Reserved, it.Available = round4(it.OnHand), round4(it.Reserved), round4(it.Available)
	return &it, nil
}

func scanLevel(s scanner) (*Level, error) {
	var l Level
	if err := s.Scan(&l.ID, &l.ItemID, &l.SKU, &l.ItemName, &l.LocationID, &l.LocationCode, &l.LocationName,
		&l.OnHand, &l.Reserved, &l.Available, &l.Incoming, &l.SafetyStock, &l.AvailableOver, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.OnHand, l.Reserved, l.Available = round4(l.OnHand), round4(l.Reserved), round4(l.Available)
	l.Incoming, l.SafetyStock, l.AvailableOver = round4(l.Incoming), round4(l.SafetyStock), round4(l.AvailableOver)
	return &l, nil
}

func scanReservation(s scanner) (*Reservation, error) {
	var r Reservation
	var meta string
	if err := s.Scan(&r.ID, &r.ItemID, &r.SKU, &r.ItemName, &r.LocationID, &r.LocationCode, &r.LocationName,
		&r.Quantity, &r.Status, &r.ReferenceApp, &r.ReferenceType, &r.ReferenceID, &meta,
		&r.CreatedAt, &r.UpdatedAt, &r.ExpiresAt, &r.CommittedAt, &r.ReleasedAt); err != nil {
		return nil, err
	}
	r.Quantity = round4(r.Quantity)
	r.Metadata = jsonMap(meta)
	return &r, nil
}

func scanMovement(s scanner) (*Movement, error) {
	var m Movement
	var meta string
	if err := s.Scan(&m.ID, &m.ItemID, &m.SKU, &m.ItemName, &m.LocationID, &m.LocationCode, &m.LocationName,
		&m.Type, &m.QuantityDelta, &m.OnHandAfter, &m.ReservedAfter, &m.ReferenceApp, &m.ReferenceType, &m.ReferenceID,
		&m.Reason, &m.Actor, &meta, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.QuantityDelta, m.OnHandAfter, m.ReservedAfter = round4(m.QuantityDelta), round4(m.OnHandAfter), round4(m.ReservedAfter)
	m.Metadata = jsonMap(meta)
	return &m, nil
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	pid := projectScope(globalCtx)
	var out struct {
		Items        int     `json:"items"`
		Locations    int     `json:"locations"`
		OnHand       float64 `json:"on_hand"`
		Reserved     float64 `json:"reserved"`
		Available    float64 `json:"available"`
		Reservations int     `json:"active_reservations"`
	}
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM inventory_items WHERE project_id=? AND archived=0`, pid).Scan(&out.Items)
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM inventory_locations WHERE project_id=? AND active=1`, pid).Scan(&out.Locations)
	_ = globalCtx.AppDB().QueryRow(`SELECT COALESCE(SUM(on_hand),0), COALESCE(SUM(reserved),0), COALESCE(SUM(on_hand-reserved),0) FROM inventory_levels WHERE project_id=?`, pid).Scan(&out.OnHand, &out.Reserved, &out.Available)
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM inventory_reservations WHERE project_id=? AND status='active'`, pid).Scan(&out.Reservations)
	httpJSON(w, out)
}

func (a *App) handleLocations(w http.ResponseWriter, r *http.Request) {
	args := queryArgs(r)
	switch r.Method {
	case http.MethodGet:
		out, err := listLocations(globalCtx.AppDB(), projectScope(globalCtx), args)
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		readJSON(r, &body)
		out, err := createLocation(globalCtx.AppDB(), projectScope(globalCtx), body)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleLocation(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r.URL.Path, "/locations/")
	switch r.Method {
	case http.MethodGet:
		out, err := getLocation(globalCtx.AppDB(), projectScope(globalCtx), id)
		httpResult(w, out, err)
	case http.MethodPatch:
		var body map[string]any
		readJSON(r, &body)
		out, err := updateLocation(globalCtx.AppDB(), projectScope(globalCtx), id, body)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleItems(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := listItems(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		readJSON(r, &body)
		out, err := createItem(globalCtx.AppDB(), projectScope(globalCtx), body)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleItem(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r.URL.Path, "/items/")
	switch r.Method {
	case http.MethodGet:
		out, err := getItemByID(globalCtx.AppDB(), projectScope(globalCtx), id)
		httpResult(w, out, err)
	case http.MethodPatch:
		var body map[string]any
		readJSON(r, &body)
		out, err := updateItem(globalCtx.AppDB(), projectScope(globalCtx), id, body)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleLevels(w http.ResponseWriter, r *http.Request) {
	out, err := listLevels(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handleAvailability(w http.ResponseWriter, r *http.Request) {
	out, err := availabilityCheck(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handleAdjust(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	readJSON(r, &body)
	out, err := adjustStock(globalCtx.AppDB(), projectScope(globalCtx), "adjust", body)
	httpResult(w, out, err)
}

func (a *App) handleReceive(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	readJSON(r, &body)
	body["quantity_delta"] = numArg(body, "quantity")
	out, err := adjustStock(globalCtx.AppDB(), projectScope(globalCtx), "receive", body)
	httpResult(w, out, err)
}

func (a *App) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	readJSON(r, &body)
	out, err := transferStock(globalCtx.AppDB(), projectScope(globalCtx), body)
	httpResult(w, out, err)
}

func (a *App) handleReserve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	readJSON(r, &body)
	out, err := reserveStock(globalCtx.AppDB(), projectScope(globalCtx), body)
	httpResult(w, out, err)
}

func (a *App) handleReservations(w http.ResponseWriter, r *http.Request) {
	out, err := listReservations(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handleReservationAction(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r.URL.Path, "/reservations/")
	var body map[string]any
	readJSON(r, &body)
	body["reservation_id"] = id
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/reservations/%d", id)), "/")
	status := "released"
	if action == "commit" {
		status = "committed"
	}
	out, err := finishReservation(globalCtx.AppDB(), projectScope(globalCtx), body, status)
	httpResult(w, out, err)
}

func (a *App) handleMovements(w http.ResponseWriter, r *http.Request) {
	out, err := listMovements(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func projectScope(ctxs ...*sdk.AppCtx) string {
	if len(ctxs) > 0 && ctxs[0] != nil {
		if pid := strings.TrimSpace(ctxs[0].CurrentProject()); pid != "" {
			return pid
		}
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return pid
	}
	return "default"
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func typ(t string) map[string]any { return map[string]any{"type": t} }

func locationProps() map[string]any {
	return map[string]any{"name": typ("string"), "code": typ("string"), "type": typ("string"), "address": typ("object"), "active": typ("boolean"), "metadata": typ("object")}
}

func itemProps() map[string]any {
	return map[string]any{"sku": typ("string"), "name": typ("string"), "catalog_product_id": typ("integer"), "catalog_price_id": typ("integer"), "barcode": typ("string"), "unit": typ("string"), "track_quantity": typ("boolean"), "allow_backorder": typ("boolean"), "metadata": typ("object")}
}

func levelFilterProps() map[string]any {
	return map[string]any{"item_id": typ("integer"), "sku": typ("string"), "location_id": typ("integer"), "low_stock_only": typ("boolean")}
}

func availabilityProps() map[string]any {
	return map[string]any{"item_id": typ("integer"), "sku": typ("string"), "quantity": typ("number"), "location_id": typ("integer")}
}

func adjustProps() map[string]any {
	p := availabilityProps()
	p["quantity_delta"] = typ("number")
	p["reason"], p["actor"], p["reference_app"], p["reference_type"], p["reference_id"], p["metadata"] = typ("string"), typ("string"), typ("string"), typ("string"), typ("string"), typ("object")
	return p
}

func receiveProps() map[string]any {
	p := availabilityProps()
	p["quantity"], p["reason"], p["actor"], p["reference_app"], p["reference_type"], p["reference_id"], p["metadata"] = typ("number"), typ("string"), typ("string"), typ("string"), typ("string"), typ("string"), typ("object")
	return p
}

func transferProps() map[string]any {
	return map[string]any{"item_id": typ("integer"), "sku": typ("string"), "from_location_id": typ("integer"), "to_location_id": typ("integer"), "quantity": typ("number"), "reason": typ("string"), "actor": typ("string"), "reference_app": typ("string"), "reference_type": typ("string"), "reference_id": typ("string"), "metadata": typ("object")}
}

func reserveProps() map[string]any {
	return map[string]any{"item_id": typ("integer"), "sku": typ("string"), "location_id": typ("integer"), "quantity": typ("number"), "reference_app": typ("string"), "reference_type": typ("string"), "reference_id": typ("string"), "expires_at": typ("string"), "metadata": typ("object")}
}

func reservationRefProps() map[string]any {
	return map[string]any{"reservation_id": typ("integer"), "reference_app": typ("string"), "reference_type": typ("string"), "reference_id": typ("string"), "reason": typ("string"), "actor": typ("string")}
}

func reservationFilterProps() map[string]any {
	p := reservationRefProps()
	p["item_id"], p["location_id"], p["status"], p["limit"] = typ("integer"), typ("integer"), typ("string"), typ("integer")
	return p
}

func movementFilterProps() map[string]any {
	return map[string]any{"item_id": typ("integer"), "location_id": typ("integer"), "reference_app": typ("string"), "reference_type": typ("string"), "reference_id": typ("string"), "type": typ("string"), "limit": typ("integer")}
}

func emitLevel(ctx *sdk.AppCtx, out map[string]any) {
	if ctx == nil || out == nil {
		return
	}
	for _, key := range []string{"level", "from_level", "to_level"} {
		if l, ok := out[key].(*Level); ok && l != nil {
			ctx.Emit("inventory.level.changed", map[string]any{"item_id": l.ItemID, "location_id": l.LocationID, "on_hand": l.OnHand, "reserved": l.Reserved, "available": l.Available})
		}
	}
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func readJSON(r *http.Request, dst any) {
	if r.Body == nil {
		return
	}
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(dst)
}

func queryArgs(r *http.Request) map[string]any {
	out := map[string]any{}
	for k, vals := range r.URL.Query() {
		if len(vals) == 0 {
			continue
		}
		v := vals[0]
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			out[k] = i
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
			continue
		}
		if v == "true" || v == "false" {
			out[k] = v == "true"
			continue
		}
		out[k] = v
	}
	return out
}

func intArg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func numArg(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	default:
		return 0
	}
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case int:
		return v != 0
	case float64:
		return v != 0
	default:
		return false
	}
}

func mapArg(args map[string]any, key string) map[string]any {
	if m, ok := args[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func copyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonText(v any, def string) string {
	if v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return def
		}
		if json.Valid([]byte(s)) {
			return s
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return def
	}
	return string(b)
}

func jsonMap(s string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(firstNonEmpty(s, "{}")), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func normalizeSKU(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func normalizeCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func ptrIfValid(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func firstErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func skipNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func clamp(v int64, min, max, def int) int {
	if v == 0 {
		return def
	}
	if v < int64(min) {
		return min
	}
	if v > int64(max) {
		return max
	}
	return int(v)
}

func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func round4(f float64) float64 {
	if f < 0 {
		return -round4(-f)
	}
	return float64(int64(f*10000+0.5)) / 10000
}

func expireReservations(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id FROM inventory_reservations
		 WHERE status='active' AND expires_at IS NOT NULL
		   AND datetime(expires_at)<=CURRENT_TIMESTAMP
		 ORDER BY id LIMIT 500`)
	if err != nil {
		return err
	}
	type candidate struct {
		id  int64
		pid string
	}
	var candidates []candidate
	for rows.Next() {
		var row candidate
		if err := rows.Scan(&row.id, &row.pid); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		out, err := finishReservation(ctx.AppDB(), candidate.pid, map[string]any{
			"reservation_id": candidate.id, "actor": "system:expiration", "reason": "reservation TTL elapsed",
		}, "expired")
		if err != nil {
			if strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "active reservation not found") {
				continue
			}
			return err
		}
		if reservation, ok := out["reservation"].(*Reservation); ok {
			ctx.WithProject(candidate.pid).Emit("inventory.reservation.expired", map[string]any{
				"reservation_id": reservation.ID, "item_id": reservation.ItemID,
				"location_id": reservation.LocationID, "quantity": reservation.Quantity,
				"reference_app": reservation.ReferenceApp, "reference_type": reservation.ReferenceType,
				"reference_id": reservation.ReferenceID,
			})
		}
	}
	return nil
}

func inventoryConfigInt(ctx *sdk.AppCtx, key string, fallback int64) int64 {
	if ctx == nil || ctx.Config() == nil {
		return fallback
	}
	value := strings.TrimSpace(ctx.Config().Get(key))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 || parsed > 1440 {
		return fallback
	}
	return parsed
}

func main() {
	app := &App{}
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest            { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error     { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error     { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route           { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool              { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory    { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker             { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler { return w.app.EventHandlers() }
