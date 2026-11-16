// Operator authoring for interactive lessons: the element library (with a
// sandboxed preview), runner bindings, the lesson block editor, and course
// export/import. Rendered by CommunityPanel.

import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { buildSrcdoc } from "./portal/src/elementSdk";

export type CallTool = <T>(tool: string, args: Record<string, unknown>) => Promise<T>;

interface CourseElement {
  id: string;
  slug: string;
  name: string;
  description: string;
  current_version: number;
  draft_runners: string[];
  draft_settings_schema: Record<string, unknown>;
  draft_changed: boolean;
  draft_source?: string;
  archived_at?: string;
  used_by_blocks: number;
}

interface ElementVersion {
  version: number;
  checksum: string;
  runners: string[];
  published_at: string;
}

interface Runner {
  name: string;
  app: string;
  tool: string;
}

type BlockType = "markdown" | "lesson_body" | "video" | "resources" | "quizzes" | "assignments" | "element";
type Slot = "main" | "stage" | "side";

interface LessonBlock {
  id?: string;
  type: BlockType;
  slot: Slot;
  title: string;
  element_id?: string;
  element_version?: number;
  element_name?: string;
  settings: Record<string, unknown>;
  required_steps: string[];
  height?: number;
}

const BLOCK_TYPES: { value: BlockType; label: string }[] = [
  { value: "markdown", label: "Text (Markdown)" },
  { value: "element", label: "Interactive element" },
  { value: "lesson_body", label: "Lesson body" },
  { value: "video", label: "Lesson video" },
  { value: "resources", label: "Resources" },
  { value: "quizzes", label: "Quizzes" },
  { value: "assignments", label: "Assignments" },
];

const inputClass = "w-full bg-bg-secondary border border-border rounded px-3 py-2 text-sm";
const monoStyle = { fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", fontSize: 12 };

function button(tone: "primary" | "secondary" | "ghost" = "secondary") {
  if (tone === "primary") return "px-2.5 py-1.5 rounded bg-accent text-bg text-sm font-medium disabled:opacity-50";
  if (tone === "ghost") return "px-2 py-1 rounded text-text-muted hover:bg-bg-secondary text-sm disabled:opacity-50";
  return "px-2.5 py-1.5 rounded border border-border text-sm text-text hover:bg-bg-secondary disabled:opacity-50";
}

function errorText(err: unknown) {
  return err instanceof Error ? err.message : String(err);
}

export function WideDialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="absolute inset-0 bg-black/40 flex items-center justify-center p-4 z-20"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="flex w-full flex-col bg-bg border border-border rounded shadow-xl"
        style={{ maxWidth: 1180, maxHeight: "calc(100vh - 2rem)" }}
      >
        <header className="px-4 py-3 border-b border-border flex items-center justify-between">
          <div className="font-medium">{title}</div>
          <button className={button("ghost")} onClick={onClose}>
            Close
          </button>
        </header>
        <div className="overflow-y-auto p-4">{children}</div>
      </div>
    </div>
  );
}

function Label({ children }: { children: ReactNode }) {
  return <div className="text-xs uppercase tracking-wide text-text-muted mb-1">{children}</div>;
}

function parseJSONObject(text: string, what: string): Record<string, unknown> {
  const trimmed = text.trim();
  if (!trimmed) return {};
  let value: unknown;
  try {
    value = JSON.parse(trimmed);
  } catch (err) {
    throw new Error(`${what} is not valid JSON: ${errorText(err)}`);
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${what} must be a JSON object`);
  return value as Record<string, unknown>;
}

// ─── Sandboxed preview (panel side) ──────────────────────────────

/** Mounts element html with the real SDK; answers ready with the given
 *  settings and logs everything else instead of saving it. */
export function ElementPreview({ html, settings, height = 460 }: { html: string; settings: Record<string, unknown>; height?: number }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [log, setLog] = useState<string[]>([]);
  const [reload, setReload] = useState(0);
  const srcdoc = useMemo(() => buildSrcdoc(html), [html, reload]);
  const live = useRef(settings);
  live.current = settings;
  useEffect(() => {
    setLog([]);
    let port: MessagePort | null = null;
    const add = (line: string) => setLog((current) => [...current.slice(-40), line]);
    function onMessage(event: MessageEvent) {
      if (event.source !== frame.current?.contentWindow || event.data?.type !== "course-element:hello") return;
      port?.close();
      const channel = new MessageChannel();
      port = channel.port1;
      port.onmessage = (e) => {
        const msg = e.data || {};
        const reply = (result: unknown) => port?.postMessage({ id: msg.id, result });
        switch (msg.method) {
          case "ready":
            add("ready");
            return reply({ settings: live.current, state: null, steps: [], lesson: { id: "preview", title: "Preview" }, block: { id: "preview" } });
          case "save":
            add(`save ${JSON.stringify(msg.params).slice(0, 120)}`);
            return reply({ ok: true });
          case "emit":
            add(`emit ${msg.params?.name} ${JSON.stringify(msg.params?.payload ?? null).slice(0, 100)}`);
            return reply({ ok: true });
          case "complete":
            add(`complete ${msg.params?.step}`);
            return reply({ lesson_completed: false });
          case "run":
            add(`run ${msg.params?.runner} (not executed in preview)`);
            return port?.postMessage({ id: msg.id, error: "Runners are not called from the preview" });
          default:
            return port?.postMessage({ id: msg.id, error: "unknown method" });
        }
      };
      frame.current?.contentWindow?.postMessage({ type: "course-element:connect" }, "*", [channel.port2]);
    }
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      port?.close();
    };
  }, [srcdoc]);
  return (
    <div className="space-y-2">
      <iframe
        ref={frame}
        title="Element preview"
        sandbox="allow-scripts"
        srcDoc={srcdoc}
        className="w-full border border-border rounded bg-bg"
        style={{ height }}
      />
      <div className="flex items-center justify-between gap-2">
        <div className="text-xs text-text-muted">Bridge messages (saves and steps are not stored in preview)</div>
        <button className={button("ghost")} onClick={() => setReload((v) => v + 1)}>
          Reload preview
        </button>
      </div>
      <pre className="bg-bg-secondary border border-border rounded p-2 overflow-auto" style={{ ...monoStyle, maxHeight: 120, margin: 0 }}>
        {log.length ? log.join("\n") : "No messages yet."}
      </pre>
    </div>
  );
}

// ─── Element library ─────────────────────────────────────────────

export function ElementLibraryDialog({ communityId, call, onClose }: { communityId: string; call: CallTool; onClose: () => void }) {
  const [elements, setElements] = useState<CourseElement[]>([]);
  const [runners, setRunners] = useState<Runner[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"elements" | "runners">("elements");

  async function refresh(select?: string) {
    const [list, bound] = await Promise.all([
      call<{ elements: CourseElement[] }>("course_elements_list", { community_id: communityId, include_archived: true }),
      call<{ runners: Runner[] }>("element_runners_list", { community_id: communityId }),
    ]);
    setElements(list.elements);
    setRunners(bound.runners);
    if (select !== undefined) setSelected(select);
  }
  useEffect(() => {
    refresh().catch((err) => setError(errorText(err)));
  }, [communityId]);

  async function act(fn: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <WideDialog title="Interactive elements" onClose={onClose}>
      <div className="flex gap-2 mb-3">
        <button className={button(tab === "elements" ? "primary" : "secondary")} onClick={() => setTab("elements")}>
          Library
        </button>
        <button className={button(tab === "runners" ? "primary" : "secondary")} onClick={() => setTab("runners")}>
          Runners
        </button>
      </div>
      {error && <div className="text-sm text-warn mb-3">{error}</div>}
      {tab === "runners" ? (
        <RunnersEditor runners={runners} busy={busy} onSave={(next) => act(async () => {
          await call("element_runners_set", { community_id: communityId, runners: next });
          await refresh();
        })} />
      ) : (
        <div className="flex gap-4" style={{ minHeight: 520 }}>
          <aside className="space-y-2" style={{ width: 260, flexShrink: 0 }}>
            <button className={button("primary") + " w-full"} disabled={busy} onClick={() => setSelected("new")}>
              New element
            </button>
            <button
              className={button() + " w-full"}
              disabled={busy}
              onClick={() => act(async () => {
                await call("course_elements_install_starters", { community_id: communityId });
                await refresh();
              })}
            >
              Install starter elements
            </button>
            <ul className="border border-border rounded">
              {elements.length === 0 && <li className="p-3 text-sm text-text-muted">No elements yet.</li>}
              {elements.map((e) => (
                <li key={e.id}>
                  <button
                    className={"w-full text-left px-3 py-2 border-b border-border " + (selected === e.id ? "bg-bg-secondary" : "hover:bg-bg-secondary")}
                    onClick={() => setSelected(e.id)}
                  >
                    <div className="text-sm font-medium truncate">{e.name}</div>
                    <div className="text-xs text-text-muted">
                      {e.slug} · {e.current_version ? `v${e.current_version}` : "unpublished"}
                      {e.draft_changed ? " · draft changes" : ""}
                      {e.archived_at ? " · archived" : ""} · {e.used_by_blocks} blocks
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </aside>
          <section className="flex-1 min-w-0">
            {selected ? (
              <ElementEditor
                key={selected}
                communityId={communityId}
                elementId={selected === "new" ? "" : selected}
                call={call}
                busy={busy}
                act={act}
                onSaved={(id) => refresh(id)}
              />
            ) : (
              <div className="p-4 text-sm text-text-muted">
                Elements are HTML/JS programs that run sandboxed inside lessons. They get <code>window.lesson</code> with
                <code> ready()</code>, <code>save(state)</code>, <code>emit(name, payload)</code>, <code>on(name, fn)</code>,
                <code> complete(step)</code> and <code>run(runner, input)</code>. Pick one, or install the starters.
              </div>
            )}
          </section>
        </div>
      )}
    </WideDialog>
  );
}

const NEW_ELEMENT_HTML = `<!doctype html>
<html>
<head>
<style>
  body { font: 14px system-ui, sans-serif; margin: 0; padding: 16px; }
  button { padding: 6px 12px; }
</style>
</head>
<body>
  <h3 id="title">Hello</h3>
  <button id="go">Done</button>
  <script type="module">
    const { settings, state } = await lesson.ready();
    document.getElementById("title").textContent = settings.title || "Hello";
    document.getElementById("go").onclick = () => {
      lesson.save({ clicked: true });
      lesson.complete("clicked");
    };
  </script>
</body>
</html>
`;

function ElementEditor({
  communityId,
  elementId,
  call,
  busy,
  act,
  onSaved,
}: {
  communityId: string;
  elementId: string;
  call: CallTool;
  busy: boolean;
  act: (fn: () => Promise<void>) => Promise<void>;
  onSaved: (id: string) => Promise<void>;
}) {
  const [element, setElement] = useState<CourseElement | null>(null);
  const [versions, setVersions] = useState<ElementVersion[]>([]);
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [html, setHTML] = useState(elementId ? "" : NEW_ELEMENT_HTML);
  const [runnersText, setRunnersText] = useState("");
  const [schemaText, setSchemaText] = useState("{}");
  const [previewSettings, setPreviewSettings] = useState("{}");
  const [previewHTML, setPreviewHTML] = useState(elementId ? "" : NEW_ELEMENT_HTML);
  const [upgrade, setUpgrade] = useState(false);
  const [notice, setNotice] = useState("");

  async function load(id: string) {
    const out = await call<{ element: CourseElement; versions: ElementVersion[] }>("course_elements_get", { id });
    setElement(out.element);
    setVersions(out.versions);
    setSlug(out.element.slug);
    setName(out.element.name);
    setDescription(out.element.description);
    setHTML(out.element.draft_source || "");
    setPreviewHTML(out.element.draft_source || "");
    setRunnersText(out.element.draft_runners.join(", "));
    setSchemaText(JSON.stringify(out.element.draft_settings_schema || {}, null, 2));
  }
  useEffect(() => {
    if (elementId) void act(() => load(elementId));
  }, [elementId]);

  const args = () => ({
    name,
    description,
    html,
    runners: runnersText.split(",").map((r) => r.trim()).filter(Boolean),
    settings_schema: parseJSONObject(schemaText, "Settings schema"),
  });

  const save = () =>
    act(async () => {
      setNotice("");
      const out = element
        ? await call<CourseElement>("course_elements_save", { id: element.id, ...args() })
        : await call<CourseElement>("course_elements_save", { community_id: communityId, slug, ...args() });
      await onSaved(out.id);
      await load(out.id);
      setNotice("Draft saved. Members still see the published version.");
    });

  const publish = () =>
    act(async () => {
      if (!element) return;
      await call("course_elements_save", { id: element.id, ...args() });
      const out = await call<{ version: number; created: boolean; upgraded_blocks: number }>("course_elements_publish", {
        id: element.id,
        upgrade_blocks: upgrade,
      });
      await onSaved(element.id);
      await load(element.id);
      setNotice(
        out.created
          ? `Published v${out.version}.${upgrade ? ` ${out.upgraded_blocks} lesson block(s) moved to it.` : " Lesson blocks stay on their version until you move them."}`
          : `No changes since v${out.version}.`,
      );
    });

  const archive = () =>
    act(async () => {
      if (!element) return;
      await call("course_elements_archive", { id: element.id, archived: !element.archived_at });
      await onSaved(element.id);
      await load(element.id);
    });

  let settingsObject: Record<string, unknown> = {};
  let settingsError = "";
  try {
    settingsObject = parseJSONObject(previewSettings, "Preview settings");
  } catch (err) {
    settingsError = errorText(err);
  }

  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "minmax(0, 1fr) minmax(0, 1fr)" }}>
      <div className="space-y-3 min-w-0">
        <div className="grid gap-3" style={{ gridTemplateColumns: "1fr 1fr" }}>
          <div>
            <Label>Name</Label>
            <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <Label>Slug</Label>
            <input className={inputClass} value={slug} disabled={Boolean(element)} onChange={(e) => setSlug(e.target.value)} placeholder="my-element" />
          </div>
        </div>
        <div>
          <Label>Description</Label>
          <input className={inputClass} value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <div>
          <Label>HTML (full document)</Label>
          <textarea className={inputClass} style={{ ...monoStyle, height: 300 }} spellCheck={false} value={html} onChange={(e) => setHTML(e.target.value)} />
          <div className="text-xs text-text-muted mt-1">{html.length.toLocaleString()} / 400,000 bytes</div>
        </div>
        <div className="grid gap-3" style={{ gridTemplateColumns: "1fr 1fr" }}>
          <div>
            <Label>Runners it may call</Label>
            <input className={inputClass} value={runnersText} onChange={(e) => setRunnersText(e.target.value)} placeholder="e.g. arduino, js" />
          </div>
          <div>
            <Label>Versions</Label>
            <div className="text-sm text-text-muted py-2">
              {versions.length ? versions.map((v) => `v${v.version}`).join(", ") : "None published"}
            </div>
          </div>
        </div>
        <div>
          <Label>Settings schema (JSON Schema, documents per-block settings)</Label>
          <textarea className={inputClass} style={{ ...monoStyle, height: 90 }} spellCheck={false} value={schemaText} onChange={(e) => setSchemaText(e.target.value)} />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button className={button()} disabled={busy || !name || (!element && !slug)} onClick={save}>
            Save draft
          </button>
          {element && (
            <>
              <button className={button("primary")} disabled={busy || !html.trim()} onClick={publish}>
                Publish
              </button>
              <label className="text-sm flex items-center gap-1">
                <input type="checkbox" checked={upgrade} onChange={(e) => setUpgrade(e.target.checked)} />
                Move lesson blocks to the new version
              </label>
              <button className={button("ghost")} disabled={busy} onClick={archive}>
                {element.archived_at ? "Restore" : "Archive"}
              </button>
            </>
          )}
        </div>
        {notice && <div className="text-sm text-text-muted">{notice}</div>}
      </div>
      <div className="space-y-3 min-w-0">
        <div>
          <Label>Preview settings (JSON passed to ready())</Label>
          <textarea className={inputClass} style={{ ...monoStyle, height: 90 }} spellCheck={false} value={previewSettings} onChange={(e) => setPreviewSettings(e.target.value)} />
          {settingsError && <div className="text-xs text-warn">{settingsError}</div>}
        </div>
        <div className="flex justify-end">
          <button className={button()} onClick={() => setPreviewHTML(html)}>
            Preview current HTML
          </button>
        </div>
        {previewHTML ? <ElementPreview html={previewHTML} settings={settingsObject} /> : <div className="text-sm text-text-muted">Nothing to preview.</div>}
      </div>
    </div>
  );
}

function RunnersEditor({ runners, busy, onSave }: { runners: Runner[]; busy: boolean; onSave: (next: Runner[]) => void }) {
  const [rows, setRows] = useState<Runner[]>(runners);
  useEffect(() => setRows(runners), [runners]);
  const update = (i: number, patch: Partial<Runner>) => setRows((current) => current.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="space-y-3">
      <p className="text-sm text-text-muted">
        A runner lets an element ask the platform to run something (compile a sketch, execute code). Elements must declare the
        runner name, and the call goes to the app tool bound here with <code>{"{runner, input, element, version}"}</code>. Members are
        limited to 20 runs per minute.
      </p>
      {rows.map((r, i) => (
        <div key={i} className="grid gap-2 items-center" style={{ gridTemplateColumns: "1fr 1fr 1fr auto" }}>
          <input className={inputClass} placeholder="name (e.g. arduino)" value={r.name} onChange={(e) => update(i, { name: e.target.value })} />
          <input className={inputClass} placeholder="app (e.g. pcb)" value={r.app} onChange={(e) => update(i, { app: e.target.value })} />
          <input className={inputClass} placeholder="tool" value={r.tool} onChange={(e) => update(i, { tool: e.target.value })} />
          <button className={button("ghost")} onClick={() => setRows((current) => current.filter((_, j) => j !== i))}>
            Remove
          </button>
        </div>
      ))}
      <div className="flex gap-2">
        <button className={button()} onClick={() => setRows((current) => [...current, { name: "", app: "", tool: "" }])}>
          Add runner
        </button>
        <button className={button("primary")} disabled={busy} onClick={() => onSave(rows)}>
          Save runners
        </button>
      </div>
    </div>
  );
}

// ─── Lesson block editor ─────────────────────────────────────────

interface EditableBlock extends LessonBlock {
  key: string;
  settingsText: string;
  stepsText: string;
}

function toEditable(block: LessonBlock, i: number): EditableBlock {
  return {
    ...block,
    key: block.id || `new-${i}-${Math.random().toString(36).slice(2)}`,
    settingsText: block.type === "markdown" ? "" : JSON.stringify(block.settings || {}, null, 2),
    stepsText: (block.required_steps || []).join(", "),
  };
}

export function LessonBlocksDialog({
  communityId,
  lessonId,
  lessonTitle,
  call,
  onClose,
}: {
  communityId: string;
  lessonId: string;
  lessonTitle: string;
  call: CallTool;
  onClose: () => void;
}) {
  const [layout, setLayout] = useState<"article" | "stage">("article");
  const [blocks, setBlocks] = useState<EditableBlock[]>([]);
  const [elements, setElements] = useState<CourseElement[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [previewKey, setPreviewKey] = useState("");

  async function load() {
    const [out, lib] = await Promise.all([
      call<{ layout: "article" | "stage"; blocks: LessonBlock[] }>("lesson_blocks_list", { lesson_id: lessonId }),
      call<{ elements: CourseElement[] }>("course_elements_list", { community_id: communityId }),
    ]);
    setLayout(out.layout);
    setBlocks(out.blocks.map(toEditable));
    setElements(lib.elements);
  }
  useEffect(() => {
    load().catch((err) => setError(errorText(err)));
  }, [lessonId]);

  const update = (key: string, patch: Partial<EditableBlock>) =>
    setBlocks((current) => current.map((b) => (b.key === key ? { ...b, ...patch } : b)));
  const move = (key: string, delta: number) =>
    setBlocks((current) => {
      const i = current.findIndex((b) => b.key === key);
      const j = i + delta;
      if (i < 0 || j < 0 || j >= current.length) return current;
      const next = current.slice();
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
  const add = (type: BlockType) =>
    setBlocks((current) => [
      ...current,
      toEditable(
        {
          type,
          slot: type === "element" ? (layout === "stage" ? "stage" : "main") : layout === "stage" ? "side" : "main",
          title: "",
          settings: type === "markdown" ? { body: "" } : {},
          required_steps: [],
          element_id: type === "element" ? elements[0]?.id : undefined,
        },
        current.length,
      ),
    ]);

  function payload() {
    return blocks.map((b, i) => {
      const out: Record<string, unknown> = { type: b.type, slot: b.slot, title: b.title };
      if (b.id) out.id = b.id;
      if (b.type === "markdown") out.settings = { body: String(b.settings?.body || "") };
      else if (b.type === "element") {
        out.settings = parseJSONObject(b.settingsText, `Block ${i + 1} settings`);
        out.element_id = b.element_id;
        if (b.element_version) out.element_version = b.element_version;
        out.required_steps = b.stepsText.split(",").map((s) => s.trim()).filter(Boolean);
      }
      if (b.height) out.height = b.height;
      return out;
    });
  }

  async function save() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await call("lesson_blocks_set", { lesson_id: lessonId, layout, blocks: payload() });
      await load();
      setNotice(blocks.length ? "Blocks saved." : "Blocks cleared; the lesson uses the classic view.");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  const preview = blocks.find((b) => b.key === previewKey);
  const previewElement = preview && elements.find((e) => e.id === preview.element_id);
  const [previewSource, setPreviewSource] = useState("");
  useEffect(() => {
    setPreviewSource("");
    if (!preview?.element_id) return;
    const version = preview.element_version || previewElement?.current_version;
    call<{ versions: { version: number; source?: string }[] }>("course_elements_get", { id: preview.element_id, version })
      .then((out) => setPreviewSource(out.versions.find((v) => v.version === version)?.source || ""))
      .catch((err) => setError(errorText(err)));
  }, [previewKey, preview?.element_id, preview?.element_version]);
  let previewSettings: Record<string, unknown> = {};
  try {
    previewSettings = preview ? parseJSONObject(preview.settingsText, "settings") : {};
  } catch {
    previewSettings = {};
  }

  return (
    <WideDialog title={`Lesson blocks · ${lessonTitle}`} onClose={onClose}>
      <div className="flex flex-wrap items-end gap-3 mb-3">
        <div>
          <Label>Layout</Label>
          <select className={inputClass} value={layout} onChange={(e) => setLayout(e.target.value as "article" | "stage")}>
            <option value="article">Article (one column, in order)</option>
            <option value="stage">Stage (large stage + side panel)</option>
          </select>
        </div>
        <div className="flex flex-wrap gap-1">
          {BLOCK_TYPES.map((t) => (
            <button key={t.value} className={button()} disabled={t.value === "element" && elements.length === 0} onClick={() => add(t.value)}>
              + {t.label}
            </button>
          ))}
        </div>
      </div>
      {elements.length === 0 && (
        <div className="text-sm text-text-muted mb-3">The element library is empty. Open Interactive elements to install the starters or write one.</div>
      )}
      {error && <div className="text-sm text-warn mb-3">{error}</div>}
      <div className="grid gap-4" style={{ gridTemplateColumns: preview ? "minmax(0, 1fr) minmax(0, 1fr)" : "minmax(0, 1fr)" }}>
        <ol className="space-y-2 min-w-0">
          {blocks.length === 0 && (
            <li className="p-3 text-sm text-text-muted border border-border rounded">
              No blocks: members see the classic lesson (video, body, resources, assignments, quizzes).
            </li>
          )}
          {blocks.map((b, i) => (
            <li key={b.key} className="border border-border rounded p-3 space-y-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-text-muted">{i + 1}</span>
                <select className={inputClass} style={{ width: "auto" }} value={b.type} onChange={(e) => update(b.key, { type: e.target.value as BlockType })}>
                  {BLOCK_TYPES.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </select>
                {layout === "stage" && (
                  <select className={inputClass} style={{ width: "auto" }} value={b.slot} onChange={(e) => update(b.key, { slot: e.target.value as Slot })}>
                    <option value="stage">Stage</option>
                    <option value="side">Side panel</option>
                    <option value="main">Below</option>
                  </select>
                )}
                <input className={inputClass} style={{ flex: 1, minWidth: 140 }} placeholder={b.type === "markdown" && layout === "stage" && b.slot === "side" ? "Tab title" : "Title (optional)"} value={b.title} onChange={(e) => update(b.key, { title: e.target.value })} />
                <button className={button("ghost")} disabled={i === 0} onClick={() => move(b.key, -1)} aria-label="Move up">Up</button>
                <button className={button("ghost")} disabled={i === blocks.length - 1} onClick={() => move(b.key, 1)} aria-label="Move down">Down</button>
                <button className={button("ghost")} onClick={() => setBlocks((current) => current.filter((x) => x.key !== b.key))}>Remove</button>
              </div>
              {b.type === "markdown" && (
                <textarea
                  className={inputClass}
                  style={{ height: 110 }}
                  placeholder="Markdown"
                  value={String(b.settings?.body || "")}
                  onChange={(e) => update(b.key, { settings: { body: e.target.value } })}
                />
              )}
              {b.type === "element" && (
                <>
                  <div className="flex flex-wrap items-center gap-2">
                    <select
                      className={inputClass}
                      style={{ width: "auto" }}
                      value={b.element_id || ""}
                      onChange={(e) => update(b.key, { element_id: e.target.value, element_version: undefined })}
                    >
                      {!elements.some((e) => e.id === b.element_id) && b.element_id && <option value={b.element_id}>{b.element_name || "Archived element"}</option>}
                      {elements.map((e) => (
                        <option key={e.id} value={e.id} disabled={!e.current_version}>
                          {e.name}
                          {e.current_version ? ` (v${e.current_version})` : " (unpublished)"}
                        </option>
                      ))}
                    </select>
                    <span className="text-xs text-text-muted">
                      {b.element_version ? `Pinned to v${b.element_version}` : "Uses the current version when saved"}
                    </span>
                    {b.element_version && elements.find((e) => e.id === b.element_id)?.current_version !== b.element_version && (
                      <button className={button("ghost")} onClick={() => update(b.key, { element_version: undefined })}>
                        Use latest
                      </button>
                    )}
                    <input
                      className={inputClass}
                      style={{ width: 110 }}
                      type="number"
                      min={120}
                      max={2000}
                      placeholder="Height px"
                      value={b.height || ""}
                      onChange={(e) => update(b.key, { height: e.target.value ? Number(e.target.value) : undefined })}
                    />
                    <button className={button()} onClick={() => setPreviewKey(previewKey === b.key ? "" : b.key)}>
                      {previewKey === b.key ? "Hide preview" : "Preview"}
                    </button>
                  </div>
                  <div>
                    <Label>Settings (JSON)</Label>
                    <textarea className={inputClass} style={{ ...monoStyle, height: 130 }} spellCheck={false} value={b.settingsText} onChange={(e) => update(b.key, { settingsText: e.target.value })} />
                  </div>
                  <div>
                    <Label>Required steps (comma separated; the lesson completes when all are reported)</Label>
                    <input className={inputClass} value={b.stepsText} onChange={(e) => update(b.key, { stepsText: e.target.value })} placeholder="e.g. led-blinked" />
                  </div>
                </>
              )}
            </li>
          ))}
        </ol>
        {preview && (
          <div className="min-w-0">
            {previewSource ? <ElementPreview html={previewSource} settings={previewSettings} height={preview.height || 520} /> : <div className="text-sm text-text-muted">Loading preview…</div>}
          </div>
        )}
      </div>
      <div className="flex items-center gap-3 mt-4">
        <button className={button("primary")} disabled={busy} onClick={() => void save()}>
          {busy ? "Saving…" : "Save blocks"}
        </button>
        {notice && <span className="text-sm text-text-muted">{notice}</span>}
      </div>
    </WideDialog>
  );
}

// ─── Course export / import ──────────────────────────────────────

export async function exportCourse(call: CallTool, spaceId: string, slug: string) {
  const pkg = await call<Record<string, unknown>>("course_export", { space_id: spaceId });
  const blob = new Blob([JSON.stringify(pkg, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${slug || "course"}.course.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  return pkg;
}

export function CourseImportDialog({
  communityId,
  call,
  onClose,
  onImported,
}: {
  communityId: string;
  call: CallTool;
  onClose: () => void;
  onImported: (spaceId: string) => void;
}) {
  const [text, setText] = useState("");
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ space_id: string; lessons: number; elements: number; notes?: string[] } | null>(null);
  let parsed: Record<string, any> | null = null;
  try {
    parsed = text.trim() ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  async function load(file?: File) {
    if (!file) return;
    const content = await file.text();
    setText(content);
    try {
      const pkg = JSON.parse(content);
      setSlug(`${pkg.course?.slug || "course"}-copy`);
      setName(pkg.course?.name || "");
    } catch {
      setError("That file is not JSON.");
    }
  }
  async function submit() {
    setBusy(true);
    setError("");
    try {
      const out = await call<{ space_id: string; lessons: number; elements: number; notes?: string[] }>("course_import", {
        community_id: communityId,
        package: parseJSONObject(text, "Package"),
        slug: slug || undefined,
        name: name || undefined,
      });
      setResult(out);
      onImported(out.space_id);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <WideDialog title="Import course" onClose={onClose}>
      <div className="space-y-3" style={{ maxWidth: 720 }}>
        <p className="text-sm text-text-muted">
          Import a course exported from a Community app. Elements it uses are added to this community's library (matched by slug
          and checksum). Videos and resource files are Storage files and are not included.
        </p>
        <input type="file" accept="application/json,.json" onChange={(e) => void load(e.target.files?.[0])} />
        <textarea className={inputClass} style={{ ...monoStyle, height: 160 }} placeholder="…or paste the JSON package" value={text} onChange={(e) => setText(e.target.value)} />
        {parsed && (
          <div className="text-sm text-text-muted">
            {String(parsed.course?.name || "Untitled")} · {(parsed.sections || []).length} sections ·{" "}
            {(parsed.sections || []).reduce((n: number, s: any) => n + (s.lessons || []).length, 0)} lessons · {(parsed.elements || []).length} elements
          </div>
        )}
        <div className="grid gap-3" style={{ gridTemplateColumns: "1fr 1fr" }}>
          <div>
            <Label>New course slug</Label>
            <input className={inputClass} value={slug} onChange={(e) => setSlug(e.target.value)} />
          </div>
          <div>
            <Label>New course name</Label>
            <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
        </div>
        {error && <div className="text-sm text-warn">{error}</div>}
        {result && (
          <div className="text-sm">
            Imported {result.lessons} lessons and {result.elements} elements.
            {(result.notes || []).map((n) => (
              <div key={n} className="text-text-muted">
                {n}
              </div>
            ))}
          </div>
        )}
        <button className={button("primary")} disabled={busy || !parsed} onClick={() => void submit()}>
          {busy ? "Importing…" : "Import"}
        </button>
      </div>
    </WideDialog>
  );
}
