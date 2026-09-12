import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Panel from "./ProcessesPanel";
const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
});
const process = {
  id: "p",
  name: "Weekly review",
  description: "Review operations",
  status: "paused",
  version: 1,
  execution_mode: "agent",
  owner_agent_id: 7,
  completion_criteria: "Approved report",
  instructions: "Review",
  sync_pending: false,
};
async function mount(history: object, overrides: object = {}) {
  const selectedProcess = { ...process, ...overrides };
  window.history.replaceState(null, "", "/");
  globalThis.fetch = (async (url: unknown) => {
    const path = String(url).split("?")[0];
    if (path === "/api/agents")
      return Response.json([
        { id: 7, name: "Owner" },
        { id: 8, name: "Cooking agent" },
      ]);
    if (path.endsWith("/runs")) return Response.json(history);
    if (path.endsWith("/p"))
      return Response.json({
        process: selectedProcess,
        versions: [{ version: 1, definition: selectedProcess }],
      });
    return Response.json({ processes: [selectedProcess] });
  }) as typeof fetch;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root.render(<Panel projectId="project-a" />));
  return container;
}
async function click(text: string) {
  const button = Array.from(document.querySelectorAll("button")).find(
    (b) => b.textContent?.trim() === text,
  );
  expect(button).toBeTruthy();
  await act(async () => button!.click());
}
test("execution selector defaults to direct and allows optional Tasks", async () => {
  await mount({});
  await click("+ New process");
  const select = document.querySelector<HTMLSelectElement>("#pc-mode")!;
  expect(select.value).toBe("agent");
  await act(async () => {
    select.value = "tasks";
    select.dispatchEvent(
      new window.Event("change", { bubbles: true }) as unknown as Event,
    );
  });
  expect(document.body.textContent).toContain("Requires Tasks 3.6.0");
});
test("mixed history renders direct evidence and links only Tasks records", async () => {
  await mount({
    direct_runs: [
      {
        id: "direct",
        version: 1,
        state: "completed",
        result: "Direct report approved",
        progress: 100,
        created_at: "2026-09-12T10:00:00Z",
      },
    ],
    runs: [
      {
        version: 1,
        task: {
          id: "task-1",
          title: "Older Tasks run",
          state: "completed",
          result: "Task report",
          created_at: "2026-09-11T10:00:00Z",
        },
      },
    ],
    tasks_error: "offline",
  });
  await click("Weekly review");
  await click("Runs");
  expect(document.body.textContent).toContain("Direct report approved");
  expect(document.body.textContent).toContain(
    "Tasks history unavailable: offline",
  );
  const links = document.querySelectorAll('a[href*="/apps/tasks/"]');
  expect(links.length).toBe(1);
  expect(links[0].getAttribute("href")).toContain("task-1");
});

const assignment = {
  id: "photo",
  process_id: "p",
  revision: 1,
  name: "Photography Patreon",
  target: "Photo page",
  owner_agent_id: 7,
  execution_mode: "agent",
  procedure_version: 1,
  follow_latest: true,
  parameters: { page: "photo" },
  status: "active",
  sync_pending: false,
  sync_error: "",
};
test("assignment run sends its ID and opens parameter fields", async () => {
  await mount(
    {},
    {
      status: "active",
      assignments: [
        assignment,
        {
          ...assignment,
          id: "cooking",
          name: "Cooking Patreon",
          owner_agent_id: 8,
          parameters: { page: "cooking" },
        },
      ],
      parameters: [
        { key: "page", label: "Patreon page", type: "string", required: true },
      ],
    },
  );
  await click("Weekly review");
  await click("Assignments");
  const card = Array.from(document.querySelectorAll("article")).find((x) =>
    x.textContent?.includes("Cooking Patreon"),
  )!;
  await act(async () =>
    Array.from(card.querySelectorAll("button"))
      .find((x) => x.textContent === "Run now")!
      .click(),
  );
  expect(document.querySelector("[role=dialog]")?.textContent).toContain(
    "Cooking agent",
  );
  expect(
    document.querySelector<HTMLInputElement>("#run-parameter-page")?.value,
  ).toBe("cooking");
  const read = globalThis.fetch;
  let payload: any;
  globalThis.fetch = (async (url: unknown, init?: RequestInit) => {
    if (init?.method === "POST") {
      payload = JSON.parse(String(init.body));
      return Response.json({ run: { id: "run" } });
    }
    return read(url as string, init);
  }) as typeof fetch;
  await click("Start run");
  expect(payload.assignment_id).toBe("cooking");
  expect(payload.idempotency_key).toBeTruthy();
});
test("run history filters independent assignments", async () => {
  const runs = [
    {
      id: "r1",
      assignment_id: "photo",
      assignment,
      version: 1,
      state: "completed",
      result: "Photo result",
      created_at: "2026-09-12T10:00:00Z",
    },
    {
      id: "r2",
      assignment_id: "cooking",
      assignment: { ...assignment, name: "Cooking", owner_agent_id: 8 },
      version: 1,
      state: "blocked",
      error: "Cooking approval needed",
      created_at: "2026-09-12T11:00:00Z",
    },
  ];
  await mount(
    { direct_runs: runs },
    {
      assignments: [
        assignment,
        { ...assignment, id: "cooking", name: "Cooking", owner_agent_id: 8 },
      ],
    },
  );
  await click("Weekly review");
  await click("Runs");
  const select = document.querySelector<HTMLSelectElement>(
    '[aria-label="Filter run assignment"]',
  )!;
  await act(async () => {
    select.value = "photo";
    select.dispatchEvent(
      new window.Event("change", { bubbles: true }) as unknown as Event,
    );
  });
  expect(document.body.textContent).toContain("Photo result");
  expect(document.body.textContent).not.toContain("Cooking approval needed");
});
test("editing a paused assignment preserves parameters and uses revision", async () => {
  await mount(
    {},
    {
      assignments: [{ ...assignment, status: "paused" }],
      parameters: [
        { key: "page", label: "Patreon page", type: "string", required: true },
      ],
    },
  );
  await click("Weekly review");
  await click("Assignments");
  await click("Edit assignment");
  expect(
    document.querySelector<HTMLInputElement>("#parameter-page")?.value,
  ).toBe("photo");
  const select =
    document.querySelector<HTMLSelectElement>("#assignment-agent")!;
  await act(async () => {
    select.value = "8";
    select.dispatchEvent(
      new window.Event("change", { bubbles: true }) as unknown as Event,
    );
  });
  const read = globalThis.fetch;
  let payload: any;
  let path = "";
  globalThis.fetch = (async (url: unknown, init?: RequestInit) => {
    if (init?.method === "PUT") {
      payload = JSON.parse(String(init.body));
      path = String(url);
      return Response.json({});
    }
    return read(url as string, init);
  }) as typeof fetch;
  await act(async () =>
    document.querySelector("form")!.dispatchEvent(
      new window.Event("submit", {
        bubbles: true,
        cancelable: true,
      }) as unknown as Event,
    ),
  );
  expect(path).toContain("/assignments/photo");
  expect(payload.expected_revision).toBe(1);
  expect(payload.assignment.owner_agent_id).toBe(8);
  expect(payload.assignment.parameters.page).toBe("photo");
});

const workflowSteps = [
  {
    key: "write",
    name: "Write post",
    role: "writer",
    kind: "work",
    instructions: "Write the draft",
    expected_output: "Draft",
    depends_on: [],
  },
  {
    key: "review",
    name: "Review post",
    role: "reviewer",
    kind: "approval",
    instructions: "Review the draft",
    expected_output: "Decision",
    depends_on: ["write"],
  },
  {
    key: "publish",
    name: "Publish post",
    role: "publisher",
    kind: "work",
    instructions: "Publish approved draft",
    expected_output: "URL",
    depends_on: ["review"],
  },
];
test("workflow template creates editable dependencies and approval gate", async () => {
  await mount({});
  await click("+ New process");
  await click("Use research → write → review → publish");
  expect(document.querySelector<HTMLInputElement>("#step-role-2")?.value).toBe(
    "reviewer",
  );
  expect(document.querySelector<HTMLSelectElement>("#step-kind-2")?.value).toBe(
    "approval",
  );
  expect(document.querySelectorAll("fieldset input:checked").length).toBe(3);
});
test("assignment saves agent role bindings and defaults approval to human", async () => {
  await mount(
    {},
    {
      steps: workflowSteps,
      assignments: [{ ...assignment, status: "paused" }],
    },
  );
  await click("Weekly review");
  await click("Assignments");
  await click("Edit assignment");
  expect(
    document.querySelector<HTMLSelectElement>("#role-reviewer")?.value,
  ).toBe("human");
  const select = document.querySelector<HTMLSelectElement>("#role-writer")!;
  await act(async () => {
    select.value = "8";
    select.dispatchEvent(
      new window.Event("change", { bubbles: true }) as unknown as Event,
    );
  });
  const read = globalThis.fetch;
  let payload: any;
  globalThis.fetch = (async (url: unknown, init?: RequestInit) => {
    if (init?.method === "PUT") {
      payload = JSON.parse(String(init.body));
      return Response.json({});
    }
    return read(url as string, init);
  }) as typeof fetch;
  await act(async () =>
    document
      .querySelector("form")!
      .dispatchEvent(
        new window.Event("submit", {
          bubbles: true,
          cancelable: true,
        }) as unknown as Event,
      ),
  );
  expect(payload.assignment.roles.writer).toEqual({
    kind: "agent",
    agent_id: 8,
  });
});
test("workflow history offers human review only after predecessor completion", async () => {
  await mount({
    direct_runs: [
      {
        id: "team-run",
        workflow: true,
        state: "waiting",
        version: 1,
        created_at: "2026-09-12T10:00:00Z",
        steps: workflowSteps.map((definition, i) => ({
          id: `step-${i}`,
          key: definition.key,
          definition,
          executor:
            i === 1 ? { kind: "human" } : { kind: "agent", agent_id: 7 },
          state: ["completed", "waiting", "pending"][i],
          output: i === 0 ? "Draft evidence" : "",
          updated_at: "2026-09-12T10:00:00Z",
        })),
      },
    ],
  });
  await click("Weekly review");
  await click("Runs");
  expect(document.body.textContent).toContain("Team workflow run");
  expect(document.body.textContent).toContain("Draft evidence");
  expect(
    Array.from(document.querySelectorAll("button")).filter(
      (b) => b.textContent === "Review & decide",
    ).length,
  ).toBe(1);
  expect(document.body.textContent).not.toContain("Complete human step");
  await click("Review & decide");
  expect(document.body.textContent).toContain("Completed inputs");
  expect(
    document.querySelector<HTMLButtonElement>("button.primary:disabled"),
  ).toBeTruthy();
  expect(document.body.textContent).toContain("Reject & stop run");
});
