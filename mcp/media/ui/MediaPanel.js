// Iframe-fallback panel for the media app. The dashboard renders the
// React MediaPanel by default; this is what shows up if you open the
// panel URL directly or install media into a host that doesn't have a
// native registration.

(function () {
  const params = new URLSearchParams(window.location.search);
  const projectId = params.get("project_id") || "";
  const installId = params.get("install_id") || "";
  const API = "/api/apps/media";
  const STORAGE = "/api/apps/storage";

  function withQuery(path, base) {
    const u = new URL((base || API) + path, window.location.origin);
    if (projectId) u.searchParams.set("project_id", projectId);
    if (installId) u.searchParams.set("install_id", installId);
    return u.toString();
  }

  async function load() {
    const root = document.getElementById("root");
    try {
      const res = await fetch(withQuery("/media"), { credentials: "same-origin" });
      const data = await res.json();
      const rows = data.media || [];
      if (rows.length === 0) {
        root.className = "empty";
        root.textContent = "No indexed media yet. Upload audio/video/image to storage and the indexer will pick it up.";
        return;
      }
      root.className = "grid";
      root.innerHTML = "";
      for (const r of rows) {
        const tile = document.createElement("div");
        tile.className = "tile";
        const thumb = (r.derivations || []).find((d) => d.kind === "thumbnail" || d.kind === "waveform");
        if (thumb) {
          const img = document.createElement("img");
          img.src = withQuery("/files/" + thumb.storage_file_id + "/content", STORAGE);
          tile.appendChild(img);
        }
        const h = document.createElement("h3");
        h.textContent = r.file_id;
        tile.appendChild(h);
        const meta = document.createElement("div");
        meta.className = "meta";
        const bits = [];
        if (r.duration_ms) bits.push(formatDuration(r.duration_ms));
        if (r.width && r.height) bits.push(`${r.width}×${r.height}`);
        if (r.video_codec) bits.push(r.video_codec);
        if (r.audio_codec) bits.push(r.audio_codec);
        meta.textContent = bits.join(" · ") || "—";
        tile.appendChild(meta);
        root.appendChild(tile);
      }
    } catch (e) {
      root.className = "empty";
      root.textContent = "Error: " + e.message;
    }
  }

  function formatDuration(ms) {
    const s = Math.round(ms / 1000);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = s % 60;
    if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
    return `${m}:${String(sec).padStart(2, "0")}`;
  }

  if (!projectId) {
    document.getElementById("root").textContent =
      "Missing project_id — the dashboard should provide one.";
  } else {
    load();
  }
})();
