// RobotPanel — agent navigation eval sandbox.
//
// React shell delegating to a vanilla mount(root) routine that builds
// the DOM, drives the canvas, and runs the poll loop. The hybrid keeps
// the v0.1.x panel small without rewriting the canvas + activity feed
// as idiomatic React; that refactor lands in v0.2 alongside items.
//
// Built via `bun run scripts/build-panels.ts` from the apps/ repo
// root; the .mjs alongside this file is the generated artifact.

import { useEffect, useRef } from "react";

const API = "/api/apps/robot";
const TILE = 24;

const COLORS: Record<string, string> = {
  floor: "#f4f4f5",
  wall: "#1f2937",
  goal: "#22c55e",
  agent: "#3b82f6",
  fog: "#27272a",
  oob: "#09090b",
  item: "#f59e0b",
  hazard: "#ef4444",
  grid: "#d4d4d8",
};

function mount(root: HTMLElement): () => void {
  root.innerHTML = `
    <style>
      .robot-panel { font: 13px/1.4 system-ui, sans-serif; color: #18181b; padding: 16px; }
      .robot-panel.dark { color: #e4e4e7; }
      .robot-row { display: flex; gap: 16px; align-items: flex-start; }
      .robot-col { flex: 1; min-width: 0; }
      .robot-h { font-size: 11px; text-transform: uppercase; letter-spacing: .04em; color: #71717a; margin-bottom: 4px; }
      .robot-title { font-size: 18px; font-weight: 600; margin: 0 0 4px 0; }
      .robot-desc { color: #52525b; margin: 0 0 12px 0; }
      .robot-canvas-wrap { background: #fafafa; border: 1px solid #e4e4e7; border-radius: 6px; padding: 12px; display: inline-block; }
      .robot-feed { max-height: 480px; overflow: auto; border: 1px solid #e4e4e7; border-radius: 6px; padding: 8px; background: #fafafa; }
      .robot-feed-row { display: grid; grid-template-columns: 36px 90px 1fr; gap: 8px; padding: 3px 4px; border-bottom: 1px dashed #e4e4e7; font-family: ui-monospace, monospace; font-size: 12px; }
      .robot-feed-row:last-child { border-bottom: none; }
      .robot-step { color: #71717a; text-align: right; }
      .robot-tool { color: #2563eb; }
      .robot-result { color: #18181b; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
      .robot-metrics { display: flex; gap: 16px; font-size: 13px; margin-top: 8px; }
      .robot-metric b { color: #18181b; }
      .robot-controls { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; }
      .robot-controls select, .robot-controls button { font: inherit; padding: 4px 8px; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; }
      .robot-controls button { cursor: pointer; background: #2563eb; color: #fff; border-color: #2563eb; }
      .robot-controls button:hover { background: #1d4ed8; }
      .robot-controls button:disabled { background: #a1a1aa; border-color: #a1a1aa; cursor: default; }
      .robot-status-pill { font-size: 11px; padding: 2px 8px; border-radius: 999px; background: #e4e4e7; color: #18181b; }
      .robot-status-pill.success { background: #dcfce7; color: #166534; }
      .robot-status-pill.timeout { background: #fef3c7; color: #92400e; }
      .robot-status-pill.active  { background: #dbeafe; color: #1e40af; }
      .robot-dpad { display: grid; grid-template-columns: repeat(3, 36px); grid-template-rows: repeat(3, 36px); gap: 4px; margin-top: 12px; }
      .robot-dpad button { font: inherit; font-weight: 600; cursor: pointer; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; color: #18181b; }
      .robot-dpad button:hover:not(:disabled) { background: #f4f4f5; }
      .robot-dpad button:active:not(:disabled) { background: #e4e4e7; }
      .robot-dpad button:disabled { color: #a1a1aa; cursor: default; }
      .robot-dpad-N { grid-column: 2; grid-row: 1; }
      .robot-dpad-W { grid-column: 1; grid-row: 2; }
      .robot-dpad-E { grid-column: 3; grid-row: 2; }
      .robot-dpad-S { grid-column: 2; grid-row: 3; }
      .robot-drive { display: flex; gap: 16px; align-items: flex-start; margin-top: 12px; }
      .robot-drive-extras { display: flex; flex-direction: column; gap: 4px; margin-top: 22px; }
      .robot-drive-extras button { font: inherit; padding: 4px 10px; border-radius: 4px; border: 1px solid #d4d4d8; background: #fff; color: #18181b; cursor: pointer; }
      .robot-drive-extras button:disabled { color: #a1a1aa; cursor: default; }
      .robot-drive-hint { font-size: 11px; color: #71717a; align-self: center; }
    </style>
    <div class="robot-panel">
      <h2 class="robot-title">Robot</h2>
      <p class="robot-desc">Agent navigation eval sandbox. Pick a scenario, start an episode, watch the agent's tool calls land in the feed.</p>

      <div class="robot-controls">
        <span class="robot-h">Scenario</span>
        <select data-role="scenario-picker"></select>
        <button data-role="start">Start episode</button>
        <span class="robot-h">Episode</span>
        <select data-role="episode-picker"></select>
        <span class="robot-status-pill" data-role="status">idle</span>
      </div>

      <div class="robot-row">
        <div class="robot-col" style="flex: 0 0 auto;">
          <div class="robot-h">World</div>
          <div class="robot-canvas-wrap"><canvas data-role="grid"></canvas></div>
          <div class="robot-metrics">
            <span class="robot-metric">steps <b data-role="m-steps">—</b></span>
            <span class="robot-metric">optimal <b data-role="m-optimal">—</b></span>
            <span class="robot-metric">ratio <b data-role="m-ratio">—</b></span>
          </div>
          <div class="robot-drive">
            <div class="robot-dpad">
              <button class="robot-dpad-N" data-dir="N" title="Move north (↑)">↑</button>
              <button class="robot-dpad-W" data-dir="W" title="Move west (←)">←</button>
              <button class="robot-dpad-E" data-dir="E" title="Move east (→)">→</button>
              <button class="robot-dpad-S" data-dir="S" title="Move south (↓)">↓</button>
            </div>
            <div class="robot-drive-extras">
              <button data-role="pick" title="Pick up an item (inert in v0.1)">pick</button>
              <button data-role="drop" title="Drop the held item (inert in v0.1)">drop</button>
            </div>
            <span class="robot-drive-hint">arrow keys also move</span>
          </div>
        </div>
        <div class="robot-col">
          <div class="robot-h">Activity</div>
          <div class="robot-feed" data-role="feed"></div>
        </div>
      </div>
    </div>
  `;

  const els: any = {
    scenPicker: root.querySelector('[data-role="scenario-picker"]'),
    epPicker:   root.querySelector('[data-role="episode-picker"]'),
    startBtn:   root.querySelector('[data-role="start"]'),
    statusPill: root.querySelector('[data-role="status"]'),
    canvas:     root.querySelector('[data-role="grid"]'),
    feed:       root.querySelector('[data-role="feed"]'),
    mSteps:     root.querySelector('[data-role="m-steps"]'),
    mOptimal:   root.querySelector('[data-role="m-optimal"]'),
    mRatio:     root.querySelector('[data-role="m-ratio"]'),
    dpadBtns:   Array.from(root.querySelectorAll(".robot-dpad button")),
    pickBtn:    root.querySelector('[data-role="pick"]'),
    dropBtn:    root.querySelector('[data-role="drop"]'),
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
    btn.addEventListener("click", () => driveMove(btn.dataset.dir));
  }
  els.pickBtn.addEventListener("click", () => driveAction("pick"));
  els.dropBtn.addEventListener("click", () => driveAction("drop"));

  // Keyboard arrows. Listen at document so the panel doesn't need
  // focus; ignore when the user is typing in an input/select.
  const keyHandler = (ev: KeyboardEvent) => {
    const target = ev.target as HTMLElement | null;
    if (target && /^(INPUT|SELECT|TEXTAREA)$/.test(target.tagName)) return;
    const map: Record<string, string> = { ArrowUp: "N", ArrowDown: "S", ArrowLeft: "W", ArrowRight: "E" };
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
      els.statusPill.textContent = "idle";
      els.statusPill.className = "robot-status-pill";
      els.mSteps.textContent = els.mOptimal.textContent = els.mRatio.textContent = "—";
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

  function renderStatus(pill: any, ep: any) {
    pill.classList.remove("success", "timeout", "active");
    if (ep.terminal_reason === "success") {
      pill.textContent = "success";
      pill.classList.add("success");
    } else if (ep.terminal_reason === "timeout") {
      pill.textContent = "timeout";
      pill.classList.add("timeout");
    } else {
      pill.textContent = "active";
      pill.classList.add("active");
    }
  }
}

function drawGrid(canvas: HTMLCanvasElement, scen: any, agentPos: any, steps: any[]) {
  if (!canvas || !scen) return;
  const w = scen.grid.width;
  const h = scen.grid.height;
  canvas.width = w * TILE + 1;
  canvas.height = h * TILE + 1;
  const ctx = canvas.getContext("2d")!;

  ctx.fillStyle = COLORS.floor;
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  const walls: number[][] = scen.walls || [];
  ctx.fillStyle = COLORS.wall;
  for (const [x, y] of walls) {
    ctx.fillRect(x * TILE, y * TILE, TILE, TILE);
  }

  if (scen.goal) {
    const [gx, gy] = scen.goal;
    ctx.fillStyle = COLORS.goal;
    ctx.fillRect(gx * TILE + 4, gy * TILE + 4, TILE - 8, TILE - 8);
  }

  if (steps && steps.length > 0) {
    ctx.strokeStyle = "rgba(59,130,246,0.35)";
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
  }

  if (agentPos) {
    const [ax, ay] = agentPos;
    ctx.fillStyle = COLORS.agent;
    ctx.beginPath();
    ctx.arc(ax * TILE + TILE / 2, ay * TILE + TILE / 2, TILE / 3, 0, Math.PI * 2);
    ctx.fill();
  }

  ctx.strokeStyle = COLORS.grid;
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
    root.innerHTML = `<div style="color:#71717a; font-size:12px; padding:8px;">no steps yet</div>`;
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
      return `<div class="robot-feed-row">
      <span class="robot-step">${s.step}</span>
      <span class="robot-tool">${escapeHTML(s.tool)}</span>
      <span class="robot-result">${escapeHTML(summary)}</span>
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
  return <div ref={ref} style={{ height: "100%", overflow: "auto" }} />;
}
