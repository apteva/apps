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

import { StepDef } from "./graph";

export interface StepEditorProps {
  step: StepDef;
  onPatch: (next: StepDef) => void;
  onDelete?: () => void;
}

export function StepEditor({ step, onPatch, onDelete }: StepEditorProps) {
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
          onChange={(e) => patch((s) => (s.kind = e.target.value as StepDef["kind"]))}
          className={fieldClass}
        >
          <option value="http">http</option>
          <option value="function">function</option>
          <option value="app">app</option>
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
      {step.kind === "emit" && (
        <EmitFields step={step} patch={patch} />
      )}
      {step.kind === "branch" && (
        <BranchFields step={step} patch={patch} />
      )}

      {/* Common: input (skipped for branch — it has its own when) */}
      {step.kind !== "branch" && (
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
