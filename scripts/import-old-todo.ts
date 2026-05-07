#!/usr/bin/env bun
// One-shot importer: old Bun/JSON todo-app → new Apteva todo sidecar.
//
// Old shape (under <from>/):
//   projects.json   {id, name, color, goal, ...}
//   lists.json      {id, projectId, name, goal, ...}
//   tasks/<list-id>.json  array of {id, projectId, listId, text, done,
//                                   deadline, completedAt, duration}
//
// Mapping → new schema (mcp/todo):
//   project           → list (name + color)
//   list              → tag  (slugified list.name)
//   task.text         → todo.title
//   task.projectId    → todo.list_id (resolved via project→list map)
//   task.listId       → one entry in todo.tags
//   task.deadline     → todo.due_at  (Go normaliseDue accepts YYYY-MM-DD)
//   task.done         → POST /todos/{id}/complete after create
//   task.duration     → dropped (no analog)
//   project.goal,
//   list.goal         → dropped (logged as a count)
//
// Auth: APTEVA_API_KEY env var, else api_key from ~/.apteva/apteva.json.
// Idempotency: writes <from>/.imported on success; --force re-runs.

import { existsSync, readdirSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";

interface OldProject {
  id: string;
  name: string;
  color?: string;
  goal?: number;
}
interface OldList {
  id: string;
  projectId: string;
  name: string;
  goal?: number;
}
interface OldTask {
  id: string;
  projectId: string;
  listId: string;
  text: string;
  done: boolean;
  deadline?: string | null;
  createdAt?: string;
  completedAt?: string | null;
  duration?: number;
}
interface NewList {
  id: number;
  name: string;
  color: string;
  archived: boolean;
}
interface NewTodo {
  id: number;
}

function arg(name: string, def?: string): string | undefined {
  const i = process.argv.indexOf(name);
  return i === -1 ? def : (process.argv[i + 1] ?? def);
}
function flag(name: string): boolean {
  return process.argv.includes(name);
}

function slugify(s: string): string {
  const slug = s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
  return slug || "tag";
}

async function loadAuth(): Promise<string> {
  if (process.env.APTEVA_API_KEY) return process.env.APTEVA_API_KEY;
  const cfgPath = join(homedir(), ".apteva", "apteva.json");
  const cfg = JSON.parse(await readFile(cfgPath, "utf8"));
  if (!cfg.api_key) throw new Error(`no api_key in ${cfgPath}`);
  return cfg.api_key as string;
}

let API_BASE = "";
let TOKEN = "";

async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${TOKEN}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${method} ${path} → ${res.status}: ${text.slice(0, 200)}`);
  }
  if (res.status === 204) return null as T;
  return (await res.json()) as T;
}

async function main() {
  const fromDir = arg("--from", join(homedir(), "Documents/code-old/todo-app/data"))!;
  const apiBase = arg("--api", "http://localhost:5280")!;
  API_BASE = `${apiBase.replace(/\/$/, "")}/api/apps/todo`;
  const dryRun = flag("--dry-run");
  const force = flag("--force");
  const skipDone = flag("--skip-done");

  TOKEN = await loadAuth();

  if (!existsSync(fromDir)) throw new Error(`--from path does not exist: ${fromDir}`);
  const marker = join(fromDir, ".imported");
  if (existsSync(marker) && !force && !dryRun) {
    throw new Error(`already imported (marker: ${marker}). pass --force to re-run.`);
  }

  console.log(`from : ${fromDir}`);
  console.log(`api  : ${API_BASE}`);
  if (dryRun) console.log("mode : DRY RUN — no writes");
  console.log();

  const projects: OldProject[] = JSON.parse(
    await readFile(join(fromDir, "projects.json"), "utf8"),
  );
  const lists: OldList[] = JSON.parse(
    await readFile(join(fromDir, "lists.json"), "utf8"),
  );
  const tasksDir = join(fromDir, "tasks");
  const taskFiles = readdirSync(tasksDir).filter((f) => f.endsWith(".json"));
  let allTasks: OldTask[] = [];
  for (const f of taskFiles) {
    const arr: OldTask[] = JSON.parse(await readFile(join(tasksDir, f), "utf8"));
    allTasks = allTasks.concat(arr);
  }
  const doneCount = allTasks.filter((t) => t.done).length;
  console.log(
    `loaded: ${projects.length} projects, ${lists.length} lists, ` +
      `${allTasks.length} tasks (${doneCount} done) across ${taskFiles.length} files`,
  );

  if (dryRun) {
    console.log(`\nwould create / reuse lists for:`);
    for (const p of projects) console.log(`  - ${p.name}  (color ${p.color ?? "—"})`);
    const tagSet = [...new Set(lists.map((l) => slugify(l.name)))];
    console.log(`\nwould produce ${tagSet.length} unique tags (first 15):`);
    console.log("  " + tagSet.slice(0, 15).join(", ") + (tagSet.length > 15 ? ", …" : ""));
    const droppedGoals =
      projects.filter((p) => p.goal).length + lists.filter((l) => l.goal).length;
    const droppedDurations = allTasks.filter((t) => (t.duration ?? 0) > 0).length;
    console.log(
      `\ndropped fields: ${droppedGoals} goal values, ${droppedDurations} duration values`,
    );
    return;
  }

  // 1. Lists — dedupe by lowercased name against what's already there.
  const existing = await api<NewList[]>("GET", "/lists");
  const byName = new Map<string, number>();
  for (const l of existing) byName.set(l.name.toLowerCase(), l.id);

  const projectToListID = new Map<string, number>();
  let listsCreated = 0;
  let listsReused = 0;
  for (const p of projects) {
    let id = byName.get(p.name.toLowerCase());
    if (id == null) {
      const created = await api<NewList>("POST", "/lists", {
        name: p.name,
        color: p.color ?? "#3b82f6",
      });
      id = created.id;
      byName.set(p.name.toLowerCase(), id);
      listsCreated++;
      console.log(`+ list  ${p.name}  (id=${id})`);
    } else {
      listsReused++;
      console.log(`= list  ${p.name}  (id=${id}, reused)`);
    }
    projectToListID.set(p.id, id);
  }

  // 2. Old list → tag string (just remember the slug, don't pre-create).
  const oldListToTag = new Map<string, string>();
  for (const l of lists) oldListToTag.set(l.id, slugify(l.name));

  // 3. Todos.
  let imported = 0;
  let completed = 0;
  let skipped = 0;
  let failed = 0;
  let unmappedList = 0;

  for (const t of allTasks) {
    if (skipDone && t.done) {
      skipped++;
      continue;
    }
    const listId = projectToListID.get(t.projectId);
    if (listId == null) unmappedList++;
    const tag = oldListToTag.get(t.listId);
    const tags = tag ? [tag] : [];
    try {
      const created = await api<NewTodo>("POST", "/todos", {
        title: t.text,
        list_id: listId ?? 0,
        priority: 4,
        due_at: t.deadline ?? "",
        tags,
        source: "human",
      });
      imported++;
      if (t.done) {
        await api("POST", `/todos/${created.id}/complete`);
        completed++;
      }
    } catch (e) {
      failed++;
      console.warn(`! ${t.id}: ${(e as Error).message}`);
    }
  }

  console.log("\nimport summary");
  console.log(`  lists created   : ${listsCreated}`);
  console.log(`  lists reused    : ${listsReused}`);
  console.log(`  todos imported  : ${imported}`);
  console.log(`  marked done     : ${completed}`);
  console.log(`  skipped         : ${skipped}`);
  console.log(`  failed          : ${failed}`);
  if (unmappedList > 0) {
    console.log(
      `  unmapped projectId → inbox: ${unmappedList} (todo created with list_id=null)`,
    );
  }

  if (failed === 0) {
    await writeFile(marker, new Date().toISOString() + "\n");
    console.log(`\nmarker written: ${marker}`);
  } else {
    console.log("\nnot writing marker — failures present; fix and re-run with --force.");
  }
}

main().catch((e) => {
  console.error("fatal:", (e as Error).message);
  process.exit(1);
});
