// HealthPanel — personal health log.
//
// Layout:
//   ┌─ goals ribbon: pass/fail chips ──────────────────────────────┐
//   │ NL log box                                                  │
//   ├─ left: pinned kinds list ─┬─ right: chart + workouts ──────┤
//   │  · weight  78.4 kg        │ sparkline of selected kind     │
//   │  · sleep   7.2 h avg      │                                │
//   │  · mood    6.5            │ recent workouts                │
//   │  + add kind…              │                                │
//   └───────────────────────────┴────────────────────────────────┘
//
// Charts are inline SVG sparklines — no chart library, intentionally
// modest. Click a pinned kind on the left to switch the right chart.

import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/health";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Metric {
  id: number;
  kind: string;
  value: number;
  unit: string;
  notes: string;
  source: string;
  recorded_at: string;
  created_at: string;
}

interface Workout {
  id: number;
  kind: string;
  started_at: string;
  duration_min: number;
  distance_km?: number;
  avg_hr?: number;
  perceived?: number;
  notes: string;
  source: string;
}

interface Goal {
  id: number;
  kind: string;
  op: "gte" | "lte" | "eq";
  target: number;
  cadence: "daily" | "weekly";
  enabled: boolean;
}

interface GoalStatus {
  goal: Goal;
  observed: number;
  pass: boolean;
}

interface KindRow {
  kind: string;
  count: number;
  last_value: number;
  unit: string;
  last_at: string;
  pretty?: string;
}

type Window = "today" | "7d" | "30d" | "90d";

export default function HealthPanel({}: NativePanelProps) {
  const [pins, setPins] = useState<string[]>([]);
  const [kinds, setKinds] = useState<KindRow[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [window, setWindow] = useState<Window>("30d");
  const [series, setSeries] = useState<Metric[]>([]);
  const [workouts, setWorkouts] = useState<Workout[]>([]);
  const [goalStatus, setGoalStatus] = useState<GoalStatus[]>([]);
  const [logText, setLogText] = useState("");
  const [status, setStatus] = useState("");

  const loadKinds = useCallback(async () => {
    try {
      const res = await fetch(`${API}/kinds`, { credentials: "same-origin" });
      if (res.ok) setKinds(await res.json() || []);
    } catch {}
  }, []);

  const loadPins = useCallback(async () => {
    try {
      const res = await fetch(`${API}/pins`, { credentials: "same-origin" });
      if (res.ok) {
        const data = await res.json();
        setPins(data || []);
        if ((data || []).length && !selected) setSelected(data[0]);
      }
    } catch {}
  }, [selected]);

  const loadSeries = useCallback(async () => {
    if (!selected) { setSeries([]); return; }
    try {
      const res = await fetch(
        `${API}/metrics?kind=${encodeURIComponent(selected)}&from=${window}`,
        { credentials: "same-origin" },
      );
      if (res.ok) setSeries(await res.json() || []);
    } catch {}
  }, [selected, window]);

  const loadWorkouts = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/workouts?from=${window}`,
        { credentials: "same-origin" },
      );
      if (res.ok) setWorkouts(await res.json() || []);
    } catch {}
  }, [window]);

  const loadGoals = useCallback(async () => {
    try {
      const res = await fetch(`${API}/goals_status`, { credentials: "same-origin" });
      if (res.ok) setGoalStatus(await res.json() || []);
    } catch {}
  }, []);

  useEffect(() => { loadKinds(); loadPins(); loadGoals(); }, [loadKinds, loadPins, loadGoals]);
  useEffect(() => { loadSeries(); }, [loadSeries]);
  useEffect(() => { loadWorkouts(); }, [loadWorkouts]);

  const submitLog = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!logText.trim()) return;
    try {
      const res = await fetch(`${API}/log`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: logText, source: "human" }),
      });
      if (!res.ok) { setStatus("Log: " + (await res.text())); return; }
      const out = await res.json();
      setStatus(out.parsed ? "Logged." : `Stored (unparsed): ${out.reason ?? ""}`);
      setLogText("");
      loadKinds(); loadSeries(); loadWorkouts(); loadGoals();
    } catch (e) {
      setStatus("Log: " + (e as Error).message);
    }
  };

  const togglePin = async (kind: string) => {
    const next = pins.includes(kind)
      ? pins.filter((k) => k !== kind)
      : [...pins, kind];
    await fetch(`${API}/pins`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ Kinds: next }),
    });
    setPins(next);
    if (!selected && next.length) setSelected(next[0]);
  };

  const removeWorkout = async (id: number) => {
    if (!confirm("Delete this workout?")) return;
    await fetch(`${API}/workouts/${id}`, { method: "DELETE", credentials: "same-origin" });
    loadWorkouts();
  };

  const pinnedKindRows = useMemo(() => {
    return pins.map((p) => kinds.find((k) => k.kind === p) ?? {
      kind: p, count: 0, last_value: 0, unit: "", last_at: "",
    });
  }, [pins, kinds]);

  const unpinnedKinds = useMemo(
    () => kinds.filter((k) => !pins.includes(k.kind) && k.kind !== "unparsed"),
    [kinds, pins],
  );

  return (
    <div className="h-full flex flex-col">
      {goalStatus.length > 0 && (
        <div className="border-b border-border px-4 py-2 flex items-center gap-3 text-xs">
          {goalStatus.map((g) => (
            <span
              key={g.goal.id}
              className={`px-2 py-0.5 rounded ${
                g.pass ? "text-success border border-success/40" : "text-warn border border-warn/40"
              }`}
            >
              {g.goal.kind} {g.goal.op === "gte" ? "≥" : g.goal.op === "lte" ? "≤" : "="} {g.goal.target}
              {" · "}
              <span className="text-text-dim">{g.observed}</span>
            </span>
          ))}
        </div>
      )}

      <form onSubmit={submitLog} className="px-4 py-3 border-b border-border flex gap-2">
        <input
          value={logText}
          onChange={(e) => setLogText(e.target.value)}
          placeholder="Log… 'weight 78.4', 'slept 7h30', 'ran 5k 26min', 'bp 122/78'"
          className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
        />
        <select
          value={window}
          onChange={(e) => setWindow(e.target.value as Window)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
        >
          <option value="today">Today</option>
          <option value="7d">7 days</option>
          <option value="30d">30 days</option>
          <option value="90d">90 days</option>
        </select>
        <span className="text-text-dim text-xs self-center">{status}</span>
      </form>

      <div className="flex-1 flex overflow-hidden">
        <aside className="w-56 border-r border-border overflow-auto p-2 flex flex-col gap-1 text-sm">
          <div className="text-xs uppercase text-text-dim px-2 mb-1">Pinned</div>
          {pinnedKindRows.length === 0 && (
            <div className="text-text-muted text-xs px-2 py-2">
              Pin a kind below to chart it.
            </div>
          )}
          {pinnedKindRows.map((k) => (
            <button
              key={k.kind}
              onClick={() => setSelected(k.kind)}
              className={`text-left px-2 py-1 rounded flex items-center justify-between ${
                selected === k.kind ? "bg-bg-card text-text" : "text-text-muted hover:text-text"
              }`}
            >
              <span>{k.pretty ?? k.kind}</span>
              <span className="text-text-dim text-xs">
                {k.count > 0 ? `${k.last_value} ${k.unit}` : "—"}
              </span>
            </button>
          ))}
          {unpinnedKinds.length > 0 && (
            <>
              <div className="text-xs uppercase text-text-dim px-2 mt-3 mb-1">Available</div>
              {unpinnedKinds.map((k) => (
                <button
                  key={k.kind}
                  onClick={() => togglePin(k.kind)}
                  className="text-left px-2 py-1 rounded text-text-muted hover:text-text"
                >
                  + {k.pretty ?? k.kind}
                </button>
              ))}
            </>
          )}
          {pinnedKindRows.length > 0 && (
            <button
              onClick={() => selected && togglePin(selected)}
              className="mt-3 text-text-muted hover:text-error text-xs px-2 py-1 text-left"
            >
              Unpin {selected}
            </button>
          )}
        </aside>

        <main className="flex-1 overflow-auto p-4 flex flex-col gap-4">
          {selected && (
            <section>
              <div className="flex items-center gap-2 mb-2">
                <div className="text-text font-medium">{selected}</div>
                <span className="text-text-dim text-xs">{series.length} points</span>
              </div>
              <Sparkline points={series} />
            </section>
          )}

          <section>
            <div className="flex items-center gap-2 mb-2">
              <div className="text-text font-medium">Recent workouts</div>
              <span className="text-text-dim text-xs">{workouts.length} sessions</span>
            </div>
            {workouts.length === 0 ? (
              <div className="text-text-muted text-sm py-3">
                No workouts yet. Try logging "ran 5k 26min".
              </div>
            ) : (
              <ul className="flex flex-col">
                {workouts.map((w) => (
                  <li
                    key={w.id}
                    className="flex items-center gap-3 py-1.5 border-b border-border/50 group"
                  >
                    <span className="text-text-dim text-xs w-24">
                      {w.started_at.slice(0, 10)}
                    </span>
                    <span className="text-text text-sm w-16">{w.kind}</span>
                    <span className="text-text-muted text-xs w-16">{w.duration_min}m</span>
                    {w.distance_km != null && (
                      <span className="text-text-muted text-xs w-16">
                        {w.distance_km.toFixed(1)}km
                      </span>
                    )}
                    {w.notes && (
                      <span className="text-text-dim text-xs flex-1 truncate">{w.notes}</span>
                    )}
                    <button
                      onClick={() => removeWorkout(w.id)}
                      className="opacity-0 group-hover:opacity-100 text-text-muted hover:text-error text-xs"
                    >
                      ×
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </main>
      </div>
    </div>
  );
}

function Sparkline({ points }: { points: Metric[] }) {
  if (points.length < 2) {
    return (
      <div className="text-text-muted text-sm py-6 border border-dashed border-border rounded text-center">
        Need at least 2 data points to chart.
      </div>
    );
  }
  const W = 600;
  const H = 120;
  const PAD = 8;
  const xs = points.map((p) => new Date(p.recorded_at).getTime());
  const ys = points.map((p) => p.value);
  const xMin = Math.min(...xs);
  const xMax = Math.max(...xs);
  const yMin = Math.min(...ys);
  const yMax = Math.max(...ys);
  const yRange = yMax - yMin || 1;
  const xRange = xMax - xMin || 1;
  const px = (x: number) => PAD + ((x - xMin) / xRange) * (W - 2 * PAD);
  const py = (y: number) => H - PAD - ((y - yMin) / yRange) * (H - 2 * PAD);
  const path = points
    .map((p, i) => `${i === 0 ? "M" : "L"} ${px(xs[i]).toFixed(1)} ${py(ys[i]).toFixed(1)}`)
    .join(" ");
  const last = points[points.length - 1];
  return (
    <div className="border border-border rounded p-2">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full h-32">
        <path d={path} fill="none" stroke="currentColor" strokeWidth={2} className="text-accent" />
        {points.map((p, i) => (
          <circle
            key={i}
            cx={px(xs[i])}
            cy={py(ys[i])}
            r={2}
            className="fill-accent"
          />
        ))}
      </svg>
      <div className="flex items-center justify-between text-xs text-text-dim mt-1">
        <span>min {yMin.toFixed(1)}</span>
        <span className="text-text">last {last.value} {last.unit}</span>
        <span>max {yMax.toFixed(1)}</span>
      </div>
    </div>
  );
}
