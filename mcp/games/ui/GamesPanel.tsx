// GamesPanel — dashboard surface for the Games app.
//
// Tabs: Players (search + detail with bans, stats, data, achievements),
// Leaderboards (boards, periods, entries, reset), Definitions (stat and
// achievement definitions), Settings (how the title is wired to Auth).
// Talks to /api/apps/games/admin/* through the platform proxy; live
// updates come from the app's AppBus events.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/games";

function apiUrl(path: string, projectId: string): string {
  const sep = path.includes("?") ? "&" : "?";
  return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
}

async function apiRequest<T>(url: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    ...(init.headers as Record<string, string> | undefined),
  };
  if (init.body) headers["Content-Type"] = "application/json";
  const response = await fetch(url, {
    credentials: "same-origin",
    ...init,
    headers,
  });
  if (!response.ok) {
    let detail = "";
    try {
      const body = (await response.json()) as {
        error?: string;
        detail?: string;
      };
      detail = body.error || body.detail || "";
    } catch {
      detail = "";
    }
    throw new Error(detail || `HTTP ${response.status}`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

function errorMessage(label: string, error: unknown): string {
  const detail = error instanceof Error ? error.message : String(error);
  return `${label}: ${detail}`;
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface AppEventEnvelope<T = unknown> {
  seq: number;
  topic: string;
  data?: T;
}

function useAppEvents<T = unknown>(
  app: string,
  projectId: string,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const shared = (
      window as typeof window & {
        __aptevaAppEvents?: {
          subscribe: (
            app: string,
            projectId: string,
            fn: (ev: AppEventEnvelope<T>) => void,
          ) => () => void;
        };
      }
    ).__aptevaAppEvents;
    if (shared) return shared.subscribe(app, projectId, handler);
    let since = 0;
    let es: EventSource | null = null;
    let stopped = false;
    let retry: number | null = null;
    const connect = () => {
      if (stopped) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}` +
        (since > 0 ? `&since=${since}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= since) return;
          since = ev.seq;
          handler(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (retry) window.clearTimeout(retry);
          retry = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      stopped = true;
      if (retry) window.clearTimeout(retry);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// ─── types ───────────────────────────────────────────────────────────

interface Player {
  id: number;
  auth_user_id: number;
  display_name: string;
  avatar_url?: string;
  region?: string;
  locale?: string;
  metadata: unknown;
  status: string;
  kind: string;
  login_count: number;
  first_login_at?: string;
  last_login_at?: string;
  created_at?: string;
}
interface Ban {
  id: number;
  reason?: string;
  source?: string;
  expires_at?: string;
  lifted_at?: string;
  created_at?: string;
}
interface PlayerStat {
  stat: string;
  value: number;
  version: number;
  updated_at?: string;
}
interface DataEntry {
  key: string;
  value: unknown;
  visibility: string;
  version: number;
  updated_at?: string;
}
interface PlayerAchievement {
  key: string;
  source?: string;
  unlocked_at: string;
}
interface AuditEvent {
  id: number;
  event: string;
  source?: string;
  metadata?: string;
  occurred_at: string;
}
interface PlayerContext {
  player: Player;
  active_ban: Ban | null;
  bans: Ban[];
  stats: PlayerStat[];
  data: DataEntry[];
  achievements: PlayerAchievement[];
  audit: AuditEvent[];
}
interface StatDef {
  id: number;
  name: string;
  aggregation: string;
  client_writable: boolean;
  description?: string;
}
interface Leaderboard {
  id: number;
  name: string;
  display_name?: string;
  stat: string;
  sort: string;
  reset: string;
  season_days?: number;
  current_period: string;
  period_started_at?: string;
}
interface Entry {
  rank: number;
  player_id: number;
  display_name: string;
  score: number;
  updated_at?: string;
}
interface AchievementDef {
  id: number;
  key: string;
  name: string;
  description?: string;
  stat?: string;
  threshold: number;
  op: string;
  hidden: boolean;
}
interface Summary {
  players: {
    total: number;
    active: number;
    banned: number;
    new_7d: number;
    active_24h: number;
    active_7d: number;
  };
  stat_defs: number;
  leaderboards: number;
  achievements: number;
}

const inputCls =
  "bg-bg-input border border-border rounded px-2 py-1 text-sm w-full";
const primaryBtn =
  "border border-accent text-accent rounded px-2 py-1 text-xs hover:bg-accent hover:text-bg disabled:opacity-50";
const quietBtn =
  "border border-border rounded px-2 py-1 text-xs text-text-muted hover:text-text disabled:opacity-50";
const dangerBtn =
  "border border-border rounded px-2 py-1 text-xs text-red hover:bg-bg-input disabled:opacity-50";
const label = "text-[10px] uppercase tracking-wide text-text-dim";

function fmtTime(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  return isNaN(d.getTime()) ? s : d.toLocaleString();
}

function fmtScore(n: number): string {
  return Number.isInteger(n) ? String(n) : n.toFixed(2);
}

function Pill({
  tone,
  children,
}: {
  tone: "ok" | "warn" | "muted";
  children: string;
}) {
  const cls =
    tone === "ok"
      ? "border-accent text-accent"
      : tone === "warn"
        ? "border-border text-red"
        : "border-border text-text-dim";
  return (
    <span
      className={`inline-block border rounded-full px-1.5 text-[10px] uppercase tracking-wide ${cls}`}
    >
      {children}
    </span>
  );
}

// ─── panel ───────────────────────────────────────────────────────────

type Tab = "players" | "leaderboards" | "definitions" | "settings";

interface Game {
  description: string;
  id: string;
  name: string;
  slug: string;
  status: "active" | "archived";
  legacy: boolean;
  auth_organization_slug: string;
}

export default function GamesPanel(props: NativePanelProps) {
  return <GameCatalog key={props.projectId} projectId={props.projectId} />;
}
function GameCatalog({ projectId }: { projectId: string }) {
  const [games, setGames] = useState<Game[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try {
      const out = await apiRequest<{ games: Game[] }>(
        apiUrl("/admin/games", projectId),
      );
      setGames(out.games);
      setError("");
    } catch (e) {
      setError(errorMessage("Could not load games", e));
    }
  }, [projectId]);
  useEffect(() => {
    void load();
  }, [load]);
  const mutate = async (path: string, method: string, body?: unknown) => {
    setBusy(true);
    try {
      const out = await apiRequest<{ game: Game }>(apiUrl(path, projectId), {
        method,
        body: JSON.stringify(body ?? {}),
      });
      await load();
      return out.game;
    } catch (e) {
      setError(errorMessage("Could not save game", e));
    } finally {
      setBusy(false);
    }
  };
  const game = games.find((g) => g.id === selected && g.status === "active");
  if (game)
    return (
      <div className="h-full flex flex-col">
        <div className="flex gap-3 items-center px-3 py-2 border-b border-border">
          <button className={quietBtn} onClick={() => setSelected(null)}>
            All games
          </button>
          <strong>{game.name}</strong>
          <span className="text-xs text-text-dim">{game.slug}</span>
        </div>
        <div className="flex-1 min-h-0">
          <GamesWorkspace
            key={projectId + ":" + game.id}
            projectId={projectId}
            gameId={game.id}
            gameName={game.name}
          />
        </div>
      </div>
    );
  return (
    <div className="p-4 overflow-auto h-full space-y-4 text-sm">
      <div>
        <h2 className="text-lg font-medium">Games</h2>
        <p className="text-text-muted text-xs">
          Each game has its own players, saves, progression and login
          configuration.
        </p>
      </div>
      {error && (
        <div role="alert" className="text-red">
          {error}
        </div>
      )}
      <form
        className="grid gap-2 max-w-2xl"
        onSubmit={async (e) => {
          e.preventDefault();
          const g = await mutate(
            editing ? `/admin/games/${editing}` : "/admin/games",
            editing ? "PATCH" : "POST",
            { name, slug, description },
          );
          if (g) {
            setName("");
            setSlug("");
            setDescription("");
            setEditing(null);
            if (!editing) setSelected(g.id);
          }
        }}
      >
        <input
          aria-label="Game title"
          className={inputCls}
          placeholder="Game title"
          value={name}
          maxLength={100}
          onChange={(e) => setName(e.target.value)}
          required
        />
        <input
          aria-label="Game slug"
          className={inputCls}
          placeholder="game-slug"
          value={slug}
          disabled={editing !== null}
          pattern="[a-z][a-z0-9_-]{0,63}"
          onChange={(e) => setSlug(e.target.value)}
          required
        />
        <textarea
          aria-label="Game description"
          className={inputCls}
          placeholder="Description (optional)"
          value={description}
          maxLength={4000}
          rows={3}
          onChange={(e) => setDescription(e.target.value)}
        />
        <button className={primaryBtn} disabled={busy}>
          {editing ? "Save game" : "Create game"}
        </button>
        {editing && (
          <button
            type="button"
            className={quietBtn}
            disabled={busy}
            onClick={() => {
              setEditing(null);
              setName("");
              setSlug("");
              setDescription("");
            }}
          >
            Cancel edit
          </button>
        )}
      </form>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        {games.map((g) => (
          <article
            key={g.id}
            className="border border-border rounded p-3 space-y-2"
          >
            <div className="flex items-center gap-2">
              <strong>{g.name}</strong>
              <span className="text-xs text-text-dim">
                {g.status}
                {g.legacy ? " · legacy clients" : ""}
              </span>
            </div>
            <div className="font-mono text-xs text-text-muted">{g.slug}</div>
            {g.description && (
              <p className="text-xs text-text-muted whitespace-pre-wrap break-words">
                {g.description}
              </p>
            )}
            <div className="flex gap-2">
              <button
                className={primaryBtn}
                disabled={busy || g.status !== "active"}
                onClick={() => setSelected(g.id)}
              >
                Open game
              </button>
              <button
                className={quietBtn}
                disabled={busy}
                onClick={() => {
                  setEditing(g.id);
                  setName(g.name);
                  setSlug(g.slug);
                  setDescription(g.description || "");
                }}
              >
                Edit game
              </button>
              <button
                className={quietBtn}
                disabled={busy}
                onClick={async () => {
                  if (
                    g.status === "active" &&
                    !window.confirm(
                      `Archive ${g.name}? Players cannot access it until restored.`,
                    )
                  )
                    return;
                  await mutate(
                    `/admin/games/${g.id}/${g.status === "active" ? "archive" : "restore"}`,
                    "POST",
                  );
                }}
              >
                {g.status === "active" ? "Archive" : "Restore"}
              </button>
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}

function GamesWorkspace({
  projectId,
  gameId,
  gameName,
}: {
  projectId: string;
  gameId: string;
  gameName: string;
}) {
  const url = useCallback(
    (path: string) =>
      apiUrl(path, projectId) + `&game_id=${encodeURIComponent(gameId)}`,
    [projectId, gameId],
  );
  const [tab, setTab] = useState<Tab>("players");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [summaryError, setSummaryError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const refreshTimer = useRef<number | null>(null);

  const loadSummary = useCallback(async () => {
    if (!projectId) return;
    try {
      setSummary(await apiRequest<Summary>(url("/admin/stats")));
      setSummaryError(null);
    } catch (error) {
      setSummaryError(errorMessage("Games unavailable", error));
    }
  }, [projectId, url]);

  useEffect(() => {
    void loadSummary();
  }, [loadSummary, refreshKey]);

  useAppEvents<{ game_id?: string }>("games", projectId, (ev) => {
    if (ev.data?.game_id && ev.data.game_id !== gameId) return;
    if (refreshTimer.current !== null) return;
    refreshTimer.current = window.setTimeout(() => {
      refreshTimer.current = null;
      setRefreshKey((k) => k + 1);
    }, 400);
  });
  useEffect(
    () => () => {
      if (refreshTimer.current !== null)
        window.clearTimeout(refreshTimer.current);
    },
    [],
  );

  const tabs: { key: Tab; label: string }[] = [
    { key: "players", label: "Players" },
    { key: "leaderboards", label: "Leaderboards" },
    { key: "definitions", label: "Definitions" },
    { key: "settings", label: "Settings" },
  ];

  return (
    <div className="h-full w-full flex flex-col overflow-hidden text-sm">
      <header className="flex items-center gap-1 px-3 py-2 border-b border-border shrink-0">
        {tabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={`px-2 py-1 rounded ${tab === t.key ? "bg-bg-card text-text" : "text-text-muted hover:text-text"}`}
          >
            {t.label}
          </button>
        ))}
        <div className="ml-auto flex items-center gap-3 text-xs text-text-muted">
          {summary && (
            <>
              <span>
                <span className="text-text font-medium">
                  {summary.players.total}
                </span>{" "}
                players
              </span>
              <span>
                <span className="text-text font-medium">
                  {summary.players.active_24h}
                </span>{" "}
                active 24h
              </span>
              <span>
                <span
                  className={
                    summary.players.banned > 0
                      ? "text-red font-medium"
                      : "text-text font-medium"
                  }
                >
                  {summary.players.banned}
                </span>{" "}
                banned
              </span>
            </>
          )}
          {summaryError && <span className="text-red">{summaryError}</span>}
        </div>
      </header>
      <div className="flex-1 min-h-0 overflow-hidden">
        {tab === "players" && (
          <PlayersTab
            gameName={gameName}
            url={url}
            refreshKey={refreshKey}
            onChanged={loadSummary}
          />
        )}
        {tab === "leaderboards" && (
          <LeaderboardsTab
            gameName={gameName}
            url={url}
            refreshKey={refreshKey}
          />
        )}
        {tab === "definitions" && (
          <DefinitionsTab url={url} refreshKey={refreshKey} />
        )}
        {tab === "settings" && (
          <SettingsTab url={url} projectId={projectId} gameId={gameId} />
        )}
      </div>
    </div>
  );
}

// ─── players ─────────────────────────────────────────────────────────

function PlayersTab({
  url,
  refreshKey,
  onChanged,
  gameName,
}: {
  gameName: string;
  url: (p: string) => string;
  refreshKey: number;
  onChanged: () => void;
}) {
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [players, setPlayers] = useState<Player[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [offset, setOffset] = useState(0);
  const requestVersion = useRef(0);
  useEffect(() => {
    setOffset(0);
  }, [q, status]);
  useEffect(
    () => () => {
      requestVersion.current++;
    },
    [],
  );

  const load = useCallback(async () => {
    const version = ++requestVersion.current;
    setLoading(true);
    try {
      const params = new URLSearchParams({
        limit: "50",
        offset: String(offset),
      });
      if (q.trim()) params.set("q", q.trim());
      if (status) params.set("status", status);
      const out = await apiRequest<{ players: Player[]; total: number }>(
        url(`/admin/players?${params.toString()}`),
      );
      if (version !== requestVersion.current) return;
      setPlayers(out.players);
      setTotal(out.total);
      setError(null);
    } catch (e) {
      if (version === requestVersion.current)
        setError(errorMessage("Could not load players", e));
    } finally {
      if (version === requestVersion.current) setLoading(false);
    }
  }, [url, q, status, offset]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 150);
    return () => {
      window.clearTimeout(timer);
      requestVersion.current++;
    };
  }, [load, refreshKey]);

  return (
    <div className="h-full flex overflow-hidden">
      <aside className="w-72 border-r border-border flex flex-col min-h-0">
        <div className="p-2 flex gap-1 border-b border-border">
          <input
            className={inputCls}
            placeholder="Search name or id"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <select
            className="bg-bg-input border border-border rounded px-1 text-xs"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            <option value="">all</option>
            <option value="active">active</option>
            <option value="banned">banned</option>
          </select>
        </div>
        <div className="flex-1 overflow-auto">
          {loading && players.length === 0 && (
            <div className="p-3 text-text-dim text-xs">Loading…</div>
          )}
          {error && <div className="p-3 text-red text-xs">{error}</div>}
          {!loading && !error && players.length === 0 && (
            <div className="p-3 text-text-dim text-xs">
              No players yet. The first{" "}
              <span className="font-mono">/v1/login/device</span> call creates
              one.
            </div>
          )}
          {players.map((p) => (
            <button
              key={p.id}
              onClick={() => setSelected(p.id)}
              className={`w-full text-left px-3 py-1.5 border-b border-border flex items-center gap-2 ${selected === p.id ? "bg-bg-card" : "hover:bg-bg-input"}`}
            >
              <span className="flex-1 min-w-0">
                <span className="block truncate text-text">
                  {p.display_name || `Player ${p.id}`}
                </span>
                <span className="block text-[10px] text-text-dim">
                  #{p.id} · {p.kind} · {p.login_count} logins
                </span>
              </span>
              {p.status === "banned" && <Pill tone="warn">banned</Pill>}
            </button>
          ))}
        </div>
        <div className="px-3 py-1 text-[10px] text-text-dim border-t border-border">
          {total} matching
        </div>
        <div className="p-2 flex items-center justify-between text-xs">
          <button
            className={quietBtn}
            disabled={offset === 0 || loading}
            onClick={() => setOffset(Math.max(0, offset - 50))}
          >
            Previous
          </button>
          <span>{total} players</span>
          <button
            className={quietBtn}
            disabled={offset + 50 >= total || loading}
            onClick={() => setOffset(offset + 50)}
          >
            Next
          </button>
        </div>
      </aside>
      <div className="flex-1 min-w-0 overflow-auto">
        {selected === null ? (
          <div className="p-6 text-text-dim text-xs">
            Select a player to see their profile, statistics, saves,
            achievements, and bans.
          </div>
        ) : (
          <PlayerDetail
            gameName={gameName}
            key={selected}
            id={selected}
            url={url}
            refreshKey={refreshKey}
            onChanged={() => {
              void load();
              onChanged();
            }}
          />
        )}
      </div>
    </div>
  );
}

function PlayerDetail({
  id,
  url,
  refreshKey,
  onChanged,
  gameName,
}: {
  gameName: string;
  id: number;
  url: (p: string) => string;
  refreshKey: number;
  onChanged: () => void;
}) {
  const [ctx, setCtx] = useState<PlayerContext | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [banReason, setBanReason] = useState("");
  const [banDays, setBanDays] = useState("");
  const [statName, setStatName] = useState("");
  const [statValue, setStatValue] = useState("");
  const [name, setName] = useState("");
  const nameDirty = useRef(false);
  const detailVersion = useRef(0);
  useEffect(
    () => () => {
      detailVersion.current++;
    },
    [],
  );

  const load = useCallback(async () => {
    const version = ++detailVersion.current;
    try {
      const out = await apiRequest<PlayerContext>(url(`/admin/players/${id}`));
      if (version !== detailVersion.current) return;
      setCtx(out);
      if (!nameDirty.current) setName(out.player.display_name);
      setError(null);
    } catch (e) {
      if (version === detailVersion.current)
        setError(errorMessage("Could not load player", e));
    }
  }, [url, id]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await load();
      onChanged();
      setError(null);
    } catch (e) {
      setError(errorMessage(label, e));
    } finally {
      setBusy(false);
    }
  };

  if (!ctx)
    return (
      <div className="p-4 text-text-dim text-xs">{error || "Loading…"}</div>
    );
  const p = ctx.player;

  return (
    <div className="p-4 space-y-4">
      <div className="flex items-start gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <input
              className={`${inputCls} max-w-xs`}
              value={name}
              onChange={(e) => {
                nameDirty.current = true;
                setName(e.target.value);
              }}
            />
            <button
              className={quietBtn}
              disabled={busy || name.trim() === p.display_name}
              onClick={() =>
                run("Rename failed", async () => {
                  await apiRequest(url(`/admin/players/${id}`), {
                    method: "PATCH",
                    body: JSON.stringify({ display_name: name.trim() }),
                  });
                  nameDirty.current = false;
                })
              }
            >
              Rename
            </button>
            {ctx.active_ban ? (
              <Pill tone="warn">banned</Pill>
            ) : (
              <Pill tone="ok">active</Pill>
            )}
            <Pill tone="muted">{p.kind}</Pill>
          </div>
          <div className="text-[11px] text-text-dim mt-1">
            player #{p.id} · auth user #{p.auth_user_id} · {p.login_count}{" "}
            logins · first {fmtTime(p.first_login_at)} · last{" "}
            {fmtTime(p.last_login_at)}
            {p.region ? ` · ${p.region}` : ""}
            {p.locale ? ` · ${p.locale}` : ""}
          </div>
        </div>
      </div>
      {error && <div className="text-red text-xs">{error}</div>}

      <section className="grid grid-cols-2 gap-4">
        <div>
          <div className={`${label} mb-1`}>Statistics</div>
          {ctx.stats.length === 0 ? (
            <div className="text-text-dim text-xs">No statistics yet.</div>
          ) : (
            <table className="w-full text-xs">
              <tbody>
                {ctx.stats.map((s) => (
                  <tr key={s.stat} className="border-b border-border">
                    <td className="py-0.5 font-mono">{s.stat}</td>
                    <td className="py-0.5 text-right">{fmtScore(s.value)}</td>
                    <td className="py-0.5 text-right text-text-dim">
                      v{s.version}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <div className="flex gap-1 mt-2">
            <input
              className={inputCls}
              placeholder="stat"
              value={statName}
              onChange={(e) => setStatName(e.target.value)}
            />
            <input
              className={`${inputCls} w-24`}
              placeholder="value"
              value={statValue}
              onChange={(e) => setStatValue(e.target.value)}
            />
            <button
              className={primaryBtn}
              disabled={
                busy ||
                !statName.trim() ||
                statValue.trim() === "" ||
                !Number.isFinite(Number(statValue))
              }
              onClick={() =>
                run("Stat write failed", async () => {
                  await apiRequest(url(`/admin/players/${id}/stats`), {
                    method: "POST",
                    body: JSON.stringify({
                      updates: [
                        { stat: statName.trim(), value: Number(statValue) },
                      ],
                    }),
                  });
                  setStatValue("");
                })
              }
            >
              Write
            </button>
          </div>
        </div>
        <div>
          <div className={`${label} mb-1`}>Achievements</div>
          {ctx.achievements.length === 0 ? (
            <div className="text-text-dim text-xs">None unlocked.</div>
          ) : (
            <ul className="text-xs space-y-0.5">
              {ctx.achievements.map((a) => (
                <li
                  key={a.key}
                  className="flex justify-between border-b border-border py-0.5"
                >
                  <span className="font-mono">{a.key}</span>
                  <span className="text-text-dim">
                    {fmtTime(a.unlocked_at)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      <section>
        <div className={`${label} mb-1`}>Player data</div>
        {ctx.data.length === 0 ? (
          <div className="text-text-dim text-xs">No saved keys.</div>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="text-text-dim text-left">
                <th className="font-normal py-0.5">key</th>
                <th className="font-normal py-0.5">visibility</th>
                <th className="font-normal py-0.5">version</th>
                <th className="font-normal py-0.5">value</th>
              </tr>
            </thead>
            <tbody>
              {ctx.data.map((d) => (
                <tr key={d.key} className="border-b border-border align-top">
                  <td className="py-0.5 font-mono">{d.key}</td>
                  <td className="py-0.5 text-text-muted">{d.visibility}</td>
                  <td className="py-0.5 text-text-dim">{d.version}</td>
                  <td className="py-0.5 font-mono text-text-muted truncate max-w-md">
                    {JSON.stringify(d.value).slice(0, 160)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="grid grid-cols-2 gap-4">
        <div>
          <div className={`${label} mb-1`}>Moderation</div>
          {ctx.active_ban ? (
            <div className="text-xs space-y-2">
              <div>
                Banned
                {ctx.active_ban.expires_at
                  ? ` until ${fmtTime(ctx.active_ban.expires_at)}`
                  : " permanently"}
                {ctx.active_ban.reason ? ` — ${ctx.active_ban.reason}` : ""}
              </div>
              <button
                className={primaryBtn}
                disabled={busy}
                onClick={() =>
                  run("Unban failed", () =>
                    apiRequest(url(`/admin/players/${id}/unban`), {
                      method: "POST",
                      body: "{}",
                    }),
                  )
                }
              >
                Lift ban
              </button>
            </div>
          ) : (
            <div className="flex gap-1">
              <input
                className={inputCls}
                placeholder="reason"
                value={banReason}
                onChange={(e) => setBanReason(e.target.value)}
              />
              <input
                className={`${inputCls} w-20`}
                placeholder="days"
                value={banDays}
                onChange={(e) => setBanDays(e.target.value)}
              />
              <button
                className={dangerBtn}
                disabled={busy || !banReason.trim()}
                onClick={() =>
                  run("Ban failed", async () => {
                    if (
                      !window.confirm(`Ban ${p.display_name} in ${gameName}?`)
                    )
                      return;
                    const days = Number(banDays);
                    const expires_at =
                      banDays.trim() && !isNaN(days) && days > 0
                        ? new Date(Date.now() + days * 86400000).toISOString()
                        : "";
                    await apiRequest(url(`/admin/players/${id}/ban`), {
                      method: "POST",
                      body: JSON.stringify({
                        reason: banReason.trim(),
                        expires_at,
                      }),
                    });
                    setBanReason("");
                    setBanDays("");
                  })
                }
              >
                Ban
              </button>
            </div>
          )}
          {ctx.bans.length > 0 && (
            <ul className="text-[11px] text-text-dim mt-2 space-y-0.5">
              {ctx.bans.map((b) => (
                <li key={b.id}>
                  {fmtTime(b.created_at)} · {b.reason || "no reason"} ·{" "}
                  {b.lifted_at
                    ? "lifted"
                    : b.expires_at
                      ? `until ${fmtTime(b.expires_at)}`
                      : "permanent"}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <div className={`${label} mb-1`}>Recent activity</div>
          <ul className="text-[11px] text-text-muted space-y-0.5">
            {ctx.audit.map((a) => (
              <li key={a.id} className="flex gap-2">
                <span className="text-text-dim shrink-0">
                  {fmtTime(a.occurred_at)}
                </span>
                <span className="font-mono">{a.event}</span>
                {a.source && <span className="text-text-dim">{a.source}</span>}
              </li>
            ))}
            {ctx.audit.length === 0 && (
              <li className="text-text-dim">Nothing yet.</li>
            )}
          </ul>
        </div>
      </section>
    </div>
  );
}

// ─── leaderboards ────────────────────────────────────────────────────

function LeaderboardsTab({
  url,
  refreshKey,
  gameName,
}: {
  gameName: string;
  url: (p: string) => string;
  refreshKey: number;
}) {
  const [boards, setBoards] = useState<Leaderboard[]>([]);
  const [picked, setPicked] = useState<string | null>(null);
  const [page, setPage] = useState<{
    period: string;
    total: number;
    entries: Entry[];
  } | null>(null);
  const [periods, setPeriods] = useState<string[]>([]);
  const [period, setPeriod] = useState("");
  const [offset, setOffset] = useState(0);
  const entriesVersion = useRef(0);
  useEffect(() => {
    setOffset(0);
  }, [picked, period]);
  useEffect(
    () => () => {
      entriesVersion.current++;
    },
    [],
  );
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({
    name: "",
    display_name: "",
    stat: "",
    sort: "desc",
    reset: "none",
    season_days: "30",
  });
  const [showForm, setShowForm] = useState(false);

  const loadBoards = useCallback(async () => {
    try {
      const out = await apiRequest<{ leaderboards: Leaderboard[] }>(
        url("/admin/leaderboards"),
      );
      setBoards(out.leaderboards);
      setError(null);
      if (!picked && out.leaderboards.length > 0)
        setPicked(out.leaderboards[0].name);
    } catch (e) {
      setError(errorMessage("Could not load leaderboards", e));
    }
  }, [url, picked]);

  const loadEntries = useCallback(async () => {
    if (!picked) return;
    const version = ++entriesVersion.current;
    try {
      const params = new URLSearchParams({
        limit: "100",
        offset: String(offset),
      });
      if (period) params.set("period", period);
      const out = await apiRequest<{
        page: { period: string; total: number; entries: Entry[] };
        periods: string[];
      }>(
        url(
          `/admin/leaderboards/${encodeURIComponent(picked)}/entries?${params.toString()}`,
        ),
      );
      if (version !== entriesVersion.current) return;
      setPage(out.page);
      setPeriods(out.periods);
      setError(null);
    } catch (e) {
      if (version === entriesVersion.current)
        setError(errorMessage("Could not load entries", e));
    }
  }, [url, picked, period, offset]);

  useEffect(() => {
    void loadBoards();
  }, [loadBoards, refreshKey]);
  useEffect(() => {
    setPage(null);
    void loadEntries();
    return () => {
      entriesVersion.current++;
    };
  }, [loadEntries, refreshKey]);

  const board = useMemo(
    () => boards.find((b) => b.name === picked) || null,
    [boards, picked],
  );

  const create = async () => {
    setBusy(true);
    try {
      await apiRequest(url("/admin/leaderboards"), {
        method: "POST",
        body: JSON.stringify({
          ...form,
          season_days: Number(form.season_days) || 0,
        }),
      });
      setForm({
        name: "",
        display_name: "",
        stat: "",
        sort: "desc",
        reset: "none",
        season_days: "30",
      });
      setShowForm(false);
      await loadBoards();
    } catch (e) {
      setError(errorMessage("Create failed", e));
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    if (
      !board ||
      !window.confirm(
        `[${gameName}] Start a fresh period on "${board.name}"? Players see an empty board immediately.`,
      )
    )
      return;
    setBusy(true);
    try {
      await apiRequest(
        url(`/admin/leaderboards/${encodeURIComponent(board.name)}/reset`),
        { method: "POST", body: "{}" },
      );
      setPeriod("");
      await loadBoards();
      await loadEntries();
    } catch (e) {
      setError(errorMessage("Reset failed", e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex overflow-hidden">
      <aside className="w-64 border-r border-border flex flex-col min-h-0">
        <div className="flex items-center justify-between px-3 py-2 border-b border-border">
          <span className={label}>Leaderboards</span>
          <button className={quietBtn} onClick={() => setShowForm((s) => !s)}>
            {showForm ? "Close" : "New"}
          </button>
        </div>
        {showForm && (
          <div className="p-2 space-y-1 border-b border-border">
            <input
              className={inputCls}
              placeholder="name (slug)"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
            <input
              className={inputCls}
              placeholder="display name"
              value={form.display_name}
              onChange={(e) =>
                setForm({ ...form, display_name: e.target.value })
              }
            />
            <input
              className={inputCls}
              placeholder="stat"
              value={form.stat}
              onChange={(e) => setForm({ ...form, stat: e.target.value })}
            />
            <div className="flex gap-1">
              <select
                className="bg-bg-input border border-border rounded px-1 text-xs flex-1"
                value={form.sort}
                onChange={(e) => setForm({ ...form, sort: e.target.value })}
              >
                <option value="desc">highest first</option>
                <option value="asc">lowest first</option>
              </select>
              <select
                className="bg-bg-input border border-border rounded px-1 text-xs flex-1"
                value={form.reset}
                onChange={(e) => setForm({ ...form, reset: e.target.value })}
              >
                <option value="none">all-time</option>
                <option value="daily">daily</option>
                <option value="weekly">weekly</option>
                <option value="monthly">monthly</option>
                <option value="season">season</option>
              </select>
            </div>
            {form.reset === "season" && (
              <input
                className={inputCls}
                placeholder="season length (days)"
                value={form.season_days}
                onChange={(e) =>
                  setForm({ ...form, season_days: e.target.value })
                }
              />
            )}
            <button
              className={primaryBtn}
              disabled={busy || !form.name.trim() || !form.stat.trim()}
              onClick={() => void create()}
            >
              Create
            </button>
          </div>
        )}
        <div className="flex-1 overflow-auto">
          {boards.length === 0 && (
            <div className="p-3 text-text-dim text-xs">
              No leaderboards yet.
            </div>
          )}
          {boards.map((b) => (
            <button
              key={b.name}
              onClick={() => {
                setPicked(b.name);
                setPeriod("");
              }}
              className={`w-full text-left px-3 py-1.5 border-b border-border ${picked === b.name ? "bg-bg-card" : "hover:bg-bg-input"}`}
            >
              <span className="block truncate text-text">
                {b.display_name || b.name}
              </span>
              <span className="block text-[10px] text-text-dim">
                {b.stat} · {b.reset} · {b.current_period}
              </span>
            </button>
          ))}
        </div>
      </aside>
      <div className="flex-1 min-w-0 overflow-auto p-4">
        {error && <div className="text-red text-xs mb-2">{error}</div>}
        {!board ? (
          <div className="text-text-dim text-xs">
            Create a leaderboard over a statistic. Periods roll over on their
            own; past periods stay readable here.
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 mb-3">
              <div className="flex-1 min-w-0">
                <div className="text-text font-medium truncate">
                  {board.display_name || board.name}
                </div>
                <div className="text-[11px] text-text-dim">
                  over <span className="font-mono">{board.stat}</span> ·{" "}
                  {board.sort === "asc" ? "lowest first" : "highest first"} ·{" "}
                  {board.reset}
                  {board.reset === "season"
                    ? ` (${board.season_days} days)`
                    : ""}{" "}
                  · current period{" "}
                  <span className="font-mono">{board.current_period}</span>
                </div>
              </div>
              <select
                className="bg-bg-input border border-border rounded px-1 text-xs"
                value={period}
                onChange={(e) => setPeriod(e.target.value)}
              >
                <option value="">current period</option>
                {periods
                  .filter((p) => p !== board.current_period)
                  .map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
              </select>
              <button
                className={dangerBtn}
                disabled={busy}
                onClick={() => void reset()}
              >
                Reset now
              </button>
            </div>
            {!page || page.entries.length === 0 ? (
              <div className="text-text-dim text-xs">
                No entries in this period.
              </div>
            ) : (
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-text-dim text-left">
                    <th className="font-normal py-0.5 w-12">#</th>
                    <th className="font-normal py-0.5">player</th>
                    <th className="font-normal py-0.5 text-right">score</th>
                    <th className="font-normal py-0.5 text-right">updated</th>
                  </tr>
                </thead>
                <tbody>
                  {page.entries.map((e) => (
                    <tr key={e.player_id} className="border-b border-border">
                      <td className="py-0.5 text-text-dim">{e.rank}</td>
                      <td className="py-0.5">
                        {e.display_name}{" "}
                        <span className="text-text-dim">#{e.player_id}</span>
                      </td>
                      <td className="py-0.5 text-right font-mono">
                        {fmtScore(e.score)}
                      </td>
                      <td className="py-0.5 text-right text-text-dim">
                        {fmtTime(e.updated_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {page && (
              <div className="text-xs flex gap-3 mt-2">
                <button
                  className={quietBtn}
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - 100))}
                >
                  Previous
                </button>
                <span>
                  {page.total} entries in {page.period}
                </span>
                <button
                  className={quietBtn}
                  disabled={offset + 100 >= page.total}
                  onClick={() => setOffset(offset + 100)}
                >
                  Next
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ─── definitions ─────────────────────────────────────────────────────

function DefinitionsTab({
  url,
  refreshKey,
}: {
  url: (p: string) => string;
  refreshKey: number;
}) {
  const [stats, setStats] = useState<StatDef[]>([]);
  const [achievements, setAchievements] = useState<AchievementDef[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [statForm, setStatForm] = useState({
    name: "",
    aggregation: "last",
    client_writable: false,
    description: "",
  });
  const [achForm, setAchForm] = useState({
    key: "",
    name: "",
    description: "",
    stat: "",
    threshold: "",
    op: "gte",
    hidden: false,
  });

  const load = useCallback(async () => {
    try {
      const [s, a] = await Promise.all([
        apiRequest<{ stats: StatDef[] }>(url("/admin/stat-defs")),
        apiRequest<{ achievements: AchievementDef[] }>(
          url("/admin/achievements"),
        ),
      ]);
      setStats(s.stats);
      setAchievements(a.achievements);
      setError(null);
    } catch (e) {
      setError(errorMessage("Could not load definitions", e));
    }
  }, [url]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const submit = async (
    label: string,
    path: string,
    body: unknown,
    after: () => void,
  ) => {
    setBusy(true);
    try {
      await apiRequest(url(path), {
        method: "POST",
        body: JSON.stringify(body),
      });
      after();
      await load();
    } catch (e) {
      setError(errorMessage(label, e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full overflow-auto p-4 grid grid-cols-2 gap-6">
      {error && <div className="col-span-2 text-red text-xs">{error}</div>}
      <section>
        <div className={`${label} mb-2`}>Statistics</div>
        <table className="w-full text-xs mb-3">
          <thead>
            <tr className="text-text-dim text-left">
              <th className="font-normal py-0.5">name</th>
              <th className="font-normal py-0.5">fold</th>
              <th className="font-normal py-0.5">client may write</th>
            </tr>
          </thead>
          <tbody>
            {stats.map((s) => (
              <tr key={s.name} className="border-b border-border">
                <td className="py-0.5 font-mono">{s.name}</td>
                <td className="py-0.5">{s.aggregation}</td>
                <td className="py-0.5">
                  {s.client_writable ? (
                    <Pill tone="warn">yes</Pill>
                  ) : (
                    <Pill tone="muted">server only</Pill>
                  )}
                </td>
              </tr>
            ))}
            {stats.length === 0 && (
              <tr>
                <td colSpan={3} className="py-1 text-text-dim">
                  No statistics defined. Server writes define them on the fly as
                  last-value stats.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <div className="space-y-1">
          <input
            className={inputCls}
            placeholder="name, e.g. high_score"
            value={statForm.name}
            onChange={(e) => setStatForm({ ...statForm, name: e.target.value })}
          />
          <div className="flex gap-1 items-center">
            <select
              className="bg-bg-input border border-border rounded px-1 text-xs"
              value={statForm.aggregation}
              onChange={(e) =>
                setStatForm({ ...statForm, aggregation: e.target.value })
              }
            >
              <option value="last">last value</option>
              <option value="max">keep the best</option>
              <option value="min">keep the lowest</option>
              <option value="sum">accumulate</option>
            </select>
            <label className="text-xs text-text-muted flex items-center gap-1">
              <input
                type="checkbox"
                checked={statForm.client_writable}
                onChange={(e) =>
                  setStatForm({
                    ...statForm,
                    client_writable: e.target.checked,
                  })
                }
              />
              client may write
            </label>
          </div>
          <input
            className={inputCls}
            placeholder="description"
            value={statForm.description}
            onChange={(e) =>
              setStatForm({ ...statForm, description: e.target.value })
            }
          />
          <button
            className={primaryBtn}
            disabled={busy || !statForm.name.trim()}
            onClick={() =>
              void submit(
                "Stat definition failed",
                "/admin/stat-defs",
                statForm,
                () =>
                  setStatForm({
                    name: "",
                    aggregation: "last",
                    client_writable: false,
                    description: "",
                  }),
              )
            }
          >
            Save statistic
          </button>
        </div>
      </section>
      <section>
        <div className={`${label} mb-2`}>Achievements</div>
        <table className="w-full text-xs mb-3">
          <thead>
            <tr className="text-text-dim text-left">
              <th className="font-normal py-0.5">key</th>
              <th className="font-normal py-0.5">name</th>
              <th className="font-normal py-0.5">rule</th>
            </tr>
          </thead>
          <tbody>
            {achievements.map((a) => (
              <tr key={a.key} className="border-b border-border">
                <td className="py-0.5 font-mono">
                  {a.key}
                  {a.hidden ? (
                    <span className="text-text-dim"> (hidden)</span>
                  ) : null}
                </td>
                <td className="py-0.5">{a.name}</td>
                <td className="py-0.5 text-text-muted">
                  {a.stat
                    ? `${a.stat} ${a.op} ${fmtScore(a.threshold)}`
                    : "manual"}
                </td>
              </tr>
            ))}
            {achievements.length === 0 && (
              <tr>
                <td colSpan={3} className="py-1 text-text-dim">
                  No achievements defined.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <div className="space-y-1">
          <div className="flex gap-1">
            <input
              className={inputCls}
              placeholder="key, e.g. first-win"
              value={achForm.key}
              onChange={(e) => setAchForm({ ...achForm, key: e.target.value })}
            />
            <input
              className={inputCls}
              placeholder="name"
              value={achForm.name}
              onChange={(e) => setAchForm({ ...achForm, name: e.target.value })}
            />
          </div>
          <input
            className={inputCls}
            placeholder="description"
            value={achForm.description}
            onChange={(e) =>
              setAchForm({ ...achForm, description: e.target.value })
            }
          />
          <div className="flex gap-1 items-center">
            <input
              className={inputCls}
              placeholder="stat (blank = manual)"
              value={achForm.stat}
              onChange={(e) => setAchForm({ ...achForm, stat: e.target.value })}
            />
            <select
              className="bg-bg-input border border-border rounded px-1 text-xs"
              value={achForm.op}
              onChange={(e) => setAchForm({ ...achForm, op: e.target.value })}
            >
              <option value="gte">≥</option>
              <option value="gt">&gt;</option>
              <option value="lte">≤</option>
              <option value="lt">&lt;</option>
              <option value="eq">=</option>
            </select>
            <input
              className={`${inputCls} w-24`}
              placeholder="threshold"
              value={achForm.threshold}
              onChange={(e) =>
                setAchForm({ ...achForm, threshold: e.target.value })
              }
            />
            <label className="text-xs text-text-muted flex items-center gap-1 shrink-0">
              <input
                type="checkbox"
                checked={achForm.hidden}
                onChange={(e) =>
                  setAchForm({ ...achForm, hidden: e.target.checked })
                }
              />
              hidden
            </label>
          </div>
          <button
            className={primaryBtn}
            disabled={busy || !achForm.key.trim() || !achForm.name.trim()}
            onClick={() =>
              void submit(
                "Achievement definition failed",
                "/admin/achievements",
                { ...achForm, threshold: Number(achForm.threshold) || 0 },
                () =>
                  setAchForm({
                    key: "",
                    name: "",
                    description: "",
                    stat: "",
                    threshold: "",
                    op: "gte",
                    hidden: false,
                  }),
              )
            }
          >
            Save achievement
          </button>
        </div>
      </section>
    </div>
  );
}

// ─── settings ────────────────────────────────────────────────────────

function SettingsTab({
  url,
  projectId,
  gameId,
}: {
  url: (p: string) => string;
  projectId: string;
  gameId: string;
}) {
  const [settings, setSettings] = useState<Record<string, unknown> | null>(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    apiRequest<Record<string, unknown>>(url("/admin/settings"))
      .then(setSettings)
      .catch((e) => setError(errorMessage("Could not load settings", e)));
  }, [url]);
  const base = `${window.location.origin}/api/apps/games/v2/games/${gameId}`;
  return (
    <div className="h-full overflow-auto p-4 space-y-4 max-w-2xl">
      {error && <div className="text-red text-xs">{error}</div>}
      <section>
        <div className={`${label} mb-1`}>Player API</div>
        <div className="text-xs text-text-muted">
          Game builds call these routes with no platform token. Login returns an
          Auth session; every other route needs{" "}
          <span className="font-mono">
            Authorization: Bearer &lt;access_token&gt;
          </span>
          .
        </div>
        <pre className="mt-2 text-[11px] bg-bg-input border border-border rounded p-2 overflow-auto">{`POST ${base}/login/device?project_id=${projectId}
{"device_id":"<random installation secret, at least 32 bytes>","display_name":"Ada"}

PUT  ${base}/data/save?project_id=${projectId}
{"value":{"level":4},"version":1}

POST ${base}/stats?project_id=${projectId}
{"updates":[{"stat":"score","value":120}]}`}</pre>
      </section>
      <section>
        <div className={`${label} mb-1`}>Auth wiring</div>
        <table className="text-xs">
          <tbody>
            <tr>
              <td className="pr-4 text-text-dim py-0.5">organization</td>
              <td className="font-mono">
                {String(settings?.auth_organization_slug ?? "—")}
              </td>
            </tr>
            <tr>
              <td className="pr-4 text-text-dim py-0.5">OAuth client</td>
              <td className="font-mono">
                {String(
                  settings?.auth_client_id || "registered on first login",
                )}
              </td>
            </tr>
            <tr>
              <td className="pr-4 text-text-dim py-0.5">analytics</td>
              <td>{settings?.analytics_enabled ? "on" : "off"}</td>
            </tr>
          </tbody>
        </table>
        <div className="text-xs text-text-muted mt-2">
          Identity (device ids, sessions, email upgrades) lives in the Auth app
          under each player's auth user id. Bans and erasure apply only to this
          game. Custom login requires a one-time ticket issued by your trusted
          game server.
        </div>
      </section>
    </div>
  );
}
