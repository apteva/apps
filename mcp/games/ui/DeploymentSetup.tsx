import { useEffect, useState } from "react";
import type { ReactNode } from "react";
type Item = Record<string, any>;
type Props = {
  call: (action: string, data?: Item) => Promise<any>;
  run: (f: () => Promise<void>) => void;
  busy: boolean;
  reload: () => Promise<void>;
  targets: Item[];
};
const input =
  "bg-bg-input border border-border rounded px-2 py-1 text-sm w-full";
const button =
  "border border-border rounded px-3 py-1 text-xs disabled:opacity-50 hover:bg-bg-input";
const card = "border border-border rounded p-3 space-y-3";
function Field({
  label,
  value,
  change,
}: {
  label: string;
  value: string;
  change: (s: string) => void;
}) {
  return (
    <label className="block text-xs space-y-1">
      <span>{label}</span>
      <input
        className={input}
        value={value}
        onChange={(e) => change(e.target.value)}
      />
    </label>
  );
}
function Select({
  label,
  value,
  change,
  children,
}: {
  label: string;
  value: string;
  change: (s: string) => void;
  children: ReactNode;
}) {
  return (
    <label className="block text-xs space-y-1">
      <span>{label}</span>
      <select
        className={input}
        value={value}
        onChange={(e) => change(e.target.value)}
      >
        {children}
      </select>
    </label>
  );
}
function Lines({
  label,
  value,
  change,
}: {
  label: string;
  value: string;
  change: (s: string) => void;
}) {
  return (
    <label className="block text-xs space-y-1">
      <span>{label}</span>
      <textarea
        className={input}
        rows={3}
        value={value}
        onChange={(e) => change(e.target.value)}
      />
    </label>
  );
}
const stageText = (st: Item[] = []) =>
  st
    .map(
      (x) =>
        `${x.name} | ${x.command?.length === 3 && x.command[0] === "sh" ? x.command[2] : x.command?.map((a: string) => `'${a.replace(/'/g, `'\\''`)}'`).join(" ")}`,
    )
    .join("\n");
const stages = (text: string, previous: Item[] = []) =>
  text
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      const i = s.indexOf("|");
      if (i < 1 || !s.slice(i + 1).trim())
        throw Error("Each stage needs name | command");
      return {
        ...previous.find((stage) => stage.name === s.slice(0, i).trim()),
        name: s.slice(0, i).trim(),
        command: ["sh", "-ec", s.slice(i + 1).trim()],
      };
    });
export function DeploymentSetup({ call, run, busy, reload, targets }: Props) {
  const [options, setOptions] = useState<Item>({});
  const [history, setHistory] = useState<Item[]>([]);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<Item>({
    repo_slug: "",
    repo_name: "",
    create_repo: false,
    deployment_id: 0,
    deployment_name: "",
    environment: "production",
    platform: "ios",
    recipe_id: "kiln-ios",
    recipe_version: "1",
    runner_backend: "runner",
    runner_url: "",
    runner_deployment_id: 0,
    runner_environment: "production",
    version_name: "1.0.0",
    bundle_id: "",
    scheme: "",
    dependencies: [],
  });
  const [review, setReview] = useState<Item>();
  const [receipt, setReceipt] = useState<Item>();
  const [verified, setVerified] = useState("");
  const [reason, setReason] = useState("");
  const [depRepo, setDepRepo] = useState("");
  const [depPath, setDepPath] = useState("engine");
  const [pin, setPin] = useState<Item>();
  const [editor, setEditor] = useState<Item>();
  const [configTarget, setConfigTarget] = useState("");
  const [configOpen, setConfigOpen] = useState(false);
  const change = (k: string, v: any) => setForm((f) => ({ ...f, [k]: v }));
  const field = (label: string, k: string) => (
    <Field label={label} value={form[k] || ""} change={(v) => change(k, v)} />
  );
  const recipes: Item[] = options.recipes || [];
  const recipe = recipes.find(
    (r) => r.id === form.recipe_id && r.version === form.recipe_version,
  );
  const load = async () => {
    const [o, h] = await Promise.all([
      call("setup_options"),
      call("setup_history"),
    ]);
    setOptions(o || {});
    setHistory(Array.isArray(h) ? h : []);
    setError("");
  };
  useEffect(() => {
    let alive = true;
    Promise.all([call("setup_options"), call("setup_history")])
      .then(([o, h]) => {
        if (alive) {
          setOptions(o || {});
          setHistory(Array.isArray(h) ? h : []);
        }
      })
      .catch((e) => {
        if (alive) setError(e.message);
      });
    return () => {
      alive = false;
    };
  }, [call]);
  const submit = () =>
    run(async () => {
      if (!review) return;
      setReceipt(
        await call("setup", {
          request_key: review.request_key,
          setup: review.input,
        }),
      );
      await reload();
      await load();
    });
  const recipeFields = (
    <div className="space-y-2">
      <Select
        label="Build recipe"
        value={`${form.recipe_id}:${form.recipe_version}`}
        change={(v) => {
          const r = recipes.find((r) => `${r.id}:${r.version}` === v);
          if (r)
            setForm((f) => ({
              ...f,
              recipe_id: r.id,
              recipe_version: r.version,
              platform: r.platform,
            }));
        }}
      >
        <option value="">Select recipe</option>
        {recipes
          .filter((r) => r.platform === form.platform)
          .map((r) => (
            <option key={`${r.id}:${r.version}`} value={`${r.id}:${r.version}`}>
              {r.name} v{r.version}
            </option>
          ))}
      </Select>
      {recipe && (
        <>
          <p>{recipe.description}</p>
          <p className="text-xs">
            Tools: {recipe.tools?.join(", ") || "Defined by recipe"} · generated
            directory: {recipe.build_directory || "source root"}
          </p>
          <details>
            <summary>Recipe commands and artifacts</summary>
            {recipe.prepare?.map((s: Item) => (
              <p key={s.name}>
                <strong>{s.name}</strong>: <code>{s.command?.join(" ")}</code>
              </p>
            ))}
            <p>Outputs: {recipe.outputs?.join(", ")}</p>
            <p>
              Artifact tests:{" "}
              {recipe.tests?.map((s: Item) => s.name).join(", ")}
            </p>
          </details>
        </>
      )}
      <Select
        label="Build runner"
        value={
          form.runner_deployment_id
            ? `${form.runner_deployment_id}:${form.runner_environment}`
            : form.runner_backend
        }
        change={(v) => {
          const r = options.runners?.find((r: Item) => r.id === v);
          setForm((f) => ({
            ...f,
            runner_deployment_id: r?.deployment_id || 0,
            runner_environment: r?.environment || "production",
            runner_backend: r?.backend || v,
          }));
        }}
      >
        <option value="runner">Apteva capsule runner</option>
        <option value="local">Deploy host (toolchain unchecked)</option>
        {options.runners
          ?.filter(
            (r: Item) =>
              (!recipe?.os || r.os === recipe.os) &&
              (!recipe?.arch || r.arch === recipe.arch),
          )
          .map((r: Item) => (
            <option key={r.id} value={r.id}>
              {r.name} · {r.backend} · {r.os || "OS unchecked"}
            </option>
          ))}
      </Select>
      {!form.runner_deployment_id && form.runner_backend === "runner" && (
        <>
          {field("Capsule runner URL", "runner_url")}
          <p className="text-xs">
            Configure the runner token in Deploy. Build execution verifies OS
            and tools.
          </p>
        </>
      )}
      {["ios", "android"].includes(form.platform) && (
        <>
          {field(
            form.platform === "ios" ? "Bundle ID" : "Package ID",
            form.platform === "ios" ? "bundle_id" : "package_name",
          )}
          {form.platform === "ios" && field("Xcode scheme", "scheme")}
          {field("App version", "version_name")}
        </>
      )}
    </div>
  );
  const dependencyFields = (
    <div className="space-y-2">
      <h4>Pinned sibling dependencies</h4>
      <Select
        label="Dependency repository"
        value={depRepo}
        change={(v) => {
          setDepRepo(v);
          setPin(undefined);
        }}
      >
        <option value="">Select dependency repository</option>
        {options.repositories?.map((r: Item) => (
          <option key={r.id} value={r.slug}>
            {r.name}
          </option>
        ))}
      </Select>
      <Field label="Sibling directory" value={depPath} change={setDepPath} />
      <button
        className={button}
        disabled={busy || !depRepo}
        onClick={() =>
          run(async () =>
            setPin(await call("source_pin", { repo_slug: depRepo })),
          )
        }
      >
        Capture dependency pin
      </button>
      {pin && (
        <>
          <p>
            Revision {pin.source_revision} · expires {pin.expires_at}
          </p>
          <button
            className={button}
            onClick={() => {
              change("dependencies", [
                ...form.dependencies.filter((d: Item) => d.path !== depPath),
                { slug: depRepo, path: depPath, snapshot_id: pin.snapshot_id },
              ]);
              setPin(undefined);
            }}
          >
            Use pinned dependency
          </button>
        </>
      )}
      {form.dependencies.map((d: Item) => (
        <p key={d.path}>
          {d.slug} → ../{d.path} · {d.snapshot_id}{" "}
          <button
            className={button}
            onClick={() =>
              change(
                "dependencies",
                form.dependencies.filter((x: Item) => x.path !== d.path),
              )
            }
          >
            Remove dependency
          </button>
        </p>
      ))}
    </div>
  );
  return (
    <section className={card} aria-label="Deployment setup">
      <h3 className="font-medium">Deployment setup</h3>
      <p className="text-sm text-text-muted">
        Choose source, recipe and runner here. Deploy owns signing, store
        accounts and release policy.
      </p>
      {error && <p role="alert">{error}</p>}
      {options.issues?.map((i: Item, n: number) => (
        <p role="alert" key={n}>
          {i.reason}{" "}
          <a className="underline" href={i.action_url}>
            Open app settings
          </a>
        </p>
      ))}
      <div className="flex gap-2">
        <button className={button} onClick={() => setOpen((v) => !v)}>
          Set up deployment
        </button>
        <button className={button} disabled={busy} onClick={() => run(load)}>
          Refresh setup options
        </button>
        <a className={button} href={options.deploy_url || "/apps"}>
          Configure signing and runners in Deploy
        </a>
      </div>
      {open && !review && (
        <div className="space-y-3">
          <h4>1. Source repository</h4>
          <Select
            label="Setup repository"
            value={form.create_repo ? "new" : form.repo_slug}
            change={(v) => {
              change("create_repo", v === "new");
              if (v !== "new") change("repo_slug", v);
            }}
          >
            <option value="">Select repository</option>
            <option value="new">Create repository</option>
            {options.repositories?.map((r: Item) => (
              <option key={r.id} value={r.slug}>
                {r.name} ({r.slug})
              </option>
            ))}
          </Select>
          {form.create_repo && (
            <>
              {field("Repository name", "repo_name")}
              {field("Repository slug", "repo_slug")}
              <p>
                A new repository starts empty. Add game source in Code before
                building.
              </p>
            </>
          )}
          <h4>2. Target and environment</h4>
          <Select
            label="Setup deployment"
            value={String(form.deployment_id)}
            change={(v) => {
              const d = options.deployments?.find(
                (d: Item) => Number(d.id) === Number(v),
              );
              setForm((f) => ({
                ...f,
                deployment_id: Number(v),
                create_environment: false,
                environment: d?.environments?.some(
                  (e: Item) => e.name === "production",
                )
                  ? "production"
                  : d?.environments?.[0]?.name || "production",
              }));
            }}
          >
            <option value="0">Create deployment</option>
            {options.deployments?.map((d: Item) => (
              <option key={d.id} value={d.id}>
                {d.name} ({d.target_kind})
              </option>
            ))}
          </Select>
          {!form.deployment_id && field("Deployment name", "deployment_name")}
          <Select
            label="Setup platform"
            value={form.platform}
            change={(v) =>
              setForm((f) => ({
                ...f,
                platform: v,
                recipe_id: v === "ios" ? "kiln-ios" : "command-artifact",
                recipe_version: "1",
              }))
            }
          >
            {["ios", "android", "desktop", "steam"].map((p) => (
              <option key={p}>{p}</option>
            ))}
          </Select>
          {!!form.deployment_id && (
            <Select
              label="Existing deployment environment"
              value={form.create_environment ? "new" : form.environment}
              change={(v) => {
                change("create_environment", v === "new");
                if (v !== "new") change("environment", v);
              }}
            >
              <option value="new">Create environment</option>
              {options.deployments
                ?.find((d: Item) => d.id === form.deployment_id)
                ?.environments?.map((e: Item) => (
                  <option key={e.id} value={e.name}>
                    {e.name}
                  </option>
                ))}
            </Select>
          )}
          {(!form.deployment_id || form.create_environment) &&
            field("Setup environment", "environment")}
          {form.environment !== "production" && (
            <label>
              <input
                type="checkbox"
                checked={!!form.create_environment}
                onChange={(e) => change("create_environment", e.target.checked)}
              />{" "}
              Create this environment
            </label>
          )}
          {!form.deployment_id && (
            <>
              <h4>3. Recipe and runner</h4>
              {recipeFields}
              {dependencyFields}
            </>
          )}
          <button
            className={button}
            disabled={
              busy ||
              !form.repo_slug ||
              (!form.deployment_id && (!recipe || !form.deployment_name)) ||
              !!options.issues?.some((i: Item) => i.blocking !== false)
            }
            onClick={() =>
              setReview({
                request_key: crypto.randomUUID(),
                input: structuredClone(form),
              })
            }
          >
            Review association
          </button>
        </div>
      )}
      {open && review && (
        <div className={card}>
          <h4>Review deployment association</h4>
          <p>
            Source: {review.input.repo_slug}{" "}
            {review.input.create_repo ? "(create)" : "(existing)"}
          </p>
          <p>
            Target: {review.input.deployment_id || review.input.deployment_name}{" "}
            · {review.input.platform} / {review.input.environment}
          </p>
          {!review.input.deployment_id && (
            <>
              <p>
                Recipe: {review.input.recipe_id} v{review.input.recipe_version}{" "}
                · runner:{" "}
                {review.input.runner_deployment_id ||
                  review.input.runner_backend}
              </p>
              <p>
                Dependencies:{" "}
                {review.input.dependencies
                  ?.map((d: Item) => `${d.slug} at ../${d.path}`)
                  .join(", ") || "none"}
              </p>
            </>
          )}
          {!receipt && (
            <>
              <button className={button} disabled={busy} onClick={submit}>
                Confirm setup
              </button>
              <button className={button} onClick={() => setReview(undefined)}>
                Edit setup
              </button>
            </>
          )}
          {receipt && (
            <>
              <p role="status">
                {receipt.status} · {receipt.stage}
              </p>
              {receipt.error && <p role="alert">{receipt.error}</p>}
              <p>
                Completed:{" "}
                {Object.keys(receipt.results || {}).join(", ") ||
                  "no steps yet"}
              </p>
              {receipt.results?.target && (
                <p>
                  Target linked: {receipt.results.target.name} ·{" "}
                  {receipt.results.target.environment}
                </p>
              )}
              {["pending", "blocked"].includes(receipt.status) && (
                <button className={button} disabled={busy} onClick={submit}>
                  Retry remaining setup steps
                </button>
              )}
              {receipt.status === "blocked" && (
                <button
                  className={button}
                  disabled={busy}
                  onClick={() => {
                    const createdDeployment =
                      receipt.results?.deployment?.deployment?.id;
                    setForm({
                      ...form,
                      ...review.input,
                      dependencies: review.input.dependencies || [],
                      create_repo: receipt.results?.repository
                        ? false
                        : review.input.create_repo,
                      deployment_id:
                        createdDeployment || review.input.deployment_id || 0,
                      create_environment: receipt.results?.environment
                        ? false
                        : review.input.create_environment,
                    });
                    setReview(undefined);
                    setReceipt(undefined);
                  }}
                >
                  Edit remaining setup using completed resources
                </button>
              )}
              {["unknown", "dispatching"].includes(receipt.status) && (
                <>
                  <p>
                    Verify the uncertain {receipt.stage} in Code or Deploy.
                    Retry remains blocked until recovery.
                  </p>
                  {receipt.stage === "deployment" && (
                    <Field
                      label="Verified deployment ID"
                      value={verified}
                      change={setVerified}
                    />
                  )}
                  <button
                    className={button}
                    disabled={busy}
                    onClick={() =>
                      run(async () => {
                        setReceipt(
                          await call("setup_reconcile", {
                            request_key: review.request_key,
                            deployment_id: Number(verified),
                            confirm: true,
                          }),
                        );
                        await load();
                      })
                    }
                  >
                    Recover verified resource
                  </button>
                  <Field
                    label="Reason no resource was created"
                    value={reason}
                    change={setReason}
                  />
                  <button
                    className={button}
                    disabled={busy || !reason}
                    onClick={() =>
                      run(async () => {
                        setReceipt(
                          await call("setup_reconcile", {
                            request_key: review.request_key,
                            resolution: "not_created",
                            reason,
                            confirm: true,
                          }),
                        );
                        await load();
                      })
                    }
                  >
                    Confirm no resource was created
                  </button>
                </>
              )}
              {receipt.status === "complete" && (
                <button
                  className={button}
                  onClick={() => {
                    setReceipt(undefined);
                    setReview(undefined);
                    setOpen(false);
                  }}
                >
                  Finish setup
                </button>
              )}
            </>
          )}
        </div>
      )}
      <details>
        <summary>Saved setup progress ({history.length})</summary>
        {history.map((h) => (
          <div className={card} key={h.request_key}>
            <p>
              {h.input?.repo_slug} · {h.status} · {h.stage}
            </p>
            <p>Completed: {Object.keys(h.results || {}).join(", ")}</p>
            <button
              className={button}
              onClick={() => {
                setReview({ request_key: h.request_key, input: h.input });
                setReceipt(h);
                setOpen(true);
              }}
            >
              Open saved setup
            </button>
          </div>
        ))}
      </details>
      <details onToggle={(e) => setConfigOpen(e.currentTarget.open)}>
        <summary>Configure an existing game target</summary>
        {configOpen && (
          <>
            <Select
              label="Target to configure"
              value={configTarget}
              change={(v) => {
                setConfigTarget(v);
                const t = targets.find((t) => t.id === v);
                if (t)
                  run(async () => {
                    const detail = await call("release_status", {
                      target_id: t.id,
                    });
                    const d = detail.deployment || {};
                    const cfg = JSON.parse(d.target_config_json || "{}");
                    const source = JSON.parse(d.source_extra_json || "{}");
                    setForm((f) => ({
                      ...f,
                      platform: t.platform,
                      dependencies: source.dependencies || [],
                      snapshot_id: source.snapshot_id || "",
                      bundle_id: cfg.bundle_id || "",
                      package_name: cfg.package_name || "",
                      scheme: cfg.scheme || "",
                      version_name: cfg.version_name || f.version_name,
                      recipe_id:
                        cfg.games_recipe?.id ||
                        (t.platform === "ios"
                          ? "kiln-ios"
                          : "command-artifact"),
                      recipe_version: cfg.games_recipe?.version || "1",
                      runner_deployment_id: t.deployment_id,
                      runner_environment: t.environment,
                    }));
                  });
              }}
            >
              <option value="">Select target</option>
              {targets.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} · {t.environment}
                </option>
              ))}
            </Select>
            {recipeFields}
            {dependencyFields}
            <p>
              Signing, account selections and release policy remain owned by
              Deploy.
            </p>
            <button
              className={button}
              disabled={busy || !configTarget || !recipe}
              onClick={() =>
                run(async () => {
                  await call("configure", {
                    target_id: configTarget,
                    setup: form,
                  });
                  await reload();
                  await load();
                })
              }
            >
              Apply recipe and runner
            </button>
          </>
        )}
      </details>
      <details>
        <summary>Save a reusable recipe</summary>
        <button
          className={button}
          disabled={!recipe}
          onClick={() =>
            setEditor({
              ...recipe,
              id: `${recipe?.id}-custom`,
              version: "1",
              prepareText: stageText(recipe?.prepare),
              testsText: stageText(recipe?.tests),
              outputsText: recipe?.outputs?.join("\n"),
            })
          }
        >
          Customize selected recipe
        </button>
        {editor && (
          <div className="space-y-2">
            {[
              ["Recipe ID", "id"],
              ["Recipe name", "name"],
              ["Recipe version", "version"],
              ["Recipe platform", "platform"],
              ["Recipe OS", "os"],
              ["Build framework", "framework"],
              ["Build command", "build_cmd"],
              ["Generated project directory", "build_directory"],
            ].map(([l, k]) => (
              <Field
                key={k}
                label={l}
                value={editor[k] || ""}
                change={(v) => setEditor((e) => ({ ...e, [k]: v }))}
              />
            ))}
            {[
              [
                "Preparation stages (name | command, one per line)",
                "prepareText",
              ],
              ["Artifact outputs (one relative path per line)", "outputsText"],
              [
                "Final-artifact tests (name | command, one per line)",
                "testsText",
              ],
            ].map(([l, k]) => (
              <Lines
                key={k}
                label={l}
                value={editor[k]}
                change={(v) => setEditor((e) => ({ ...e, [k]: v }))}
              />
            ))}
            <button
              className={button}
              disabled={busy}
              onClick={() =>
                run(async () => {
                  const { prepareText, testsText, outputsText, ...r } = editor;
                  await call("recipe_save", {
                    recipe: {
                      ...r,
                      prepare: stages(prepareText, r.prepare),
                      tests: stages(testsText, r.tests),
                      outputs: outputsText
                        .split("\n")
                        .map((s: string) => s.trim())
                        .filter(Boolean),
                    },
                  });
                  await load();
                  change("recipe_id", r.id);
                  change("recipe_version", r.version);
                  setEditor(undefined);
                })
              }
            >
              Save recipe version
            </button>
          </div>
        )}
      </details>
    </section>
  );
}
export function ReadinessChecklist({ value }: { value: Item }) {
  return (
    <section className={card} aria-label="Deployment readiness">
      <h4>Deployment readiness</h4>
      <p className="text-xs">
        Checked{" "}
        {value.checked_at
          ? new Date(value.checked_at).toLocaleString()
          : "not yet"}{" "}
        · build {value.build_id || "not selected"} · channel{" "}
        {value.channel || "not selected"}
      </p>
      {value.checks?.map((c: Item) => (
        <div key={c.id} className="border-t border-border pt-2">
          <strong>{c.name}</strong> <span>{c.status}</span>
          <p className="text-xs text-text-muted">
            {c.kind === "evidence" ? "Build evidence" : "Configuration"} ·{" "}
            {c.reason}
          </p>
          {c.evidence_at && (
            <p className="text-xs">
              Evidence from {new Date(c.evidence_at).toLocaleString()}
            </p>
          )}
          {c.action_url ? (
            <a className="underline text-xs" href={c.action_url}>
              {c.action}
            </a>
          ) : (
            <p className="text-xs">Next: {c.action}</p>
          )}
        </div>
      ))}
    </section>
  );
}
export function BuildEvidence({ build }: { build: Item }) {
  let manifest: Item = {};
  try {
    manifest = JSON.parse(build.artifact_manifest_json || "{}");
  } catch {}
  const evidence = manifest.pipeline;
  const failure =
    String(build.error || "").match(
      /stage (.+?) (?:failed|missing output)/,
    )?.[1] ||
    String(build.error || "").match(/xcodebuild (archive|export)/)?.[0];
  return (
    <article className={card}>
      <h4>
        Build #{build.id} · {build.status}
      </h4>
      {build.source_sha && (
        <p className="text-xs">Built source: {build.source_sha}</p>
      )}
      {build.error && (
        <p role="alert">
          {failure ? `Failed stage: ${failure}. ` : "Build execution failed. "}
          {build.error}
        </p>
      )}
      {build.artifact_download_url && (
        <a className="underline" href={build.artifact_download_url}>
          Download build #{build.id} artifacts
        </a>
      )}
      {evidence ? (
        <>
          <p>
            Artifact tests:{" "}
            {evidence.tests?.join(", ") || "No passing tests reported"}
          </p>
          <p className="text-xs">
            Evidence checked {evidence.completed_at} · artifact{" "}
            {evidence.artifact_sha256}
          </p>
        </>
      ) : (
        <p className="text-xs">
          Final-artifact test evidence has not been reported.
        </p>
      )}
      {manifest.files?.map((f: Item, i: number) => (
        <p key={i} className="text-xs">
          {f.path || f.name} · {f.size} bytes
        </p>
      ))}
    </article>
  );
}
