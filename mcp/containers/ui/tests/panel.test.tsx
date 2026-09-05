import { afterEach, beforeEach, expect, test } from "bun:test";
import { GlobalWindow as Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import ContainersPanel, {
  apiURL,
  imageName,
  numeric,
} from "../ContainersPanel";
let root: Root;
let dom: Window;
let requests: Array<{ url: string; init?: RequestInit }>;
let failStop = false;
const originalFetch = globalThis.fetch;
const workload = {
  id: "wrk_one",
  name: "one",
  image: "nginx:alpine",
  status: "running",
  instance_id: 0,
  health_status: "running",
  created_at: "",
  ports: [
    {
      bind_addr: "127.0.0.1",
      host_port: 1234,
      container_port: 80,
      protocol: "tcp",
    },
  ],
};
beforeEach(() => {
  dom = new Window({ url: "http://localhost" });
  for (const name of [
    "window",
    "document",
    "HTMLElement",
    "Event",
    "MouseEvent",
    "KeyboardEvent",
    "navigator",
  ])
    Object.defineProperty(globalThis, name, {
      configurable: true,
      value: name === "window" ? dom : (dom as any)[name],
    });
  (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  requests = [];
  failStop = false;
  globalThis.fetch = (async (input: any, init?: RequestInit) => {
    const url = String(input);
    requests.push({ url, init });
    if (failStop && url.includes("/stop"))
      return new Response("stop deliberately failed", { status: 500 });
    const data = url.includes("/hosts")
      ? { hosts: [{ id: 0, name: "Local", status: "ready" }] }
      : url.includes("/blueprints")
        ? { blueprints: [] }
        : url.includes("status=destroyed")
          ? {
              workloads: [
                {
                  ...workload,
                  id: "wrk_retained",
                  name: "retained",
                  status: "destroyed",
                  volumes: [{ name: "data", mount_path: "/data" }],
                },
              ],
            }
          : { workloads: [workload] };
    return Response.json(data);
  }) as typeof fetch;
});
afterEach(async () => {
  await act(async () => root.unmount());
  await dom.happyDOM.close();
  globalThis.fetch = originalFetch;
});
async function render(projectId = "a", installId = 7) {
  await act(async () => {
    root.render(
      <ContainersPanel projectId={projectId} installId={installId} />,
    );
    await new Promise((r) => setTimeout(r, 5));
  });
}
async function click(text: string) {
  const b = Array.from(document.querySelectorAll("button")).find(
    (x) => x.textContent === text,
  );
  expect(b).toBeDefined();
  await act(async () => {
    b!.click();
    await new Promise((r) => setTimeout(r, 5));
  });
}

test("API selectors preserve action query and distinguish installs", () => {
  expect(
    apiURL("/workloads/w?delete_volumes=1", {
      installId: 17,
      projectId: "a b",
    }),
  ).toBe(
    "/api/apps/containers/_install/17/api/workloads/w?delete_volumes=1&project_id=a+b",
  );
  expect(apiURL("/hosts", { installId: 18, projectId: "" })).not.toContain(
    "project_id=",
  );
});
test("numeric validation and registry names", () => {
  for (const v of ["nope", "NaN", "Infinity", "-1", "65536", "1.5"])
    expect(() => numeric(v, "Port", 0, 65535)).toThrow();
  expect(numeric("", "port", 0, 65535)).toBe(0);
  expect(imageName("registry.example:5000/team/nginx:1.2")).toBe("nginx");
});
test("all requests change scope and unsaved form is reset", async () => {
  await render();
  expect(requests.length).toBeGreaterThan(2);
  expect(
    requests.every(
      (r) => r.url.includes("_install/7/") && r.url.includes("project_id=a"),
    ),
  ).toBe(true);
  const firstName = (document.querySelector("input") as HTMLInputElement).value;
  requests = [];
  await render("b", 8);
  expect(
    requests.every(
      (r) => r.url.includes("_install/8/") && r.url.includes("project_id=b"),
    ),
  ).toBe(true);
  expect((document.querySelector("input") as HTMLInputElement).value).not.toBe(
    firstName,
  );
});
test("operation error survives successful refresh", async () => {
  await render();
  failStop = true;
  await click("Stop");
  expect(document.body.textContent).toContain("stop deliberately failed");
  await click("Refresh");
  expect(document.body.textContent).toContain("stop deliberately failed");
  await click("Dismiss");
  expect(document.body.textContent).not.toContain("stop deliberately failed");
});
test("retained volumes are discovered and offered for reuse", async () => {
  await render();
  expect(document.body.textContent).toContain("Retained volumes");
  await click("Reuse in form");
  expect(
    Array.from(document.querySelectorAll("textarea")).some((e) =>
      e.value.includes("data:/data@wrk_retained"),
    ),
  ).toBe(true);
});
test("quick run does not reuse retained volume settings", async () => {
  await render();
  await click("Reuse in form");
  await click("Run");
  const post = requests.find(
    (r) => r.init?.method === "POST" && r.url.includes("/workloads?"),
  );
  expect(post).toBeDefined();
  const data = JSON.parse(String(post!.init!.body));
  expect(data.env).toEqual({});
  expect(data.volumes).toEqual([]);
  expect(data.files).toEqual([]);
});
test("destroy dialog traps focus and restores it", async () => {
  await render();
  const trigger = Array.from(document.querySelectorAll("button")).find(
    (e) => e.textContent === "Destroy",
  )!;
  trigger.focus();
  await click("Destroy");
  const dialog = document.querySelector('[role="alertdialog"]')!;
  expect(dialog.contains(document.activeElement)).toBe(true);
  await act(async () => {
    document.activeElement!.dispatchEvent(
      new dom.KeyboardEvent("keydown", {
        key: "Tab",
        shiftKey: true,
        bubbles: true,
      }) as any,
    );
  });
  expect(dialog.contains(document.activeElement)).toBe(true);
  await click("Cancel");
  expect(document.activeElement === trigger).toBe(true);
});
