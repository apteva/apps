// Pure layout: WorkflowDef -> {nodes, edges} with x/y. Used by the
// SVG renderer in WorkflowGraph.tsx and re-used by hit-testing.
//
// Layout strategy is intentionally simple — workflows in v0.1 are
// linear lists with branch/goto annotations, not free-form DAGs.
// Steps stack vertically; branch.else and on_error edges curve out
// to the side and connect via a target step's port.
//
// We deliberately do not pull in dagre / elk: the schema is small
// enough that ~80 LOC of hand-written layout produces a cleaner,
// more predictable result for the cases we actually run.

export interface StepDef {
  id: string;
  kind: "http" | "function" | "app" | "emit" | "branch";
  // Common
  input?: unknown;
  on_error?: GotoSpec;
  retry?: { max?: number; backoff_seconds?: number };
  // http
  url?: string;
  app?: string;
  path?: string;
  method?: string;
  // function
  name?: string;
  // app
  tool?: string;
  // emit
  topic?: string;
  data?: unknown;
  // branch
  when?: string;
  else?: GotoSpec;
}

export interface GotoSpec {
  goto?: string;
  end?: boolean;
  fail?: boolean;
  message?: string;
}

export interface WorkflowDef {
  name: string;
  trigger?: { kind: string; topic?: string; source?: string; cron?: string };
  steps: StepDef[];
}

export interface NodeLayout {
  id: string;
  step: StepDef;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface EdgeLayout {
  // Edges are keyed by (source, target, kind). The renderer uses the
  // kind to pick stroke/dash styling.
  id: string;
  fromID: string;
  toID: string | null; // null when the edge terminates at "end" or "fail"
  kind: "sequential" | "else" | "on_error" | "end" | "fail";
  // Optional label drawn near the edge — used for "fail: <message>"
  // sentinels at the bottom of the graph.
  label?: string;
}

export const NODE_W = 200;
export const NODE_H = 64;
export const ROW_H = 110; // vertical gap between sequential steps
export const SIDE_OFFSET = 240; // horizontal offset for goto-target arcs

export function layoutWorkflow(def: WorkflowDef): {
  nodes: NodeLayout[];
  edges: EdgeLayout[];
  width: number;
  height: number;
} {
  const nodes: NodeLayout[] = def.steps.map((s, i) => ({
    id: s.id,
    step: s,
    // Center column at x=0; step-editor side panel is to the right
    // of the SVG, so we leave x=0 for the main spine.
    x: 0,
    y: i * ROW_H,
    width: NODE_W,
    height: NODE_H,
  }));

  const edges: EdgeLayout[] = [];
  // Walk steps in declared order, building the sequential spine
  // plus any branch.else / on_error annotations as siblings.
  for (let i = 0; i < def.steps.length; i++) {
    const s = def.steps[i];
    const next = def.steps[i + 1];

    // Sequential edge to the next step in declaration order. Branch
    // steps still draw a sequential edge for the truthy path —
    // matching runner.go's "matched ? idx+1 : else".
    if (next) {
      edges.push({
        id: `seq:${s.id}->${next.id}`,
        fromID: s.id,
        toID: next.id,
        kind: "sequential",
      });
    }

    // branch.else: jump to a goto target, end the run, or fail it.
    if (s.kind === "branch" && s.else) {
      edges.push(makeGotoEdge(`else:${s.id}`, s.id, s.else, "else"));
    }
    // on_error: same shape, different semantics + styling.
    if (s.on_error) {
      edges.push(makeGotoEdge(`err:${s.id}`, s.id, s.on_error, "on_error"));
    }
  }

  const width = NODE_W + SIDE_OFFSET; // main spine + room for side-arcs
  const height = nodes.length * ROW_H + NODE_H;
  return { nodes, edges, width, height };
}

function makeGotoEdge(
  id: string,
  from: string,
  goto: GotoSpec,
  kind: "else" | "on_error",
): EdgeLayout {
  if (goto.end) {
    return { id, fromID: from, toID: null, kind: "end", label: goto.message };
  }
  if (goto.fail) {
    return { id, fromID: from, toID: null, kind: "fail", label: goto.message };
  }
  return {
    id,
    fromID: from,
    toID: goto.goto || null,
    kind,
  };
}

// Geometry helpers used by the renderer to draw the curves.
//
// portFor returns the (x,y) the edge should attach to on a node.
// Sequential edges anchor bottom-center → top-center. Side edges
// (else / on_error) anchor right-center on the source and left-
// center on the target so they don't overlap the sequential spine.
export function portFor(
  node: NodeLayout,
  side: "top" | "bottom" | "left" | "right",
): [number, number] {
  switch (side) {
    case "top":
      return [node.x + node.width / 2, node.y];
    case "bottom":
      return [node.x + node.width / 2, node.y + node.height];
    case "left":
      return [node.x, node.y + node.height / 2];
    case "right":
      return [node.x + node.width, node.y + node.height / 2];
  }
}

// arcPath returns an SVG path string for a curved arc between two
// points. Used by side edges; sequential edges use plain straight
// lines because they share the spine.
export function arcPath(from: [number, number], to: [number, number]): string {
  const [x1, y1] = from;
  const [x2, y2] = to;
  // Control point pulled to the right of both endpoints so the
  // arc bulges around the spine; for upward goto-edges we still
  // need to bulge right (workflows can goto an earlier step).
  const cx = Math.max(x1, x2) + 80;
  const cy = (y1 + y2) / 2;
  return `M ${x1} ${y1} Q ${cx} ${cy} ${x2} ${y2}`;
}

// findNodeAt returns the node at screen-space (x, y), accounting
// for the transform applied by pan/zoom. The renderer passes its
// transform into this function rather than running a hit-test in
// SVG coords directly, which keeps click handlers simple.
export function findNodeAt(
  nodes: NodeLayout[],
  px: number,
  py: number,
): NodeLayout | null {
  for (const n of nodes) {
    if (
      px >= n.x &&
      px <= n.x + n.width &&
      py >= n.y &&
      py <= n.y + n.height
    ) {
      return n;
    }
  }
  return null;
}
