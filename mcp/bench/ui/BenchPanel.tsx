import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/bench";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Budget {
  duration_ms: number;
  cost_usd: number;
  tokens_total: number;
  turns: number;
}

interface Scenario {
  id: string;
  name: string;
  prompt: string;
  environment_id?: string;
  snapshot_id?: string;
  checks: unknown[];
  budget: Budget;
}

interface Pack {
  id: string;
  name: string;
  description: string;
  state: "draft" | "sealed";
  version?: string;
  digest?: string;
  scoring_version?: string;
  source_pack_id?: string;
  scenarios: Scenario[];
  updated_at: string;
}

interface TargetSummary {
  target_index: number;
  label: string;
  runs: number;
  verified: number;
  invalid: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  average_duration_ms: number;
  average_tokens: number;
}

interface Summary {
  total: number;
  verified: number;
  invalid: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  targets: TargetSummary[];
}

interface Run {
  id: string;
  pack_id: string;
  pack_name: string;
  pack_version: string;
  pack_digest: string;
  name: string;
  trials: number;
  status: string;
  summary: Summary;
  error?: string;
  created_at: string;
}

interface LeaderboardRow {
  label: string;
  runs: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  average_duration_ms: number;
  average_tokens: number;
  scenarios: number;
  mixed_cost_basis: boolean;
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(body.error || response.statusText);
  }
  return response.json();
}

const pct = (value: number) => `${Math.round(value * 100)}%`;
const seconds = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`);
const short = (digest?: string) => (digest ? digest.slice(0, 12) : "");

export default function BenchPanel({}: NativePanelProps) {
  const [packs, setPacks] = useState<Pack[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [selectedId, setSelectedId] = useState<string>("");
  const [tab, setTab] = useState<"definition" | "runs" | "leaderboard">("definition");
  const [board, setBoard] = useState<LeaderboardRow[]>([]);
  const [status, setStatus] = useState("");

  const selected = useMemo(() => packs.find((p) => p.id === selectedId), [packs, selectedId]);
  const packRuns = useMemo(
    () => runs.filter((run) => !selected || run.pack_digest === selected.digest),
    [runs, selected],
  );

  const refresh = useCallback(async () => {
    try {
      const [nextPacks, nextRuns] = await Promise.all([
        call<Pack[]>("/api/packs"),
        call<Run[]>("/api/runs?limit=50"),
      ]);
      setPacks(nextPacks || []);
      setRuns(nextRuns || []);
      setSelectedId((current) => current || nextPacks?.[0]?.id || "");
    } catch (error) {
      setStatus(String(error));
    }
  }, []);

  useEffect(() => {
    void refresh();
    // Runs advance on a 5s worker tick; match it rather than poll harder.
    const timer = setInterval(() => void refresh(), 5000);
    return () => clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    if (tab !== "leaderboard" || !selected?.digest) {
      setBoard([]);
      return;
    }
    call<{ rows: LeaderboardRow[] }>(`/api/packs/${selected.id}/leaderboard`)
      .then((result) => setBoard(result.rows || []))
      .catch((error) => setStatus(String(error)));
  }, [tab, selected]);

  const seal = async (pack: Pack) => {
    setStatus("");
    try {
      const sealed = await call<Pack>(`/api/packs/${pack.id}/seal`, { method: "POST", body: "{}" });
      await refresh();
      setSelectedId(sealed.id);
      setStatus(`Sealed ${sealed.name} v${sealed.version} · ${short(sealed.digest)}`);
    } catch (error) {
      setStatus(String(error));
    }
  };

  const fork = async (pack: Pack) => {
    setStatus("");
    try {
      const draft = await call<Pack>(`/api/packs/${pack.id}/fork`, { method: "POST", body: "{}" });
      await refresh();
      setSelectedId(draft.id);
    } catch (error) {
      setStatus(String(error));
    }
  };

  const drafts = packs.filter((pack) => pack.state === "draft");
  const sealed = packs.filter((pack) => pack.state === "sealed");

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <div className="flex items-center gap-3 border-b border-border px-4 py-2">
        <span className="font-medium">Bench</span>
        {selected && (
          <span className="text-xs text-text-dim">
            {selected.state === "sealed"
              ? `v${selected.version} · ${short(selected.digest)}`
              : "draft · not runnable until sealed"}
          </span>
        )}
        {status && <span className="ml-auto text-xs text-text-dim truncate max-w-[40%]">{status}</span>}
      </div>

      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "260px 1fr" }}>
        <div className="border-r border-border overflow-auto p-3 flex flex-col gap-3">
          <PackGroup label="Drafts" packs={drafts} selectedId={selectedId} onSelect={setSelectedId} />
          <PackGroup label="Sealed" packs={sealed} selectedId={selectedId} onSelect={setSelectedId} />
          {packs.length === 0 && (
            <div className="text-xs text-text-dim">
              No packs yet. Create one with the <code>bench_pack_create</code> tool, add scenarios, then seal it.
            </div>
          )}
        </div>

        <div className="overflow-auto p-4 flex flex-col gap-4">
          {!selected && <div className="text-sm text-text-dim">Select a pack.</div>}

          {selected && (
            <>
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="font-medium">{selected.name}</div>
                  <div className="text-xs text-text-dim">{selected.description || "No description."}</div>
                </div>
                <div className="flex gap-2">
                  {selected.state === "draft" && (
                    <button className="border border-border rounded px-2 py-1 text-sm" onClick={() => void seal(selected)}>
                      Seal version
                    </button>
                  )}
                  <button className="border border-border rounded px-2 py-1 text-sm" onClick={() => void fork(selected)}>
                    Fork to draft
                  </button>
                </div>
              </div>

              <div className="flex gap-2 text-sm border-b border-border">
                {(["definition", "runs", "leaderboard"] as const).map((key) => (
                  <button
                    key={key}
                    onClick={() => setTab(key)}
                    className={`px-2 py-1 border-b-2 ${tab === key ? "border-text" : "border-transparent text-text-dim"}`}
                  >
                    {key[0].toUpperCase() + key.slice(1)}
                  </button>
                ))}
              </div>

              {tab === "definition" && <Definition pack={selected} />}
              {tab === "runs" && <Runs runs={packRuns} />}
              {tab === "leaderboard" && <Leaderboard pack={selected} rows={board} />}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function PackGroup({
  label,
  packs,
  selectedId,
  onSelect,
}: {
  label: string;
  packs: Pack[];
  selectedId: string;
  onSelect: (id: string) => void;
}) {
  if (packs.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      <div className="text-xs text-text-dim uppercase tracking-wide">{label}</div>
      {packs.map((pack) => (
        <button
          key={pack.id}
          onClick={() => onSelect(pack.id)}
          className={`text-left border rounded px-2 py-1.5 ${
            pack.id === selectedId ? "border-text" : "border-border"
          }`}
        >
          <div className="font-medium text-sm">{pack.name}</div>
          <div className="text-xs text-text-dim">
            {pack.state === "sealed" ? `v${pack.version} · ${short(pack.digest)}` : "draft"} ·{" "}
            {pack.scenarios?.length || 0} scenario{(pack.scenarios?.length || 0) === 1 ? "" : "s"}
          </div>
        </button>
      ))}
    </div>
  );
}

function Definition({ pack }: { pack: Pack }) {
  return (
    <div className="flex flex-col gap-3">
      {pack.state === "sealed" && (
        <div className="text-xs text-text-dim border border-border rounded p-2">
          Scoring contract <code>{pack.scoring_version}</code>. Results are only comparable with other runs of
          this digest under the same contract.
        </div>
      )}
      {(pack.scenarios || []).map((scenario) => (
        <div key={scenario.id} className="border border-border rounded p-3 flex flex-col gap-2">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="font-medium text-sm">{scenario.name}</div>
              <div className="text-xs text-text-dim">{scenario.id}</div>
            </div>
            <div className="text-xs text-text-dim">
              {scenario.checks?.length || 0} check{(scenario.checks?.length || 0) === 1 ? "" : "s"}
            </div>
          </div>
          <div className="text-sm">{scenario.prompt}</div>
          <div className="text-xs text-text-dim flex flex-wrap gap-3">
            <span>≤ {seconds(scenario.budget.duration_ms)}</span>
            <span>≤ {scenario.budget.turns} turns</span>
            <span>≤ {scenario.budget.tokens_total.toLocaleString()} tokens</span>
            {scenario.budget.cost_usd > 0 && <span>≤ ${scenario.budget.cost_usd}</span>}
            <span>{scenario.snapshot_id || scenario.environment_id || "no pinned world"}</span>
          </div>
        </div>
      ))}
      {(pack.scenarios || []).length === 0 && (
        <div className="text-sm text-text-dim">
          No scenarios. Add them with <code>bench_scenario_put</code>.
        </div>
      )}
    </div>
  );
}

function Runs({ runs }: { runs: Run[] }) {
  if (runs.length === 0) return <div className="text-sm text-text-dim">No runs for this pack yet.</div>;
  return (
    <div className="flex flex-col gap-3">
      {runs.map((run) => (
        <div key={run.id} className="border border-border rounded p-3 flex flex-col gap-2">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="font-medium text-sm">{run.name || run.id}</div>
              <div className="text-xs text-text-dim">
                {run.status} · {run.trials} trial{run.trials === 1 ? "" : "s"} ·{" "}
                {new Date(run.created_at).toLocaleString()}
              </div>
            </div>
            {run.summary?.verified > 0 && (
              <div className="text-right">
                <div className="font-medium text-sm">{pct(run.summary.pass_rate)}</div>
                <div className="text-xs text-text-dim">{run.summary.average_score}/100</div>
              </div>
            )}
          </div>
          {run.error && <div className="text-xs text-text-dim">{run.error}</div>}
          {run.summary?.invalid > 0 && (
            <div className="text-xs text-text-dim">
              {run.summary.invalid} result{run.summary.invalid === 1 ? "" : "s"} withheld as harness failures —
              excluded from the pass rate.
            </div>
          )}
          {(run.summary?.targets || []).map((target) => (
            <div key={target.target_index} className="text-xs flex justify-between gap-4 border-t border-border pt-1">
              <span>{target.label}</span>
              <span className="text-text-dim">
                {pct(target.pass_rate)} · {target.average_score}/100 · {seconds(target.average_duration_ms)} ·{" "}
                {Math.round(target.average_tokens).toLocaleString()} tok
                {target.invalid > 0 ? ` · ${target.invalid} withheld` : ""}
              </span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

function Leaderboard({ pack, rows }: { pack: Pack; rows: LeaderboardRow[] }) {
  if (pack.state !== "sealed") {
    return <div className="text-sm text-text-dim">Only sealed packs have a leaderboard. Seal a version first.</div>;
  }
  if (rows.length === 0) return <div className="text-sm text-text-dim">No admitted results yet.</div>;
  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs text-text-dim">
        Every admitted result for digest <code>{short(pack.digest)}</code> under <code>{pack.scoring_version}</code>.
        Ranked by pass rate; score breaks ties.
      </div>
      <table className="text-sm w-full">
        <thead className="text-xs text-text-dim">
          <tr className="text-left border-b border-border">
            <th className="py-1">Target</th>
            <th>Pass</th>
            <th>Score</th>
            <th>Duration</th>
            <th>Tokens</th>
            <th>Runs</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.label} className="border-b border-border">
              <td className="py-1">
                {row.label}
                {row.mixed_cost_basis && (
                  <span className="text-xs text-text-dim" title="Some runs priced in cost, others in tokens">
                    {" "}
                    · mixed basis
                  </span>
                )}
              </td>
              <td>{pct(row.pass_rate)}</td>
              <td>{row.average_score}</td>
              <td>{seconds(row.average_duration_ms)}</td>
              <td>{Math.round(row.average_tokens).toLocaleString()}</td>
              <td className="text-text-dim">{row.runs}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
