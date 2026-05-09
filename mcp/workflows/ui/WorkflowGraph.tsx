// SVG renderer for the workflow graph. Pan via mouse drag, zoom
// via scroll. Each node shows its kind icon + step id. Selected
// node gets an accent border. Run-status overlay paints nodes
// ok/error/timeout/skipped/running with a pulsing animation on
// the actively-running step (driven by workflow.step.started /
// workflow.step.completed events from the sidecar).

import { useCallback, useEffect, useRef, useState } from "react";
import {
  arcPath,
  EdgeLayout,
  findNodeAt,
  layoutWorkflow,
  NodeLayout,
  NODE_H,
  NODE_W,
  portFor,
  WorkflowDef,
} from "./graph";

// ─── Public types ──────────────────────────────────────────────────

export interface StepStatus {
  // Status string from the runner: ok | error | timeout | skipped |
  // running | pending. The graph paints accordingly; "running" gets
  // the pulse animation.
  status: string;
  attempt?: number;
  error?: string;
}

export interface WorkflowGraphProps {
  def: WorkflowDef;
  // Per-step status overlay. Keys are step ids. When undefined the
  // graph renders in "neutral" mode (no run context). When provided
  // the graph paints colors and pulses the currently-running step.
  statuses?: Record<string, StepStatus>;
  // Currently-selected step id. Renderer outlines the corresponding
  // node so click → side panel feels connected.
  selectedID?: string | null;
  onSelect?: (stepID: string) => void;
}

// ─── Component ─────────────────────────────────────────────────────

export function WorkflowGraph({
  def,
  statuses,
  selectedID,
  onSelect,
}: WorkflowGraphProps) {
  const { nodes, edges, width, height } = layoutWorkflow(def);

  // Pan/zoom state. We apply this via a single <g transform=…>
  // rather than mutating per-node positions — the layout stays
  // canonical, the camera moves.
  const [tx, setTx] = useState(40);
  const [ty, setTy] = useState(20);
  const [scale, setScale] = useState(1);
  const dragRef = useRef<{ startX: number; startY: number; baseTx: number; baseTy: number } | null>(null);
  const svgRef = useRef<SVGSVGElement | null>(null);

  // Pan: hold-and-drag empty space (we hit-test for nodes first
  // and bail to selection if a node is hit).
  const onMouseDown = useCallback(
    (e: React.MouseEvent<SVGSVGElement>) => {
      const svg = svgRef.current;
      if (!svg) return;
      const rect = svg.getBoundingClientRect();
      const sx = (e.clientX - rect.left - tx) / scale;
      const sy = (e.clientY - rect.top - ty) / scale;
      const hit = findNodeAt(nodes, sx, sy);
      if (hit && onSelect) {
        onSelect(hit.id);
        return;
      }
      dragRef.current = {
        startX: e.clientX,
        startY: e.clientY,
        baseTx: tx,
        baseTy: ty,
      };
    },
    [nodes, onSelect, tx, ty, scale],
  );

  // Pan: drag handler; bound on window so a fast-moving mouse
  // doesn't escape the SVG.
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      const d = dragRef.current;
      if (!d) return;
      setTx(d.baseTx + (e.clientX - d.startX));
      setTy(d.baseTy + (e.clientY - d.startY));
    };
    const onUp = () => {
      dragRef.current = null;
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
    return () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
  }, []);

  // Zoom: mouse wheel, anchored at cursor so zooming feels
  // natural. Clamp scale to 0.4..2.5.
  const onWheel = useCallback(
    (e: React.WheelEvent<SVGSVGElement>) => {
      e.preventDefault();
      const svg = svgRef.current;
      if (!svg) return;
      const rect = svg.getBoundingClientRect();
      const cursorX = e.clientX - rect.left;
      const cursorY = e.clientY - rect.top;
      const factor = e.deltaY > 0 ? 0.9 : 1.1;
      const newScale = Math.max(0.4, Math.min(2.5, scale * factor));
      // Re-center so the point under the cursor stays put.
      const dx = (cursorX - tx) * (newScale / scale - 1);
      const dy = (cursorY - ty) * (newScale / scale - 1);
      setScale(newScale);
      setTx(tx - dx);
      setTy(ty - dy);
    },
    [scale, tx, ty],
  );

  // ─── Render ──────────────────────────────────────────────────────

  return (
    <svg
      ref={svgRef}
      onMouseDown={onMouseDown}
      onWheel={onWheel}
      className="w-full h-full bg-bg-input/30 cursor-grab active:cursor-grabbing select-none"
      role="img"
      aria-label={`Workflow graph for ${def.name}`}
    >
      <defs>
        <marker
          id="arrow"
          viewBox="0 0 10 10"
          refX="8"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" />
        </marker>
        <marker
          id="arrow-orange"
          viewBox="0 0 10 10"
          refX="8"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill="rgb(251 146 60)" />
        </marker>
        <marker
          id="arrow-red"
          viewBox="0 0 10 10"
          refX="8"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill="rgb(239 68 68)" />
        </marker>
        {/* Pulse keyframes for the currently-running node. Defined
            inside <defs> so the animation rides with the SVG, not
            with the panel's CSS bundle. */}
        <style>{`
          @keyframes wf-pulse {
            0%, 100% { opacity: 1; }
            50% { opacity: 0.45; }
          }
          .wf-pulse-running {
            animation: wf-pulse 1.2s ease-in-out infinite;
          }
        `}</style>
      </defs>

      <g transform={`translate(${tx}, ${ty}) scale(${scale})`}>
        {/* Edges first so nodes paint on top. */}
        {edges.map((e) => (
          <Edge key={e.id} edge={e} nodes={nodes} width={width} height={height} />
        ))}
        {nodes.map((n) => (
          <Node
            key={n.id}
            n={n}
            status={statuses?.[n.id]}
            selected={selectedID === n.id}
          />
        ))}
      </g>
    </svg>
  );
}

// ─── Edge component ────────────────────────────────────────────────

function Edge({
  edge,
  nodes,
  height,
}: {
  edge: EdgeLayout;
  nodes: NodeLayout[];
  width: number;
  height: number;
}) {
  const from = nodes.find((n) => n.id === edge.fromID);
  if (!from) return null;

  // Sequential edges follow the vertical spine: bottom of source →
  // top of target (next sibling). Side edges curve right.
  if (edge.kind === "sequential") {
    const to = nodes.find((n) => n.id === edge.toID);
    if (!to) return null;
    const [x1, y1] = portFor(from, "bottom");
    const [x2, y2] = portFor(to, "top");
    return (
      <line
        x1={x1}
        y1={y1}
        x2={x2}
        y2={y2}
        stroke="rgb(100 116 139)"
        strokeWidth={1.5}
        markerEnd="url(#arrow)"
      />
    );
  }

  // else / on_error / end / fail share the right-side bulge but
  // differ in styling.
  const isErr = edge.kind === "on_error" || edge.kind === "fail";
  const stroke = isErr ? "rgb(239 68 68)" : "rgb(251 146 60)";
  const marker = isErr ? "url(#arrow-red)" : "url(#arrow-orange)";
  const dash = "6 4";
  const fromPort = portFor(from, "right");

  // Terminal edges (end/fail) just point off-graph to a labeled
  // sentinel. We draw a short stub + a label.
  if (edge.kind === "end" || edge.kind === "fail" || !edge.toID) {
    const label = edge.kind === "fail" ? `fail${edge.label ? ": " + edge.label : ""}` : "end";
    const [x1, y1] = fromPort;
    const x2 = x1 + 80;
    const y2 = y1;
    return (
      <g>
        <path
          d={arcPath([x1, y1], [x2, y2])}
          fill="none"
          stroke={stroke}
          strokeWidth={1.5}
          strokeDasharray={dash}
          markerEnd={marker}
        />
        <text
          x={x2 + 6}
          y={y2 + 4}
          fontSize={10}
          fill={stroke}
          className="font-mono"
        >
          {label}
        </text>
      </g>
    );
  }

  const to = nodes.find((n) => n.id === edge.toID);
  if (!to) return null;
  const toPort = portFor(to, "right");
  return (
    <path
      d={arcPath(fromPort, toPort)}
      fill="none"
      stroke={stroke}
      strokeWidth={1.5}
      strokeDasharray={dash}
      markerEnd={marker}
    />
  );
}

// ─── Node component ────────────────────────────────────────────────

function Node({
  n,
  status,
  selected,
}: {
  n: NodeLayout;
  status?: StepStatus;
  selected: boolean;
}) {
  const palette = nodePalette(n.step.kind);
  const statusFill = status ? statusBg(status.status) : palette.fill;
  const statusStroke = status && status.status !== "running" ? statusBorder(status.status) : palette.stroke;

  return (
    <g
      transform={`translate(${n.x}, ${n.y})`}
      className={status?.status === "running" ? "wf-pulse-running" : undefined}
    >
      <rect
        width={NODE_W}
        height={NODE_H}
        rx={6}
        fill={statusFill}
        stroke={selected ? "rgb(34 197 94)" : statusStroke}
        strokeWidth={selected ? 2 : 1}
      />
      <text
        x={12}
        y={20}
        fontSize={10}
        fill="rgb(148 163 184)"
        className="font-mono uppercase tracking-wide"
      >
        {kindLabel(n.step.kind)}
      </text>
      <text
        x={12}
        y={40}
        fontSize={13}
        fontWeight={600}
        fill="rgb(226 232 240)"
        className="font-mono"
      >
        {truncate(n.id, 22)}
      </text>
      <text x={12} y={56} fontSize={10} fill="rgb(148 163 184)">
        {summaryFor(n.step)}
      </text>
      {status && status.status !== "running" && (
        <text
          x={NODE_W - 8}
          y={20}
          fontSize={10}
          textAnchor="end"
          fill={statusBorder(status.status)}
          className="font-mono uppercase"
        >
          {status.status}
        </text>
      )}
      {status?.attempt && status.attempt > 1 && (
        <text
          x={NODE_W - 8}
          y={56}
          fontSize={10}
          textAnchor="end"
          fill="rgb(148 163 184)"
        >
          retry {status.attempt}
        </text>
      )}
    </g>
  );
}

// ─── Per-kind styling ──────────────────────────────────────────────

function nodePalette(kind: string): { fill: string; stroke: string } {
  switch (kind) {
    case "http":
      return { fill: "rgb(15 23 42)", stroke: "rgb(34 197 94)" };
    case "function":
      return { fill: "rgb(15 23 42)", stroke: "rgb(168 85 247)" };
    case "app":
      return { fill: "rgb(15 23 42)", stroke: "rgb(59 130 246)" };
    case "emit":
      return { fill: "rgb(15 23 42)", stroke: "rgb(234 179 8)" };
    case "branch":
      return { fill: "rgb(15 23 42)", stroke: "rgb(244 114 182)" };
    default:
      return { fill: "rgb(15 23 42)", stroke: "rgb(100 116 139)" };
  }
}

function statusBg(s: string): string {
  switch (s) {
    case "ok":
      return "rgba(34 197 94 / 0.15)";
    case "error":
    case "timeout":
    case "fail":
      return "rgba(239 68 68 / 0.15)";
    case "skipped":
      return "rgba(100 116 139 / 0.1)";
    case "running":
      return "rgba(34 197 94 / 0.2)";
  }
  return "rgb(15 23 42)";
}

function statusBorder(s: string): string {
  switch (s) {
    case "ok":
      return "rgb(34 197 94)";
    case "error":
    case "timeout":
    case "fail":
      return "rgb(239 68 68)";
    case "skipped":
      return "rgb(100 116 139)";
    case "running":
      return "rgb(34 197 94)";
  }
  return "rgb(100 116 139)";
}

function kindLabel(kind: string): string {
  return kind.toUpperCase();
}

function summaryFor(step: { kind: string; url?: string; app?: string; path?: string; name?: string; tool?: string; topic?: string; when?: string }): string {
  switch (step.kind) {
    case "http":
      if (step.url) return truncate(step.url, 32);
      if (step.app && step.path) return `${step.app}${step.path}`;
      return "http";
    case "function":
      return step.name || "function";
    case "app":
      return `${step.app || "?"}.${step.tool || "?"}`;
    case "emit":
      return step.topic || "emit";
    case "branch":
      return truncate(step.when || "branch", 32);
    default:
      return step.kind;
  }
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}
