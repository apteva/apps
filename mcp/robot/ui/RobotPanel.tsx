// RobotPanel — agent navigation eval sandbox.
//
// React shell delegating to a vanilla mount(root) routine. Built via
// `bun run scripts/build-panels.ts` from the apps/ repo root.
//
// Styling strictly through the dashboard's Tailwind theme tokens
// (text-text, bg-bg-card, border-border, text-accent, bg-info/20,
// etc.) so the panel auto-themes on data-theme + data-mode flips.
// Canvas colours read the same CSS variables at draw time so the
// world view recolours in lockstep with the rest of the dashboard.

import { useEffect, useRef } from "react";

const API = "/api/apps/robot";
const TILE = 24;

// Pill class fragments — the base wrapper plus one of four state
// fragments. State strings have to appear as literal tokens here so
// Tailwind's source-scan picks them up and emits the rules.
const PILL_BASE = "text-xs px-2 py-0.5 rounded-full ml-1";
const PILL_STATES: Record<string, string> = {
  idle: "bg-bg-hover text-text-muted",
  active: "bg-info/20 text-info",
  success: "bg-success/20 text-success",
  timeout: "bg-warn/20 text-warn",
};

const BTN_DPAD =
  "bg-bg-input border border-border rounded text-text font-semibold cursor-pointer " +
  "hover:bg-bg-hover active:bg-bg-card disabled:opacity-50 disabled:cursor-default";
const BTN_EXTRA =
  "px-3 py-1 rounded border border-border bg-bg-input text-text cursor-pointer " +
  "hover:bg-bg-hover disabled:opacity-50 disabled:cursor-default text-sm";

function canvasColors() {
  const cs = getComputedStyle(document.documentElement);
  const v = (name: string, fallback: string) =>
    cs.getPropertyValue(`--${name}`).trim() || fallback;
  return {
    floor: v("bg-input", "#f4f4f5"),
    wall: v("text-dim", "#888"),
    goal: v("success", "#22c55e"),
    agent: v("info", "#3b82f6"),
    grid: v("border-subtle", "#333"),
    trail: v("info", "#3b82f6"),
  };
}

function mount(root: HTMLElement): () => void {
  root.innerHTML = `
    <div class="p-6 text-sm text-text">
      <h2 class="text-lg font-semibold text-text mb-1">Robot</h2>
      <p class="text-text-muted mb-4">Agent navigation eval sandbox. Pick a scenario, start an episode, watch the agent's tool calls land in the feed.</p>

      <div class="flex items-center gap-2 mb-4 flex-wrap">
        <span class="text-xs uppercase tracking-wider text-text-dim">Scenario</span>
        <select data-role="scenario-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <button data-role="start" class="bg-accent text-bg hover:bg-accent-hover rounded px-3 py-1 text-sm font-medium cursor-pointer disabled:opacity-50 disabled:cursor-default">Start episode</button>
        <span class="text-xs uppercase tracking-wider text-text-dim ml-2">Episode</span>
        <select data-role="episode-picker" class="bg-bg-input border border-border rounded px-2 py-1 text-text text-sm"></select>
        <span data-role="status" class="${PILL_BASE} ${PILL_STATES.idle}">idle</span>
      </div>

      <div class="flex gap-4 items-start flex-wrap">
        <div class="flex-shrink-0">
          <div class="text-xs uppercase tracking-wider text-text-dim mb-1">World</div>
          <div class="bg-bg-card border border-border rounded p-3 inline-block">
            <canvas data-role="grid"></canvas>
          </div>
          <div class="flex gap-4 text-sm text-text-muted mt-2">
            <span>steps <b class="text-text" data-role="m-steps">—</b></span>
            <span>optimal <b class="text-text" data-role="m-optimal">—</b></span>
            <span>ratio <b class="text-text" data-role="m-ratio">—</b></span>
          </div>

          <div class="flex gap-4 items-start mt-3">
            <div class="grid gap-1" style="grid-template-columns: repeat(3, 36px); grid-template-rows: repeat(3, 36px);">
              <button data-dir="N" class="${BTN_DPAD}" style="grid-column: 2; grid-row: 1;" title="Move north (↑)">↑</button>
              <button data-dir="W" class="${BTN_DPAD}" style="grid-column: 1; grid-row: 2;" title="Move west (←)">←</button>
              <button data-dir="E" class="${BTN_DPAD}" style="grid-column: 3; grid-row: 2;" title="Move east (→)">→</button>
              <button data-dir="S" class="${BTN_DPAD}" style="grid-column: 2; grid-row: 3;" title="Move south (↓)">↓</button>
            </div>
            <div class="flex flex-col gap-1 mt-[22px]">
              <button data-role="pick" class="${BTN_EXTRA}" title="Pick up an item (inert in v0.1)">pick</button>
              <button data-role="drop" class="${BTN_EXTRA}" title="Drop the held item (inert in v0.1)">drop</button>
            </div>
            <span class="text-xs text-text-dim self-center">arrow keys also move</span>
          </div>
        </div>

        <div class="flex-1 min-w-0">
          <div class="text-xs uppercase tracking-wider text-text-dim mb-1">Activity</div>
          <div data-role="feed" class="max-h-[480px] overflow-auto bg-bg-card border border-border rounded p-2"></div>
        </div>
      </div>
    </div>
  `;

  const els: any = {
    scenPicker: root.querySelector('[data-role="scenario-picker"]'),
    epPicker: root.querySelector('[data-role="episode-picker"]'),
    startBtn: root.querySelector('[data-role="start"]'),
    statusPill: root.querySelector('[data-role="status"]'),
    canvas: root.querySelector('[data-role="grid"]'),
    feed: root.querySelector('[data-role="feed"]'),
    mSteps: root.querySelector('[data-role="m-steps"]'),
    mOptimal: root.querySelector('[data-role="m-optimal"]'),
    mRatio: root.querySelector('[data-role="m-ratio"]'),
    dpadBtns: Array.from(root.querySelectorAll("[data-dir]")) as HTMLButtonElement[],
    pickBtn: root.querySelector('[data-role="pick"]'),
    dropBtn: root.querySelector('[data-role="drop"]'),
  };

  const state: any = {
    scenarios: [],
    episodes: [],
    activeEpisode: null,
    activeScenario: null,
    pollHandle: null,
  };

  els.scenPicker.addEventListener("change", () => {
    state.activeScenario = state.scenarios.find((s: any) => s.id === els.scenPicker.value);
    drawGrid(els.canvas, state.activeScenario, null, []);
  });
  els.epPicker.addEventListener("change", () => loadEpisode(els.epPicker.value));
  els.startBtn.addEventListener("click", startEpisode);

  for (const btn of els.dpadBtns) {
    btn.addEventListener("click", () => driveMove(btn.dataset.dir!));
  }
  els.pickBtn.addEventListener("click", () => driveAction("pick"));
  els.dropBtn.addEventListener("click", () => driveAction("drop"));

  // Keyboard arrows. Listen at document so the panel doesn't need
  // focus; ignore when the user is typing in an input/select.
  const keyHandler = (ev: KeyboardEvent) => {
    const target = ev.target as HTMLElement | null;
    if (target && /^(INPUT|SELECT|TEXTAREA)$/.test(target.tagName)) return;
    const map: Record<string, string> = {
      ArrowUp: "N",
      ArrowDown: "S",
      ArrowLeft: "W",
      ArrowRight: "E",
    };
    const dir = map[ev.key];
    if (!dir) return;
    if (!canDrive()) return;
    ev.preventDefault();
    driveMove(dir);
  };
  document.addEventListener("keydown", keyHandler);

  refreshAll();
  const refreshHandle = window.setInterval(refreshAll, 5_000);

  return function cleanup() {
    window.clearInterval(refreshHandle);
    document.removeEventListener("keydown", keyHandler);
    stopPolling();
  };

  async function refreshAll() {
    await Promise.all([loadScenarios(), loadEpisodes()]);
  }

  async function loadScenarios() {
    const res = await fetch(`${API}/scenarios`);
    if (!res.ok) return;
    const data = await res.json();
    state.scenarios = data.scenarios || [];
    const cur = els.scenPicker.value;
    els.scenPicker.innerHTML = state.scenarios
      .map((s: any) => `<option value="${s.id}">${escapeHTML(s.name)}</option>`)
      .join("");
    if (cur) els.scenPicker.value = cur;
    state.activeScenario =
      state.scenarios.find((s: any) => s.id === els.scenPicker.value) || state.scenarios[0];
    if (!state.activeEpisode && state.activeScenario) {
      drawGrid(els.canvas, state.activeScenario, null, []);
    }
  }

  async function loadEpisodes() {
    const res = await fetch(`${API}/episodes?limit=20`);
    if (!res.ok) return;
    const data = await res.json();
    state.episodes = data.episodes || [];
    const cur = els.epPicker.value;
    els.epPicker.innerHTML =
      `<option value="">— recent episodes —</option>` +
      state.episodes
        .map((e: any) => {
          const tag = e.terminal_reason ? ` (${e.terminal_reason})` : " (active)";
          return `<option value="${e.episode_id}">${shortID(e.episode_id)} · ${escapeHTML(
            e.scenario_id,
          )}${tag}</option>`;
        })
        .join("");
    if (cur) els.epPicker.value = cur;
    if (!state.activeEpisode && state.episodes.length > 0) {
      const newest = state.episodes[0];
      els.epPicker.value = newest.episode_id;
      loadEpisode(newest.episode_id);
    }
  }

  async function startEpisode() {
    const scenID = els.scenPicker.value;
    if (!scenID) return;
    els.startBtn.disabled = true;
    try {
      const res = await fetch(`${API}/episodes`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scenario_id: scenID, model: "manual" }),
      });
      if (!res.ok) {
        alert(`Start failed: ${await res.text()}`);
        return;
      }
      const data = await res.json();
      await loadEpisodes();
      els.epPicker.value = data.episode_id;
      loadEpisode(data.episode_id);
    } finally {
      els.startBtn.disabled = false;
    }
  }

  async function loadEpisode(id: string) {
    if (!id) {
      stopPolling();
      state.activeEpisode = null;
      drawGrid(els.canvas, state.activeScenario, null, []);
      els.feed.innerHTML = "";
      setPillState(els.statusPill, "idle", "idle");
      els.mSteps.textContent = "—";
      els.mOptimal.textContent = "—";
      els.mRatio.textContent = "—";
      updateDriveAvailability();
      return;
    }
    await refreshEpisode(id);
    startPolling(id);
  }

  function startPolling(id: string) {
    stopPolling();
    state.pollHandle = window.setInterval(() => refreshEpisode(id), 1500);
  }

  function stopPolling() {
    if (state.pollHandle) window.clearInterval(state.pollHandle);
    state.pollHandle = null;
  }

  async function refreshEpisode(id: string) {
    const res = await fetch(`${API}/episodes/${id}`);
    if (!res.ok) return;
    const data = await res.json();
    const ep = data.episode;
    const steps = data.steps || [];
    state.activeEpisode = ep;
    const scen = state.scenarios.find((s: any) => s.id === ep.scenario_id);
    if (scen) drawGrid(els.canvas, scen, ep.position, steps);
    renderFeed(els.feed, steps);
    renderMetrics(els, ep);
    renderStatus(els.statusPill, ep);
    updateDriveAvailability();
    if (ep.terminal_reason) stopPolling();
  }

  function canDrive() {
    const ep = state.activeEpisode;
    return ep && !ep.terminal_reason;
  }

  function updateDriveAvailability() {
    const enabled = canDrive();
    for (const b of els.dpadBtns) b.disabled = !enabled;
    els.pickBtn.disabled = !enabled;
    els.dropBtn.disabled = !enabled;
  }

  async function driveMove(dir: string) {
    const ep = state.activeEpisode;
    if (!ep || ep.terminal_reason) return;
    try {
      await fetch(`${API}/episodes/${ep.episode_id}/move`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ direction: dir }),
      });
    } catch (_) {
      /* ignore — next poll reconciles */
    }
    refreshEpisode(ep.episode_id);
  }

  async function driveAction(action: string) {
    const ep = state.activeEpisode;
    if (!ep || ep.terminal_reason) return;
    try {
      await fetch(`${API}/episodes/${ep.episode_id}/${action}`, { method: "POST" });
    } catch (_) {
      /* ignore */
    }
    refreshEpisode(ep.episode_id);
  }

  function renderMetrics(els: any, ep: any) {
    els.mSteps.textContent = ep.steps;
    els.mOptimal.textContent = ep.optimal_steps || "—";
    els.mRatio.textContent = ep.optimality_ratio ? ep.optimality_ratio.toFixed(2) : "—";
  }

  function renderStatus(pill: HTMLElement, ep: any) {
    if (ep.terminal_reason === "success") setPillState(pill, "success", "success");
    else if (ep.terminal_reason === "timeout") setPillState(pill, "timeout", "timeout");
    else setPillState(pill, "active", "active");
  }
}

function setPillState(pill: HTMLElement, state: string, label: string) {
  pill.className = `${PILL_BASE} ${PILL_STATES[state]}`;
  pill.textContent = label;
}

function drawGrid(canvas: HTMLCanvasElement, scen: any, agentPos: any, steps: any[]) {
  if (!canvas || !scen) return;
  const w = scen.grid.width;
  const h = scen.grid.height;
  canvas.width = w * TILE + 1;
  canvas.height = h * TILE + 1;
  const ctx = canvas.getContext("2d")!;
  const c = canvasColors();

  ctx.fillStyle = c.floor;
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  const walls: number[][] = scen.walls || [];
  ctx.fillStyle = c.wall;
  for (const [x, y] of walls) {
    ctx.fillRect(x * TILE, y * TILE, TILE, TILE);
  }

  if (scen.goal) {
    const [gx, gy] = scen.goal;
    ctx.fillStyle = c.goal;
    ctx.fillRect(gx * TILE + 4, gy * TILE + 4, TILE - 8, TILE - 8);
  }

  if (steps && steps.length > 0) {
    ctx.strokeStyle = c.trail;
    ctx.globalAlpha = 0.35;
    ctx.lineWidth = 2;
    ctx.beginPath();
    let started = false;
    for (const s of steps) {
      const cx = s.pos_x * TILE + TILE / 2;
      const cy = s.pos_y * TILE + TILE / 2;
      if (!started) {
        ctx.moveTo(cx, cy);
        started = true;
      } else {
        ctx.lineTo(cx, cy);
      }
    }
    ctx.stroke();
    ctx.globalAlpha = 1;
  }

  if (agentPos) {
    const [ax, ay] = agentPos;
    ctx.fillStyle = c.agent;
    ctx.beginPath();
    ctx.arc(ax * TILE + TILE / 2, ay * TILE + TILE / 2, TILE / 3, 0, Math.PI * 2);
    ctx.fill();
  }

  ctx.strokeStyle = c.grid;
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  for (let x = 0; x <= w; x++) {
    ctx.moveTo(x * TILE + 0.5, 0);
    ctx.lineTo(x * TILE + 0.5, h * TILE);
  }
  for (let y = 0; y <= h; y++) {
    ctx.moveTo(0, y * TILE + 0.5);
    ctx.lineTo(w * TILE, y * TILE + 0.5);
  }
  ctx.stroke();
}

function renderFeed(root: HTMLElement, steps: any[]) {
  if (!steps || steps.length === 0) {
    root.innerHTML = `<div class="text-text-dim text-xs p-2">no steps yet</div>`;
    return;
  }
  const ordered = [...steps].sort((a: any, b: any) => b.step - a.step);
  root.innerHTML = ordered
    .map((s: any) => {
      let summary = "";
      try {
        const r = JSON.parse(s.result);
        if (s.tool === "move") {
          summary = r.moved ? `→ ok` : `→ ${r.reason}`;
        } else {
          summary = r.reason || JSON.stringify(r).slice(0, 60);
        }
      } catch {
        summary = (s.result?.slice ? s.result.slice(0, 60) : "") || "";
      }
      return `<div class="grid gap-2 px-1 py-0.5 border-b border-border-subtle last:border-b-0 font-mono text-xs" style="grid-template-columns: 36px 90px 1fr;">
        <span class="text-text-dim text-right">${s.step}</span>
        <span class="text-info">${escapeHTML(s.tool)}</span>
        <span class="text-text whitespace-nowrap overflow-hidden text-ellipsis" title="${escapeHTML(summary)}">${escapeHTML(summary)}</span>
      </div>`;
    })
    .join("");
}

function shortID(id: string): string {
  return id ? id.slice(-6) : "";
}

function escapeHTML(s: any): string {
  return String(s ?? "").replace(/[&<>"']/g, (c: string) => {
    const map: Record<string, string> = {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    };
    return map[c];
  });
}

export default function RobotPanel() {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    return mount(ref.current);
  }, []);
  return <div ref={ref} className="h-full overflow-auto" />;
}
