import { Window } from "happy-dom";
import { test, expect, afterEach } from "bun:test";
const dom = new Window({ url: "http://localhost/apps/tables/page" });
for (const key of [
  "window",
  "document",
  "navigator",
  "HTMLElement",
  "HTMLInputElement",
  "Node",
  "Event",
  "MouseEvent",
  "KeyboardEvent",
  "MutationObserver",
])
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: key === "window" ? dom : (dom as any)[key],
  });
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
const React = await import("react");
const { render, fireEvent, cleanup, act } = await import(
  "@testing-library/react"
);
const {
  default: TablesPanel,
  RowEditor,
  InsertDialog,
} = await import("./TablesPanel");
const { parseInputValue, parseJSON, stringifyJSON } = await import(
  "./lib/values"
);
const { useResource } = await import("./lib/useResource");
const { Dialog } = await import("./Dialog");
const { tablePanelUrl, rowPanelUrl, rowStatusVariant } = await import(
  "./lib/tables"
);
const originalFetch = globalThis.fetch;
const flush = async () => {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });
};
afterEach(() => {
  cleanup();
  globalThis.fetch = originalFetch;
  window.history.replaceState(null, "", "/apps/tables/page");
});
const table = {
  id: 1,
  name: "books",
  scope: "project",
  created_at: "",
  row_count: 1,
  columns: [
    { name: "title", type: "text", nullable: false },
    { name: "note", type: "text", nullable: true },
  ],
} as any;
const response = (value: unknown) =>
  new Response(stringifyJSON(value), { status: 200 });

test("editing sends only dirty fields and preserves empty strings", async () => {
  let patch: any;
  const ui = render(
    <table>
      <tbody>
        <RowEditor
          table={table}
          row={{ id: 1, title: "old", note: "" }}
          onCancel={() => {}}
          onDelete={() => {}}
          onSave={(x) => {
            patch = x;
          }}
        />
      </tbody>
    </table>,
  );
  fireEvent.change(ui.getByLabelText("title"), { target: { value: "new" } });
  fireEvent.click(ui.getByText("save"));
  await flush();
  expect(patch).toEqual({ title: "new" });
  fireEvent.change(ui.getByLabelText("note"), {
    target: { value: "explicit empty" },
  });
  fireEvent.change(ui.getByLabelText("note"), { target: { value: "" } });
  fireEvent.click(ui.getByText("save"));
  await flush();
  expect(patch).toEqual({ title: "new", note: "" });
  fireEvent.change(ui.getByLabelText("note value mode"), {
    target: { value: "null" },
  });
  fireEvent.click(ui.getByText("save"));
  await flush();
  expect(patch.note).toBeNull();
});
test("invalid typed input is rejected", () => {
  for (const [type, value] of [
    ["bool", "TRUE"],
    ["json", '{"broken":'],
    ["number", ""],
    ["number", "Infinity"],
    ["file_id", "1.9"],
    ["datetime", "yesterday"],
  ])
    expect(() =>
      parseInputValue({ name: "value", type, nullable: false } as any, value!),
    ).toThrow();
  expect(
    parseInputValue(
      { name: "id", type: "file_id", nullable: false },
      "9007199254740993",
    ),
  ).toBe("9007199254740993");
  const json =
    '{"id":9007199254740993,"small":1,"decimal":0.1234567890123456789}';
  expect(stringifyJSON(parseJSON(json))).toBe(json);
});
test("defaulted required fields can be omitted and submissions are gated", async () => {
  let submitted = 0;
  let complete!: () => void;
  const pending = new Promise<void>((r) => (complete = r));
  const ui = render(
    <InsertDialog
      table={{
        ...table,
        columns: [
          { name: "status", type: "text", nullable: false, default: "pending" },
        ],
      }}
      onCancel={() => {}}
      onSubmit={(row) => {
        expect(row).toEqual({});
        submitted++;
        return pending;
      }}
    />,
  );
  fireEvent.click(ui.getByText("Insert"));
  fireEvent.click(ui.getByText("Saving…"));
  expect(submitted).toBe(1);
  await act(async () => complete());
});
test("required missing values stay in the form with an error", async () => {
  let called = false;
  const ui = render(
    <InsertDialog
      table={table}
      onCancel={() => {}}
      onSubmit={async () => {
        called = true;
      }}
    />,
  );
  fireEvent.click(ui.getByText("Insert"));
  await flush();
  expect(called).toBe(false);
  expect(ui.getByRole("alert").textContent).toContain("required");
});
test("late table response cannot replace the newly selected table", async () => {
  const pending = new Map<string, (v: Response) => void>();
  (window as any).__aptevaAppEvents = { subscribe: () => () => {} };
  globalThis.fetch = ((url: any) => {
    const path = new URL(String(url), "http://localhost").pathname;
    if (path.endsWith("/tables"))
      return Promise.resolve(
        response({
          tables: [
            { ...table, name: "alpha" },
            { ...table, id: 2, name: "beta" },
          ],
          has_more: false,
        }),
      );
    if (!path.endsWith("/rows"))
      return Promise.resolve(
        response({
          ...table,
          id: path.endsWith("beta") ? 2 : 1,
          name: path.split("/").pop(),
        }),
      );
    return new Promise((resolve) => pending.set(path, resolve));
  }) as typeof fetch;
  const ui = render(
    <TablesPanel appName="tables" projectId="p" installId={1} />,
  );
  await flush();
  await flush();
  fireEvent.click(ui.getByText("beta"));
  await flush();
  await flush();
  const resolveRows = (name: string, title: string) =>
    pending.get(`/api/apps/tables/tables/${name}/rows`)!(
      response({
        rows: [{ id: 1, _revision: 1, title, note: "" }],
        has_more: false,
        next_offset: 1,
      }),
    );
  await act(async () => resolveRows("beta", "correct beta row"));
  await flush();
  await act(async () => resolveRows("alpha", "stale alpha row"));
  await flush();
  expect(ui.container.textContent).not.toContain("stale alpha row");
  expect(ui.container.textContent).toContain("correct beta row");
});
test("resources cancel stale identities and coalesce refresh storms", async () => {
  const pending: {
    key: string;
    signal: AbortSignal;
    resolve: (v: string) => void;
  }[] = [];
  function Probe({ resource, epoch }: { resource: string; epoch: number }) {
    const result = useResource(
      resource,
      epoch,
      (signal) =>
        new Promise<string>((resolve) =>
          pending.push({ key: resource, signal, resolve }),
        ),
    );
    return <span>{result.data ?? "loading"}</span>;
  }
  const ui = render(<Probe resource="a" epoch={0} />);
  for (let i = 1; i < 8; i++) ui.rerender(<Probe resource="a" epoch={i} />);
  expect(pending.length).toBe(1);
  await act(async () => pending[0]!.resolve("first"));
  expect(pending.length).toBe(2);
  ui.rerender(<Probe resource="b" epoch={8} />);
  expect(pending[1]!.signal.aborted).toBe(true);
  expect(ui.container.textContent).toBe("loading");
  await act(async () => pending[1]!.resolve("stale"));
  expect(ui.container.textContent).toBe("loading");
  await act(async () => pending[2]!.resolve("correct"));
  expect(ui.container.textContent).toBe("correct");
});
test("dialogs trap focus, dismiss by Escape and restore focus", () => {
  const before = document.createElement("button");
  document.body.append(before);
  before.focus();
  let closed = false;
  const ui = render(
    <Dialog
      title="Edit"
      onClose={() => {
        closed = true;
      }}
    >
      <input aria-label="first" />
      <button>last</button>
    </Dialog>,
  );
  expect(document.activeElement).toBe(ui.getByLabelText("first"));
  ui.getByText("last").focus();
  fireEvent.keyDown(ui.getByText("last"), { key: "Tab" });
  expect(document.activeElement).toBe(ui.getByLabelText("first"));
  fireEvent.keyDown(ui.getByLabelText("first"), { key: "Escape" });
  expect(closed).toBe(true);
  ui.unmount();
  expect(document.activeElement).toBe(before);
  before.remove();
});
test("card links use the dashboard page and exact status vocabulary", () => {
  expect(tablePanelUrl("books")).toBe("/apps/tables/page?table=books");
  expect(rowPanelUrl("books", "9007199254740993")).toContain(
    "row=9007199254740993",
  );
  expect(rowStatusVariant("unpaid")).toBe("neutral");
  expect(rowStatusVariant("inactive")).toBe("neutral");
  expect(rowStatusVariant(" paid ")).toBe("success");
});

test("the panel forwards cursor continuation and exact optimistic edit tokens", async () => {
  const calls: { url: URL; body: any; method: string }[] = [];
  (window as any).__aptevaAppEvents = { subscribe: () => () => {} };
  const row = {
    id: "9007199254740993",
    _revision: 3,
    title: "before",
    note: "",
  };
  globalThis.fetch = (async (input: any, options: any) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({
      url,
      body: options.body ? parseJSON(options.body) : null,
      method: options.method,
    });
    const path = url.pathname;
    if (path.endsWith("/tables"))
      return response({ tables: [{ ...table, id: 41 }], has_more: false });
    if (path.endsWith("/rows"))
      return response({
        rows: [row],
        has_more: !url.searchParams.has("cursor"),
        next_cursor: "opaque-cursor",
        next_offset: 1,
      });
    if (path.endsWith(row.id)) return response({ row, found: true });
    return response({ ...table, id: 41 });
  }) as typeof fetch;
  const ui = render(
    <TablesPanel appName="tables" projectId="p" installId={9} />,
  );
  await flush();
  await flush();
  fireEvent.click(ui.getByText("Next"));
  await flush();
  expect(
    calls.some(
      (call) => call.url.searchParams.get("cursor") === "opaque-cursor",
    ),
  ).toBe(true);
  fireEvent.click(ui.getByText("before"));
  await flush();
  fireEvent.change(ui.getByLabelText("title"), { target: { value: "after" } });
  fireEvent.click(ui.getByText("save"));
  await flush();
  const patch = calls.find((call) => call.method === "PATCH")!;
  expect(patch.url.pathname).toEndWith(row.id);
  expect(patch.url.searchParams.get("expected_revision")).toBe("3");
  expect(patch.url.searchParams.get("expected_table_id")).toBe("41");
  expect(patch.body).toEqual({ title: "after" });
  expect(
    calls
      .filter((call) => call.url.pathname.endsWith("/rows"))
      .every((call) => call.url.searchParams.get("include_total") === "false"),
  ).toBe(true);
});
