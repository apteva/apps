// Apteva Recipes app - recipe library, nutrition estimates, meal planning, and
// shopping-list generation.
//
// Recipes intentionally sits beside Pantry instead of inside it. Pantry owns
// real inventory and lots; Recipes owns reusable cooking knowledge. The two are
// connected by optional name-based matching in v0.1, with explicit
// pantry_item_id mappings left open for later versions.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: recipes
display_name: Recipes
version: 0.1.3
description: Recipe library, nutrition estimates, meal planning, and pantry-aware shopping lists.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  integrations:
    - role: pantry
      kind: app
      compatible_app_names: [pantry]
      capabilities: [inventory.read]
      required: false
      label: "Pantry (optional)"
      hint: "When bound or installed, Recipes can subtract pantry stock from shopping lists and suggest meals from available ingredients."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: recipes_create, description: "Create a structured recipe with ingredients, steps, tags, and optional nutrition overrides." }
    - { name: recipes_update, description: "Patch a recipe. Ingredients, steps, and tags are replaced when supplied." }
    - { name: recipes_get, description: "Fetch one recipe with ingredients, steps, tags, and nutrition." }
    - { name: recipes_search, description: "Search recipes by title, ingredient, tag, cuisine, meal_type, or max_total_minutes." }
    - { name: recipes_archive, description: "Archive a recipe." }
    - { name: recipes_scale, description: "Scale a recipe to a target serving count." }
    - { name: recipes_estimate_nutrition, description: "Estimate nutrition for a recipe_id or ad-hoc ingredients." }
    - { name: recipes_meal_plan_add, description: "Add a recipe to the meal plan. Args: date, slot, recipe_id, servings?, notes?." }
    - { name: recipes_meal_plan_list, description: "List meal plan entries between from and to dates." }
    - { name: recipes_shopping_list, description: "Generate a merged shopping list from recipe_ids or a meal-plan window; optionally subtract pantry stock." }
    - { name: recipes_suggest_from_ingredients, description: "Rank recipes by matching available ingredient names." }
  ui_panels:
    - slot: project.page
      label: Recipes
      icon: chef-hat
      entry: /ui/RecipesPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/recipes
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/recipes.db
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
		return errors.New("recipes requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("recipes mounted", "project_id", projectScope(ctx))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/recipes", Handler: a.handleRecipes},
		{Pattern: "/recipes/", Handler: a.handleRecipe},
		{Pattern: "/meal_plan", Handler: a.handleMealPlan},
		{Pattern: "/shopping_list", Handler: a.handleShoppingList},
		{Pattern: "/suggest", Handler: a.handleSuggest},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "recipes_create", Description: "Create a recipe. Args: title, servings?, ingredients[], steps[], tags?, cuisine?, meal_type?, nutrition?.", InputSchema: schemaObject(recipeProps(), []string{"title"}), Handler: a.toolCreate},
		{Name: "recipes_update", Description: "Patch a recipe. Args: id plus any recipe fields; ingredients/steps/tags replace existing values when supplied.", InputSchema: schemaObject(withID(recipeProps()), []string{"id"}), Handler: a.toolUpdate},
		{Name: "recipes_get", Description: "Fetch one recipe. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolGet},
		{Name: "recipes_search", Description: "Search recipes. Args: q?, ingredient?, tag?, cuisine?, meal_type?, max_total_minutes?, include_archived?, limit?.", InputSchema: schemaObject(searchProps(), nil), Handler: a.toolSearch},
		{Name: "recipes_archive", Description: "Archive a recipe. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolArchive},
		{Name: "recipes_scale", Description: "Scale recipe ingredients. Args: id, servings.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "servings": typ("number")}, []string{"id", "servings"}), Handler: a.toolScale},
		{Name: "recipes_estimate_nutrition", Description: "Estimate nutrition. Args: recipe_id? or ingredients[].", InputSchema: schemaObject(map[string]any{"recipe_id": typ("integer"), "ingredients": arr("object")}, nil), Handler: a.toolEstimateNutrition},
		{Name: "recipes_meal_plan_add", Description: "Add to meal plan. Args: date, slot, recipe_id, servings?, notes?.", InputSchema: schemaObject(map[string]any{"date": typ("string"), "slot": typ("string"), "recipe_id": typ("integer"), "servings": typ("number"), "notes": typ("string")}, []string{"date", "recipe_id"}), Handler: a.toolMealPlanAdd},
		{Name: "recipes_meal_plan_list", Description: "List meal plan. Args: from?, to?, limit?.", InputSchema: schemaObject(map[string]any{"from": typ("string"), "to": typ("string"), "limit": typ("integer")}, nil), Handler: a.toolMealPlanList},
		{Name: "recipes_shopping_list", Description: "Merged shopping list. Args: recipe_ids?, meal_plan_from?, meal_plan_to?, subtract_pantry?.", InputSchema: schemaObject(map[string]any{"recipe_ids": arr("integer"), "meal_plan_from": typ("string"), "meal_plan_to": typ("string"), "subtract_pantry": typ("boolean")}, nil), Handler: a.toolShoppingList},
		{Name: "recipes_suggest_from_ingredients", Description: "Rank recipes by available ingredients. Args: ingredients[], limit?.", InputSchema: schemaObject(map[string]any{"ingredients": arr("string"), "limit": typ("integer")}, []string{"ingredients"}), Handler: a.toolSuggest},
	}
}

type Recipe struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Cuisine      string   `json:"cuisine"`
	MealType     string   `json:"meal_type"`
	Servings     float64  `json:"servings"`
	PrepMinutes  int      `json:"prep_minutes"`
	CookMinutes  int      `json:"cook_minutes"`
	TotalMinutes int      `json:"total_minutes"`
	SourceURL    string   `json:"source_url"`
	Notes        string   `json:"notes"`
	Archived     bool     `json:"archived"`
	Tags         []string `json:"tags,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type Ingredient struct {
	ID             int64   `json:"id,omitempty"`
	RecipeID       int64   `json:"recipe_id,omitempty"`
	Position       int     `json:"position"`
	Name           string  `json:"name"`
	NormalizedName string  `json:"normalized_name"`
	Quantity       float64 `json:"quantity"`
	Unit           string  `json:"unit"`
	Preparation    string  `json:"preparation"`
	Optional       bool    `json:"optional"`
	PantryItemID   int64   `json:"pantry_item_id,omitempty"`
	Nutrition      Macro   `json:"nutrition"`
	Source         string  `json:"source"`
}

type Step struct {
	ID          int64  `json:"id,omitempty"`
	RecipeID    int64  `json:"recipe_id,omitempty"`
	Position    int    `json:"position"`
	Instruction string `json:"instruction"`
	TimerMin    int    `json:"timer_min"`
}

type RecipeDetail struct {
	Recipe
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []Step       `json:"steps"`
	Nutrition   Nutrition    `json:"nutrition"`
}

type Macro struct {
	Calories float64 `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	CarbsG   float64 `json:"carbs_g"`
	FatG     float64 `json:"fat_g"`
	FiberG   float64 `json:"fiber_g"`
	SugarG   float64 `json:"sugar_g"`
	SodiumMG float64 `json:"sodium_mg"`
}

type Nutrition struct {
	Total      Macro   `json:"total"`
	PerServing Macro   `json:"per_serving"`
	Servings   float64 `json:"servings"`
	Confidence string  `json:"confidence"`
}

type MealPlanEntry struct {
	ID        int64         `json:"id"`
	Date      string        `json:"date"`
	Slot      string        `json:"slot"`
	RecipeID  int64         `json:"recipe_id"`
	Title     string        `json:"title"`
	Servings  float64       `json:"servings"`
	Notes     string        `json:"notes"`
	CookedAt  string        `json:"cooked_at,omitempty"`
	Nutrition Nutrition     `json:"nutrition"`
	Recipe    *RecipeDetail `json:"recipe,omitempty"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

type ShoppingLine struct {
	Name              string  `json:"name"`
	NormalizedName    string  `json:"normalized_name"`
	Quantity          float64 `json:"quantity"`
	Unit              string  `json:"unit"`
	RecipeCount       int     `json:"recipe_count"`
	AvailableQuantity float64 `json:"available_quantity,omitempty"`
	BuyQuantity       float64 `json:"buy_quantity"`
	InPantry          bool    `json:"in_pantry"`
	Optional          bool    `json:"optional"`
}

type Suggestion struct {
	RecipeID          int64    `json:"recipe_id"`
	Title             string   `json:"title"`
	Matched           []string `json:"matched"`
	Missing           []string `json:"missing"`
	MatchRatio        float64  `json:"match_ratio"`
	RequiredCount     int      `json:"required_count"`
	MatchedCount      int      `json:"matched_count"`
	TotalMinutes      int      `json:"total_minutes"`
	Servings          float64  `json:"servings"`
	EstimatedCalories float64  `json:"estimated_calories_per_serving"`
}

type nutrient struct {
	Aliases []string
	Macro
}

var starterNutrition = []nutrient{
	{[]string{"chicken breast", "chicken"}, Macro{165, 31, 0, 3.6, 0, 0, 74}},
	{[]string{"egg", "eggs"}, Macro{143, 13, 1.1, 9.5, 0, 1.1, 142}},
	{[]string{"rice", "white rice", "cooked rice"}, Macro{130, 2.7, 28, 0.3, 0.4, 0.1, 1}},
	{[]string{"brown rice"}, Macro{123, 2.7, 25.6, 1, 1.8, 0.2, 4}},
	{[]string{"pasta"}, Macro{158, 5.8, 31, 0.9, 1.8, 0.6, 1}},
	{[]string{"spinach"}, Macro{23, 2.9, 3.6, 0.4, 2.2, 0.4, 79}},
	{[]string{"tomato", "tomatoes"}, Macro{18, 0.9, 3.9, 0.2, 1.2, 2.6, 5}},
	{[]string{"onion", "onions"}, Macro{40, 1.1, 9.3, 0.1, 1.7, 4.2, 4}},
	{[]string{"garlic"}, Macro{149, 6.4, 33, 0.5, 2.1, 1, 17}},
	{[]string{"olive oil", "oil"}, Macro{884, 0, 0, 100, 0, 0, 2}},
	{[]string{"butter"}, Macro{717, 0.9, 0.1, 81, 0, 0.1, 11}},
	{[]string{"milk"}, Macro{61, 3.2, 4.8, 3.3, 0, 5.1, 43}},
	{[]string{"cheddar", "cheese"}, Macro{403, 25, 1.3, 33, 0, 0.5, 621}},
	{[]string{"yogurt", "greek yogurt"}, Macro{59, 10, 3.6, 0.4, 0, 3.2, 36}},
	{[]string{"oats", "oatmeal"}, Macro{389, 16.9, 66, 6.9, 10.6, 0, 2}},
	{[]string{"flour"}, Macro{364, 10, 76, 1, 2.7, 0.3, 2}},
	{[]string{"sugar"}, Macro{387, 0, 100, 0, 0, 100, 1}},
	{[]string{"potato", "potatoes"}, Macro{77, 2, 17, 0.1, 2.2, 0.8, 6}},
	{[]string{"carrot", "carrots"}, Macro{41, 0.9, 10, 0.2, 2.8, 4.7, 69}},
	{[]string{"broccoli"}, Macro{34, 2.8, 6.6, 0.4, 2.6, 1.7, 33}},
	{[]string{"beans", "black beans"}, Macro{132, 8.9, 24, 0.5, 8.7, 0.3, 1}},
	{[]string{"lentils"}, Macro{116, 9, 20, 0.4, 7.9, 1.8, 2}},
	{[]string{"salmon"}, Macro{208, 20, 0, 13, 0, 0, 59}},
	{[]string{"tuna"}, Macro{132, 28, 0, 1.3, 0, 0, 47}},
	{[]string{"beef"}, Macro{250, 26, 0, 15, 0, 0, 72}},
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

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createRecipe(ctx.AppDB(), projectScope(ctx), args)
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return updateRecipe(ctx.AppDB(), projectScope(ctx), intArg(args, "id", 0), args)
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return getRecipe(ctx.AppDB(), projectScope(ctx), intArg(args, "id", 0))
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return searchRecipes(ctx.AppDB(), projectScope(ctx), args)
}

func (a *App) toolArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	_, err := ctx.AppDB().Exec(`UPDATE recipes SET archived = 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, projectScope(ctx), id)
	return map[string]any{"id": id, "archived": true}, err
}

func (a *App) toolScale(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return scaleRecipe(ctx.AppDB(), projectScope(ctx), intArg(args, "id", 0), floatArg(args, "servings", 0))
}

func (a *App) toolEstimateNutrition(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if id := intArg(args, "recipe_id", 0); id != 0 {
		d, err := getRecipe(ctx.AppDB(), projectScope(ctx), id)
		if err != nil {
			return nil, err
		}
		return d.Nutrition, nil
	}
	ings := parseIngredients(args["ingredients"])
	total := Macro{}
	for i := range ings {
		ings[i].Nutrition = estimateIngredient(ctx.AppDB(), projectScope(ctx), ings[i])
		total = addMacro(total, ings[i].Nutrition)
	}
	return Nutrition{Total: roundMacro(total), PerServing: roundMacro(total), Servings: 1, Confidence: "estimate"}, nil
}

func (a *App) toolMealPlanAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return addMealPlanEntry(ctx.AppDB(), projectScope(ctx), args)
}

func (a *App) toolMealPlanList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listMealPlan(ctx.AppDB(), projectScope(ctx), args)
}

func (a *App) toolShoppingList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return buildShoppingList(ctx, args)
}

func (a *App) toolSuggest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return suggestFromIngredients(ctx.AppDB(), projectScope(ctx), stringsSlice(args["ingredients"]), int(intArg(args, "limit", 10)))
}

func (a *App) handleRecipes(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		args := queryArgs(r)
		v, err := searchRecipes(ctx.AppDB(), projectScope(ctx), args)
		writeResult(w, v, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		v, err := createRecipe(ctx.AppDB(), projectScope(ctx), in)
		writeResult(w, v, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleRecipe(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/recipes/")
	switch r.Method {
	case http.MethodGet:
		v, err := getRecipe(ctx.AppDB(), projectScope(ctx), id)
		writeResult(w, v, err)
	case http.MethodPatch:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		v, err := updateRecipe(ctx.AppDB(), projectScope(ctx), id, in)
		writeResult(w, v, err)
	case http.MethodDelete:
		_, err := ctx.AppDB().Exec(`UPDATE recipes SET archived = 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, projectScope(ctx), id)
		writeResult(w, map[string]any{"id": id, "archived": true}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleMealPlan(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		v, err := listMealPlan(ctx.AppDB(), projectScope(ctx), queryArgs(r))
		writeResult(w, v, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		v, err := addMealPlanEntry(ctx.AppDB(), projectScope(ctx), in)
		writeResult(w, v, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleShoppingList(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	args := queryArgs(r)
	v, err := buildShoppingList(ctx, args)
	writeResult(w, v, err)
}

func (a *App) handleSuggest(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	args := queryArgs(r)
	v, err := suggestFromIngredients(ctx.AppDB(), projectScope(ctx), stringsSlice(args["ingredients"]), int(intArg(args, "limit", 10)))
	writeResult(w, v, err)
}

func createRecipe(db *sql.DB, pid string, in map[string]any) (*RecipeDetail, error) {
	title := cleanName(strArg(in, "title", ""))
	if title == "" {
		return nil, errors.New("title required")
	}
	servings := floatArg(in, "servings", 1)
	if servings <= 0 {
		servings = 1
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO recipes
		(project_id, title, description, cuisine, meal_type, servings, prep_minutes, cook_minutes, source_url, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, title, strArg(in, "description", ""), cleanName(strArg(in, "cuisine", "")), cleanName(strArg(in, "meal_type", "")),
		servings, intArg(in, "prep_minutes", 0), intArg(in, "cook_minutes", 0), strArg(in, "source_url", ""), strArg(in, "notes", ""))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := replaceIngredients(tx, pid, id, parseIngredients(in["ingredients"])); err != nil {
		return nil, err
	}
	if err := replaceSteps(tx, pid, id, parseSteps(in["steps"])); err != nil {
		return nil, err
	}
	if err := replaceTags(tx, id, stringsSlice(in["tags"])); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getRecipe(db, pid, id)
}

func updateRecipe(db *sql.DB, pid string, id int64, in map[string]any) (*RecipeDetail, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	current, err := getRecipe(db, pid, id)
	if err != nil {
		return nil, err
	}
	title := current.Title
	if hasKey(in, "title") {
		title = cleanName(strArg(in, "title", title))
	}
	servings := current.Servings
	if hasKey(in, "servings") {
		servings = floatArg(in, "servings", servings)
		if servings <= 0 {
			servings = current.Servings
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE recipes SET title = ?, description = ?, cuisine = ?, meal_type = ?, servings = ?,
		prep_minutes = ?, cook_minutes = ?, source_url = ?, notes = ?, archived = ?, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND id = ?`,
		title,
		strArg(in, "description", current.Description),
		cleanName(strArg(in, "cuisine", current.Cuisine)),
		cleanName(strArg(in, "meal_type", current.MealType)),
		servings,
		intArg(in, "prep_minutes", int64(current.PrepMinutes)),
		intArg(in, "cook_minutes", int64(current.CookMinutes)),
		strArg(in, "source_url", current.SourceURL),
		strArg(in, "notes", current.Notes),
		boolToInt(boolArg(in, "archived", current.Archived)),
		pid, id)
	if err != nil {
		return nil, err
	}
	if hasKey(in, "ingredients") {
		if err := replaceIngredients(tx, pid, id, parseIngredients(in["ingredients"])); err != nil {
			return nil, err
		}
	}
	if hasKey(in, "steps") {
		if err := replaceSteps(tx, pid, id, parseSteps(in["steps"])); err != nil {
			return nil, err
		}
	}
	if hasKey(in, "tags") {
		if err := replaceTags(tx, id, stringsSlice(in["tags"])); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getRecipe(db, pid, id)
}

func getRecipe(db *sql.DB, pid string, id int64) (*RecipeDetail, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	var r Recipe
	err := db.QueryRow(`SELECT id, title, description, cuisine, meal_type, servings, prep_minutes, cook_minutes,
		source_url, notes, archived, created_at, updated_at
		FROM recipes WHERE project_id = ? AND id = ?`, pid, id).Scan(
		&r.ID, &r.Title, &r.Description, &r.Cuisine, &r.MealType, &r.Servings, &r.PrepMinutes, &r.CookMinutes,
		&r.SourceURL, &r.Notes, &r.Archived, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.TotalMinutes = r.PrepMinutes + r.CookMinutes
	tags, err := getTags(db, id)
	if err != nil {
		return nil, err
	}
	r.Tags = tags
	ingredients, err := getIngredients(db, pid, id)
	if err != nil {
		return nil, err
	}
	steps, err := getSteps(db, pid, id)
	if err != nil {
		return nil, err
	}
	total := Macro{}
	estimated := 0
	for i := range ingredients {
		if ingredients[i].Nutrition == (Macro{}) {
			ingredients[i].Nutrition = estimateIngredient(db, pid, ingredients[i])
			estimated++
		}
		total = addMacro(total, ingredients[i].Nutrition)
	}
	return &RecipeDetail{
		Recipe:      r,
		Ingredients: ingredients,
		Steps:       steps,
		Nutrition: Nutrition{
			Total:      roundMacro(total),
			PerServing: roundMacro(divMacro(total, r.Servings)),
			Servings:   r.Servings,
			Confidence: confidence(estimated, len(ingredients)),
		},
	}, nil
}

func searchRecipes(db *sql.DB, pid string, in map[string]any) ([]RecipeDetail, error) {
	limit := intArg(in, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"r.project_id = ?"}
	args := []any{pid}
	if !boolArg(in, "include_archived", false) {
		where = append(where, "r.archived = 0")
	}
	if q := strings.TrimSpace(strArg(in, "q", "")); q != "" {
		where = append(where, `(lower(r.title) LIKE lower(?) OR lower(r.description) LIKE lower(?) OR EXISTS (
			SELECT 1 FROM recipe_ingredients ri WHERE ri.recipe_id = r.id AND lower(ri.name) LIKE lower(?)
		))`)
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	if v := cleanName(strArg(in, "cuisine", "")); v != "" {
		where = append(where, "lower(r.cuisine) = lower(?)")
		args = append(args, v)
	}
	if v := cleanName(strArg(in, "meal_type", "")); v != "" {
		where = append(where, "lower(r.meal_type) = lower(?)")
		args = append(args, v)
	}
	if v := normalizeName(strArg(in, "ingredient", "")); v != "" {
		where = append(where, "EXISTS (SELECT 1 FROM recipe_ingredients ri WHERE ri.recipe_id = r.id AND ri.normalized_name LIKE ?)")
		args = append(args, "%"+v+"%")
	}
	if v := normalizeTag(strArg(in, "tag", "")); v != "" {
		where = append(where, "EXISTS (SELECT 1 FROM recipe_tags rt WHERE rt.recipe_id = r.id AND rt.tag = ?)")
		args = append(args, v)
	}
	if max := intArg(in, "max_total_minutes", 0); max > 0 {
		where = append(where, "(r.prep_minutes + r.cook_minutes) <= ?")
		args = append(args, max)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT r.id FROM recipes r WHERE `+strings.Join(where, " AND ")+` ORDER BY lower(r.title) LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := []RecipeDetail{}
	for _, id := range ids {
		d, err := getRecipe(db, pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}

func replaceIngredients(tx *sql.Tx, pid string, recipeID int64, ingredients []Ingredient) error {
	if _, err := tx.Exec(`DELETE FROM recipe_ingredients WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for i, ing := range ingredients {
		ing.Name = cleanName(ing.Name)
		if ing.Name == "" {
			continue
		}
		if ing.Position == 0 {
			ing.Position = i + 1
		}
		ing.NormalizedName = normalizeName(ing.Name)
		if ing.Source == "" {
			ing.Source = "estimate"
		}
		m := ing.Nutrition
		if _, err := tx.Exec(`INSERT INTO recipe_ingredients
			(project_id, recipe_id, position, name, normalized_name, quantity, unit, preparation, optional, pantry_item_id,
			 calories, protein_g, carbs_g, fat_g, fiber_g, sugar_g, sodium_mg, source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, recipeID, ing.Position, ing.Name, ing.NormalizedName, ing.Quantity, strings.TrimSpace(ing.Unit),
			strings.TrimSpace(ing.Preparation), boolToInt(ing.Optional), ing.PantryItemID,
			nullIfZero(m.Calories), nullIfZero(m.ProteinG), nullIfZero(m.CarbsG), nullIfZero(m.FatG),
			nullIfZero(m.FiberG), nullIfZero(m.SugarG), nullIfZero(m.SodiumMG), normaliseNutritionSource(ing.Source)); err != nil {
			return err
		}
	}
	return nil
}

func replaceSteps(tx *sql.Tx, pid string, recipeID int64, steps []Step) error {
	if _, err := tx.Exec(`DELETE FROM recipe_steps WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for i, step := range steps {
		step.Instruction = strings.TrimSpace(step.Instruction)
		if step.Instruction == "" {
			continue
		}
		if step.Position == 0 {
			step.Position = i + 1
		}
		if _, err := tx.Exec(`INSERT INTO recipe_steps (project_id, recipe_id, position, instruction, timer_min) VALUES (?, ?, ?, ?, ?)`,
			pid, recipeID, step.Position, step.Instruction, step.TimerMin); err != nil {
			return err
		}
	}
	return nil
}

func replaceTags(tx *sql.Tx, recipeID int64, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM recipe_tags WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = normalizeTag(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		if _, err := tx.Exec(`INSERT INTO recipe_tags (recipe_id, tag) VALUES (?, ?)`, recipeID, tag); err != nil {
			return err
		}
	}
	return nil
}

func getIngredients(db *sql.DB, pid string, recipeID int64) ([]Ingredient, error) {
	rows, err := db.Query(`SELECT id, recipe_id, position, name, normalized_name, quantity, unit, preparation, optional,
		COALESCE(pantry_item_id, 0), calories, protein_g, carbs_g, fat_g, fiber_g, sugar_g, sodium_mg, source
		FROM recipe_ingredients WHERE project_id = ? AND recipe_id = ? ORDER BY position, id`, pid, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ingredient{}
	for rows.Next() {
		var ing Ingredient
		var c, p, carbs, fat, fiber, sugar, sodium sql.NullFloat64
		if err := rows.Scan(&ing.ID, &ing.RecipeID, &ing.Position, &ing.Name, &ing.NormalizedName, &ing.Quantity, &ing.Unit,
			&ing.Preparation, &ing.Optional, &ing.PantryItemID, &c, &p, &carbs, &fat, &fiber, &sugar, &sodium, &ing.Source); err != nil {
			return nil, err
		}
		ing.Nutrition = Macro{nullFloat(c), nullFloat(p), nullFloat(carbs), nullFloat(fat), nullFloat(fiber), nullFloat(sugar), nullFloat(sodium)}
		out = append(out, ing)
	}
	return out, rows.Err()
}

func getSteps(db *sql.DB, pid string, recipeID int64) ([]Step, error) {
	rows, err := db.Query(`SELECT id, recipe_id, position, instruction, timer_min FROM recipe_steps WHERE project_id = ? AND recipe_id = ? ORDER BY position, id`, pid, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var s Step
		if err := rows.Scan(&s.ID, &s.RecipeID, &s.Position, &s.Instruction, &s.TimerMin); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func getTags(db *sql.DB, recipeID int64) ([]string, error) {
	rows, err := db.Query(`SELECT tag FROM recipe_tags WHERE recipe_id = ? ORDER BY tag`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func scaleRecipe(db *sql.DB, pid string, id int64, servings float64) (map[string]any, error) {
	if servings <= 0 {
		return nil, errors.New("servings must be > 0")
	}
	d, err := getRecipe(db, pid, id)
	if err != nil {
		return nil, err
	}
	factor := servings / d.Servings
	scaled := make([]Ingredient, 0, len(d.Ingredients))
	for _, ing := range d.Ingredients {
		ing.Quantity = round2(ing.Quantity * factor)
		ing.Nutrition = roundMacro(mulMacro(ing.Nutrition, factor))
		scaled = append(scaled, ing)
	}
	nut := d.Nutrition
	nut.Total = roundMacro(mulMacro(nut.Total, factor))
	nut.PerServing = roundMacro(divMacro(nut.Total, servings))
	nut.Servings = servings
	return map[string]any{"recipe_id": id, "title": d.Title, "servings": servings, "factor": round2(factor), "ingredients": scaled, "nutrition": nut}, nil
}

func addMealPlanEntry(db *sql.DB, pid string, in map[string]any) (*MealPlanEntry, error) {
	date := normaliseDate(strArg(in, "date", ""))
	if date == "" {
		return nil, errors.New("date required")
	}
	recipeID := intArg(in, "recipe_id", 0)
	if recipeID == 0 {
		return nil, errors.New("recipe_id required")
	}
	if _, err := getRecipe(db, pid, recipeID); err != nil {
		return nil, err
	}
	servings := floatArg(in, "servings", 1)
	if servings <= 0 {
		servings = 1
	}
	slot := normaliseSlot(strArg(in, "slot", "dinner"))
	res, err := db.Exec(`INSERT INTO meal_plan_entries (project_id, plan_date, slot, recipe_id, servings, notes)
		VALUES (?, ?, ?, ?, ?, ?)`, pid, date, slot, recipeID, servings, strArg(in, "notes", ""))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	entries, err := listMealPlan(db, pid, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, sql.ErrNoRows
	}
	return &entries[0], nil
}

func listMealPlan(db *sql.DB, pid string, in map[string]any) ([]MealPlanEntry, error) {
	limit := intArg(in, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := []string{"m.project_id = ?"}
	args := []any{pid}
	if id := intArg(in, "id", 0); id != 0 {
		where = append(where, "m.id = ?")
		args = append(args, id)
	}
	if from := normaliseDate(strArg(in, "from", "")); from != "" {
		where = append(where, "m.plan_date >= ?")
		args = append(args, from)
	}
	if to := normaliseDate(strArg(in, "to", "")); to != "" {
		where = append(where, "m.plan_date <= ?")
		args = append(args, to)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT m.id, m.plan_date, m.slot, m.recipe_id, r.title, m.servings, m.notes,
		COALESCE(m.cooked_at, ''), m.created_at, m.updated_at
		FROM meal_plan_entries m JOIN recipes r ON r.id = m.recipe_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.plan_date, CASE m.slot WHEN 'breakfast' THEN 1 WHEN 'lunch' THEN 2 WHEN 'dinner' THEN 3 WHEN 'snack' THEN 4 ELSE 5 END, m.id
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	out := []MealPlanEntry{}
	for rows.Next() {
		var e MealPlanEntry
		if err := rows.Scan(&e.ID, &e.Date, &e.Slot, &e.RecipeID, &e.Title, &e.Servings, &e.Notes, &e.CookedAt, &e.CreatedAt, &e.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		e := &out[i]
		d, err := getRecipe(db, pid, e.RecipeID)
		if err != nil {
			return nil, err
		}
		scaled, err := scaleRecipe(db, pid, e.RecipeID, e.Servings)
		if err != nil {
			return nil, err
		}
		e.Nutrition, _ = scaled["nutrition"].(Nutrition)
		e.Recipe = d
	}
	return out, nil
}

func buildShoppingList(ctx *sdk.AppCtx, in map[string]any) ([]ShoppingLine, error) {
	pid := projectScope(ctx)
	db := ctx.AppDB()
	type sourceRecipe struct {
		recipeID int64
		servings float64
	}
	var sources []sourceRecipe
	for _, id := range intSlice(in["recipe_ids"]) {
		d, err := getRecipe(db, pid, id)
		if err != nil {
			return nil, err
		}
		sources = append(sources, sourceRecipe{recipeID: id, servings: d.Servings})
	}
	if from := normaliseDate(strArg(in, "meal_plan_from", "")); from != "" {
		to := normaliseDate(strArg(in, "meal_plan_to", from))
		entries, err := listMealPlan(db, pid, map[string]any{"from": from, "to": to, "limit": 500})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			sources = append(sources, sourceRecipe{recipeID: e.RecipeID, servings: e.Servings})
		}
	}
	lines := map[string]*ShoppingLine{}
	for _, src := range sources {
		scaled, err := scaleRecipe(db, pid, src.recipeID, src.servings)
		if err != nil {
			return nil, err
		}
		ingredients, _ := scaled["ingredients"].([]Ingredient)
		for _, ing := range ingredients {
			if ing.Optional {
				continue
			}
			key := ing.NormalizedName + "|" + strings.ToLower(strings.TrimSpace(ing.Unit))
			line := lines[key]
			if line == nil {
				line = &ShoppingLine{Name: ing.Name, NormalizedName: ing.NormalizedName, Unit: ing.Unit, Optional: ing.Optional}
				lines[key] = line
			}
			line.Quantity += ing.Quantity
			line.BuyQuantity += ing.Quantity
			line.RecipeCount++
		}
	}
	out := make([]ShoppingLine, 0, len(lines))
	for _, line := range lines {
		line.Quantity = round2(line.Quantity)
		line.BuyQuantity = round2(line.BuyQuantity)
		out = append(out, *line)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if boolArg(in, "subtract_pantry", false) {
		subtractPantry(ctx, out)
	}
	return out, nil
}

func subtractPantry(ctx *sdk.AppCtx, lines []ShoppingLine) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return
	}
	var pantryItems []struct {
		Name            string  `json:"name"`
		DefaultUnit     string  `json:"default_unit"`
		TotalQuantity   float64 `json:"total_quantity"`
		CurrentQuantity float64 `json:"current_quantity"`
	}
	if err := ctx.PlatformAPI().CallAppResult("pantry", "pantry_items_list", map[string]any{"limit": 500}, &pantryItems); err != nil {
		return
	}
	stock := map[string]float64{}
	for _, it := range pantryItems {
		qty := it.TotalQuantity
		if qty == 0 {
			qty = it.CurrentQuantity
		}
		key := normalizeName(it.Name) + "|" + strings.ToLower(strings.TrimSpace(it.DefaultUnit))
		stock[key] += qty
		stock[normalizeName(it.Name)+"|"] += qty
	}
	for i := range lines {
		key := lines[i].NormalizedName + "|" + strings.ToLower(strings.TrimSpace(lines[i].Unit))
		available := stock[key]
		if available == 0 {
			available = stock[lines[i].NormalizedName+"|"]
		}
		if available > 0 {
			lines[i].InPantry = true
			lines[i].AvailableQuantity = round2(available)
			lines[i].BuyQuantity = round2(maxFloat(0, lines[i].Quantity-available))
		}
	}
}

func suggestFromIngredients(db *sql.DB, pid string, available []string, limit int) ([]Suggestion, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	have := map[string]bool{}
	for _, item := range available {
		if n := normalizeName(item); n != "" {
			have[n] = true
		}
	}
	recipes, err := searchRecipes(db, pid, map[string]any{"limit": 500})
	if err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, recipe := range recipes {
		seen := map[string]bool{}
		var matched, missing []string
		for _, ing := range recipe.Ingredients {
			if ing.Optional || seen[ing.NormalizedName] {
				continue
			}
			seen[ing.NormalizedName] = true
			if have[ing.NormalizedName] || fuzzyHave(have, ing.NormalizedName) {
				matched = append(matched, ing.Name)
			} else {
				missing = append(missing, ing.Name)
			}
		}
		required := len(matched) + len(missing)
		if required == 0 {
			continue
		}
		ratio := float64(len(matched)) / float64(required)
		out = append(out, Suggestion{
			RecipeID: recipe.ID, Title: recipe.Title, Matched: matched, Missing: missing,
			MatchRatio: round2(ratio), RequiredCount: required, MatchedCount: len(matched),
			TotalMinutes: recipe.TotalMinutes, Servings: recipe.Servings,
			EstimatedCalories: recipe.Nutrition.PerServing.Calories,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MatchRatio != out[j].MatchRatio {
			return out[i].MatchRatio > out[j].MatchRatio
		}
		if len(out[i].Missing) != len(out[j].Missing) {
			return len(out[i].Missing) < len(out[j].Missing)
		}
		return out[i].TotalMinutes < out[j].TotalMinutes
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func estimateIngredient(db *sql.DB, pid string, ing Ingredient) Macro {
	if ing.Nutrition != (Macro{}) {
		return roundMacro(ing.Nutrition)
	}
	if m, ok := nutritionOverride(db, pid, ing.NormalizedName); ok {
		return roundMacro(mulMacro(m, gramsFor(ing)/100))
	}
	if m, ok := starterMacro(ing.NormalizedName); ok {
		return roundMacro(mulMacro(m, gramsFor(ing)/100))
	}
	return Macro{}
}

func nutritionOverride(db *sql.DB, pid, name string) (Macro, bool) {
	var grams float64
	var m Macro
	err := db.QueryRow(`SELECT grams, calories, protein_g, carbs_g, fat_g, fiber_g, sugar_g, sodium_mg
		FROM nutrition_overrides WHERE project_id = ? AND normalized_name = ?`, pid, name).
		Scan(&grams, &m.Calories, &m.ProteinG, &m.CarbsG, &m.FatG, &m.FiberG, &m.SugarG, &m.SodiumMG)
	if err != nil || grams <= 0 {
		return Macro{}, false
	}
	return divMacro(mulMacro(m, 100), grams), true
}

func starterMacro(name string) (Macro, bool) {
	name = normalizeName(name)
	for _, n := range starterNutrition {
		for _, alias := range n.Aliases {
			a := normalizeName(alias)
			if name == a || strings.Contains(name, a) || strings.Contains(a, name) {
				return n.Macro, true
			}
		}
	}
	return Macro{}, false
}

func gramsFor(ing Ingredient) float64 {
	q := ing.Quantity
	if q <= 0 {
		q = 1
	}
	unit := strings.ToLower(strings.TrimSpace(ing.Unit))
	switch unit {
	case "g", "gram", "grams":
		return q
	case "kg", "kilogram", "kilograms":
		return q * 1000
	case "mg":
		return q / 1000
	case "ml", "milliliter", "milliliters":
		return q
	case "l", "liter", "liters":
		return q * 1000
	case "tbsp", "tablespoon", "tablespoons":
		return q * 15
	case "tsp", "teaspoon", "teaspoons":
		return q * 5
	case "cup", "cups":
		return q * 240
	case "oz", "ounce", "ounces":
		return q * 28.3495
	case "lb", "pound", "pounds":
		return q * 453.592
	case "each", "piece", "pieces", "":
		return q * 100
	default:
		return q * 100
	}
}

func parseIngredients(v any) []Ingredient {
	arrv, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Ingredient, 0, len(arrv))
	for i, raw := range arrv {
		m, ok := raw.(map[string]any)
		if !ok {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, Ingredient{Position: i + 1, Name: cleanName(s), Quantity: 1, Unit: ""})
			}
			continue
		}
		name := cleanName(strArg(m, "name", ""))
		if name == "" {
			continue
		}
		ing := Ingredient{
			Position:     int(intArg(m, "position", int64(i+1))),
			Name:         name,
			Quantity:     floatArg(m, "quantity", 0),
			Unit:         strings.TrimSpace(strArg(m, "unit", "")),
			Preparation:  strings.TrimSpace(strArg(m, "preparation", "")),
			Optional:     boolArg(m, "optional", false),
			PantryItemID: intArg(m, "pantry_item_id", 0),
			Source:       normaliseNutritionSource(strArg(m, "source", "estimate")),
		}
		ing.NormalizedName = normalizeName(ing.Name)
		if n, ok := m["nutrition"].(map[string]any); ok {
			ing.Nutrition = macroFromMap(n)
			ing.Source = "manual"
		} else {
			ing.Nutrition = Macro{
				Calories: floatArg(m, "calories", 0), ProteinG: floatArg(m, "protein_g", 0),
				CarbsG: floatArg(m, "carbs_g", 0), FatG: floatArg(m, "fat_g", 0),
				FiberG: floatArg(m, "fiber_g", 0), SugarG: floatArg(m, "sugar_g", 0), SodiumMG: floatArg(m, "sodium_mg", 0),
			}
			if ing.Nutrition != (Macro{}) {
				ing.Source = "manual"
			}
		}
		out = append(out, ing)
	}
	return out
}

func parseSteps(v any) []Step {
	arrv, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Step, 0, len(arrv))
	for i, raw := range arrv {
		if s, ok := raw.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, Step{Position: i + 1, Instruction: s})
			}
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(strArg(m, "instruction", strArg(m, "text", "")))
		if text == "" {
			continue
		}
		out = append(out, Step{Position: int(intArg(m, "position", int64(i+1))), Instruction: text, TimerMin: int(intArg(m, "timer_min", 0))})
	}
	return out
}

func macroFromMap(m map[string]any) Macro {
	return Macro{
		Calories: floatArg(m, "calories", 0), ProteinG: floatArg(m, "protein_g", 0), CarbsG: floatArg(m, "carbs_g", 0),
		FatG: floatArg(m, "fat_g", 0), FiberG: floatArg(m, "fiber_g", 0), SugarG: floatArg(m, "sugar_g", 0), SodiumMG: floatArg(m, "sodium_mg", 0),
	}
}

func addMacro(a, b Macro) Macro {
	return Macro{a.Calories + b.Calories, a.ProteinG + b.ProteinG, a.CarbsG + b.CarbsG, a.FatG + b.FatG, a.FiberG + b.FiberG, a.SugarG + b.SugarG, a.SodiumMG + b.SodiumMG}
}

func mulMacro(a Macro, f float64) Macro {
	return Macro{a.Calories * f, a.ProteinG * f, a.CarbsG * f, a.FatG * f, a.FiberG * f, a.SugarG * f, a.SodiumMG * f}
}

func divMacro(a Macro, f float64) Macro {
	if f <= 0 {
		return a
	}
	return mulMacro(a, 1/f)
}

func roundMacro(a Macro) Macro {
	return Macro{round2(a.Calories), round2(a.ProteinG), round2(a.CarbsG), round2(a.FatG), round2(a.FiberG), round2(a.SugarG), round2(a.SodiumMG)}
}

func confidence(estimated, total int) string {
	if total == 0 {
		return "none"
	}
	if estimated == total {
		return "estimate"
	}
	if estimated == 0 {
		return "manual"
	}
	return "mixed"
}

func fuzzyHave(have map[string]bool, wanted string) bool {
	for h := range have {
		if strings.Contains(h, wanted) || strings.Contains(wanted, h) {
			return true
		}
	}
	return false
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

func httpErr(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func queryArgs(r *http.Request) map[string]any {
	q := r.URL.Query()
	out := map[string]any{}
	for k, values := range q {
		if len(values) == 1 {
			out[k] = values[0]
		} else {
			tmp := make([]any, 0, len(values))
			for _, v := range values {
				tmp = append(tmp, v)
			}
			out[k] = tmp
		}
	}
	return out
}

func cleanName(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func normalizeName(s string) string {
	s = strings.ToLower(cleanName(s))
	replacer := strings.NewReplacer(",", "", ".", "", "(", "", ")", "", "[", "", "]", "", "'", "")
	s = replacer.Replace(s)
	for _, suffix := range []string{" chopped", " diced", " sliced", " minced", " cooked", " raw", " fresh", " frozen"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

func normalizeTag(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), "# ")
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

func normaliseSlot(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "breakfast", "lunch", "dinner", "snack":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "other"
	}
}

func normaliseNutritionSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "manual", "import":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "estimate"
	}
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func strArg(m map[string]any, key, def string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return def
		}
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

func intArg(m map[string]any, key string, def int64) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func floatArg(m map[string]any, key string, def float64) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case float64:
		return t
	case json.Number:
		n, _ := t.Float64()
		return n
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return n
		}
	}
	return def
}

func boolArg(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return t != 0
	}
	return def
}

func stringsSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s := cleanName(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.Contains(t, ",") {
			parts := strings.Split(t, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = cleanName(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
		if s := cleanName(t); s != "" {
			return []string{s}
		}
	}
	return nil
}

func intSlice(v any) []int64 {
	switch t := v.(type) {
	case []int64:
		return t
	case []any:
		out := make([]int64, 0, len(t))
		for _, x := range t {
			if n := intArg(map[string]any{"x": x}, "x", 0); n != 0 {
				out = append(out, n)
			}
		}
		return out
	case string:
		var out []int64
		for _, p := range strings.Split(t, ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n != 0 {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullFloat(n sql.NullFloat64) float64 {
	if n.Valid {
		return n.Float64
	}
	return 0
}

func nullIfZero(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func round2(f float64) float64 {
	if f < 0 {
		return -round2(-f)
	}
	return float64(int64(f*100+0.5)) / 100
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

func arr(itemType string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": itemType}}
}

func recipeProps() map[string]any {
	return map[string]any{
		"title": typ("string"), "description": typ("string"), "cuisine": typ("string"), "meal_type": typ("string"),
		"servings": typ("number"), "prep_minutes": typ("integer"), "cook_minutes": typ("integer"),
		"source_url": typ("string"), "notes": typ("string"), "tags": arr("string"),
		"ingredients": arr("object"), "steps": arr("object"), "archived": typ("boolean"),
	}
}

func withID(props map[string]any) map[string]any {
	out := map[string]any{"id": typ("integer")}
	for k, v := range props {
		out[k] = v
	}
	return out
}

func searchProps() map[string]any {
	return map[string]any{
		"q": typ("string"), "ingredient": typ("string"), "tag": typ("string"), "cuisine": typ("string"),
		"meal_type": typ("string"), "max_total_minutes": typ("integer"), "include_archived": typ("boolean"), "limit": typ("integer"),
	}
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
