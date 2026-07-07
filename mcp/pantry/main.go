// Apteva Pantry app - food inventory for pantry, fridge, and freezer.
//
// The key model is item + lot: an item is "milk"; a lot is "1 carton
// of milk in the fridge expiring 2026-07-12". Lots make expiry,
// oldest-first consumption, and shopping suggestions predictable.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: pantry
display_name: Pantry
version: 0.2.0
description: Food inventory for pantry, fridge, and freezer.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app]
  integrations: []
  apps: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: pantry_quick_add, description: "Parse one pantry line." }
    - { name: pantry_items_create, description: "Create a pantry item." }
    - { name: pantry_items_list, description: "List items with current quantity." }
    - { name: pantry_items_get, description: "Fetch one item with lots." }
    - { name: pantry_items_update, description: "Patch an item." }
    - { name: pantry_items_archive, description: "Archive an item." }
    - { name: pantry_stock_add, description: "Add stock as a lot." }
    - { name: pantry_stock_use, description: "Consume stock FIFO by expiry." }
    - { name: pantry_lots_list, description: "List stock lots." }
    - { name: pantry_lot_update, description: "Patch a stock lot." }
    - { name: pantry_lot_delete, description: "Delete a stock lot." }
    - { name: pantry_locations_list, description: "List locations." }
    - { name: pantry_locations_create, description: "Create a location." }
    - { name: pantry_expiring, description: "Lots expiring soon." }
    - { name: pantry_low_stock, description: "Items below threshold." }
    - { name: pantry_shopping_items_create, description: "Create a manual shopping-list item." }
    - { name: pantry_shopping_items_list, description: "List manual shopping-list items." }
    - { name: pantry_shopping_items_update, description: "Patch a manual shopping-list item." }
    - { name: pantry_shopping_items_check, description: "Mark a shopping-list item checked or open." }
    - { name: pantry_shopping_items_delete, description: "Delete a shopping-list item." }
    - { name: pantry_shopping_suggestions, description: "Generated low-stock shopping suggestions." }
    - { name: pantry_shopping_list, description: "Combined manual shopping rows and suggestions." }
    - { name: pantry_summary, description: "Short pantry summary." }
  ui_panels:
    - slot: project.page
      label: Pantry
      icon: shopping-basket
      entry: /ui/PantryPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/pantry
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/pantry.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("pantry requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("pantry mounted", "project_id", projectScope(ctx))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/summary", Handler: a.handleSummary},
		{Pattern: "/quick", Handler: a.handleQuick},
		{Pattern: "/items", Handler: a.handleItems},
		{Pattern: "/items/", Handler: a.handleItem},
		{Pattern: "/lots", Handler: a.handleLots},
		{Pattern: "/lots/", Handler: a.handleLot},
		{Pattern: "/locations", Handler: a.handleLocations},
		{Pattern: "/stock/add", Handler: a.handleStockAdd},
		{Pattern: "/stock/use", Handler: a.handleStockUse},
		{Pattern: "/expiring", Handler: a.handleExpiring},
		{Pattern: "/low_stock", Handler: a.handleLowStock},
		{Pattern: "/shopping/items", Handler: a.handleShoppingItems},
		{Pattern: "/shopping/items/", Handler: a.handleShoppingItem},
		{Pattern: "/shopping_list", Handler: a.handleShoppingList},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "pantry_quick_add", Description: "Parse one pantry line. Examples: 'add 2 milk fridge exp 2026-07-12', 'use 1 eggs', 'discard spinach'. Args: text, source?.", InputSchema: schemaObject(map[string]any{"text": typ("string"), "source": typ("string")}, []string{"text"}), Handler: a.toolQuickAdd},
		{Name: "pantry_items_create", Description: "Create a pantry item/product. Args: name, category?, barcode?, brand?, default_unit?, min_quantity?, target_quantity?, notes?.", InputSchema: schemaObject(itemProps(), []string{"name"}), Handler: a.toolItemsCreate},
		{Name: "pantry_items_list", Description: "List items with total quantity. Args: q?, category?, include_archived?, limit?.", InputSchema: schemaObject(map[string]any{"q": typ("string"), "category": typ("string"), "include_archived": typ("boolean"), "limit": typ("integer")}, nil), Handler: a.toolItemsList},
		{Name: "pantry_items_get", Description: "Fetch one item with its lots. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolItemsGet},
		{Name: "pantry_items_update", Description: "Patch an item. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolItemsUpdate},
		{Name: "pantry_items_archive", Description: "Archive an item. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolItemsArchive},
		{Name: "pantry_stock_add", Description: "Add stock as a lot. Args: item_id? or name, quantity, unit?, location?, expires_at?, barcode?, brand?, category?, notes?, source?.", InputSchema: schemaObject(stockAddProps(), []string{"quantity"}), Handler: a.toolStockAdd},
		{Name: "pantry_stock_use", Description: "Consume stock oldest-expiry first. Args: item_id? or name, quantity? default 1, location_id?, action? (use|discard), notes?.", InputSchema: schemaObject(map[string]any{"item_id": typ("integer"), "name": typ("string"), "quantity": typ("number"), "location_id": typ("integer"), "action": typ("string"), "notes": typ("string")}, nil), Handler: a.toolStockUse},
		{Name: "pantry_lots_list", Description: "List stock lots. Args: item_id?, location_id?, expiring_days?, q?, limit?.", InputSchema: schemaObject(map[string]any{"item_id": typ("integer"), "location_id": typ("integer"), "expiring_days": typ("integer"), "q": typ("string"), "limit": typ("integer")}, nil), Handler: a.toolLotsList},
		{Name: "pantry_lot_update", Description: "Patch a lot. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolLotUpdate},
		{Name: "pantry_lot_delete", Description: "Delete a lot. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolLotDelete},
		{Name: "pantry_locations_list", Description: "List locations.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolLocationsList},
		{Name: "pantry_locations_create", Description: "Create a location. Args: name, kind?.", InputSchema: schemaObject(map[string]any{"name": typ("string"), "kind": typ("string")}, []string{"name"}), Handler: a.toolLocationsCreate},
		{Name: "pantry_expiring", Description: "Lots expiring soon. Args: days? default 14, limit?.", InputSchema: schemaObject(map[string]any{"days": typ("integer"), "limit": typ("integer")}, nil), Handler: a.toolExpiring},
		{Name: "pantry_low_stock", Description: "Items at or below min_quantity.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolLowStock},
		{Name: "pantry_shopping_items_create", Description: "Create a manual shopping-list item. Args: name, quantity?, unit?, category?, store?, notes?, item_id?, source?. Does not create pantry stock.", InputSchema: schemaObject(shoppingItemProps(), []string{"name"}), Handler: a.toolShoppingItemsCreate},
		{Name: "pantry_shopping_items_list", Description: "List manual shopping-list items. Args: status? (open|checked|dismissed|purchased|all; default open), limit?.", InputSchema: schemaObject(map[string]any{"status": typ("string"), "limit": typ("integer")}, nil), Handler: a.toolShoppingItemsList},
		{Name: "pantry_shopping_items_update", Description: "Patch a manual shopping-list item. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolShoppingItemsUpdate},
		{Name: "pantry_shopping_items_check", Description: "Mark a shopping-list item checked or open. Args: id, checked? default true.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "checked": typ("boolean")}, []string{"id"}), Handler: a.toolShoppingItemsCheck},
		{Name: "pantry_shopping_items_delete", Description: "Delete a shopping-list item. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolShoppingItemsDelete},
		{Name: "pantry_shopping_suggestions", Description: "Generated low-stock shopping suggestions based on target quantities.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolShoppingSuggestions},
		{Name: "pantry_shopping_list", Description: "Combined manual shopping rows plus generated low-stock suggestions.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolShoppingList},
		{Name: "pantry_summary", Description: "Short markdown summary of expiring and low-stock food. Args: days?.", InputSchema: schemaObject(map[string]any{"days": typ("integer")}, nil), Handler: a.toolSummary},
	}
}

type Item struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Category       string  `json:"category"`
	Barcode        string  `json:"barcode"`
	Brand          string  `json:"brand"`
	DefaultUnit    string  `json:"default_unit"`
	MinQuantity    float64 `json:"min_quantity"`
	TargetQuantity float64 `json:"target_quantity"`
	Notes          string  `json:"notes"`
	Archived       bool    `json:"archived"`
	TotalQuantity  float64 `json:"total_quantity"`
	LotCount       int     `json:"lot_count"`
	NextExpiresAt  string  `json:"next_expires_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type Lot struct {
	ID           int64   `json:"id"`
	ItemID       int64   `json:"item_id"`
	ItemName     string  `json:"item_name"`
	Category     string  `json:"category"`
	LocationID   int64   `json:"location_id"`
	LocationName string  `json:"location_name"`
	LocationKind string  `json:"location_kind"`
	Quantity     float64 `json:"quantity"`
	Unit         string  `json:"unit"`
	ExpiresAt    string  `json:"expires_at,omitempty"`
	OpenedAt     string  `json:"opened_at,omitempty"`
	PurchasedAt  string  `json:"purchased_at,omitempty"`
	Source       string  `json:"source"`
	Notes        string  `json:"notes"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type Location struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SortOrder int    `json:"sort_order"`
	CreatedAt string `json:"created_at"`
}

type ShoppingLine struct {
	ItemID          int64   `json:"item_id"`
	Name            string  `json:"name"`
	Category        string  `json:"category"`
	Unit            string  `json:"unit"`
	CurrentQuantity float64 `json:"current_quantity"`
	MinQuantity     float64 `json:"min_quantity"`
	TargetQuantity  float64 `json:"target_quantity"`
	BuyQuantity     float64 `json:"buy_quantity"`
}

type ShoppingItem struct {
	ID        int64   `json:"id"`
	ItemID    *int64  `json:"item_id,omitempty"`
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	Unit      string  `json:"unit"`
	Category  string  `json:"category"`
	Store     string  `json:"store"`
	Source    string  `json:"source"`
	Status    string  `json:"status"`
	Notes     string  `json:"notes"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type ShoppingList struct {
	Items       []ShoppingItem `json:"items"`
	Suggestions []ShoppingLine `json:"suggestions"`
}

type UseResult struct {
	ItemID   int64   `json:"item_id"`
	Name     string  `json:"name"`
	Action   string  `json:"action"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
	Lots     []Lot   `json:"lots"`
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

func ensureDefaultLocations(db *sql.DB, pid string) error {
	defaults := []struct {
		name string
		kind string
		sort int
	}{
		{"Pantry", "pantry", 10},
		{"Fridge", "fridge", 20},
		{"Freezer", "freezer", 30},
	}
	for _, d := range defaults {
		if _, err := db.Exec(`INSERT OR IGNORE INTO locations (project_id, name, kind, sort_order) VALUES (?, ?, ?, ?)`, pid, d.name, d.kind, d.sort); err != nil {
			return err
		}
	}
	return nil
}

func createLocation(db *sql.DB, pid, name, kind string) (*Location, error) {
	name = cleanName(name)
	if name == "" {
		return nil, errors.New("name required")
	}
	kind = normaliseLocationKind(kind)
	res, err := db.Exec(`INSERT OR IGNORE INTO locations (project_id, name, kind) VALUES (?, ?, ?)`, pid, name, kind)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return getLocationByName(db, pid, name)
	}
	return getLocation(db, pid, id)
}

func ensureLocation(db *sql.DB, pid, name string) (*Location, error) {
	if err := ensureDefaultLocations(db, pid); err != nil {
		return nil, err
	}
	name = cleanName(name)
	if name == "" {
		name = "Pantry"
	}
	if loc, err := getLocationByName(db, pid, name); err == nil {
		return loc, nil
	}
	return createLocation(db, pid, name, guessLocationKind(name))
}

func getLocation(db *sql.DB, pid string, id int64) (*Location, error) {
	var l Location
	err := db.QueryRow(`SELECT id, name, kind, sort_order, created_at FROM locations WHERE project_id = ? AND id = ?`, pid, id).Scan(&l.ID, &l.Name, &l.Kind, &l.SortOrder, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func getLocationByName(db *sql.DB, pid, name string) (*Location, error) {
	var l Location
	err := db.QueryRow(`SELECT id, name, kind, sort_order, created_at FROM locations WHERE project_id = ? AND lower(name) = lower(?)`, pid, name).Scan(&l.ID, &l.Name, &l.Kind, &l.SortOrder, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func listLocations(db *sql.DB, pid string) ([]Location, error) {
	if err := ensureDefaultLocations(db, pid); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, name, kind, sort_order, created_at FROM locations WHERE project_id = ? ORDER BY sort_order, name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.SortOrder, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func createItem(db *sql.DB, pid string, in map[string]any) (*Item, error) {
	name := cleanName(strArg(in, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	unit := strings.TrimSpace(strArg(in, "default_unit", ""))
	if unit == "" {
		unit = strings.TrimSpace(strArg(in, "unit", ""))
	}
	if unit == "" {
		unit = "each"
	}
	res, err := db.Exec(
		`INSERT INTO items (project_id, name, category, barcode, brand, default_unit, min_quantity, target_quantity, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, name) DO UPDATE SET
		   category = CASE WHEN excluded.category != '' THEN excluded.category ELSE category END,
		   barcode = CASE WHEN excluded.barcode != '' THEN excluded.barcode ELSE barcode END,
		   brand = CASE WHEN excluded.brand != '' THEN excluded.brand ELSE brand END,
		   default_unit = CASE WHEN excluded.default_unit != '' THEN excluded.default_unit ELSE default_unit END,
		   updated_at = CURRENT_TIMESTAMP`,
		pid, name, strings.TrimSpace(strArg(in, "category", "")), strings.TrimSpace(strArg(in, "barcode", "")),
		strings.TrimSpace(strArg(in, "brand", "")), unit, floatArg(in, "min_quantity", 0), floatArg(in, "target_quantity", 0),
		strings.TrimSpace(strArg(in, "notes", "")),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return getItemByName(db, pid, name)
	}
	return getItem(db, pid, id)
}

func getItem(db *sql.DB, pid string, id int64) (*Item, error) {
	items, err := listItems(db, pid, "", "", true, 1, "i.id = ?", []any{id})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func getItemByName(db *sql.DB, pid, name string) (*Item, error) {
	items, err := listItems(db, pid, "", "", true, 1, "lower(i.name) = lower(?)", []any{name})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func getItemByBarcode(db *sql.DB, pid, barcode string) (*Item, error) {
	barcode = strings.TrimSpace(barcode)
	if barcode == "" {
		return nil, sql.ErrNoRows
	}
	items, err := listItems(db, pid, "", "", true, 1, "i.barcode = ?", []any{barcode})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func listItems(db *sql.DB, pid, q, category string, includeArchived bool, limit int, extraWhere string, extraArgs []any) ([]Item, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	where := []string{"i.project_id = ?"}
	args := []any{pid}
	if !includeArchived {
		where = append(where, "i.archived = 0")
	}
	if q = strings.TrimSpace(q); q != "" {
		where = append(where, "(lower(i.name) LIKE lower(?) OR lower(i.brand) LIKE lower(?) OR i.barcode = ?)")
		like := "%" + q + "%"
		args = append(args, like, like, q)
	}
	if category = strings.TrimSpace(category); category != "" {
		where = append(where, "lower(i.category) = lower(?)")
		args = append(args, category)
	}
	if extraWhere != "" {
		where = append(where, extraWhere)
		args = append(args, extraArgs...)
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT i.id, i.name, i.category, i.barcode, i.brand, i.default_unit,
		       i.min_quantity, i.target_quantity, i.notes, i.archived,
		       COALESCE(SUM(l.quantity), 0) AS total_quantity,
		       COUNT(l.id) AS lot_count,
		       COALESCE(MIN(CASE WHEN l.expires_at IS NOT NULL AND l.quantity > 0 THEN l.expires_at END), '') AS next_expires_at,
		       i.created_at, i.updated_at
		  FROM items i
		  LEFT JOIN lots l ON l.project_id = i.project_id AND l.item_id = i.id AND l.quantity > 0
		 WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY i.id
		 ORDER BY lower(i.name)
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var archived int
		if err := rows.Scan(&it.ID, &it.Name, &it.Category, &it.Barcode, &it.Brand, &it.DefaultUnit, &it.MinQuantity, &it.TargetQuantity, &it.Notes, &archived, &it.TotalQuantity, &it.LotCount, &it.NextExpiresAt, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Archived = archived != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

func updateItem(db *sql.DB, pid string, id int64, patch map[string]any) (*Item, error) {
	item, err := getItem(db, pid, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"name":            item.Name,
		"category":        item.Category,
		"barcode":         item.Barcode,
		"brand":           item.Brand,
		"default_unit":    item.DefaultUnit,
		"min_quantity":    item.MinQuantity,
		"target_quantity": item.TargetQuantity,
		"notes":           item.Notes,
		"archived":        boolToInt(item.Archived),
	}
	for _, key := range []string{"name", "category", "barcode", "brand", "default_unit", "notes"} {
		if v, ok := patch[key].(string); ok {
			if key == "name" {
				fields[key] = cleanName(v)
			} else {
				fields[key] = strings.TrimSpace(v)
			}
		}
	}
	for _, key := range []string{"min_quantity", "target_quantity"} {
		if _, ok := patch[key]; ok {
			fields[key] = floatArg(patch, key, fields[key].(float64))
		}
	}
	if v, ok := patch["archived"].(bool); ok {
		fields["archived"] = boolToInt(v)
	}
	_, err = db.Exec(
		`UPDATE items SET name = ?, category = ?, barcode = ?, brand = ?, default_unit = ?, min_quantity = ?, target_quantity = ?, notes = ?, archived = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`,
		fields["name"], fields["category"], fields["barcode"], fields["brand"], fields["default_unit"], fields["min_quantity"], fields["target_quantity"], fields["notes"], fields["archived"], pid, id,
	)
	if err != nil {
		return nil, err
	}
	return getItem(db, pid, id)
}

func addStock(db *sql.DB, pid string, in map[string]any) (*Lot, error) {
	qty := floatArg(in, "quantity", 0)
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	item, err := resolveOrCreateItem(db, pid, in)
	if err != nil {
		return nil, err
	}
	locationName := strArg(in, "location", "")
	if locID := intArg(in, "location_id", 0); locID > 0 {
		if loc, err := getLocation(db, pid, int64(locID)); err == nil {
			locationName = loc.Name
		}
	}
	loc, err := ensureLocation(db, pid, locationName)
	if err != nil {
		return nil, err
	}
	unit := strings.TrimSpace(strArg(in, "unit", ""))
	if unit == "" {
		unit = item.DefaultUnit
	}
	expires := normaliseDate(strArg(in, "expires_at", ""))
	opened := normaliseDate(strArg(in, "opened_at", ""))
	purchased := normaliseDate(strArg(in, "purchased_at", ""))
	source := normaliseSource(strArg(in, "source", "human"))
	res, err := db.Exec(
		`INSERT INTO lots (project_id, item_id, location_id, quantity, unit, expires_at, opened_at, purchased_at, source, notes)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		pid, item.ID, loc.ID, qty, unit, expires, opened, purchased, source, strings.TrimSpace(strArg(in, "notes", "")),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = db.Exec(`INSERT INTO stock_events (project_id, item_id, lot_id, action, quantity, unit, location_id, notes) VALUES (?, ?, ?, 'add', ?, ?, ?, ?)`, pid, item.ID, id, qty, unit, loc.ID, strings.TrimSpace(strArg(in, "notes", "")))
	return getLot(db, pid, id)
}

func resolveOrCreateItem(db *sql.DB, pid string, in map[string]any) (*Item, error) {
	if id := intArg(in, "item_id", 0); id > 0 {
		return getItem(db, pid, int64(id))
	}
	if barcode := strings.TrimSpace(strArg(in, "barcode", "")); barcode != "" {
		if item, err := getItemByBarcode(db, pid, barcode); err == nil {
			return item, nil
		}
	}
	name := cleanName(strArg(in, "name", ""))
	if name == "" {
		return nil, errors.New("item_id or name required")
	}
	if item, err := getItemByName(db, pid, name); err == nil {
		if barcode := strings.TrimSpace(strArg(in, "barcode", "")); barcode != "" && item.Barcode == "" {
			_, _ = updateItem(db, pid, item.ID, map[string]any{"barcode": barcode})
		}
		return getItem(db, pid, item.ID)
	}
	return createItem(db, pid, in)
}

func useStock(db *sql.DB, pid string, in map[string]any) (*UseResult, error) {
	qty := floatArg(in, "quantity", 1)
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	item, err := resolveExistingItem(db, pid, in)
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(strArg(in, "action", "use")))
	if action != "use" && action != "discard" {
		action = "use"
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lots, err := candidateLots(tx, pid, item.ID, int64(intArg(in, "location_id", 0)))
	if err != nil {
		return nil, err
	}
	total := 0.0
	for _, lot := range lots {
		total += lot.Quantity
	}
	if total+0.000001 < qty {
		return nil, fmt.Errorf("insufficient stock: have %.2f %s, need %.2f", total, item.DefaultUnit, qty)
	}
	remaining := qty
	changed := []Lot{}
	for _, lot := range lots {
		if remaining <= 0 {
			break
		}
		take := remaining
		if lot.Quantity < take {
			take = lot.Quantity
		}
		newQty := lot.Quantity - take
		if newQty <= 0.000001 {
			if _, err := tx.Exec(`DELETE FROM lots WHERE project_id = ? AND id = ?`, pid, lot.ID); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.Exec(`UPDATE lots SET quantity = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, newQty, pid, lot.ID); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO stock_events (project_id, item_id, lot_id, action, quantity, unit, location_id, notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, pid, item.ID, lot.ID, action, take, lot.Unit, lot.LocationID, strings.TrimSpace(strArg(in, "notes", ""))); err != nil {
			return nil, err
		}
		lot.Quantity = take
		changed = append(changed, lot)
		remaining -= take
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &UseResult{ItemID: item.ID, Name: item.Name, Action: action, Quantity: qty, Unit: item.DefaultUnit, Lots: changed}, nil
}

func resolveExistingItem(db *sql.DB, pid string, in map[string]any) (*Item, error) {
	if id := intArg(in, "item_id", 0); id > 0 {
		return getItem(db, pid, int64(id))
	}
	if barcode := strings.TrimSpace(strArg(in, "barcode", "")); barcode != "" {
		return getItemByBarcode(db, pid, barcode)
	}
	name := cleanName(strArg(in, "name", ""))
	if name == "" {
		return nil, errors.New("item_id or name required")
	}
	return getItemByName(db, pid, name)
}

func candidateLots(q queryer, pid string, itemID, locationID int64) ([]Lot, error) {
	where := "l.project_id = ? AND l.item_id = ? AND l.quantity > 0"
	args := []any{pid, itemID}
	if locationID > 0 {
		where += " AND l.location_id = ?"
		args = append(args, locationID)
	}
	rows, err := q.Query(`
		SELECT l.id, l.item_id, i.name, i.category, l.location_id, loc.name, loc.kind,
		       l.quantity, l.unit, COALESCE(l.expires_at, ''), COALESCE(l.opened_at, ''),
		       COALESCE(l.purchased_at, ''), l.source, l.notes, l.created_at, l.updated_at
		  FROM lots l
		  JOIN items i ON i.project_id = l.project_id AND i.id = l.item_id
		  JOIN locations loc ON loc.project_id = l.project_id AND loc.id = l.location_id
		 WHERE `+where+`
		 ORDER BY CASE WHEN l.expires_at IS NULL THEN 1 ELSE 0 END, l.expires_at, l.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLots(rows)
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func getLot(db *sql.DB, pid string, id int64) (*Lot, error) {
	lots, err := listLots(db, pid, map[string]any{"id": id, "limit": 1})
	if err != nil {
		return nil, err
	}
	if len(lots) == 0 {
		return nil, sql.ErrNoRows
	}
	return &lots[0], nil
}

func listLots(db *sql.DB, pid string, opts map[string]any) ([]Lot, error) {
	where := []string{"l.project_id = ?", "l.quantity > 0"}
	args := []any{pid}
	if id := intArg(opts, "id", 0); id > 0 {
		where = append(where, "l.id = ?")
		args = append(args, id)
	}
	if itemID := intArg(opts, "item_id", 0); itemID > 0 {
		where = append(where, "l.item_id = ?")
		args = append(args, itemID)
	}
	if locationID := intArg(opts, "location_id", 0); locationID > 0 {
		where = append(where, "l.location_id = ?")
		args = append(args, locationID)
	}
	if q := strings.TrimSpace(strArg(opts, "q", "")); q != "" {
		where = append(where, "(lower(i.name) LIKE lower(?) OR lower(i.brand) LIKE lower(?) OR i.barcode = ?)")
		like := "%" + q + "%"
		args = append(args, like, like, q)
	}
	if days := intArg(opts, "expiring_days", 0); days > 0 {
		until := time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02")
		where = append(where, "l.expires_at IS NOT NULL AND l.expires_at <= ?")
		args = append(args, until)
	}
	limit := intArg(opts, "limit", 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT l.id, l.item_id, i.name, i.category, l.location_id, loc.name, loc.kind,
		       l.quantity, l.unit, COALESCE(l.expires_at, ''), COALESCE(l.opened_at, ''),
		       COALESCE(l.purchased_at, ''), l.source, l.notes, l.created_at, l.updated_at
		  FROM lots l
		  JOIN items i ON i.project_id = l.project_id AND i.id = l.item_id
		  JOIN locations loc ON loc.project_id = l.project_id AND loc.id = l.location_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY CASE WHEN l.expires_at IS NULL THEN 1 ELSE 0 END, l.expires_at, lower(i.name), l.created_at
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLots(rows)
}

func scanLots(rows *sql.Rows) ([]Lot, error) {
	out := []Lot{}
	for rows.Next() {
		var l Lot
		if err := rows.Scan(&l.ID, &l.ItemID, &l.ItemName, &l.Category, &l.LocationID, &l.LocationName, &l.LocationKind, &l.Quantity, &l.Unit, &l.ExpiresAt, &l.OpenedAt, &l.PurchasedAt, &l.Source, &l.Notes, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func updateLot(db *sql.DB, pid string, id int64, patch map[string]any) (*Lot, error) {
	lot, err := getLot(db, pid, id)
	if err != nil {
		return nil, err
	}
	quantity := lot.Quantity
	unit := lot.Unit
	locationID := lot.LocationID
	expires := lot.ExpiresAt
	opened := lot.OpenedAt
	notes := lot.Notes
	if _, ok := patch["quantity"]; ok {
		quantity = floatArg(patch, "quantity", quantity)
	}
	if v, ok := patch["unit"].(string); ok {
		unit = strings.TrimSpace(v)
	}
	if _, ok := patch["location_id"]; ok {
		locationID = int64(intArg(patch, "location_id", int(locationID)))
	}
	if v, ok := patch["location"].(string); ok {
		loc, err := ensureLocation(db, pid, v)
		if err != nil {
			return nil, err
		}
		locationID = loc.ID
	}
	if v, ok := patch["expires_at"].(string); ok {
		expires = normaliseDate(v)
	}
	if v, ok := patch["opened_at"].(string); ok {
		opened = normaliseDate(v)
	}
	if v, ok := patch["notes"].(string); ok {
		notes = strings.TrimSpace(v)
	}
	if quantity < 0 {
		return nil, errors.New("quantity cannot be negative")
	}
	_, err = db.Exec(`UPDATE lots SET quantity = ?, unit = ?, location_id = ?, expires_at = NULLIF(?, ''), opened_at = NULLIF(?, ''), notes = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, quantity, unit, locationID, expires, opened, notes, pid, id)
	if err != nil {
		return nil, err
	}
	if quantity <= 0 {
		return nil, db.QueryRow(`DELETE FROM lots WHERE project_id = ? AND id = ? RETURNING id`, pid, id).Scan(&id)
	}
	return getLot(db, pid, id)
}

func expiringLots(db *sql.DB, pid string, days, limit int) ([]Lot, error) {
	if days <= 0 {
		days = 14
	}
	return listLots(db, pid, map[string]any{"expiring_days": days, "limit": limit})
}

func lowStockItems(db *sql.DB, pid string) ([]ShoppingLine, error) {
	rows, err := db.Query(`
		SELECT i.id, i.name, i.category, i.default_unit,
		       COALESCE(SUM(l.quantity), 0) AS current_quantity,
		       i.min_quantity, i.target_quantity
		  FROM items i
		  LEFT JOIN lots l ON l.project_id = i.project_id AND l.item_id = i.id AND l.quantity > 0
		 WHERE i.project_id = ? AND i.archived = 0 AND i.min_quantity > 0
		 GROUP BY i.id
		HAVING current_quantity <= i.min_quantity
		 ORDER BY lower(i.name)`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShoppingLine{}
	for rows.Next() {
		var s ShoppingLine
		if err := rows.Scan(&s.ItemID, &s.Name, &s.Category, &s.Unit, &s.CurrentQuantity, &s.MinQuantity, &s.TargetQuantity); err != nil {
			return nil, err
		}
		target := s.TargetQuantity
		if target <= 0 {
			target = s.MinQuantity
		}
		s.BuyQuantity = round2(target - s.CurrentQuantity)
		if s.BuyQuantity < 0 {
			s.BuyQuantity = 0
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func createShoppingItem(db *sql.DB, pid string, in map[string]any) (*ShoppingItem, error) {
	name := cleanName(strArg(in, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	qty := floatArg(in, "quantity", 1)
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	unit := strings.TrimSpace(strArg(in, "unit", ""))
	if unit == "" {
		unit = "each"
	}
	var itemID any
	if id := intArg(in, "item_id", 0); id > 0 {
		itemID = id
	} else if item, err := getItemByName(db, pid, name); err == nil {
		itemID = item.ID
		if unit == "each" && item.DefaultUnit != "" {
			unit = item.DefaultUnit
		}
	}
	source := normaliseShoppingSource(strArg(in, "source", "manual"))
	status := normaliseShoppingStatus(strArg(in, "status", "open"))
	res, err := db.Exec(
		`INSERT INTO shopping_list_items (project_id, item_id, name, quantity, unit, category, store, source, status, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, itemID, name, qty, unit, strings.TrimSpace(strArg(in, "category", "")),
		strings.TrimSpace(strArg(in, "store", "")), source, status, strings.TrimSpace(strArg(in, "notes", "")),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getShoppingItem(db, pid, id)
}

func getShoppingItem(db *sql.DB, pid string, id int64) (*ShoppingItem, error) {
	rows, err := db.Query(
		`SELECT id, item_id, name, quantity, unit, category, store, source, status, notes, created_at, updated_at
		   FROM shopping_list_items
		  WHERE project_id = ? AND id = ?`, pid, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanShoppingItems(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func listShoppingItems(db *sql.DB, pid, status string, limit int) ([]ShoppingItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	status = normaliseShoppingListFilter(status)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, item_id, name, quantity, unit, category, store, source, status, notes, created_at, updated_at
		   FROM shopping_list_items
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY CASE status WHEN 'open' THEN 0 WHEN 'checked' THEN 1 WHEN 'purchased' THEN 2 ELSE 3 END,
		           lower(category), lower(name), created_at
		  LIMIT ?`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShoppingItems(rows)
}

func scanShoppingItems(rows *sql.Rows) ([]ShoppingItem, error) {
	out := []ShoppingItem{}
	for rows.Next() {
		var it ShoppingItem
		var itemID sql.NullInt64
		if err := rows.Scan(&it.ID, &itemID, &it.Name, &it.Quantity, &it.Unit, &it.Category, &it.Store, &it.Source, &it.Status, &it.Notes, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		if itemID.Valid {
			id := itemID.Int64
			it.ItemID = &id
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func updateShoppingItem(db *sql.DB, pid string, id int64, patch map[string]any) (*ShoppingItem, error) {
	item, err := getShoppingItem(db, pid, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"item_id":  item.ItemID,
		"name":     item.Name,
		"quantity": item.Quantity,
		"unit":     item.Unit,
		"category": item.Category,
		"store":    item.Store,
		"source":   item.Source,
		"status":   item.Status,
		"notes":    item.Notes,
	}
	if _, ok := patch["item_id"]; ok {
		if id := intArg(patch, "item_id", 0); id > 0 {
			v := int64(id)
			fields["item_id"] = &v
		} else {
			fields["item_id"] = (*int64)(nil)
		}
	}
	for _, key := range []string{"name", "unit", "category", "store", "notes"} {
		if v, ok := patch[key].(string); ok {
			if key == "name" {
				fields[key] = cleanName(v)
			} else {
				fields[key] = strings.TrimSpace(v)
			}
		}
	}
	if _, ok := patch["quantity"]; ok {
		fields["quantity"] = floatArg(patch, "quantity", item.Quantity)
	}
	if v, ok := patch["source"].(string); ok {
		fields["source"] = normaliseShoppingSource(v)
	}
	if v, ok := patch["status"].(string); ok {
		fields["status"] = normaliseShoppingStatus(v)
	}
	if fields["name"] == "" {
		return nil, errors.New("name required")
	}
	if fields["quantity"].(float64) <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	itemID := fields["item_id"]
	if p, ok := itemID.(*int64); ok {
		if p == nil {
			itemID = nil
		} else {
			itemID = *p
		}
	}
	_, err = db.Exec(
		`UPDATE shopping_list_items
		    SET item_id = ?, name = ?, quantity = ?, unit = ?, category = ?, store = ?, source = ?, status = ?, notes = ?, updated_at = CURRENT_TIMESTAMP
		  WHERE project_id = ? AND id = ?`,
		itemID, fields["name"], fields["quantity"], fields["unit"], fields["category"], fields["store"], fields["source"], fields["status"], fields["notes"], pid, id,
	)
	if err != nil {
		return nil, err
	}
	return getShoppingItem(db, pid, id)
}

func deleteShoppingItem(db *sql.DB, pid string, id int64) (int64, error) {
	res, err := db.Exec(`DELETE FROM shopping_list_items WHERE project_id = ? AND id = ?`, pid, id)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func shoppingList(db *sql.DB, pid string) (*ShoppingList, error) {
	items, err := listShoppingItems(db, pid, "all", 200)
	if err != nil {
		return nil, err
	}
	suggestions, err := lowStockItems(db, pid)
	if err != nil {
		return nil, err
	}
	return &ShoppingList{Items: items, Suggestions: suggestions}, nil
}

func pantrySummary(db *sql.DB, pid string, days int) (string, error) {
	exp, err := expiringLots(db, pid, days, 8)
	if err != nil {
		return "", err
	}
	low, err := lowStockItems(db, pid)
	if err != nil {
		return "", err
	}
	if days <= 0 {
		days = 14
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pantry summary\n\n")
	if len(exp) == 0 {
		fmt.Fprintf(&b, "- Nothing expires in the next %d days.\n", days)
	} else {
		fmt.Fprintf(&b, "- Expiring in the next %d days:\n", days)
		for _, l := range exp {
			fmt.Fprintf(&b, "  - %s: %s %.2f %s in %s\n", l.ExpiresAt, l.ItemName, l.Quantity, l.Unit, l.LocationName)
		}
	}
	if len(low) == 0 {
		fmt.Fprintf(&b, "- No low-stock items.\n")
	} else {
		fmt.Fprintf(&b, "- Shopping suggestions:\n")
		for _, s := range low {
			fmt.Fprintf(&b, "  - %s: buy %.2f %s (have %.2f)\n", s.Name, s.BuyQuantity, s.Unit, s.CurrentQuantity)
		}
	}
	return b.String(), nil
}

func quickAdd(db *sql.DB, pid, text, source string) (any, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text required")
	}
	lower := strings.ToLower(text)
	action := "add"
	for _, prefix := range []string{"use ", "used ", "eat ", "ate ", "discard ", "threw away ", "remove "} {
		if strings.HasPrefix(lower, prefix) {
			action = "use"
			if strings.HasPrefix(lower, "discard ") || strings.HasPrefix(lower, "threw away ") || strings.HasPrefix(lower, "remove ") {
				action = "discard"
			}
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}
	if strings.HasPrefix(lower, "add ") || strings.HasPrefix(lower, "bought ") || strings.HasPrefix(lower, "buy ") {
		parts := strings.SplitN(text, " ", 2)
		if len(parts) == 2 {
			text = strings.TrimSpace(parts[1])
		}
		action = "add"
	}
	args := parsePantryLine(text)
	args["source"] = source
	if action == "add" {
		lot, err := addStock(db, pid, args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"parsed": true, "action": "add", "lot": lot}, nil
	}
	args["action"] = action
	res, err := useStock(db, pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"parsed": true, "action": action, "result": res}, nil
}

var dateRe = regexp.MustCompile(`\b(20\d{2}-\d{1,2}-\d{1,2})\b`)

func parsePantryLine(text string) map[string]any {
	out := map[string]any{"quantity": 1.0}
	text = strings.TrimSpace(text)
	if m := dateRe.FindStringSubmatch(text); len(m) == 2 {
		out["expires_at"] = normaliseDate(m[1])
		text = strings.TrimSpace(strings.Replace(text, m[0], "", 1))
	}
	words := strings.Fields(text)
	filtered := []string{}
	for i := 0; i < len(words); i++ {
		w := strings.ToLower(strings.Trim(words[i], ","))
		if w == "exp" || w == "expires" || w == "expiry" || w == "best" || w == "before" {
			continue
		}
		if isLocationWord(w) {
			out["location"] = titleWord(w)
			continue
		}
		filtered = append(filtered, words[i])
	}
	words = filtered
	if len(words) > 0 {
		if q, ok := parseQuantity(words[0]); ok {
			out["quantity"] = q
			words = words[1:]
			if len(words) > 0 && isUnit(words[0]) {
				out["unit"] = strings.ToLower(words[0])
				words = words[1:]
			}
		}
	}
	if len(words) > 0 {
		out["name"] = cleanName(strings.Join(words, " "))
	}
	return out
}

func parseQuantity(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(s, "x"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func isUnit(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "each", "unit", "units", "kg", "g", "lb", "lbs", "oz", "l", "ml", "pack", "packs", "box", "boxes", "can", "cans", "jar", "jars", "bottle", "bottles":
		return true
	}
	return false
}

func isLocationWord(s string) bool {
	switch strings.ToLower(s) {
	case "pantry", "fridge", "freezer":
		return true
	}
	return false
}

func (a *App) toolQuickAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return quickAdd(ctx.AppDB(), projectScope(ctx), strArg(args, "text", ""), normaliseSource(strArg(args, "source", "agent")))
}
func (a *App) toolItemsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createItem(ctx.AppDB(), projectScope(ctx), args)
}
func (a *App) toolItemsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listItems(ctx.AppDB(), projectScope(ctx), strArg(args, "q", ""), strArg(args, "category", ""), boolArg(args, "include_archived", false), intArg(args, "limit", 200), "", nil)
}
func (a *App) toolItemsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := getItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)))
	if err != nil {
		return nil, err
	}
	lots, err := listLots(ctx.AppDB(), projectScope(ctx), map[string]any{"item_id": item.ID})
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": item, "lots": lots}, nil
}
func (a *App) toolItemsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return updateItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)), mapArg(args, "patch"))
}
func (a *App) toolItemsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return updateItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)), map[string]any{"archived": true})
}
func (a *App) toolStockAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return addStock(ctx.AppDB(), projectScope(ctx), args)
}
func (a *App) toolStockUse(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return useStock(ctx.AppDB(), projectScope(ctx), args)
}
func (a *App) toolLotsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listLots(ctx.AppDB(), projectScope(ctx), args)
}
func (a *App) toolLotUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return updateLot(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)), mapArg(args, "patch"))
}
func (a *App) toolLotDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	res, err := ctx.AppDB().Exec(`DELETE FROM lots WHERE project_id = ? AND id = ?`, projectScope(ctx), intArg(args, "id", 0))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"deleted": n}, nil
}
func (a *App) toolLocationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listLocations(ctx.AppDB(), projectScope(ctx))
}
func (a *App) toolLocationsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createLocation(ctx.AppDB(), projectScope(ctx), strArg(args, "name", ""), strArg(args, "kind", "pantry"))
}
func (a *App) toolExpiring(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return expiringLots(ctx.AppDB(), projectScope(ctx), intArg(args, "days", 14), intArg(args, "limit", 100))
}
func (a *App) toolLowStock(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return lowStockItems(ctx.AppDB(), projectScope(ctx))
}
func (a *App) toolShoppingItemsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createShoppingItem(ctx.AppDB(), projectScope(ctx), args)
}
func (a *App) toolShoppingItemsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listShoppingItems(ctx.AppDB(), projectScope(ctx), strArg(args, "status", "open"), intArg(args, "limit", 200))
}
func (a *App) toolShoppingItemsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return updateShoppingItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)), mapArg(args, "patch"))
}
func (a *App) toolShoppingItemsCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	status := "checked"
	if !boolArg(args, "checked", true) {
		status = "open"
	}
	return updateShoppingItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)), map[string]any{"status": status})
}
func (a *App) toolShoppingItemsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, err := deleteShoppingItem(ctx.AppDB(), projectScope(ctx), int64(intArg(args, "id", 0)))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": n}, nil
}
func (a *App) toolShoppingSuggestions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return lowStockItems(ctx.AppDB(), projectScope(ctx))
}
func (a *App) toolShoppingList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return shoppingList(ctx.AppDB(), projectScope(ctx))
}
func (a *App) toolSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	md, err := pantrySummary(ctx.AppDB(), projectScope(ctx), intArg(args, "days", 14))
	if err != nil {
		return nil, err
	}
	return map[string]any{"markdown": md}, nil
}

func (a *App) handleQuick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in map[string]any
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := quickAdd(mustCtx(r).AppDB(), projectScope(mustCtx(r)), strArg(in, "text", ""), normaliseSource(strArg(in, "source", "human")))
	writeResult(w, out, err)
}

func (a *App) handleItems(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out, err := listItems(ctx.AppDB(), projectScope(ctx), q.Get("q"), q.Get("category"), q.Get("include_archived") == "true", atoiOr(q.Get("limit"), 200), "", nil)
		writeResult(w, out, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := createItem(ctx.AppDB(), projectScope(ctx), in)
		writeResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleItem(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/items/")
	switch r.Method {
	case http.MethodGet:
		item, err := getItem(ctx.AppDB(), projectScope(ctx), id)
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		lots, err := listLots(ctx.AppDB(), projectScope(ctx), map[string]any{"item_id": item.ID})
		writeResult(w, map[string]any{"item": item, "lots": lots}, err)
	case http.MethodPatch:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := updateItem(ctx.AppDB(), projectScope(ctx), id, in)
		writeResult(w, out, err)
	case http.MethodDelete:
		out, err := updateItem(ctx.AppDB(), projectScope(ctx), id, map[string]any{"archived": true})
		writeResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleLots(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	out, err := listLots(ctx.AppDB(), projectScope(ctx), map[string]any{
		"item_id":       atoiOr(q.Get("item_id"), 0),
		"location_id":   atoiOr(q.Get("location_id"), 0),
		"expiring_days": atoiOr(q.Get("expiring_days"), 0),
		"q":             q.Get("q"),
		"limit":         atoiOr(q.Get("limit"), 200),
	})
	writeResult(w, out, err)
}

func (a *App) handleLot(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/lots/")
	switch r.Method {
	case http.MethodPatch:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := updateLot(ctx.AppDB(), projectScope(ctx), id, in)
		writeResult(w, out, err)
	case http.MethodDelete:
		res, err := ctx.AppDB().Exec(`DELETE FROM lots WHERE project_id = ? AND id = ?`, projectScope(ctx), id)
		if err != nil {
			writeResult(w, nil, err)
			return
		}
		n, _ := res.RowsAffected()
		writeJSON(w, map[string]any{"deleted": n})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleLocations(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		out, err := listLocations(ctx.AppDB(), projectScope(ctx))
		writeResult(w, out, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := createLocation(ctx.AppDB(), projectScope(ctx), strArg(in, "name", ""), strArg(in, "kind", "pantry"))
		writeResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleStockAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in map[string]any
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := addStock(mustCtx(r).AppDB(), projectScope(mustCtx(r)), in)
	writeResult(w, out, err)
}

func (a *App) handleStockUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in map[string]any
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := useStock(mustCtx(r).AppDB(), projectScope(mustCtx(r)), in)
	writeResult(w, out, err)
}

func (a *App) handleExpiring(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := expiringLots(mustCtx(r).AppDB(), projectScope(mustCtx(r)), atoiOr(q.Get("days"), 14), atoiOr(q.Get("limit"), 100))
	writeResult(w, out, err)
}

func (a *App) handleLowStock(w http.ResponseWriter, r *http.Request) {
	out, err := lowStockItems(mustCtx(r).AppDB(), projectScope(mustCtx(r)))
	writeResult(w, out, err)
}

func (a *App) handleShoppingItems(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out, err := listShoppingItems(ctx.AppDB(), projectScope(ctx), q.Get("status"), atoiOr(q.Get("limit"), 200))
		writeResult(w, out, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := createShoppingItem(ctx.AppDB(), projectScope(ctx), in)
		writeResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleShoppingItem(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/shopping/items/")
	switch r.Method {
	case http.MethodPatch:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := updateShoppingItem(ctx.AppDB(), projectScope(ctx), id, in)
		writeResult(w, out, err)
	case http.MethodDelete:
		n, err := deleteShoppingItem(ctx.AppDB(), projectScope(ctx), id)
		writeResult(w, map[string]any{"deleted": n}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleShoppingList(w http.ResponseWriter, r *http.Request) {
	out, err := shoppingList(mustCtx(r).AppDB(), projectScope(mustCtx(r)))
	writeResult(w, out, err)
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	md, err := pantrySummary(mustCtx(r).AppDB(), projectScope(mustCtx(r)), atoiOr(r.URL.Query().Get("days"), 14))
	writeResult(w, map[string]any{"markdown": md}, err)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func writeResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func cleanName(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func titleWord(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	return strings.ToUpper(s[:1]) + s[1:]
}

func normaliseLocationKind(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fridge", "freezer", "other":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "pantry"
	}
}

func guessLocationKind(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fridge", "refrigerator":
		return "fridge"
	case "freezer":
		return "freezer"
	default:
		return "pantry"
	}
}

func normaliseSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "agent", "device", "import":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "human"
	}
}

func normaliseShoppingSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low_stock", "agent", "recipe":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "manual"
	}
}

func normaliseShoppingStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "checked", "dismissed", "purchased":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "open"
	}
}

func normaliseShoppingListFilter(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "checked", "dismissed", "purchased", "all":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "open"
	}
}

func normaliseDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02", "2006-1-2", time.RFC3339, "2006/01/02", "2006/1/2"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func typ(name string) map[string]any {
	return map[string]any{"type": name}
}

func itemProps() map[string]any {
	return map[string]any{
		"name":            typ("string"),
		"category":        typ("string"),
		"barcode":         typ("string"),
		"brand":           typ("string"),
		"default_unit":    typ("string"),
		"min_quantity":    typ("number"),
		"target_quantity": typ("number"),
		"notes":           typ("string"),
	}
}

func stockAddProps() map[string]any {
	p := itemProps()
	p["item_id"] = map[string]any{"type": "integer"}
	p["quantity"] = map[string]any{"type": "number"}
	p["unit"] = map[string]any{"type": "string"}
	p["location"] = map[string]any{"type": "string"}
	p["location_id"] = map[string]any{"type": "integer"}
	p["expires_at"] = map[string]any{"type": "string"}
	p["opened_at"] = map[string]any{"type": "string"}
	p["purchased_at"] = map[string]any{"type": "string"}
	p["source"] = map[string]any{"type": "string"}
	return p
}

func shoppingItemProps() map[string]any {
	return map[string]any{
		"item_id":  typ("integer"),
		"name":     typ("string"),
		"quantity": typ("number"),
		"unit":     typ("string"),
		"category": typ("string"),
		"store":    typ("string"),
		"source":   typ("string"),
		"status":   typ("string"),
		"notes":    typ("string"),
	}
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return def
	}
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return def
	}
}

func mapArg(args map[string]any, key string) map[string]any {
	if v, ok := args[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func round2(f float64) float64 {
	if f < 0 {
		return -round2(-f)
	}
	return float64(int64(f*100+0.5)) / 100
}

func sortLotsByExpiry(lots []Lot) {
	sort.SliceStable(lots, func(i, j int) bool {
		a, b := lots[i], lots[j]
		if a.ExpiresAt == "" && b.ExpiresAt != "" {
			return false
		}
		if a.ExpiresAt != "" && b.ExpiresAt == "" {
			return true
		}
		if a.ExpiresAt != b.ExpiresAt {
			return a.ExpiresAt < b.ExpiresAt
		}
		return a.CreatedAt < b.CreatedAt
	})
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
