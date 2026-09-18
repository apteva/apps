import { useEffect, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/graphql";

export default function GraphQLPanel({ projectId }) {
  const [schema, setSchema] = useState(null);
  const [environment, setEnvironment] = useState("development");
  const [sdl, setSdl] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const url = `${API}/admin/schema?project_id=${encodeURIComponent(projectId)}&environment=${encodeURIComponent(environment)}`;
    fetch(url, { credentials: "same-origin" })
      .then(async (response) => {
        // A new environment has no published schema yet. That is an
        // editable empty state, not a panel error.
        if (response.status === 404) return { schema: null };
        if (!response.ok) {
          let message = `${response.status}`;
          try {
            const body = await response.json();
            message = body.error || message;
          } catch {
            // Keep the HTTP status when the response is not JSON.
          }
          throw new Error(message);
        }
        return response.json();
      })
      .then((body) => {
        setSchema(body.schema || null);
        setSdl(body.schema?.sdl || "");
        setError("");
      })
      .catch((loadError) => setError(loadError.message));
  }, [projectId, environment]);

  async function saveDraft(event) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const response = await fetch(
        `${API}/admin/schema?project_id=${encodeURIComponent(projectId)}&environment=${encodeURIComponent(environment)}`,
        {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sdl }),
        },
      );
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || "save failed");
      setSchema(body.schema || null);
    } catch (saveError) {
      setError(saveError.message);
    } finally {
      setSaving(false);
    }
  }

  async function publish() {
    if (!schema) return;
    setSaving(true);
    setError("");
    try {
      const response = await fetch(
        `${API}/admin/schema/publish?project_id=${encodeURIComponent(projectId)}&environment=${encodeURIComponent(environment)}`,
        {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ version: schema.version }),
        },
      );
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || "publish failed");
      setSchema(body.schema || null);
    } catch (publishError) {
      setError(publishError.message);
    } finally {
      setSaving(false);
    }
  }

  return jsxs("div", {
    className: "h-full flex flex-col bg-bg text-text",
    children: [
      jsxs("div", {
        className: "px-6 pt-6 pb-3 flex items-center justify-between border-b border-border",
        children: [
          jsx("h1", { className: "text-lg font-semibold", children: "GraphQL" }),
          jsxs("div", {
            className: "flex items-center gap-2 text-sm",
            children: [
              jsx("label", { children: "Environment" }),
              jsxs("select", {
                className: "bg-surface-2 border border-border rounded px-2 py-1",
                value: environment,
                onChange: (event) => setEnvironment(event.target.value),
                children: [
                  jsx("option", { children: "development", value: "development" }, "development"),
                  jsx("option", { children: "staging", value: "staging" }, "staging"),
                  jsx("option", { children: "production", value: "production" }, "production"),
                ],
              }),
            ],
          }),
        ],
      }),
      error && jsx("div", {
        className: "m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm",
        children: error,
      }),
      jsxs("form", {
        className: "p-6 space-y-4",
        onSubmit: saveDraft,
        children: [
          jsx("textarea", {
            className: "w-full min-h-[360px] bg-surface-2 border border-border rounded p-3 font-mono text-sm",
            value: sdl,
            onChange: (event) => setSdl(event.target.value),
            placeholder: "type Query { hello: String! }",
          }),
          jsxs("div", {
            className: "flex items-center gap-3",
            children: [
              jsx("button", {
                className: "px-3 py-1.5 rounded bg-accent text-white disabled:opacity-50",
                disabled: saving,
                type: "submit",
                children: saving ? "Saving…" : "Save draft",
              }),
              jsx("button", {
                className: "px-3 py-1.5 rounded border border-border disabled:opacity-50",
                disabled: saving || !schema || schema.status === "invalid",
                type: "button",
                onClick: publish,
                children: "Publish",
              }),
              schema && jsx("span", {
                className: "text-sm text-text-dim",
                children: `v${schema.version} · ${schema.status}`,
              }),
            ],
          }),
        ],
      }),
    ],
  });
}
