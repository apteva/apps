// File browser panel — vanilla JS, no bundler.
// Talks to /api/apps/storage/* (proxied by apteva-server).

(function () {
  const params = new URLSearchParams(window.location.search);
  const projectId = params.get("project_id") || "";
  const installId = params.get("install_id") || "";

  const API = "/api/apps/storage";
  let currentFolder = "/";

  function el(tag, props = {}, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function") {
        node.addEventListener(k.slice(2).toLowerCase(), v);
      } else if (v !== undefined && v !== null) {
        node.setAttribute(k, v);
      }
    }
    for (const c of children) {
      if (c == null) continue;
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return node;
  }

  function withQuery(path, extra = {}) {
    const u = new URL(API + path, window.location.origin);
    u.searchParams.set("project_id", projectId);
    if (installId) u.searchParams.set("install_id", installId);
    for (const [k, v] of Object.entries(extra)) {
      if (v !== undefined && v !== null) u.searchParams.set(k, String(v));
    }
    return u.toString();
  }

  async function api(method, path, params, body) {
    const opts = { method, credentials: "same-origin", headers: {} };
    if (body && !(body instanceof FormData)) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    } else if (body) {
      opts.body = body;
    }
    const res = await fetch(withQuery(path, params || {}), opts);
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }

  function setStatus(text) {
    document.getElementById("status").textContent = text;
  }

  function renderBreadcrumb() {
    const nav = document.getElementById("breadcrumb");
    nav.innerHTML = "";
    const parts = currentFolder.split("/").filter(Boolean);
    nav.appendChild(el("span", { onClick: () => navigate("/") }, "/"));
    let acc = "/";
    parts.forEach((p, i) => {
      acc += p + "/";
      nav.appendChild(el("span", { class: "sep" }, " / "));
      const last = i === parts.length - 1;
      const target = acc;
      nav.appendChild(el("span", {
        class: last ? "current" : "",
        onClick: last ? null : () => navigate(target),
      }, p));
    });
  }

  async function load() {
    setStatus("Loading…");
    try {
      const [foldersResp, filesResp] = await Promise.all([
        api("GET", "/folders", { parent: currentFolder }),
        api("GET", "/files", { folder: currentFolder }),
      ]);
      renderBreadcrumb();
      renderFolders(foldersResp.folders || []);
      renderFiles(filesResp.files || []);
      const total = (filesResp.files || []).length;
      const subs = (foldersResp.folders || []).length;
      setStatus(`${total} file${total !== 1 ? "s" : ""} · ${subs} folder${subs !== 1 ? "s" : ""}`);
    } catch (e) {
      setStatus("Error: " + e.message);
    }
  }

  function renderFolders(folders) {
    const root = document.getElementById("folders");
    root.innerHTML = "";
    for (const f of folders) {
      root.appendChild(el("div", {
        class: "folder",
        onClick: () => navigate(currentFolder + f + "/"),
      }, f));
    }
  }

  function renderFiles(files) {
    const root = document.getElementById("files");
    root.innerHTML = "";
    if (files.length === 0) {
      root.appendChild(el("div", { class: "empty" }, "No files in this folder."));
      return;
    }
    for (const f of files) {
      root.appendChild(el("div", { class: "file" },
        el("div", { class: "name", onClick: () => download(f) }, f.name),
        el("div", { class: "meta" }, formatSize(f.size_bytes)),
        el("div", { class: `vis ${f.visibility}` }, f.visibility),
        el("div", { class: "actions" },
          el("button", { onClick: () => share(f) }, "Share"),
          el("button", { class: "danger", onClick: () => del(f) }, "✕"),
        ),
      ));
    }
  }

  function formatSize(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function navigate(folder) {
    currentFolder = folder;
    load();
  }

  async function download(f) {
    const url = withQuery(`/files/${f.id}/content`);
    window.open(url, "_blank");
  }

  async function share(f) {
    try {
      // Mint a 24h signed URL via the metadata field — we POST to
      // /files/:id with a body asking for `signed`. Simpler: just
      // copy the content URL with a signature embedded server-side
      // by flipping visibility to signed and reading back.
      // For v0.1 we stay declarative — flip to signed via PATCH.
      await api("PATCH", `/files/${f.id}`, null, { visibility: "signed" });
      const fresh = await api("GET", `/files/${f.id}`);
      const url = window.location.origin + withQuery(`/files/${f.id}/content`);
      await navigator.clipboard.writeText(url).catch(() => {});
      alert(`Marked signed. URL copied to clipboard:\n${url}`);
      load();
    } catch (e) {
      alert("Share failed: " + e.message);
    }
  }

  async function del(f) {
    if (!confirm(`Delete ${f.name}?`)) return;
    try {
      await api("DELETE", `/files/${f.id}`);
      load();
    } catch (e) {
      alert("Delete failed: " + e.message);
    }
  }

  document.getElementById("upload").addEventListener("change", async (ev) => {
    const files = Array.from(ev.target.files);
    if (files.length === 0) return;
    setStatus(`Uploading ${files.length} file${files.length !== 1 ? "s" : ""}…`);
    for (const file of files) {
      const fd = new FormData();
      fd.append("file", file);
      fd.append("folder", currentFolder);
      try {
        await fetch(withQuery("/files"), {
          method: "POST", credentials: "same-origin", body: fd,
        });
      } catch (e) {
        setStatus("Upload error: " + e.message);
      }
    }
    ev.target.value = "";
    load();
  });

  document.getElementById("mk").addEventListener("click", async () => {
    const name = document.getElementById("newfolder").value.trim();
    if (!name) return;
    // S3-style: a folder exists when a file does. Drop a placeholder.
    const fd = new FormData();
    fd.append("file", new Blob([""], { type: "text/plain" }), ".placeholder");
    fd.append("folder", currentFolder + name + "/");
    try {
      await fetch(withQuery("/files"), { method: "POST", credentials: "same-origin", body: fd });
      document.getElementById("newfolder").value = "";
      load();
    } catch (e) {
      alert("Create folder failed: " + e.message);
    }
  });

  if (!projectId) {
    document.body.innerHTML =
      '<div class="empty">Missing project_id in URL — the dashboard should provide one.</div>';
  } else {
    load();
  }
})();
