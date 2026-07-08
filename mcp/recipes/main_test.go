package main

import (
	"database/sql"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestManifestParses(t *testing.T) {
	if (&App{}).Manifest().Name != "recipes" {
		t.Fatal("embedded manifest did not parse as recipes")
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "recipes" {
		t.Fatalf("external manifest name = %q, want recipes", m.Name)
	}
}

func TestCreateRecipeEstimatesNutritionAndScales(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	recipe, err := createRecipe(db, pid, map[string]any{
		"title":        "Chicken Rice Bowl",
		"servings":     2.0,
		"prep_minutes": 10.0,
		"cook_minutes": 20.0,
		"tags":         []any{"high-protein", "dinner"},
		"ingredients": []any{
			map[string]any{"name": "chicken breast", "quantity": 200.0, "unit": "g"},
			map[string]any{"name": "rice", "quantity": 200.0, "unit": "g"},
			map[string]any{"name": "spinach", "quantity": 100.0, "unit": "g"},
		},
		"steps": []any{"Cook rice.", "Sear chicken.", "Wilt spinach."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Nutrition.Total.Calories <= 0 || recipe.Nutrition.PerServing.ProteinG <= 0 {
		t.Fatalf("nutrition was not estimated: %#v", recipe.Nutrition)
	}
	if len(recipe.Ingredients) != 3 || len(recipe.Steps) != 3 {
		t.Fatalf("ingredients/steps = %d/%d, want 3/3", len(recipe.Ingredients), len(recipe.Steps))
	}

	scaled, err := scaleRecipe(db, pid, recipe.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	ingredients := scaled["ingredients"].([]Ingredient)
	if ingredients[0].Quantity != 400 {
		t.Fatalf("scaled chicken quantity = %.2f, want 400", ingredients[0].Quantity)
	}
	nutrition := scaled["nutrition"].(Nutrition)
	if nutrition.Servings != 4 || nutrition.Total.Calories <= recipe.Nutrition.Total.Calories {
		t.Fatalf("scaled nutrition = %#v", nutrition)
	}
}

func TestShoppingListAggregatesMealPlanServings(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	recipe, err := createRecipe(db, pid, map[string]any{
		"title":    "Tomato Pasta",
		"servings": 2.0,
		"ingredients": []any{
			map[string]any{"name": "pasta", "quantity": 200.0, "unit": "g"},
			map[string]any{"name": "tomato", "quantity": 300.0, "unit": "g"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addMealPlanEntry(db, pid, map[string]any{"date": "2026-07-08", "slot": "dinner", "recipe_id": recipe.ID, "servings": 4.0}); err != nil {
		t.Fatal(err)
	}
	lines, err := shoppingListForTest(db, pid, map[string]any{"meal_plan_from": "2026-07-08", "meal_plan_to": "2026-07-08"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("shopping lines = %d, want 2", len(lines))
	}
	byName := map[string]ShoppingLine{}
	for _, line := range lines {
		byName[line.NormalizedName] = line
	}
	if byName["pasta"].Quantity != 400 || byName["tomato"].Quantity != 600 {
		t.Fatalf("aggregated lines = %#v", byName)
	}
}

func TestSuggestFromIngredientsRanksMatches(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	if _, err := createRecipe(db, pid, map[string]any{
		"title": "Spinach Omelette",
		"ingredients": []any{
			map[string]any{"name": "eggs", "quantity": 2.0, "unit": "each"},
			map[string]any{"name": "spinach", "quantity": 100.0, "unit": "g"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createRecipe(db, pid, map[string]any{
		"title": "Beef Pasta",
		"ingredients": []any{
			map[string]any{"name": "beef", "quantity": 200.0, "unit": "g"},
			map[string]any{"name": "pasta", "quantity": 200.0, "unit": "g"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := suggestFromIngredients(db, pid, []string{"eggs", "spinach"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) == 0 || suggestions[0].Title != "Spinach Omelette" || suggestions[0].MatchRatio != 1 {
		t.Fatalf("suggestions = %#v", suggestions)
	}
}

func shoppingListForTest(db *sql.DB, pid string, in map[string]any) ([]ShoppingLine, error) {
	type sourceRecipe struct {
		recipeID int64
		servings float64
	}
	var sources []sourceRecipe
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
		for _, ing := range scaled["ingredients"].([]Ingredient) {
			key := ing.NormalizedName + "|" + ing.Unit
			if lines[key] == nil {
				lines[key] = &ShoppingLine{Name: ing.Name, NormalizedName: ing.NormalizedName, Unit: ing.Unit}
			}
			lines[key].Quantity += ing.Quantity
			lines[key].BuyQuantity += ing.Quantity
		}
	}
	out := []ShoppingLine{}
	for _, line := range lines {
		line.Quantity = round2(line.Quantity)
		line.BuyQuantity = round2(line.BuyQuantity)
		out = append(out, *line)
	}
	return out, nil
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/recipes.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	return db
}
