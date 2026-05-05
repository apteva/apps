// ComputerPanel — operator-facing dashboard panel for the computer
// app. Three columns: Browsers list (left), Live view (center),
// Chat transcript (right). All data comes from apteva-server
// endpoints; this file is purely presentational + fetch glue.
//
// Server endpoints consumed (added by a separate server-side PR):
//   GET  /api/browsers                            list across instances
//   GET  /api/browsers/events           (SSE)     lifecycle events
//   GET  /api/instances/{id}/computer/stream      stream descriptor
//   GET  /api/instances/{id}/screenshot           one-shot snapshot
//   GET  /api/instances/{id}/events     (SSE)     bus events as text/event-stream
//   POST /api/instances/{id}/console              inject operator message
//   POST /api/instances/{id}/agent/pause          pause agent
//   POST /api/instances/{id}/agent/resume         resume agent
//
// Until those endpoints land, the panel gracefully shows empty
// states / "endpoint not available" so operators understand they're
// looking at a wired-up shell rather than a broken page.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Card, CardHeader, StatusDot, StatusPill } from "@apteva/ui-kit";

interface BrowserRow {
  instance_id: string;
  backend: "local" | "browserbase" | "steel";
  url: string;
  status: "active" | "paused" | "idle";
}

interface BusEvent {
  type: string;             // "agent.text" | "tool.call" | "tool.result" | "console.user" | "status" | ...
  ts: string;
  payload: any;
}

type Descriptor =
  | { kind: "cdp-ws"; wsURL: string; display: { w: number; h: number }; backend: string }
  | { kind: "iframe"; url: string; backend: string };

const BACKEND_LABEL: Record<string, string> = {
  local: "Local",
  browserbase: "Browserbase",
  steel: "Steel",
};

export default function ComputerPanel() {
  const [selected, setSelected] = useState<string | null>(null);

  return (
    <div className="grid grid-cols-[260px_1fr_360px] gap-3 h-full p-3 bg-zinc-50 dark:bg-zinc-950">
      <BrowsersList selected={selected} onSelect={setSelected} />
      <LivePane instanceId={selected} />
      <ChatPane instanceId={selected} />
    </div>
  );
}

// ─── Browsers list ──────────────────────────────────────────────────

function BrowsersList({
  selected,
  onSelect,
}: {
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const [rows, setRows] = useState<BrowserRow[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const refresh = () => {
      fetch("/api/browsers")
        .then((r) => {
          if (!r.ok) throw new Error(`HTTP ${r.status}`);
          return r.json();
        })
        .then((d: BrowserRow[]) => {
          if (alive) {
            setRows(d);
            setErr(null);
            // Auto-select first row if nothing selected.
            if (!selected && d.length > 0) onSelect(d[0].instance_id);
          }
        })
        .catch((e) => {
          if (alive) setErr(String(e));
        });
    };
    refresh();
    // SSE for lifecycle; falls back to polling if /events 404s.
    let es: EventSource | null = null;
    try {
      es = new EventSource("/api/browsers/events");
      es.onmessage = () => refresh();
      es.onerror = () => {
        es?.close();
        es = null;
      };
    } catch {
      // ignore
    }
    const t = es ? null : setInterval(refresh, 5000);
    return () => {
      alive = false;
      es?.close();
      if (t) clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Card className="overflow-hidden flex flex-col">
      <CardHeader title="Browsers" right={<StatusDot variant={err ? "error" : "success"} />} />
      <div className="flex-1 overflow-y-auto -mx-3 px-3">
        {err && (
          <p className="text-xs text-zinc-500 px-1 py-2">
            {err === "TypeError: Failed to fetch"
              ? "endpoint /api/browsers not yet available — server PR pending"
              : err}
          </p>
        )}
        {!err && rows.length === 0 && (
          <p className="text-xs text-zinc-500 px-1 py-2">No active browser sessions.</p>
        )}
        <ul className="space-y-1.5">
          {rows.map((r) => (
            <BrowserListItem
              key={r.instance_id}
              row={r}
              selected={r.instance_id === selected}
              onSelect={() => onSelect(r.instance_id)}
            />
          ))}
        </ul>
      </div>
    </Card>
  );
}

function BrowserListItem({
  row,
  selected,
  onSelect,
}: {
  row: BrowserRow;
  selected: boolean;
  onSelect: () => void;
}) {
  let host = "";
  try {
    host = new URL(row.url).host;
  } catch {
    host = row.url;
  }
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        className={
          "w-full text-left px-2.5 py-2 rounded-md border transition-colors " +
          (selected
            ? "border-blue-400 bg-blue-50 dark:border-blue-500/60 dark:bg-blue-500/10"
            : "border-zinc-200 dark:border-zinc-800 hover:bg-zinc-100 dark:hover:bg-zinc-900")
        }
      >
        <div className="flex items-center justify-between gap-2 mb-0.5">
          <span className="text-xs font-medium truncate">{host || "—"}</span>
          <StatusPill
            variant={
              row.status === "active"
                ? "success"
                : row.status === "paused"
                  ? "warning"
                  : "neutral"
            }
            label={row.status}
          />
        </div>
        <div className="flex items-center gap-2 text-[11px] text-zinc-500">
          <span>{BACKEND_LABEL[row.backend] ?? row.backend}</span>
          <span>·</span>
          <span className="truncate">{row.instance_id}</span>
        </div>
      </button>
    </li>
  );
}

// ─── Live pane ──────────────────────────────────────────────────────

function LivePane({ instanceId }: { instanceId: string | null }) {
  const [descriptor, setDescriptor] = useState<Descriptor | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [paused, setPaused] = useState(false);

  useEffect(() => {
    setDescriptor(null);
    setErr(null);
    if (!instanceId) return;
    let alive = true;
    fetch(`/api/instances/${encodeURIComponent(instanceId)}/computer/stream`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json();
      })
      .then((d: Descriptor) => {
        if (alive) setDescriptor(d);
      })
      .catch((e) => {
        if (alive) setErr(String(e));
      });
    return () => {
      alive = false;
    };
  }, [instanceId]);

  const togglePause = useCallback(() => {
    if (!instanceId) return;
    const next = !paused;
    fetch(`/api/instances/${encodeURIComponent(instanceId)}/agent/${next ? "pause" : "resume"}`, {
      method: "POST",
    }).then(() => setPaused(next));
  }, [instanceId, paused]);

  return (
    <Card className="flex flex-col overflow-hidden">
      <CardHeader
        title={instanceId ? `Live · ${instanceId}` : "Live"}
        right={
          instanceId && (
            <div className="flex items-center gap-2">
              <button
                onClick={togglePause}
                className="text-xs px-2.5 py-1 rounded border border-zinc-300 dark:border-zinc-700 hover:bg-zinc-100 dark:hover:bg-zinc-800"
              >
                {paused ? "▶ Resume" : "⏸ Pause"}
              </button>
            </div>
          )
        }
      />
      <div className="flex-1 bg-black rounded relative overflow-hidden">
        {!instanceId && (
          <div className="absolute inset-0 flex items-center justify-center text-zinc-500 text-sm">
            Select a browser session on the left.
          </div>
        )}
        {instanceId && err && (
          <div className="absolute inset-0 flex items-center justify-center text-zinc-400 text-xs">
            {err}
          </div>
        )}
        {instanceId && !err && !descriptor && (
          <div className="absolute inset-0 flex items-center justify-center text-zinc-400 text-xs">
            connecting…
          </div>
        )}
        {descriptor?.kind === "iframe" && (
          <iframe
            src={descriptor.url}
            className="w-full h-full border-0"
            title="live view"
          />
        )}
        {descriptor?.kind === "cdp-ws" && (
          <PanelCDPCanvas
            wsURL={descriptor.wsURL}
            display={descriptor.display}
            interactive
          />
        )}
      </div>
    </Card>
  );
}

// PanelCDPCanvas — like LiveCard's CDPCanvas but with input
// forwarding turned on. Operator can click and type into the canvas
// and the events route through CDP into the underlying Chrome.
function PanelCDPCanvas({
  wsURL,
  display,
  interactive,
}: {
  wsURL: string;
  display: { w: number; h: number };
  interactive: boolean;
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<"connecting" | "connected" | "error">(
    "connecting",
  );

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let nextId = 1;
    const ws = new WebSocket(wsURL);
    wsRef.current = ws;

    const send = (method: string, params?: object) => {
      const id = nextId++;
      ws.send(JSON.stringify({ id, method, params: params ?? {} }));
    };

    ws.onopen = () => {
      setStatus("connected");
      send("Page.enable");
      send("Page.startScreencast", {
        format: "jpeg",
        quality: 70,
        maxWidth: display.w,
        maxHeight: display.h,
      });
    };
    ws.onerror = () => setStatus("error");
    ws.onclose = () => setStatus("error");
    ws.onmessage = (msg) => {
      let m: any;
      try {
        m = JSON.parse(msg.data);
      } catch {
        return;
      }
      if (m.method === "Page.screencastFrame") {
        const img = new Image();
        img.onload = () => {
          if (
            img.naturalWidth !== canvas.width ||
            img.naturalHeight !== canvas.height
          ) {
            canvas.width = img.naturalWidth;
            canvas.height = img.naturalHeight;
          }
          ctx.drawImage(img, 0, 0);
        };
        img.src = "data:image/jpeg;base64," + m.params.data;
        send("Page.screencastFrameAck", { sessionId: m.params.sessionId });
      }
    };

    return () => {
      try {
        ws.close();
      } catch {}
    };
  }, [wsURL, display.w, display.h]);

  // Input handlers — only registered when interactive=true.
  const dispatchMouse = useCallback(
    (type: string, e: React.MouseEvent<HTMLCanvasElement>, button: string = "left") => {
      const canvas = canvasRef.current;
      const ws = wsRef.current;
      if (!canvas || !ws || ws.readyState !== WebSocket.OPEN) return;
      const rect = canvas.getBoundingClientRect();
      const sx = canvas.width / rect.width;
      const sy = canvas.height / rect.height;
      const x = Math.round((e.clientX - rect.left) * sx);
      const y = Math.round((e.clientY - rect.top) * sy);
      ws.send(
        JSON.stringify({
          id: Date.now(),
          method: "Input.dispatchMouseEvent",
          params: { type, x, y, button, clickCount: 1 },
        }),
      );
    },
    [],
  );

  const dispatchKey = useCallback(
    (type: string, e: React.KeyboardEvent<HTMLCanvasElement>) => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      const printable = e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey;
      if (printable && type === "keyDown") {
        ws.send(
          JSON.stringify({
            id: Date.now(),
            method: "Input.insertText",
            params: { text: e.key },
          }),
        );
      } else {
        ws.send(
          JSON.stringify({
            id: Date.now(),
            method: "Input.dispatchKeyEvent",
            params: {
              type,
              key: e.key,
              code: e.code,
              windowsVirtualKeyCode: e.keyCode,
              modifiers:
                (e.altKey ? 1 : 0) |
                (e.ctrlKey ? 2 : 0) |
                (e.metaKey ? 4 : 0) |
                (e.shiftKey ? 8 : 0),
            },
          }),
        );
      }
    },
    [],
  );

  return (
    <>
      <canvas
        ref={canvasRef}
        width={display.w}
        height={display.h}
        tabIndex={interactive ? 0 : -1}
        className="w-full h-full object-contain bg-black focus:outline-none"
        onMouseMove={(e) => interactive && dispatchMouse("mouseMoved", e, "none")}
        onMouseDown={(e) => {
          if (!interactive) return;
          (e.currentTarget as HTMLCanvasElement).focus();
          dispatchMouse("mousePressed", e);
        }}
        onMouseUp={(e) => interactive && dispatchMouse("mouseReleased", e)}
        onKeyDown={(e) => {
          if (!interactive) return;
          e.preventDefault();
          dispatchKey("keyDown", e);
        }}
        onKeyUp={(e) => interactive && dispatchKey("keyUp", e)}
      />
      {status !== "connected" && (
        <div className="absolute inset-0 flex items-center justify-center text-xs text-zinc-300 bg-black/30 pointer-events-none">
          {status}
        </div>
      )}
    </>
  );
}

// ─── Chat pane ──────────────────────────────────────────────────────

function ChatPane({ instanceId }: { instanceId: string | null }) {
  const [events, setEvents] = useState<BusEvent[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const scrollRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setEvents([]);
    setErr(null);
    if (!instanceId) return;
    let alive = true;
    let es: EventSource | null = null;
    try {
      es = new EventSource(`/api/instances/${encodeURIComponent(instanceId)}/events`);
      es.onmessage = (msg) => {
        if (!alive) return;
        try {
          const ev: BusEvent = JSON.parse(msg.data);
          setEvents((prev) => [...prev, ev]);
        } catch {
          // ignore malformed
        }
      };
      es.onerror = () => setErr("event stream disconnected (server PR pending?)");
    } catch (e) {
      setErr(String(e));
    }
    return () => {
      alive = false;
      es?.close();
    };
  }, [instanceId]);

  // Auto-scroll to bottom on new events unless user scrolled up.
  const stickyRef = useRef(true);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const onScroll = () => {
      const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
      stickyRef.current = distance < 40;
    };
    el.addEventListener("scroll", onScroll);
    return () => el.removeEventListener("scroll", onScroll);
  }, []);
  useEffect(() => {
    if (stickyRef.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [events.length]);

  const submitDraft = () => {
    const text = draft.trim();
    if (!text || !instanceId) return;
    fetch(`/api/instances/${encodeURIComponent(instanceId)}/console`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
    setDraft("");
  };

  return (
    <Card className="flex flex-col overflow-hidden">
      <CardHeader title="Chat" />
      <div ref={scrollRef} className="flex-1 overflow-y-auto -mx-3 px-3 space-y-2">
        {!instanceId && (
          <p className="text-xs text-zinc-500 py-2">Select an instance to see chat.</p>
        )}
        {instanceId && err && events.length === 0 && (
          <p className="text-xs text-zinc-500 py-2">{err}</p>
        )}
        {events.map((ev, i) => (
          <ChatMessage key={i} event={ev} />
        ))}
      </div>
      <div className="mt-2 -mx-3 px-3 -mb-3 pb-3 pt-2 border-t border-zinc-200 dark:border-zinc-800 bg-zinc-50/60 dark:bg-zinc-900/40">
        <div className="flex gap-1.5">
          <input
            type="text"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                submitDraft();
              }
            }}
            disabled={!instanceId}
            placeholder={instanceId ? "Inject a message…" : "Select an instance first"}
            className="flex-1 text-xs px-2.5 py-1.5 rounded border border-zinc-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 disabled:opacity-50"
          />
          <button
            onClick={submitDraft}
            disabled={!instanceId || !draft.trim()}
            className="text-xs px-3 py-1.5 rounded bg-blue-500 text-white disabled:opacity-50 disabled:bg-zinc-400"
          >
            Send
          </button>
        </div>
      </div>
    </Card>
  );
}

function ChatMessage({ event }: { event: BusEvent }) {
  const ts = useMemo(() => {
    try {
      return new Date(event.ts).toLocaleTimeString();
    } catch {
      return event.ts;
    }
  }, [event.ts]);

  switch (event.type) {
    case "agent.text":
      return (
        <Bubble side="left" tone="agent" ts={ts} label="agent">
          {String(event.payload?.text ?? "")}
        </Bubble>
      );
    case "console.user":
    case "directive":
      return (
        <Bubble side="right" tone="user" ts={ts} label={event.type === "directive" ? "directive" : "operator"}>
          {String(event.payload?.text ?? "")}
        </Bubble>
      );
    case "tool.call":
      return (
        <ToolCall
          ts={ts}
          name={String(event.payload?.name ?? "tool")}
          args={event.payload?.args}
        />
      );
    case "tool.result":
      return (
        <ToolResult
          ts={ts}
          name={String(event.payload?.name ?? "tool")}
          result={event.payload?.result}
          ok={event.payload?.ok !== false}
        />
      );
    case "status":
    case "paused":
    case "resumed":
      return (
        <p className="text-[11px] text-center text-zinc-400 py-1">
          <span className="px-2 py-0.5 rounded bg-zinc-100 dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800">
            {event.type} · {ts}
          </span>
        </p>
      );
    default:
      return (
        <p className="text-[11px] text-zinc-400">
          {event.type} · {ts}
        </p>
      );
  }
}

function Bubble({
  side,
  tone,
  label,
  ts,
  children,
}: {
  side: "left" | "right";
  tone: "agent" | "user";
  label: string;
  ts: string;
  children: React.ReactNode;
}) {
  const bg =
    tone === "agent"
      ? "bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800"
      : "bg-blue-50 dark:bg-blue-500/10 border border-blue-200 dark:border-blue-500/30";
  return (
    <div className={"flex flex-col " + (side === "right" ? "items-end" : "items-start")}>
      <div className={"max-w-[88%] rounded-md px-2.5 py-1.5 text-xs leading-snug whitespace-pre-wrap " + bg}>
        {children}
      </div>
      <p className="text-[10px] text-zinc-400 mt-0.5 px-1">
        {label} · {ts}
      </p>
    </div>
  );
}

function ToolCall({ name, args, ts }: { name: string; args: any; ts: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="border border-zinc-200 dark:border-zinc-800 rounded-md bg-zinc-50 dark:bg-zinc-900/60 px-2.5 py-1.5">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full text-left flex items-center justify-between text-[11px] font-mono"
      >
        <span>
          <span className="text-zinc-400">▸</span> <span className="font-semibold">{name}</span>
          <span className="ml-2 text-zinc-400">{ts}</span>
        </span>
        <span className="text-zinc-400">{open ? "−" : "+"}</span>
      </button>
      {open && (
        <pre className="mt-1.5 text-[10px] font-mono text-zinc-600 dark:text-zinc-400 bg-zinc-100 dark:bg-zinc-950 rounded p-1.5 overflow-x-auto whitespace-pre-wrap">
          {safeStringify(args)}
        </pre>
      )}
    </div>
  );
}

function ToolResult({
  name,
  result,
  ok,
  ts,
}: {
  name: string;
  result: any;
  ok: boolean;
  ts: string;
}) {
  // Detect a screenshot-shaped result (base64 image).
  const screenshotURL =
    typeof result === "string" && result.startsWith("data:image/")
      ? result
      : typeof result?.screenshot_url === "string"
        ? result.screenshot_url
        : null;

  return (
    <div className="ml-3 pl-2.5 border-l-2 border-zinc-200 dark:border-zinc-800">
      <p className="text-[11px] font-mono">
        <span className={ok ? "text-emerald-500" : "text-red-500"}>
          {ok ? "→ ok" : "→ error"}
        </span>
        <span className="ml-1.5 text-zinc-500">{name}</span>
        <span className="ml-1.5 text-zinc-400">{ts}</span>
      </p>
      {screenshotURL && (
        <img
          src={screenshotURL}
          alt="tool result"
          className="mt-1 max-w-[200px] rounded border border-zinc-200 dark:border-zinc-800"
        />
      )}
      {!screenshotURL && result != null && (
        <pre className="mt-1 text-[10px] font-mono text-zinc-600 dark:text-zinc-400 whitespace-pre-wrap break-all">
          {typeof result === "string" ? result : safeStringify(result)}
        </pre>
      )}
    </div>
  );
}

function safeStringify(v: any): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
