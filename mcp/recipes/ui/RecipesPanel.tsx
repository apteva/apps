import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/recipes";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Ingredient {
  name: string;
  quantity: number;
  unit: string;
}

interface Nutrition {
  per_serving: {
    calories: number;
    protein_g: number;
    carbs_g: number;
    fat_g: number;
  };
}

interface Recipe {
  id: number;
  title: string;
  cuisine: string;
  meal_type: string;
  servings: number;
  prep_minutes: number;
  cook_minutes: number;
  total_minutes: number;
  tags?: string[];
  ingredients: Ingredient[];
  nutrition: Nutrition;
}

interface ShoppingLine {
  name: string;
  quantity: number;
  unit: string;
  buy_quantity: number;
  in_pantry: boolean;
}

export default function RecipesPanel({}: NativePanelProps) {
  const [recipes, setRecipes] = useState<Recipe[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [shopping, setShopping] = useState<ShoppingLine[]>([]);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [form, setForm] = useState({
    title: "",
    servings: "2",
    meal_type: "dinner",
    ingredients: "chicken breast, 200, g\nrice, 200, g\nspinach, 100, g",
    steps: "Prepare ingredients.\nCook until done.",
  });

  const loadRecipes = useCallback(async () => {
    const qs = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : "";
    const res = await fetch(`${API}/recipes${qs}`, { credentials: "same-origin" });
    if (res.ok) {
      const data = (await res.json()) || [];
      setRecipes(data);
      if (!selectedId && data.length) setSelectedId(data[0].id);
    }
  }, [query, selectedId]);

  const selected = useMemo(
    () => recipes.find((recipe) => recipe.id === selectedId) || null,
    [recipes, selectedId],
  );

  const loadShopping = useCallback(async () => {
    if (!selectedId) {
      setShopping([]);
      return;
    }
    const res = await fetch(`${API}/shopping_list?recipe_ids=${selectedId}&subtract_pantry=true`, {
      credentials: "same-origin",
    });
    if (res.ok) setShopping((await res.json()) || []);
  }, [selectedId]);

  useEffect(() => { loadRecipes(); }, [loadRecipes]);
  useEffect(() => { loadShopping(); }, [loadShopping]);

  const createRecipe = async (e: React.FormEvent) => {
    e.preventDefault();
    const ingredients = form.ingredients
      .split("\n")
      .map((line) => line.split(",").map((part) => part.trim()))
      .filter((parts) => parts[0])
      .map(([name, quantity, unit]) => ({ name, quantity: Number(quantity || "1"), unit: unit || "each" }));
    const steps = form.steps.split("\n").map((step) => step.trim()).filter(Boolean);
    const res = await fetch(`${API}/recipes`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: form.title,
        servings: Number(form.servings || "1"),
        meal_type: form.meal_type,
        ingredients,
        steps,
      }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    const recipe = await res.json();
    setStatus("Saved");
    setSelectedId(recipe.id);
    setForm({ ...form, title: "" });
    loadRecipes();
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="font-medium">Recipes</div>
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search"
          className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-sm w-56"
        />
      </header>
      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "280px minmax(0, 1fr) 320px" }}>
        <aside className="border-r border-border overflow-auto p-3 flex flex-col gap-2">
          {recipes.map((recipe) => (
            <button
              key={recipe.id}
              onClick={() => setSelectedId(recipe.id)}
              className={`text-left border border-border rounded p-2 hover:bg-bg-hover ${recipe.id === selectedId ? "bg-bg-hover" : ""}`}
            >
              <div className="font-medium text-sm">{recipe.title}</div>
              <div className="text-xs text-text-dim">
                {recipe.meal_type || "meal"} · {recipe.total_minutes || 0} min · {Math.round(recipe.nutrition?.per_serving?.calories || 0)} kcal
              </div>
            </button>
          ))}
        </aside>
        <main className="overflow-auto p-4">
          {selected ? (
            <div className="flex flex-col gap-4">
              <section className="border border-border rounded p-4">
                <div className="flex items-start justify-between gap-4">
                  <div>
                    <h2 className="text-xl font-semibold">{selected.title}</h2>
                    <div className="text-sm text-text-dim">
                      {selected.servings} servings · {selected.total_minutes || 0} min
                    </div>
                  </div>
                  <div className="text-right text-sm">
                    <div className="font-medium">{Math.round(selected.nutrition?.per_serving?.calories || 0)} kcal</div>
                    <div className="text-text-dim">
                      P {Math.round(selected.nutrition?.per_serving?.protein_g || 0)}g · C {Math.round(selected.nutrition?.per_serving?.carbs_g || 0)}g · F {Math.round(selected.nutrition?.per_serving?.fat_g || 0)}g
                    </div>
                  </div>
                </div>
              </section>
              <section className="border border-border rounded p-4">
                <div className="text-xs uppercase text-text-dim mb-2">Ingredients</div>
                <div className="flex flex-col gap-1">
                  {selected.ingredients.map((ingredient, idx) => (
                    <div key={`${ingredient.name}-${idx}`} className="text-sm">
                      {ingredient.quantity ? `${ingredient.quantity} ` : ""}{ingredient.unit} {ingredient.name}
                    </div>
                  ))}
                </div>
              </section>
            </div>
          ) : (
            <div className="text-text-muted text-sm">No recipe selected.</div>
          )}
        </main>
        <aside className="border-l border-border overflow-auto p-3 flex flex-col gap-4">
          <form onSubmit={createRecipe} className="border border-border rounded p-3 flex flex-col gap-2">
            <div className="text-xs uppercase text-text-dim">New Recipe</div>
            <input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} placeholder="Title" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
            <div className="flex gap-2">
              <input value={form.servings} onChange={(e) => setForm({ ...form, servings: e.target.value })} placeholder="Servings" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-24" />
              <input value={form.meal_type} onChange={(e) => setForm({ ...form, meal_type: e.target.value })} placeholder="Meal type" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm flex-1" />
            </div>
            <textarea value={form.ingredients} onChange={(e) => setForm({ ...form, ingredients: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-28" />
            <textarea value={form.steps} onChange={(e) => setForm({ ...form, steps: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-20" />
            <button disabled={!form.title.trim()} className="bg-accent text-bg rounded px-3 py-1.5 text-sm font-bold disabled:opacity-50">Save</button>
            {status && <div className="text-xs text-text-dim">{status}</div>}
          </form>
          <section className="border border-border rounded p-3">
            <div className="text-xs uppercase text-text-dim mb-2">Shopping</div>
            <div className="flex flex-col gap-1">
              {shopping.map((line) => (
                <div key={`${line.name}-${line.unit}`} className="text-sm flex justify-between gap-2">
                  <span>{line.name}</span>
                  <span className={line.in_pantry ? "text-success" : "text-text-dim"}>
                    {line.buy_quantity} {line.unit}
                  </span>
                </div>
              ))}
            </div>
          </section>
        </aside>
      </div>
    </div>
  );
}
