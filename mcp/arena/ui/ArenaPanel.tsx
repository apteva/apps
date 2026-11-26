import { useCallback, useEffect, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface GameInfo {
  id: string;
  name: string;
  summary: string;
  min_seats: number;
  max_seats: number;
  default_turn_seconds: number;
}

interface Seat {
  seat: number;
  kind: "open" | "agent" | "bot" | "human";
  agent_id?: number;
  name: string;
  bot_level?: string;
}

interface Outcome {
  winners: number[];
  ranks: number[];
  scores: number[];
  reason: string;
}

interface Match {
  id: number;
  game: string;
  title: string;
  status: string;
  version: number;
  seats: Seat[];
  to_act?: number[];
  result?: Outcome;
  turn_deadline?: string;
  seed_hash: string;
  seed?: string;
  updated_at: string;
}

interface ActionSpec {
  type: string;
  description?: string;
  args?: Record<string, any>;
}

interface StoredEvent {
  id: number;
  seat?: number;
  type: string;
  data?: Record<string, any>;
  created_at: string;
}

interface SeatState {
  match: Match;
  your_seat: number;
  your_turn: boolean;
  view?: any;
  legal_actions?: ActionSpec[];
  recent_events?: StoredEvent[];
  chat?: StoredEvent[];
}

const API = "/api/apps/arena";
const SEAT_CHOICES = [
  { value: "human:You", label: "You" },
  { value: "open", label: "Open seat" },
  { value: "bot:basic", label: "Bot (basic)" },
  { value: "bot:random", label: "Bot (random)" },
  { value: "bot:perfect", label: "Bot (perfect, tic-tac-toe)" },
  { value: "agent", label: "Agent (id)" },
];

const cardStyle = (red: boolean, hidden = false): React.CSSProperties => ({
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  width: 34,
  height: 48,
  borderRadius: 5,
  border: "1px solid rgba(0,0,0,0.25)",
  background: hidden ? "repeating-linear-gradient(45deg,#2b4a7a,#2b4a7a 4px,#355a92 4px,#355a92 8px)" : "#fdfdfb",
  color: red ? "#c0392b" : "#1a1a1a",
  fontWeight: 700,
  fontSize: 15,
  marginRight: 4,
  boxShadow: "0 1px 2px rgba(0,0,0,0.2)",
});

const SUIT: Record<string, string> = { c: "♣", d: "♦", h: "♥", s: "♠" };

function Card({ code }: { code?: string }) {
  if (!code) return <span style={cardStyle(false, true)} />;
  const rank = code[0] === "T" ? "10" : code[0];
  const suit = code[1];
  return <span style={cardStyle(suit === "d" || suit === "h")}>{rank}{SUIT[suit]}</span>;
}

function loadTokens(projectId: string): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(`arena:tokens:${projectId}`) || "{}");
  } catch {
    return {};
  }
}

function seatLabel(s?: Seat) {
  if (!s) return "";
  if (s.kind === "open") return "Open";
  return s.name || `${s.kind} ${s.seat + 1}`;
}

function describeEvent(e: StoredEvent, seats: Seat[]): string {
  const who = e.seat !== undefined && e.seat !== null ? seatLabel(seats[e.seat]) : "";
  const d = e.data || {};
  // "You call" / "Bot 2 calls"
  const v = (base: string, third = base + "s") => `${who} ${who === "You" ? base : third}`;
  switch (e.type) {
    case "placed": return `${who} placed ${d.mark} on cell ${d.cell}`;
    case "fold": case "check": return v(e.type);
    case "call": return `${v("call")} ${d.amount}`;
    case "raise": case "bet": return `${v(e.type)} to ${d.to}`;
    case "all_in": return `${v("go", "goes")} all-in to ${d.to}`;
    case "hand_started": return `Hand ${d.hand} — dealer ${seatLabel(seats[d.dealer])}`;
    case "hole_cards": return `${who} dealt ${(d.cards || []).join(" ")}`;
    case "board": return `${d.street}: ${(d.board || []).join(" ")}`;
    case "hand_result": {
      const pots = (d.summary?.pots || []).map((p: any) => {
        const names = p.winners.map((w: number) => seatLabel(seats[w]));
        const verb = names.length === 1 && names[0] !== "You" ? "wins" : "win";
        return `${names.join(" & ")} ${verb} ${p.amount}${p.hand ? ` (${p.hand})` : ""}`;
      });
      return pots.join("; ");
    }
    case "busted": return `${who} ${who === "You" ? "are" : "is"} out`;
    case "timeout": return `${who} timed out (${d.auto_action})`;
    case "game_over": return `Game over — ${d.result?.reason}`;
    case "game_started": return "Game started";
    case "cancelled": return "Match cancelled";
    default: return `${who} ${e.type}`;
  }
}

export default function ArenaPanel({ projectId }: NativePanelProps) {
  const [games, setGames] = useState<GameInfo[]>([]);
  const [matches, setMatches] = useState<Match[]>([]);
  const [filter, setFilter] = useState("active");
  const [selectedId, setSelectedId] = useState(0);
  const [state, setState] = useState<SeatState | null>(null);
  const [tokens, setTokens] = useState<Record<string, string>>(() => loadTokens(projectId));
  const [message, setMessage] = useState("");
  const [creating, setCreating] = useState(false);

  const url = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const call = useCallback(async (path: string, init?: RequestInit, token?: string) => {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (token) headers["X-Seat-Token"] = token;
    const res = await fetch(url(path), { credentials: "same-origin", ...init, headers });
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    return res.json();
  }, [url]);

  const saveToken = useCallback((matchId: number, token: string) => {
    setTokens((cur) => {
      const next = { ...cur, [String(matchId)]: token };
      localStorage.setItem(`arena:tokens:${projectId}`, JSON.stringify(next));
      return next;
    });
  }, [projectId]);

  const loadMatches = useCallback(async () => {
    try {
      const data = await call(`/matches?status=${filter}`);
      setMatches(data.matches || []);
    } catch (e) {
      setMessage((e as Error).message);
    }
  }, [call, filter]);

  useEffect(() => {
    call("/games").then((d) => setGames(d.games || [])).catch((e) => setMessage(e.message));
  }, [call]);
  useEffect(() => {
    loadMatches();
    const t = setInterval(loadMatches, 5000);
    return () => clearInterval(t);
  }, [loadMatches]);

  const token = selectedId ? tokens[String(selectedId)] : undefined;

  // Live view: fetch, then long-poll for changes.
  useEffect(() => {
    if (!selectedId) { setState(null); return; }
    let alive = true;
    let version = -1;
    const loop = async () => {
      try {
        const first: SeatState = await call(`/matches/${selectedId}`, undefined, token);
        if (!alive) return;
        setState(first);
        version = first.match.version;
        while (alive) {
          const next = await call(`/matches/${selectedId}/wait?since=${version}`, undefined, token);
          if (!alive) return;
          if (next.waiting) continue;
          setState(next);
          version = next.match.version;
          if (next.match.status === "finished" || next.match.status === "cancelled") {
            // keep the final state; poll slowly for chat
            await new Promise((r) => setTimeout(r, 4000));
            const again = await call(`/matches/${selectedId}`, undefined, token);
            if (alive) setState(again);
            return;
          }
        }
      } catch (e) {
        if (alive) {
          setMessage((e as Error).message);
          setTimeout(() => alive && loop(), 2000);
        }
      }
    };
    loop();
    return () => { alive = false; };
  }, [selectedId, token, call]);

  const act = useCallback(async (type: string, data?: Record<string, any>) => {
    if (!selectedId || !token) return;
    try {
      setMessage("");
      const st = await call(`/matches/${selectedId}/act`, { method: "POST", body: JSON.stringify({ type, data }) }, token);
      setState(st);
    } catch (e) {
      setMessage((e as Error).message);
    }
  }, [call, selectedId, token]);

  const join = useCallback(async (seat?: number) => {
    try {
      const res = await call(`/matches/${selectedId}/join`, { method: "POST", body: JSON.stringify({ seat, name: "You" }) });
      if (res.seat_token) saveToken(selectedId, res.seat_token);
      loadMatches();
    } catch (e) {
      setMessage((e as Error).message);
    }
  }, [call, selectedId, saveToken, loadMatches]);

  const cancel = useCallback(async () => {
    try {
      await call(`/matches/${selectedId}/cancel`, { method: "POST" });
      loadMatches();
    } catch (e) {
      setMessage((e as Error).message);
    }
  }, [call, selectedId, loadMatches]);

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Arena</h1>
          <p className="text-xs text-text-muted">Live matches between agents, bots and people.</p>
        </div>
        <span className="ml-auto text-xs text-text-muted">{message}</span>
        <button type="button" onClick={() => setCreating((v) => !v)} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">
          {creating ? "Close" : "New match"}
        </button>
      </header>
      <main className="flex-1 min-h-0 flex">
        <aside className="border-r border-border min-h-0 flex flex-col" style={{ width: 300, flexShrink: 0 }}>
          <div className="p-2 border-b border-border flex gap-1">
            {["active", "finished", "all"].map((f) => (
              <button key={f} type="button" onClick={() => setFilter(f)}
                className={`px-2 py-1 text-xs rounded border border-border ${filter === f ? "bg-bg-input font-medium" : "text-text-muted"}`}>
                {f}
              </button>
            ))}
          </div>
          <div className="flex-1 min-h-0 overflow-auto">
            {matches.length === 0 && <p className="p-4 text-xs text-text-muted">No matches yet. Create one, or ask an agent to call match_create.</p>}
            {matches.map((m) => (
              <button key={m.id} type="button" onClick={() => { setSelectedId(m.id); setCreating(false); }}
                className={`w-full text-left px-3 py-2 border-b border-border ${m.id === selectedId ? "bg-bg-input" : ""}`}>
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium truncate">#{m.id} {m.title}</span>
                  <span className="ml-auto text-xs text-text-muted">{m.status}</span>
                </div>
                <div className="text-xs text-text-muted truncate">{m.seats.map(seatLabel).join(" vs ")}</div>
              </button>
            ))}
          </div>
        </aside>
        <section className="flex-1 min-h-0 overflow-auto p-4">
          {creating ? (
            <CreateForm games={games} onCreate={async (body) => {
              try {
                const res = await call("/matches", { method: "POST", body: JSON.stringify(body) });
                const toks: Record<string, string> = res.seat_tokens || {};
                const first = Object.values(toks)[0];
                if (first) saveToken(res.match.id, first);
                setCreating(false);
                setFilter("active");
                setSelectedId(res.match.id);
                loadMatches();
              } catch (e) {
                setMessage((e as Error).message);
              }
            }} />
          ) : state ? (
            <MatchView state={state} hasToken={!!token} onAct={act} onJoin={join} onCancel={cancel}
              onChat={async (text) => {
                if (!token) return;
                try {
                  await call(`/matches/${selectedId}/chat`, { method: "POST", body: JSON.stringify({ text }) }, token);
                } catch (e) {
                  setMessage((e as Error).message);
                }
              }} />
          ) : (
            <p className="text-sm text-text-muted">Select a match to watch or play.</p>
          )}
        </section>
      </main>
    </div>
  );
}

function CreateForm({ games, onCreate }: { games: GameInfo[]; onCreate: (body: any) => void }) {
  const [gameId, setGameId] = useState("holdem");
  const game = games.find((g) => g.id === gameId) || games[0];
  const [seats, setSeats] = useState<string[]>(["human:You", "bot:basic", "bot:random"]);
  const [agentIds, setAgentIds] = useState<Record<number, string>>({});
  const [turn, setTurn] = useState("");
  const [stack, setStack] = useState("1000");
  const [blinds, setBlinds] = useState("10/20");
  const [maxHands, setMaxHands] = useState("0");

  useEffect(() => {
    if (!game) return;
    setSeats((cur) => {
      let next = cur.slice(0, game.max_seats);
      while (next.length < game.min_seats) next.push("bot:basic");
      return next;
    });
  }, [game]);

  if (!game) return <p className="text-sm text-text-muted">Loading games…</p>;

  const submit = () => {
    const finalSeats = seats.map((s, i) => (s === "agent" ? `agent:${(agentIds[i] || "").trim()}` : s));
    const body: any = { game: game.id, seats: finalSeats };
    if (turn.trim() !== "") body.turn_seconds = Number(turn);
    if (game.id === "holdem") {
      const [sb, bb] = blinds.split("/").map((x) => Number(x.trim()));
      body.options = { starting_stack: Number(stack), small_blind: sb, big_blind: bb, max_hands: Number(maxHands) };
    }
    onCreate(body);
  };

  const input = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm";
  return (
    <div className="flex flex-col gap-4" style={{ maxWidth: 560 }}>
      <h2 className="text-sm font-semibold">New match</h2>
      <label className="flex flex-col gap-1 text-xs text-text-muted">
        Game
        <select className={input} value={game.id} onChange={(e) => setGameId(e.target.value)}>
          {games.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
        </select>
        <span>{game.summary}</span>
      </label>
      <div className="flex flex-col gap-2">
        <span className="text-xs text-text-muted">Seats (in turn order, {game.min_seats}-{game.max_seats})</span>
        {seats.map((s, i) => (
          <div key={i} className="flex items-center gap-2">
            <span className="text-xs text-text-muted" style={{ width: 48 }}>Seat {i + 1}</span>
            <select className={input} value={s} onChange={(e) => setSeats(seats.map((x, j) => (j === i ? e.target.value : x)))}>
              {SEAT_CHOICES.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
            </select>
            {s === "agent" && (
              <input className={input} style={{ width: 90 }} placeholder="agent id" value={agentIds[i] || ""}
                onChange={(e) => setAgentIds({ ...agentIds, [i]: e.target.value })} />
            )}
            {seats.length > game.min_seats && (
              <button type="button" className="text-xs text-text-muted" onClick={() => setSeats(seats.filter((_, j) => j !== i))}>Remove</button>
            )}
          </div>
        ))}
        {seats.length < game.max_seats && (
          <button type="button" className="self-start px-2 py-1 text-xs border border-border rounded" onClick={() => setSeats([...seats, "bot:basic"])}>Add seat</button>
        )}
      </div>
      {game.id === "holdem" && (
        <div className="flex gap-3">
          <label className="flex flex-col gap-1 text-xs text-text-muted">Starting stack<input className={input} style={{ width: 100 }} value={stack} onChange={(e) => setStack(e.target.value)} /></label>
          <label className="flex flex-col gap-1 text-xs text-text-muted">Blinds<input className={input} style={{ width: 90 }} value={blinds} onChange={(e) => setBlinds(e.target.value)} /></label>
          <label className="flex flex-col gap-1 text-xs text-text-muted">Max hands (0 = no limit)<input className={input} style={{ width: 100 }} value={maxHands} onChange={(e) => setMaxHands(e.target.value)} /></label>
        </div>
      )}
      <label className="flex flex-col gap-1 text-xs text-text-muted">
        Turn clock in seconds (blank = {game.default_turn_seconds}, 0 = none)
        <input className={input} style={{ width: 120 }} value={turn} onChange={(e) => setTurn(e.target.value)} />
      </label>
      <button type="button" onClick={submit} className="self-start px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">Create</button>
      <p className="text-xs text-text-muted">Chips are play money. Agents can also create and join matches with the match_* tools.</p>
    </div>
  );
}

function MatchView({ state, hasToken, onAct, onJoin, onCancel, onChat }: {
  state: SeatState;
  hasToken: boolean;
  onAct: (type: string, data?: Record<string, any>) => void;
  onJoin: (seat?: number) => void;
  onCancel: () => void;
  onChat: (text: string) => void;
}) {
  const m = state.match;
  const [chat, setChat] = useState("");
  const logRef = useRef<HTMLDivElement>(null);
  const events = state.recent_events || [];
  useEffect(() => { logRef.current?.scrollTo(0, logRef.current.scrollHeight); }, [events.length]);
  const openSeats = m.seats.filter((s) => s.kind === "open");
  const deadline = m.turn_deadline ? new Date(m.turn_deadline) : null;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <h2 className="text-sm font-semibold">#{m.id} {m.title}</h2>
        <span className="text-xs text-text-muted">{m.status} · v{m.version}</span>
        {state.your_seat >= 0 && <span className="text-xs">You are seat {state.your_seat + 1}</span>}
        {state.your_turn && <span className="text-xs font-semibold text-accent">Your turn</span>}
        {deadline && m.status === "live" && <Countdown until={deadline} />}
        {(m.status === "waiting" || m.status === "live") && (
          <button type="button" onClick={onCancel} className="ml-auto px-2 py-1 text-xs border border-border rounded">Cancel match</button>
        )}
      </div>
      {m.status === "waiting" && (
        <div className="p-3 border border-border rounded text-sm flex items-center gap-3">
          Waiting for {openSeats.length} more player{openSeats.length === 1 ? "" : "s"}.
          {!hasToken && <button type="button" onClick={() => onJoin()} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">Take a seat</button>}
        </div>
      )}
      {m.result && (
        <div className="p-3 border border-border rounded text-sm">
          <strong>Result:</strong> {m.result.reason}. Winner{m.result.winners.length === 1 ? "" : "s"}: {m.result.winners.map((w) => seatLabel(m.seats[w])).join(", ") || "none"}
          <div className="text-xs text-text-muted mt-1" style={{ wordBreak: "break-all" }}>Seed {m.seed} (sha256 {m.seed_hash})</div>
        </div>
      )}
      {m.game === "tictactoe" && state.view && <TicTacToe view={state.view} state={state} onAct={onAct} />}
      {m.game === "holdem" && state.view && <Holdem view={state.view} state={state} onAct={onAct} />}
      <div className="flex gap-4" style={{ flexWrap: "wrap" }}>
        <div className="flex-1 border border-border rounded" style={{ minWidth: 280 }}>
          <div className="px-3 py-2 border-b border-border text-xs font-medium">Table log</div>
          <div ref={logRef} className="p-3 text-xs flex flex-col gap-1 overflow-auto" style={{ maxHeight: 220 }}>
            {events.map((e) => <div key={e.id}>{describeEvent(e, m.seats)}</div>)}
          </div>
        </div>
        <div className="flex-1 border border-border rounded flex flex-col" style={{ minWidth: 240 }}>
          <div className="px-3 py-2 border-b border-border text-xs font-medium">Chat</div>
          <div className="p-3 text-xs flex flex-col gap-1 overflow-auto" style={{ maxHeight: 180 }}>
            {(state.chat || []).map((e) => <div key={e.id}><strong>{e.data?.name}:</strong> {e.data?.text}</div>)}
          </div>
          {hasToken && (
            <form className="p-2 border-t border-border flex gap-2" onSubmit={(e) => { e.preventDefault(); if (chat.trim()) { onChat(chat); setChat(""); } }}>
              <input className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-xs" value={chat} onChange={(e) => setChat(e.target.value)} placeholder="Say something" />
              <button type="submit" className="px-2 py-1 text-xs border border-border rounded">Send</button>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

function Countdown({ until }: { until: Date }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), 500); return () => clearInterval(t); }, []);
  const s = Math.max(0, Math.round((until.getTime() - now) / 1000));
  return <span className="text-xs text-text-muted">{s}s left</span>;
}

function TicTacToe({ view, state, onAct }: { view: any; state: SeatState; onAct: (t: string, d?: any) => void }) {
  const board: number[] = view.board || [];
  const legal = new Set<number>(state.legal_actions?.[0]?.args?.cell || []);
  const marks = ["X", "O"];
  return (
    <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 72px)", gap: 6 }}>
      {board.map((v, i) => {
        const can = state.your_turn && legal.has(i);
        return (
          <button key={i} type="button" disabled={!can} onClick={() => onAct("place", { cell: i })}
            className={`border border-border rounded ${can ? "hover:bg-bg-input" : ""}`}
            style={{ width: 72, height: 72, fontSize: 32, fontWeight: 700, cursor: can ? "pointer" : "default" }}>
            {v >= 0 ? marks[v] : ""}
          </button>
        );
      })}
    </div>
  );
}

function Holdem({ view, state, onAct }: { view: any; state: SeatState; onAct: (t: string, d?: any) => void }) {
  const m = state.match;
  const legal = state.legal_actions || [];
  const raise = legal.find((a) => a.type === "raise" || a.type === "bet");
  const range = raise?.args?.to as { min: number; max: number } | undefined;
  const [amount, setAmount] = useState(0);
  useEffect(() => { if (range) setAmount(range.min); }, [range?.min, range?.max]);
  const players: any[] = view.players || [];
  const board: string[] = view.board || [];
  const last = view.last_hand;

  return (
    <div className="flex flex-col gap-3">
      <div className="p-4 border border-border rounded flex flex-col gap-3">
        <div className="flex items-center gap-4">
          <div>
            <div className="text-xs text-text-muted">Hand {view.hand} · {view.street} · blinds {view.blinds?.join("/")}</div>
            <div className="text-lg font-semibold">Pot {view.pot}</div>
          </div>
          <div className="ml-4">
            {[0, 1, 2, 3, 4].map((i) => board[i] ? <Card key={i} code={board[i]} /> : <span key={i} style={{ ...cardStyle(false), opacity: 0.15 }} />)}
          </div>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(170px, 1fr))", gap: 8 }}>
          {players.map((p) => {
            const seat = m.seats[p.seat];
            const acting = view.to_act === p.seat && m.status === "live";
            const shown = last?.shown?.[p.seat];
            return (
              <div key={p.seat} className={`p-2 rounded border ${acting ? "border-accent" : "border-border"}`} style={{ opacity: p.folded || p.out ? 0.5 : 1 }}>
                <div className="flex items-center gap-2 text-xs">
                  <span className="font-medium truncate">{seatLabel(seat)}</span>
                  {p.dealer && <span className="px-1 border border-border rounded">D</span>}
                  <span className="ml-auto text-text-muted">{seat?.kind}</span>
                </div>
                <div className="text-sm">{p.stack} chips{p.bet ? ` · bet ${p.bet}` : ""}</div>
                <div className="text-xs text-text-muted">{p.out ? "out" : p.folded ? "folded" : p.all_in ? "all-in" : acting ? "to act" : ""}</div>
                <div className="mt-1">
                  {p.hole ? p.hole.map((c: string) => <Card key={c} code={c} />) : p.out ? null : <><Card /><Card /></>}
                </div>
                {shown && p.seat !== state.your_seat && <div className="text-xs text-text-muted mt-1">Last hand showed {shown.join(" ")} ({last.hands?.[p.seat]})</div>}
              </div>
            );
          })}
        </div>
      </div>
      {state.your_seat >= 0 && m.status === "live" && (
        <div className="p-3 border border-border rounded flex items-center gap-2" style={{ flexWrap: "wrap" }}>
          {view.your_hand && <span className="text-xs text-text-muted mr-2">You have {view.your_hand}</span>}
          {!state.your_turn && <span className="text-xs text-text-muted">Waiting for {seatLabel(m.seats[view.to_act])}…</span>}
          {state.your_turn && legal.filter((a) => a.type !== "raise" && a.type !== "bet").map((a) => (
            <button key={a.type} type="button" onClick={() => onAct(a.type)} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
              {a.type === "call" ? `Call ${a.args?.amount}` : a.type === "all_in" ? `All-in (${a.args?.to})` : a.type[0].toUpperCase() + a.type.slice(1)}
            </button>
          ))}
          {state.your_turn && raise && range && (
            <>
              <input type="range" min={range.min} max={range.max} step={1} value={amount} onChange={(e) => setAmount(Number(e.target.value))} style={{ width: 160 }} />
              <input className="bg-bg-input border border-border rounded px-2 py-1 text-xs" style={{ width: 70 }} value={amount} onChange={(e) => setAmount(Number(e.target.value) || 0)} />
              <button type="button" onClick={() => onAct(raise.type, { to: amount })} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">
                {raise.type === "bet" ? "Bet" : "Raise to"} {amount}
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
