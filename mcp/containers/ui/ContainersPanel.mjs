// ui/ContainersPanel.tsx
import { useCallback, useEffect, useState } from "react";
import { jsxDEV } from "react/jsx-dev-runtime";
var API = "/api/apps/containers/api";
var TEST_IMAGES = [
  {
    slug: "nginx",
    label: "Nginx",
    image: "nginx:alpine",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5"
  },
  {
    slug: "whoami",
    label: "Whoami",
    image: "traefik/whoami:v1.10",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "128",
    cpu: "0.25"
  },
  {
    slug: "httpd",
    label: "Apache",
    image: "httpd:alpine",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5"
  },
  {
    slug: "adminer",
    label: "Adminer",
    image: "adminer:latest",
    containerPort: "8080",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5"
  }
];
var inputCls = "bg-surface-2 text-text border border-border rounded px-3 py-2 text-sm " + "placeholder:text-text-dim focus:outline-none focus:ring-1 focus:ring-accent";
function imageName(image) {
  const withoutTag = image.split("@")[0].split(":")[0] || "container";
  const base = withoutTag.split("/").pop() || "container";
  const clean = base.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
  return clean || "container";
}
function autoName(image) {
  const suffix = Math.floor(Date.now() / 1000).toString(36);
  return `test-${imageName(image)}-${suffix}`;
}
function statusClass(status) {
  if (status === "running")
    return "text-green";
  if (status === "creating")
    return "text-blue";
  if (status === "unhealthy" || status === "error")
    return "text-red";
  if (status === "stopped")
    return "text-yellow";
  return "text-text-dim";
}
async function api(path, init) {
  const r = await fetch(`${API}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers || {} }
  });
  if (!r.ok)
    throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
  return await r.json();
}
function ContainersPanel(_props) {
  const [workloads, setWorkloads] = useState([]);
  const [blueprints, setBlueprints] = useState([]);
  const [err, setErr] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [logs, setLogs] = useState(null);
  const [form, setForm] = useState({
    name: "test-nginx",
    image: "nginx:alpine",
    containerPort: "80",
    hostPort: "",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5"
  });
  const load = useCallback(async () => {
    try {
      const [w, b] = await Promise.all([
        api("/workloads"),
        api("/blueprints")
      ]);
      setWorkloads(w.workloads || []);
      setBlueprints(b.blueprints || []);
      setErr("");
    } catch (e) {
      setErr(e.message);
    }
  }, []);
  useEffect(() => {
    load();
    const t = window.setInterval(load, 1e4);
    return () => window.clearInterval(t);
  }, [load]);
  const runSpec = useCallback(async (nextForm) => {
    setBusy("run");
    setNotice("Pulling the image and starting the container. First run can take a minute.");
    try {
      const name = nextForm.name.trim() || autoName(nextForm.image);
      const ports = nextForm.containerPort ? [{
        container_port: Number(nextForm.containerPort),
        host_port: nextForm.hostPort ? Number(nextForm.hostPort) : 0,
        bind_addr: "127.0.0.1",
        protocol: "tcp"
      }] : [];
      await api("/workloads", {
        method: "POST",
        body: JSON.stringify({
          name,
          image: nextForm.image,
          ports,
          health_path: nextForm.healthPath || "/",
          resources: {
            memory_mb: Number(nextForm.memoryMB || 0),
            cpu: Number(nextForm.cpu || 0)
          }
        })
      });
      setNotice(`Queued ${name}. Workload status will update in the list.`);
      setForm((f) => ({ ...f, name: autoName(f.image) }));
      await load();
    } catch (e) {
      setErr(e.message);
      setNotice("");
    } finally {
      setBusy("");
    }
  }, [load]);
  const run = useCallback(async () => {
    await runSpec(form);
  }, [form, runSpec]);
  const fillTestImage = useCallback((preset) => {
    setForm({
      name: autoName(preset.image),
      image: preset.image,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu
    });
  }, []);
  const runTestImage = useCallback(async (preset) => {
    await runSpec({
      name: autoName(preset.image),
      image: preset.image,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu
    });
  }, [runSpec]);
  const action = useCallback(async (id, act) => {
    setBusy(`${act}:${id}`);
    try {
      await api(`/workloads/${encodeURIComponent(id)}/${act}`, { method: "POST" });
      await load();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy("");
    }
  }, [load]);
  const destroy = useCallback(async (id, name) => {
    if (!confirm(`Destroy ${name}? Docker volumes are preserved.`))
      return;
    setBusy(`destroy:${id}`);
    try {
      await api(`/workloads/${encodeURIComponent(id)}`, { method: "DELETE" });
      await load();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy("");
    }
  }, [load]);
  const showLogs = useCallback(async (w) => {
    setBusy(`logs:${w.id}`);
    try {
      const res = await api(`/workloads/${encodeURIComponent(w.id)}/logs?tail=300`);
      setLogs({ name: w.name, body: res.logs || "" });
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy("");
    }
  }, []);
  return /* @__PURE__ */ jsxDEV("div", {
    className: "h-full min-h-0 flex flex-col",
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        className: "px-6 pt-6 pb-3 border-b border-border flex items-center justify-between",
        children: [
          /* @__PURE__ */ jsxDEV("div", {
            children: [
              /* @__PURE__ */ jsxDEV("h1", {
                className: "text-lg font-semibold",
                children: "Containers"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("p", {
                className: "text-xs text-text-dim mt-1",
                children: "Local Docker workloads. Remote hosts, routes, backups, and blueprints come next."
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            className: "btn btn-sm",
            onClick: load,
            disabled: !!busy,
            children: "Refresh"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      err && /* @__PURE__ */ jsxDEV("div", {
        className: "mx-6 mt-4 rounded border border-red/40 bg-red/10 text-red px-3 py-2 text-sm",
        children: err
      }, undefined, false, undefined, this),
      notice && /* @__PURE__ */ jsxDEV("div", {
        className: "mx-6 mt-4 rounded border border-border bg-surface-2 text-text px-3 py-2 text-sm",
        children: notice
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "min-h-0 flex-1 overflow-auto p-6 grid gap-5 lg:grid-cols-[minmax(0,1fr)_360px]",
        children: [
          /* @__PURE__ */ jsxDEV("section", {
            className: "border border-border rounded bg-surface overflow-hidden",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "px-4 py-3 border-b border-border flex justify-between",
                children: [
                  /* @__PURE__ */ jsxDEV("h2", {
                    className: "text-sm font-semibold",
                    children: "Workloads"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("span", {
                    className: "text-xs text-text-dim",
                    children: [
                      workloads.length,
                      " total"
                    ]
                  }, undefined, true, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "divide-y divide-border",
                children: [
                  workloads.length === 0 && /* @__PURE__ */ jsxDEV("div", {
                    className: "p-6 text-sm text-text-dim",
                    children: "No workloads yet."
                  }, undefined, false, undefined, this),
                  workloads.map((w) => /* @__PURE__ */ jsxDEV("div", {
                    className: "p-4 space-y-3",
                    children: [
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "flex items-start justify-between gap-4",
                        children: [
                          /* @__PURE__ */ jsxDEV("div", {
                            children: [
                              /* @__PURE__ */ jsxDEV("div", {
                                className: "font-medium",
                                children: w.name
                              }, undefined, false, undefined, this),
                              /* @__PURE__ */ jsxDEV("div", {
                                className: "text-xs text-text-dim font-mono mt-1",
                                children: w.image
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this),
                          /* @__PURE__ */ jsxDEV("div", {
                            className: "text-right",
                            children: [
                              /* @__PURE__ */ jsxDEV("div", {
                                className: `text-xs uppercase font-semibold ${statusClass(w.status)}`,
                                children: w.status
                              }, undefined, false, undefined, this),
                              /* @__PURE__ */ jsxDEV("div", {
                                className: "text-xs text-text-dim mt-1",
                                children: w.health_status
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "flex flex-wrap gap-2 text-xs",
                        children: [
                          w.public_url && /* @__PURE__ */ jsxDEV("a", {
                            className: "text-accent underline",
                            href: w.public_url,
                            target: "_blank",
                            rel: "noreferrer",
                            children: w.public_url
                          }, undefined, false, undefined, this),
                          w.ports?.map((p) => /* @__PURE__ */ jsxDEV("span", {
                            className: "px-2 py-1 rounded bg-surface-2 border border-border",
                            children: [
                              p.bind_addr,
                              ":",
                              p.host_port,
                              " -> ",
                              p.container_port,
                              "/",
                              p.protocol
                            ]
                          }, `${p.container_port}-${p.host_port}`, true, undefined, this))
                        ]
                      }, undefined, true, undefined, this),
                      w.last_error && /* @__PURE__ */ jsxDEV("div", {
                        className: "text-xs text-red",
                        children: w.last_error
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "flex flex-wrap gap-2",
                        children: [
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs",
                            disabled: !!busy,
                            onClick: () => action(w.id, "start"),
                            children: "Start"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs",
                            disabled: !!busy,
                            onClick: () => action(w.id, "stop"),
                            children: "Stop"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs",
                            disabled: !!busy,
                            onClick: () => action(w.id, "restart"),
                            children: "Restart"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs",
                            disabled: !!busy,
                            onClick: () => action(w.id, "health"),
                            children: "Health"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs",
                            disabled: !!busy,
                            onClick: () => showLogs(w),
                            children: "Logs"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            className: "btn btn-xs text-red",
                            disabled: !!busy,
                            onClick: () => destroy(w.id, w.name),
                            children: "Destroy"
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this)
                    ]
                  }, w.id, true, undefined, this))
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("section", {
            className: "space-y-4",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "border border-border rounded bg-surface p-4 space-y-3",
                children: [
                  /* @__PURE__ */ jsxDEV("h2", {
                    className: "text-sm font-semibold",
                    children: "Quick Tests"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "grid gap-2",
                    children: TEST_IMAGES.map((preset) => /* @__PURE__ */ jsxDEV("div", {
                      className: "rounded border border-border/60 p-3 space-y-2",
                      children: [
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "flex items-start justify-between gap-3",
                          children: [
                            /* @__PURE__ */ jsxDEV("div", {
                              children: [
                                /* @__PURE__ */ jsxDEV("div", {
                                  className: "text-sm font-medium",
                                  children: preset.label
                                }, undefined, false, undefined, this),
                                /* @__PURE__ */ jsxDEV("div", {
                                  className: "text-xs text-text-dim font-mono mt-1",
                                  children: preset.image
                                }, undefined, false, undefined, this)
                              ]
                            }, undefined, true, undefined, this),
                            /* @__PURE__ */ jsxDEV("div", {
                              className: "text-xs text-text-dim shrink-0",
                              children: [
                                preset.containerPort,
                                "/tcp"
                              ]
                            }, undefined, true, undefined, this)
                          ]
                        }, undefined, true, undefined, this),
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "flex gap-2",
                          children: [
                            /* @__PURE__ */ jsxDEV("button", {
                              className: "btn btn-xs",
                              onClick: () => fillTestImage(preset),
                              disabled: !!busy,
                              children: "Fill"
                            }, undefined, false, undefined, this),
                            /* @__PURE__ */ jsxDEV("button", {
                              className: "btn btn-xs btn-primary",
                              onClick: () => runTestImage(preset),
                              disabled: !!busy,
                              children: "Run"
                            }, undefined, false, undefined, this)
                          ]
                        }, undefined, true, undefined, this)
                      ]
                    }, preset.slug, true, undefined, this))
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "border border-border rounded bg-surface p-4 space-y-3",
                children: [
                  /* @__PURE__ */ jsxDEV("h2", {
                    className: "text-sm font-semibold",
                    children: "Run Image"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("input", {
                    className: inputCls,
                    placeholder: "name, e.g. demo-nginx",
                    value: form.name,
                    onChange: (e) => setForm({ ...form, name: e.target.value })
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("input", {
                    className: inputCls,
                    placeholder: "image",
                    value: form.image,
                    onChange: (e) => setForm({ ...form, image: e.target.value })
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "grid grid-cols-2 gap-2",
                    children: [
                      /* @__PURE__ */ jsxDEV("input", {
                        className: inputCls,
                        placeholder: "container port",
                        value: form.containerPort,
                        onChange: (e) => setForm({ ...form, containerPort: e.target.value })
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("input", {
                        className: inputCls,
                        placeholder: "host port auto",
                        value: form.hostPort,
                        onChange: (e) => setForm({ ...form, hostPort: e.target.value })
                      }, undefined, false, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "grid grid-cols-3 gap-2",
                    children: [
                      /* @__PURE__ */ jsxDEV("input", {
                        className: inputCls,
                        placeholder: "/health",
                        value: form.healthPath,
                        onChange: (e) => setForm({ ...form, healthPath: e.target.value })
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("input", {
                        className: inputCls,
                        placeholder: "MB",
                        value: form.memoryMB,
                        onChange: (e) => setForm({ ...form, memoryMB: e.target.value })
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("input", {
                        className: inputCls,
                        placeholder: "CPU",
                        value: form.cpu,
                        onChange: (e) => setForm({ ...form, cpu: e.target.value })
                      }, undefined, false, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("button", {
                    className: "btn btn-primary w-full",
                    onClick: run,
                    disabled: busy === "run" || !form.image,
                    children: busy === "run" ? "Starting container..." : "Run container"
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "border border-border rounded bg-surface p-4",
                children: [
                  /* @__PURE__ */ jsxDEV("h2", {
                    className: "text-sm font-semibold mb-3",
                    children: "Blueprints"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "space-y-2",
                    children: blueprints.map((b) => /* @__PURE__ */ jsxDEV("div", {
                      className: "rounded border border-border/60 p-3",
                      children: [
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "text-sm font-medium",
                          children: b.name
                        }, undefined, false, undefined, this),
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "text-xs text-text-dim mt-1",
                          children: b.description
                        }, undefined, false, undefined, this)
                      ]
                    }, b.slug, true, undefined, this))
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this),
      logs && /* @__PURE__ */ jsxDEV("div", {
        className: "fixed inset-0 bg-black/50 flex items-end justify-center p-6",
        onClick: () => setLogs(null),
        children: /* @__PURE__ */ jsxDEV("div", {
          className: "bg-surface border border-border rounded max-h-[70vh] w-full max-w-5xl flex flex-col",
          onClick: (e) => e.stopPropagation(),
          children: [
            /* @__PURE__ */ jsxDEV("div", {
              className: "px-4 py-3 border-b border-border flex justify-between",
              children: [
                /* @__PURE__ */ jsxDEV("div", {
                  className: "font-medium",
                  children: [
                    "Logs: ",
                    logs.name
                  ]
                }, undefined, true, undefined, this),
                /* @__PURE__ */ jsxDEV("button", {
                  className: "btn btn-xs",
                  onClick: () => setLogs(null),
                  children: "Close"
                }, undefined, false, undefined, this)
              ]
            }, undefined, true, undefined, this),
            /* @__PURE__ */ jsxDEV("pre", {
              className: "p-4 overflow-auto text-xs font-mono whitespace-pre-wrap",
              children: logs.body || "(no logs)"
            }, undefined, false, undefined, this)
          ]
        }, undefined, true, undefined, this)
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
export {
  ContainersPanel as default
};

//# debugId=F9DBDE3BAF17F38A64756E2164756E21
