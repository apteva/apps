// Side-panel form for editing one step's config. Renders the
// fields that matter for the selected step's kind plus the
// universal common fields (input, retry, on_error). Edits mutate
// the WorkflowDef in-memory; the parent panel handles persistence
// (debounced YAML serialization + PATCH).
//
// Why no controlled inputs that round-trip through React state on
// every keystroke: we want each keystroke to update the parsed
// WorkflowDef so the graph + YAML preview stay in sync. The
// callback shape mirrors what useReducer would give us, just
// flattened so each form field can call onPatch directly.

import { useEffect, useState } from "react";

import { StepDef } from "./graph";

// Per-kind list of fields the StepDef carries on top of the common
// (id, kind, input, on_error, retry). Used by stripKindFields to
// clean the slate when the user switches a step's kind so stale
// values from the previous kind don't survive into the YAML.
const KIND_FIELDS: Record<StepDef["kind"], (keyof StepDef)[]> = {
  http: ["url", "app", "path", "method"],
  function: ["name"],
  app: ["app", "tool"],
  integration: ["connection_id", "tool"],
  emit: ["topic", "data"],
  branch: ["when", "else"],
};

// Mutates `s` in place: sets the new kind and deletes any field that
// belonged exclusively to the previous kind. Fields shared between
// kinds (e.g. `tool` for both `app` and `integration`, `app` for
// both `http` and `app`) are preserved when the next kind still uses
// them. `branch` also drops `input` since it uses `when` instead.
function stripKindFields(s: StepDef, next: StepDef["kind"]): void {
  const prev = s.kind;
  s.kind = next;
  if (prev === next) return;
  const prevSet = new Set(KIND_FIELDS[prev]);
  const nextSet = new Set(KIND_FIELDS[next]);
  for (const f of prevSet) {
    if (!nextSet.has(f)) delete (s as Record<string, unknown>)[f as string];
  }
  // Branch is the one kind that doesn't accept `input` — its
  // expression goes in `when`. Strip it so the generic Input field's
  // absence isn't confusing on a saved branch step.
  if (next === "branch") {
    delete (s as Record<string, unknown>).input;
  }
}

export interface StepEditorProps {
  step: StepDef;
  projectId: string;
  onPatch: (next: StepDef) => void;
  onDelete?: () => void;
}

// ─── Project-scoped connection catalog (lives on the dashboard
// host, not the workflows sidecar). Fetched lazily the first time
// an integration step is selected.
interface Connection {
  id: number;
  app_slug: string;
  app_name?: string;
  name?: string;
  status?: string;
}

interface ConnectionTool {
  name: string;
  description?: string;
  input_schema?: JSONSchema;
}

// JSON-Schema fragment we render against. Intentionally narrow:
// only the parts the connection catalog actually surfaces (type,
// description, default, enum, items.type, properties, required).
// Anything we don't understand falls back to the JSON textarea.
interface JSONSchema {
  type?: string | string[];
  description?: string;
  default?: unknown;
  enum?: unknown[];
  required?: string[];
  properties?: Record<string, JSONSchema>;
  items?: JSONSchema;
}

export function StepEditor({ step, projectId, onPatch, onDelete }: StepEditorProps) {
  const patch = (mutate: (s: StepDef) => void) => {
    // Clone shallowly + apply mutator. Avoids accidental shared
    // refs between parent and child state — the parent does the
    // same kind of clone on its way back to YAML.
    const next: StepDef = {
      ...step,
      retry: step.retry ? { ...step.retry } : undefined,
      on_error: step.on_error ? { ...step.on_error } : undefined,
      else: step.else ? { ...step.else } : undefined,
    };
    mutate(next);
    onPatch(next);
  };

  return (
    <div className="flex flex-col h-full overflow-auto">
      {/* Step header */}
      <div className="px-4 py-3 border-b border-border flex items-center gap-2">
        <KindBadge kind={step.kind} />
        <input
          type="text"
          value={step.id}
          onChange={(e) => patch((s) => (s.id = slugify(e.target.value)))}
          className="flex-1 bg-transparent text-text font-mono text-sm focus:outline-none"
          aria-label="Step id"
        />
        {onDelete && (
          <button
            type="button"
            onClick={onDelete}
            className="text-text-muted hover:text-red text-xs px-2 py-1 border border-border rounded"
          >
            Delete
          </button>
        )}
      </div>

      {/* Kind selector */}
      <Field label="Kind">
        <select
          value={step.kind}
          onChange={(e) => {
            const next = e.target.value as StepDef["kind"];
            // Strip the previous kind's kind-specific fields so they
            // don't linger in the YAML when the user changes their
            // mind. addStep defaults to emit (with topic: "todo")
            // which would otherwise stick around after switching to
            // integration, app, http, etc.
            patch((s) => stripKindFields(s, next));
          }}
          className={fieldClass}
        >
          <option value="http">http</option>
          <option value="function">function</option>
          <option value="app">app</option>
          <option value="integration">integration</option>
          <option value="emit">emit</option>
          <option value="branch">branch</option>
        </select>
      </Field>

      {/* Kind-specific fields */}
      {step.kind === "http" && (
        <HTTPFields step={step} patch={patch} />
      )}
      {step.kind === "function" && (
        <FunctionFields step={step} patch={patch} />
      )}
      {step.kind === "app" && (
        <AppFields step={step} patch={patch} />
      )}
      {step.kind === "integration" && (
        <IntegrationFields step={step} patch={patch} projectId={projectId} />
      )}
      {step.kind === "emit" && (
        <EmitFields step={step} patch={patch} />
      )}
      {step.kind === "branch" && (
        <BranchFields step={step} patch={patch} />
      )}

      {/* Common: input (skipped for branch — it has its own when —
          and for integration when a schema-driven form is already
          rendered inline below the tool picker). */}
      {step.kind !== "branch" && step.kind !== "integration" && (
        <Field
          label="Input"
          hint={`Available in steps as {{ steps.${step.id}.* }} after this step runs.`}
        >
          <JSONField
            value={step.input}
            onChange={(v) => patch((s) => (s.input = v))}
          />
        </Field>
      )}

      {/* Common: retry */}
      <RetryFields step={step} patch={patch} />

      {/* Common: on_error */}
      <OnErrorFields step={step} patch={patch} />
    </div>
  );
}

// ─── Kind-specific fragments ───────────────────────────────────────

function HTTPFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  return (
    <>
      <Field label="URL (absolute)" hint="Or use {app, path} below for sibling-app calls.">
        <input
          type="text"
          value={step.url || ""}
          onChange={(e) => patch((s) => (s.url = e.target.value || undefined))}
          placeholder="https://api.example.com/endpoint"
          className={fieldClass}
        />
      </Field>
      <div className="px-4 grid grid-cols-2 gap-2">
        <Field label="App" hint="Sibling-app slug, e.g. crm">
          <input
            type="text"
            value={step.app || ""}
            onChange={(e) => patch((s) => (s.app = e.target.value || undefined))}
            disabled={!!step.url}
            className={fieldClass}
          />
        </Field>
        <Field label="Path" hint="Path on the target app, e.g. /webhooks/foo">
          <input
            type="text"
            value={step.path || ""}
            onChange={(e) => patch((s) => (s.path = e.target.value || undefined))}
            disabled={!!step.url}
            className={fieldClass}
          />
        </Field>
      </div>
      <Field label="Method">
        <select
          value={step.method || ""}
          onChange={(e) => patch((s) => (s.method = e.target.value || undefined))}
          className={fieldClass}
        >
          <option value="">auto (POST when input set, GET otherwise)</option>
          <option value="GET">GET</option>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="PATCH">PATCH</option>
          <option value="DELETE">DELETE</option>
        </select>
      </Field>
    </>
  );
}

function FunctionFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  return (
    <Field label="Function name" hint="A function created via functions_create.">
      <input
        type="text"
        value={step.name || ""}
        onChange={(e) => patch((s) => (s.name = e.target.value || undefined))}
        placeholder="send-receipt"
        className={fieldClass}
      />
    </Field>
  );
}

function AppFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  return (
    <div className="px-4 grid grid-cols-2 gap-2">
      <Field label="App" hint="App slug, e.g. crm, storage, code.">
        <input
          type="text"
          value={step.app || ""}
          onChange={(e) => patch((s) => (s.app = e.target.value || undefined))}
          placeholder="crm"
          className={fieldClass}
        />
      </Field>
      <Field label="Tool" hint="MCP tool name, e.g. contacts_find.">
        <input
          type="text"
          value={step.tool || ""}
          onChange={(e) => patch((s) => (s.tool = e.target.value || undefined))}
          placeholder="contacts_find"
          className={fieldClass}
        />
      </Field>
    </div>
  );
}

function IntegrationFields({
  step,
  patch,
  projectId,
}: {
  step: StepDef;
  patch: (mutator: (s: StepDef) => void) => void;
  projectId: string;
}) {
  const [connections, setConnections] = useState<Connection[] | null>(null);
  const [connectionsErr, setConnectionsErr] = useState<string>("");
  const [tools, setTools] = useState<ConnectionTool[] | null>(null);
  const [toolsErr, setToolsErr] = useState<string>("");
  const [loadingTools, setLoadingTools] = useState(false);

  // Load this project's connections. Same-origin cookies authenticate
  // against apteva-server's /api/connections.
  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch(
          `/api/connections?project_id=${encodeURIComponent(projectId)}`,
          { credentials: "same-origin" },
        );
        if (!res.ok) throw new Error(`${res.status}`);
        const data = await res.json();
        if (cancelled) return;
        const list: Connection[] = Array.isArray(data) ? data : data.connections || [];
        setConnections(list);
      } catch (e) {
        if (!cancelled) setConnectionsErr((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  // Load tools for the selected connection. Re-fetches when the
  // user switches connections.
  useEffect(() => {
    if (!step.connection_id || !projectId) {
      setTools(null);
      return;
    }
    let cancelled = false;
    setLoadingTools(true);
    setToolsErr("");
    (async () => {
      try {
        const res = await fetch(
          `/api/connections/${step.connection_id}/tools?project_id=${encodeURIComponent(projectId)}`,
          { credentials: "same-origin" },
        );
        if (!res.ok) throw new Error(`${res.status}`);
        const data = await res.json();
        if (cancelled) return;
        const list: ConnectionTool[] = Array.isArray(data) ? data : data.tools || [];
        setTools(list);
      } catch (e) {
        if (!cancelled) setToolsErr((e as Error).message);
      } finally {
        if (!cancelled) setLoadingTools(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [step.connection_id, projectId]);

  const selectedTool = tools?.find((t) => t.name === step.tool);

  return (
    <>
      <Field
        label="Connection"
        hint={
          connectionsErr
            ? `Could not load connections: ${connectionsErr}`
            : connections === null
            ? "Loading…"
            : connections.length === 0
            ? "No connections in this project. Add one from the Connections panel."
            : "Pick a connection from this project."
        }
      >
        <select
          value={step.connection_id ?? ""}
          onChange={(e) => {
            const n = Number(e.target.value);
            patch((s) => {
              s.connection_id = Number.isFinite(n) && n > 0 ? n : undefined;
              // Switching connection invalidates the tool choice.
              s.tool = undefined;
            });
          }}
          className={fieldClass}
          disabled={!connections || connections.length === 0}
        >
          <option value="">
            {connections === null ? "Loading…" : "— pick a connection —"}
          </option>
          {connections?.map((c) => (
            <option key={c.id} value={c.id}>
              {connectionLabel(c)}
            </option>
          ))}
        </select>
      </Field>

      <Field
        label="Tool"
        hint={
          toolsErr
            ? `Could not load tools: ${toolsErr}`
            : !step.connection_id
            ? "Pick a connection first."
            : loadingTools
            ? "Loading tools…"
            : tools && tools.length === 0
            ? "This connection exposes no tools."
            : selectedTool?.description || "Pick a tool to call."
        }
      >
        <select
          value={step.tool || ""}
          onChange={(e) => {
            const next = e.target.value || undefined;
            const prev = step.tool;
            patch((s) => {
              s.tool = next;
              // Clear input when switching tools so stale props from
              // the previous tool's schema don't linger in the YAML.
              if (next !== prev) s.input = undefined;
            });
          }}
          className={fieldClass + " font-mono"}
          disabled={!tools || tools.length === 0}
        >
          <option value="">— pick a tool —</option>
          {tools?.map((t) => (
            <option key={t.name} value={t.name}>
              {t.name}
            </option>
          ))}
        </select>
      </Field>

      <SchemaInputForm
        stepID={step.id}
        schema={selectedTool?.input_schema}
        value={asRecord(step.input)}
        onChange={(next) => patch((s) => (s.input = next))}
      />
    </>
  );
}

function connectionLabel(c: Connection): string {
  const name = c.name || c.app_name || c.app_slug;
  return `${name} (id ${c.id})`;
}

function asRecord(v: unknown): Record<string, unknown> {
  if (v && typeof v === "object" && !Array.isArray(v)) {
    return v as Record<string, unknown>;
  }
  return {};
}

// ─── Schema-driven input form ──────────────────────────────────────
//
// Renders one field per property of the tool's input_schema. Falls
// back to the raw JSON textarea when:
//   - the tool didn't expose a schema, or
//   - the user toggles "Raw JSON" (for shapes the per-property form
//     can't express cleanly: arrays of objects, anyOf, etc.).
//
// Template strings ({{ input.x }}, {{ steps.foo.bar }}) are accepted
// in every field — the workflow runner's template engine resolves
// them at run time. Number/integer inputs that look like a template
// are stored as strings so the YAML round-trip preserves the
// expression. Plain numbers parse to JSON numbers as expected.

function SchemaInputForm({
  stepID,
  schema,
  value,
  onChange,
}: {
  stepID: string;
  schema?: JSONSchema;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown> | undefined) => void;
}) {
  const [rawMode, setRawMode] = useState(false);
  const props = schema?.properties;
  const hasSchemaForm = !!props && Object.keys(props).length > 0;
  const required = new Set(schema?.required || []);

  // No schema → just the JSON textarea, same hint as the
  // generic Input field.
  if (!hasSchemaForm) {
    return (
      <Field
        label="Input"
        hint={`Available downstream as {{ steps.${stepID}.* }}.`}
      >
        <JSONField
          value={value && Object.keys(value).length ? value : undefined}
          onChange={(v) => onChange(v as Record<string, unknown> | undefined)}
        />
      </Field>
    );
  }

  // Schema mode: per-property form. The raw-JSON toggle is for
  // power users who hit a shape the form can't express.
  if (rawMode) {
    return (
      <>
        <div className="px-4 mt-2 flex items-center justify-between">
          <label className="text-xs uppercase tracking-wide text-text-dim">Input</label>
          <button
            type="button"
            onClick={() => setRawMode(false)}
            className="text-accent text-[10px] hover:underline"
          >
            back to form
          </button>
        </div>
        <div className="px-4 pb-2">
          <JSONField
            value={value && Object.keys(value).length ? value : undefined}
            onChange={(v) => onChange(v as Record<string, unknown> | undefined)}
          />
          <p className="text-text-dim text-xs mt-1">
            Available downstream as <code>{`{{ steps.${stepID}.* }}`}</code>.
          </p>
        </div>
      </>
    );
  }

  const setProp = (key: string, next: unknown) => {
    const copy: Record<string, unknown> = { ...value };
    if (next === undefined) {
      delete copy[key];
    } else {
      copy[key] = next;
    }
    onChange(Object.keys(copy).length ? copy : undefined);
  };

  return (
    <>
      <div className="px-4 mt-2 flex items-center justify-between">
        <label className="text-xs uppercase tracking-wide text-text-dim">Input</label>
        <button
          type="button"
          onClick={() => setRawMode(true)}
          className="text-text-dim text-[10px] hover:text-text"
        >
          raw JSON
        </button>
      </div>

      {Object.entries(props!).map(([key, propSchema]) => (
        <SchemaPropField
          key={key}
          name={key}
          schema={propSchema}
          required={required.has(key)}
          value={value[key]}
          onChange={(v) => setProp(key, v)}
        />
      ))}

      <div className="px-4 pb-2">
        <p className="text-text-dim text-xs">
          Available downstream as <code>{`{{ steps.${stepID}.* }}`}</code>.
          Use <code>{`{{ input.x }}`}</code> or <code>{`{{ steps.foo.bar }}`}</code> in any field.
        </p>
      </div>
    </>
  );
}

function SchemaPropField({
  name,
  schema,
  required,
  value,
  onChange,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const type = primaryType(schema);
  const hint = describeSchema(schema);
  const labelText = required ? `${name} *` : name;

  // Enum → select
  if (schema.enum && schema.enum.length > 0) {
    const cur = value === undefined ? "" : String(value);
    return (
      <Field label={labelText} hint={hint}>
        <select
          value={cur}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "") return onChange(undefined);
            const match = schema.enum!.find((x) => String(x) === v);
            onChange(match !== undefined ? match : v);
          }}
          className={fieldClass}
        >
          <option value="">{required ? "— required —" : "— unset —"}</option>
          {schema.enum.map((opt, i) => (
            <option key={i} value={String(opt)}>
              {String(opt)}
            </option>
          ))}
        </select>
      </Field>
    );
  }

  // Boolean → tri-state select (unset / true / false). A checkbox
  // can't represent "unset" vs "false", which matters for optional
  // fields where the upstream tool's default differs from false.
  if (type === "boolean") {
    const cur = value === undefined ? "" : value === true ? "true" : value === false ? "false" : "";
    return (
      <Field label={labelText} hint={hint}>
        <select
          value={cur}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "") onChange(undefined);
            else if (v === "true") onChange(true);
            else if (v === "false") onChange(false);
          }}
          className={fieldClass}
        >
          <option value="">— unset —</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      </Field>
    );
  }

  // Object/array → JSON textarea (the per-property form can't
  // recurse usefully into arbitrary nested shapes).
  if (type === "object" || type === "array") {
    return (
      <Field label={labelText} hint={hint}>
        <JSONField value={value} onChange={onChange} />
      </Field>
    );
  }

  // String / number / integer → text input that also accepts
  // {{ template }} expressions.
  const textValue =
    value === undefined ? "" : typeof value === "object" ? JSON.stringify(value) : String(value);
  const isMultiline = type === "string" && /(description|html|body|message|text|content)/i.test(name);
  const placeholder =
    schema.default !== undefined ? `default: ${String(schema.default)}` : `{{ input.${name} }}`;

  const commit = (raw: string) => {
    const trimmed = raw.trim();
    if (trimmed === "") return onChange(undefined);
    if (looksLikeTemplate(trimmed) || type === "string") return onChange(raw);
    const n = Number(trimmed);
    if (Number.isFinite(n)) return onChange(type === "integer" ? Math.trunc(n) : n);
    onChange(raw);
  };

  if (isMultiline) {
    return (
      <Field label={labelText} hint={hint}>
        <textarea
          value={textValue}
          onChange={(e) => commit(e.target.value)}
          placeholder={placeholder}
          className={fieldClass + " font-mono"}
          style={{ minHeight: 72 }}
          spellCheck={false}
        />
      </Field>
    );
  }
  return (
    <Field label={labelText} hint={hint}>
      <input
        type="text"
        value={textValue}
        onChange={(e) => commit(e.target.value)}
        placeholder={placeholder}
        className={fieldClass + (type === "string" ? "" : " font-mono")}
        spellCheck={false}
      />
    </Field>
  );
}

function primaryType(s: JSONSchema): string {
  if (Array.isArray(s.type)) {
    return s.type.find((t) => t !== "null") || "string";
  }
  return s.type || "string";
}

function describeSchema(s: JSONSchema): string {
  const parts: string[] = [];
  if (s.description) parts.push(s.description);
  if (s.default !== undefined) parts.push(`Default: ${JSON.stringify(s.default)}`);
  return parts.join(" · ");
}

function looksLikeTemplate(s: string): boolean {
  return s.includes("{{") && s.includes("}}");
}

function EmitFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  return (
    <>
      <Field label="Topic" hint="Subscribers in the project's lane will receive this event.">
        <input
          type="text"
          value={step.topic || ""}
          onChange={(e) => patch((s) => (s.topic = e.target.value || undefined))}
          placeholder="invoice.processed"
          className={fieldClass}
        />
      </Field>
      <Field label="Data" hint="Falls back to the step's input when omitted.">
        <JSONField
          value={step.data}
          onChange={(v) => patch((s) => (s.data = v))}
        />
      </Field>
    </>
  );
}

function BranchFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  const goto = step.else;
  const elseKind: "goto" | "end" | "fail" =
    goto?.fail ? "fail" : goto?.end ? "end" : "goto";
  return (
    <>
      <Field label="When" hint={`Examples: "input.x > 0", "steps.lookup.found == true", "input.kind != 'invoice'".`}>
        <input
          type="text"
          value={step.when || ""}
          onChange={(e) => patch((s) => (s.when = e.target.value || undefined))}
          placeholder='input.amount > 0'
          className={fieldClass + " font-mono"}
        />
      </Field>
      <div className="px-4 mb-2 text-xs uppercase tracking-wide text-text-dim">Else (when false)</div>
      <div className="px-4 mb-3 flex gap-1">
        {(["goto", "end", "fail"] as const).map((k) => (
          <button
            key={k}
            type="button"
            onClick={() =>
              patch((s) => {
                if (k === "goto") s.else = { goto: s.else?.goto || "" };
                else if (k === "end") s.else = { end: true };
                else s.else = { fail: true, message: s.else?.message };
              })
            }
            className={
              "flex-1 px-2 py-1 text-xs border rounded " +
              (elseKind === k
                ? "border-accent text-accent bg-accent/10"
                : "border-border text-text-muted hover:bg-bg-input")
            }
          >
            {k}
          </button>
        ))}
      </div>
      {elseKind === "goto" && (
        <Field label="Goto step id">
          <input
            type="text"
            value={goto?.goto || ""}
            onChange={(e) => patch((s) => { if (s.else) s.else.goto = e.target.value; })}
            placeholder="step_id"
            className={fieldClass + " font-mono"}
          />
        </Field>
      )}
      {elseKind === "fail" && (
        <Field label="Fail message" hint="Recorded on the run row's error field.">
          <input
            type="text"
            value={goto?.message || ""}
            onChange={(e) => patch((s) => { if (s.else) s.else.message = e.target.value; })}
            className={fieldClass}
          />
        </Field>
      )}
    </>
  );
}

// ─── Common fragments ──────────────────────────────────────────────

function RetryFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  const retry = step.retry || {};
  const has = retry.max != null && retry.max > 0;
  return (
    <div className="px-4 mt-4">
      <label className="flex items-center gap-2 text-xs uppercase tracking-wide text-text-dim mb-2">
        <input
          type="checkbox"
          checked={has}
          onChange={(e) =>
            patch((s) => {
              if (e.target.checked) s.retry = { max: 3, backoff_seconds: 30 };
              else s.retry = undefined;
            })
          }
        />
        Retry on failure
      </label>
      {has && (
        <div className="grid grid-cols-2 gap-2">
          <NumberField
            label="Max attempts"
            value={retry.max ?? 3}
            min={1}
            max={10}
            onChange={(v) => patch((s) => { if (s.retry) s.retry.max = v; })}
          />
          <NumberField
            label="Backoff (s)"
            value={retry.backoff_seconds ?? 30}
            min={0}
            max={3600}
            onChange={(v) => patch((s) => { if (s.retry) s.retry.backoff_seconds = v; })}
          />
        </div>
      )}
    </div>
  );
}

function OnErrorFields({ step, patch }: { step: StepDef; patch: (mutator: (s: StepDef) => void) => void }) {
  const goto = step.on_error;
  const kind: "none" | "goto" | "end" | "fail" =
    !goto ? "none" : goto.fail ? "fail" : goto.end ? "end" : "goto";
  return (
    <div className="px-4 mt-4 mb-6">
      <div className="text-xs uppercase tracking-wide text-text-dim mb-2">On error</div>
      <div className="flex gap-1 mb-2">
        {(["none", "goto", "end", "fail"] as const).map((k) => (
          <button
            key={k}
            type="button"
            onClick={() =>
              patch((s) => {
                if (k === "none") s.on_error = undefined;
                else if (k === "goto") s.on_error = { goto: s.on_error?.goto || "" };
                else if (k === "end") s.on_error = { end: true };
                else s.on_error = { fail: true, message: s.on_error?.message };
              })
            }
            className={
              "flex-1 px-2 py-1 text-xs border rounded " +
              (kind === k
                ? "border-accent text-accent bg-accent/10"
                : "border-border text-text-muted hover:bg-bg-input")
            }
          >
            {k}
          </button>
        ))}
      </div>
      {kind === "goto" && (
        <input
          type="text"
          value={goto?.goto || ""}
          onChange={(e) => patch((s) => { if (s.on_error) s.on_error.goto = e.target.value; })}
          placeholder="step_id"
          className={fieldClass + " font-mono"}
        />
      )}
      {kind === "fail" && (
        <input
          type="text"
          value={goto?.message || ""}
          onChange={(e) => patch((s) => { if (s.on_error) s.on_error.message = e.target.value; })}
          placeholder="Reason recorded on run.error"
          className={fieldClass}
        />
      )}
    </div>
  );
}

// ─── Atoms ─────────────────────────────────────────────────────────

const fieldClass =
  "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text focus:outline-none focus:border-accent";

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="px-4 py-2">
      <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
        {label}
      </label>
      {children}
      {hint && <p className="text-text-dim text-xs mt-1">{hint}</p>}
    </div>
  );
}

function NumberField({
  label,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (v: number) => void;
}) {
  return (
    <div>
      <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
        {label}
      </label>
      <input
        type="number"
        value={value}
        min={min}
        max={max}
        onChange={(e) => {
          const n = Number(e.target.value);
          if (Number.isFinite(n)) onChange(n);
        }}
        className={fieldClass}
      />
    </div>
  );
}

// JSONField is a textarea that accepts JSON and surfaces parse
// errors inline. We don't try to validate against the workflow's
// templating expressions — that's runtime concern. Empty input
// becomes undefined; "null" becomes null; valid JSON becomes the
// parsed value.
function JSONField({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const text = value === undefined ? "" : JSON.stringify(value, null, 2);
  return (
    <textarea
      key={text} // remount on external change so user input doesn't fight derived value
      defaultValue={text}
      onBlur={(e) => {
        const t = e.target.value.trim();
        if (t === "") return onChange(undefined);
        try {
          onChange(JSON.parse(t));
        } catch {
          // Keep last valid value; do not propagate broken JSON.
          // Surfacing the error inline is a nice-to-have for v0.2.
        }
      }}
      className={fieldClass + " font-mono min-h-[80px]"}
      placeholder='e.g. { "hello": "world" }'
    />
  );
}

function KindBadge({ kind }: { kind: string }) {
  const colors: Record<string, string> = {
    http: "bg-green/15 text-green",
    function: "bg-purple/15 text-purple",
    app: "bg-blue/15 text-blue",
    integration: "bg-accent/15 text-accent",
    emit: "bg-yellow/15 text-yellow",
    branch: "bg-pink/15 text-pink",
  };
  return (
    <span
      className={`text-[10px] px-1.5 py-0.5 rounded uppercase font-mono ${
        colors[kind] || "bg-border text-text-muted"
      }`}
    >
      {kind}
    </span>
  );
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9_-]/g, "-")
    .replace(/-+/g, "-")
    .slice(0, 63);
}
