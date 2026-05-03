// Trading panel — vanilla DOM. Reads project_id from the URL the
// platform supplied; calls /api/apps/trading/* same-origin so cookies
// + session token authenticate every request.

(function () {
  const params = new URLSearchParams(window.location.search);
  const projectId = params.get("project_id") || "";
  const installId = params.get("install_id") || "";

  // Reverse-proxied at /api/apps/trading.
  const API = "/api/apps/trading";

  const state = {
    portfolios: [],            // [{id, name, allowed_classes, ...}]
    selectedId: null,
  };

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
      if (c == null || c === false) continue;
      node.appendChild(typeof c === "string" || typeof c === "number" ? document.createTextNode(String(c)) : c);
    }
    return node;
  }

  function withProject(path) {
    const u = new URL(API + path, window.location.origin);
    if (projectId) u.searchParams.set("project_id", projectId);
    if (installId) u.searchParams.set("install_id", installId);
    return u.toString();
  }
  async function api(path) {
    const r = await fetch(withProject(path), { credentials: "same-origin" });
    if (!r.ok) throw new Error(`${r.status}: ${(await r.text()) || r.statusText}`);
    return r.json();
  }

  // ── Format helpers ──────────────────────────────────────────────
  const usd0 = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 0, maximumFractionDigits: 0 });
  const usd2 = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const fmtMoney  = (n) => usd2.format(n);
  const fmtMoney0 = (n) => usd0.format(n);
  const fmtMoneySigned = (n) => (n > 0 ? "+" : n < 0 ? "−" : "") + usd2.format(Math.abs(n));
  const fmtPctSigned = (n, d = 2) => (n > 0 ? "+" : n < 0 ? "−" : "") + Math.abs(n).toFixed(d) + "%";
  const fmtQty = (n) => (Math.abs(n) >= 1 ? n.toLocaleString("en-US", { maximumFractionDigits: 0 }) : n.toFixed(4));
  const fmtPolyPrice = (n) => (n * 100).toFixed(0) + "¢";
  const timeAgo = (iso) => {
    const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) return s + "s";
    const m = Math.floor(s / 60);
    if (m < 60) return m + "m";
    const h = Math.floor(m / 60);
    if (h < 24) return h + "h";
    return Math.floor(h / 24) + "d";
  };
  const symLabel = (sym) => sym.startsWith("POLY:") ? sym.slice(5) : sym;
  const isPoly = (cls) => cls === "polymarket";

  // ── Initial bootstrap ───────────────────────────────────────────
  async function init() {
    try {
      const r = await api("/portfolios");
      state.portfolios = r.portfolios || [];
    } catch (e) {
      renderError("Failed to load portfolios: " + e.message);
      return;
    }
    if (state.portfolios.length === 0) {
      renderEmpty();
      return;
    }
    state.selectedId = state.portfolios[0].id;
    renderHeader();
    await refreshDetail();
    // Event-driven updates + 30s heartbeat as a fallback for missed
    // events (browser tab suspended, SSE briefly dropped).
    subscribeAppEvents();
    setInterval(refreshDetail, 30000);
  }

  // ── App-event subscription ─────────────────────────────────────
  // Same shape storage uses: SSE on /api/app-events/<app>,
  // sequence-replay on reconnect, dispatch by topic.
  function subscribeAppEvents() {
    let lastSeq = 0;
    let es = null;
    let reconnectTimer = null;
    const url = () => {
      let u = `/api/app-events/trading?project_id=${encodeURIComponent(projectId)}`;
      if (lastSeq > 0) u += `&since=${lastSeq}`;
      return u;
    };
    const connect = () => {
      es = new EventSource(url(), { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data);
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handleEvent(ev);
        } catch (err) { console.warn("[trading-panel] bad event", err); }
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) clearTimeout(reconnectTimer);
          reconnectTimer = setTimeout(connect, 2000);
        }
      };
    };
    connect();
  }

  function handleEvent(ev) {
    const sel = state.selectedId;
    const pid = ev.data && (ev.data.portfolio_id || ev.data.id);
    switch (ev.topic) {
      case "tick":
        // Tick is the only high-frequency event. Update the data-pill
        // in place; refresh detail since marks moved (positions + equity drift).
        if (ev.data && ev.data.providers) {
          renderDataPill({ providers: ev.data.providers });
        }
        refreshDetail();
        return;
      case "portfolio.created":
      case "portfolio.status.changed":
        api("/portfolios").then((r) => {
          state.portfolios = r.portfolios || [];
          renderHeader();
        }).catch(() => {});
        if (pid === sel) refreshDetail();
        return;
      case "order.placed":
      case "order.filled":
      case "order.cancelled":
      case "order.rejected":
      case "position.changed":
      case "journal.appended":
      case "watchlist.changed":
      case "alert.fired":
        if (pid && pid !== sel) return;
        refreshDetail();
        return;
      default:
        return;
    }
  }

  function renderEmpty() {
    document.body.innerHTML = "";
    document.body.appendChild(el("div", { class: "empty" }, "No portfolios in this project yet. Create one through an agent or from the Trading desk."));
  }

  function renderError(msg) {
    document.body.innerHTML = "";
    document.body.appendChild(el("div", { class: "empty" }, msg));
  }

  function renderHeader() {
    const sel = document.getElementById("portfolio-select");
    sel.innerHTML = "";
    for (const p of state.portfolios) {
      sel.appendChild(el("option", { value: p.id }, p.name));
    }
    sel.value = state.selectedId;
    sel.onchange = async () => {
      state.selectedId = parseInt(sel.value, 10);
      await refreshDetail();
    };
  }

  async function refreshDetail() {
    const id = state.selectedId;
    if (id == null) return;
    try {
      const [pfRes, posRes, ordRes, journalRes, healthRes] = await Promise.all([
        api(`/portfolios/${id}`),
        api(`/portfolios/${id}/positions`),
        api(`/portfolios/${id}/orders?limit=10`),
        api(`/portfolios/${id}/journal?limit=10`),
        api(`/healthz/details`).catch(() => null),
      ]);
      renderPortfolio(pfRes.portfolio);
      renderPositions(posRes.positions || []);
      renderOrders(ordRes.orders || []);
      renderJournal(journalRes.entries || []);
      renderDataPill(healthRes);
      document.getElementById("updated-at").textContent = "Updated " + new Date().toLocaleTimeString();
    } catch (e) {
      console.error(e);
    }
  }

  // Show a single pill summarising data freshness for this portfolio.
  // We pick the most-relevant asset class from the portfolio's
  // allowed_classes and report its provider. Live = up, mock = muted,
  // stale = warn.
  function renderDataPill(health) {
    const pill = document.getElementById("data-pill");
    if (!pill) return;
    if (!health || !health.providers) {
      pill.textContent = "";
      pill.className = "data-pill";
      return;
    }
    const sel = state.portfolios.find((p) => p.id === state.selectedId);
    const cls = (sel?.allowed_classes || []).find((c) => health.providers[c]) || "crypto";
    const cls_h = health.providers[cls] || {};
    const name = (cls_h.name || "mock").replace("-public", "");
    const stale = !!cls_h.stale;
    const isLive = name !== "mock" && !stale;

    pill.className = "data-pill " + (stale ? "stale" : isLive ? "live" : "");
    pill.innerHTML = "";
    pill.appendChild(el("span", { class: "dot" }));
    pill.appendChild(document.createTextNode(`${cls} · ${stale ? "stale" : name}`));
    pill.title = `Provider for ${cls}: ${cls_h.name || "mock"}` + (cls_h.errors_60s ? `  (errors_60s=${cls_h.errors_60s})` : "");
  }

  function renderPortfolio(p) {
    const status = document.getElementById("portfolio-status");
    status.className = "status-pill " + p.status;
    status.textContent = p.status;

    const stats = document.getElementById("stats");
    stats.innerHTML = "";
    stats.appendChild(stat("Equity", fmtMoney(p.equity)));
    stats.appendChild(stat("Day P&L", fmtMoneySigned(p.day_pnl), p.day_pnl >= 0 ? "up" : "down", fmtPctSigned(p.day_pnl_pct)));
    stats.appendChild(stat("Open P&L", fmtMoneySigned(p.open_pnl), p.open_pnl >= 0 ? "up" : "down", fmtPctSigned(p.open_pnl_pct)));
    stats.appendChild(stat("Cash", fmtMoney0(p.cash)));
  }

  function stat(label, value, tone, sub) {
    return el("div", { class: "stat" },
      el("div", { class: "label" }, label),
      el("div", { class: "value " + (tone || "") },
        value,
        sub ? el("span", { class: "sub " + (tone || "") }, sub) : null,
      ),
    );
  }

  function renderPositions(rows) {
    const body = document.getElementById("positions-body");
    body.innerHTML = "";
    if (rows.length === 0) {
      body.appendChild(el("tr", {}, el("td", { colspan: 6, class: "num", style: "color:var(--tertiary)" }, "No positions.")));
      return;
    }
    for (const p of rows) {
      const up = p.unrealized_pnl >= 0;
      const fmtPrice = isPoly(p.asset_class) ? fmtPolyPrice : fmtMoney;
      const sym = isPoly(p.asset_class) ? `${p.outcome} ${symLabel(p.symbol)}` : p.symbol;
      body.appendChild(el("tr", {},
        el("td", {}, sym),
        el("td", { class: "num" }, fmtQty(p.qty)),
        el("td", { class: "num" }, fmtPrice(p.market_price)),
        el("td", { class: "num" }, fmtMoney(p.market_value)),
        el("td", { class: "num " + (up ? "up" : "down") },
          fmtMoneySigned(p.unrealized_pnl) + " ",
          el("span", { style: "font-size:10px;opacity:0.8" }, fmtPctSigned(p.unrealized_pnl_pct)),
        ),
        el("td", { class: "num" }, p.weight_pct.toFixed(1) + "%"),
      ));
    }
  }

  function renderOrders(rows) {
    const body = document.getElementById("orders-body");
    body.innerHTML = "";
    if (rows.length === 0) {
      body.appendChild(el("tr", {}, el("td", { colspan: 6, class: "num", style: "color:var(--tertiary)" }, "No orders.")));
      return;
    }
    for (const o of rows) {
      const sym = isPoly(o.asset_class) ? symLabel(o.symbol) : o.symbol;
      body.appendChild(el("tr", {},
        el("td", { style: "color:var(--tertiary);font-size:10px" }, o.id),
        el("td", {}, sym),
        el("td", {}, el("span", { class: "pill " + o.side }, o.side.toUpperCase())),
        el("td", { style: "font-size:10px;color:var(--muted)" }, o.type),
        el("td", { class: "num" }, fmtQty(o.qty)),
        el("td", {}, el("span", { class: "pill " + o.status }, o.status)),
      ));
    }
  }

  function renderJournal(entries) {
    const list = document.getElementById("journal-body");
    list.innerHTML = "";
    if (entries.length === 0) {
      list.appendChild(el("li", { class: "empty" }, "No journal entries yet."));
      return;
    }
    for (const e of entries) {
      list.appendChild(el("li", {},
        el("span", { class: "kind " + e.kind }, e.kind),
        el("span", { class: "body" }, e.body),
        el("span", { class: "when" }, timeAgo(e.created_at)),
      ));
    }
  }

  init();
})();
