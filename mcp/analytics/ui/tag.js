/* Apteva Analytics — static-site tag.
 *
 * Drop on any website:
 *   <script async src="https://YOUR-APTEVA/api/apps/analytics/ui/tag.js"
 *           data-key="wk_live_..."></script>
 *
 * Auto-fires a page_view on load and on SPA route changes. It also records
 * outbound clicks on external links, like GA4 Enhanced Measurement. Custom events:
 *   apa("signup", { plan: "pro" });
 *
 * Acquisition: utm_source / utm_medium / utm_campaign / utm_term /
 * utm_content and external referrers become "touches". The first touch and
 * the latest touch are kept (each expires after data-attribution-days,
 * default 90) and attached to every event as utm_* / touch_* and
 * first_utm_* / first_* props. Direct visits never overwrite a touch.
 *
 * Optional attributes:
 *   data-consent="required"   keep nothing on the device (no localStorage, no
 *                             cookie) until apa("consent", "granted"); events
 *                             are still sent with per-page identifiers.
 *                             apa("consent", "denied") wipes stored state.
 *   data-attribution-days="N" touch lifetime in days (1-730, default 90).
 *   data-cookie="true"        also mirror the visitor ID into a first-party
 *                             apa_vid cookie so the site's backend can read it
 *                             and call analytics_identify.
 *   data-cookie-domain=".example.com"  cookie scope (implies data-cookie);
 *                             referrers inside it are not counted as touches.
 *   data-linker="app.example.com,shop.example.org"  hand the visitor and
 *                             session to these hosts on link clicks, through a
 *                             short-lived _apa URL parameter the receiving tag
 *                             removes from the address bar.
 *
 * apa.visitorId() returns the anonymous visitor ID and apa.attribution() the
 * current touches. Linking a visitor to a signed-in user happens server-side
 * (analytics_identify); the browser never asserts who the user is.
 *
 * Sends GET /collect (no-cors, keepalive) so it works cross-origin with
 * no CORS preflight; falls back to an image pixel. Deliveries that fail at
 * the network level are queued (max 50, 24 h) and retried on the next page
 * load or when the browser comes back online; the server drops duplicates by
 * event ID. The write key is the only credential and is write-only — safe to
 * ship in page source.
 */
(function () {
  "use strict";

  if (window.apa && window.apa.loaded) return;

  var self =
    document.currentScript ||
    (function () {
      var els = document.getElementsByTagName("script");
      for (var i = els.length - 1; i >= 0; i--) {
        if (els[i].src && els[i].src.indexOf("/ui/tag.js") !== -1)
          return els[i];
      }
      return null;
    })();
  if (!self) return;

  function attr(name) {
    return self.getAttribute(name) || "";
  }
  var key = attr("data-key");
  var base = self.src.split("?")[0].replace(/\/ui\/tag\.js$/, "");
  var COLLECT = base + "/collect";
  if (!key) {
    try {
      console.warn("[apteva-analytics] missing data-key on tag.js");
    } catch (e) {}
  }

  var DAY = 24 * 60 * 60 * 1000;
  var attributionDays = parseInt(attr("data-attribution-days"), 10);
  if (!(attributionDays >= 1 && attributionDays <= 730)) attributionDays = 90;
  var cookieDomain = attr("data-cookie-domain").replace(/^\.+/, "").toLowerCase();
  var useCookie = attr("data-cookie") === "true" || !!cookieDomain;
  var linkerHosts = attr("data-linker")
    .toLowerCase()
    .split(",")
    .map(function (h) {
      return h.replace(/^\s+|\s+$/g, "");
    })
    .filter(Boolean);
  var persist = attr("data-consent") !== "required";

  // ─── storage (gated on consent) ─────────────────────────────────────
  var KEYS = ["apa_vid", "apa_sid", "apa_session_v2", "apa_attr_v1", "apa_queue_v1"];
  function load(k) {
    if (!persist) return null;
    try {
      return localStorage.getItem(k);
    } catch (e) {
      return null;
    }
  }
  function save(k, v) {
    if (!persist) return;
    try {
      localStorage.setItem(k, v);
    } catch (e) {}
  }
  function loadJSON(k) {
    try {
      return JSON.parse(load(k) || "null");
    } catch (e) {
      return null;
    }
  }
  function writeCookie(value, maxAge) {
    try {
      document.cookie =
        "apa_vid=" +
        encodeURIComponent(value) +
        "; Path=/; Max-Age=" +
        maxAge +
        "; SameSite=Lax" +
        (cookieDomain ? "; Domain=" + cookieDomain : "") +
        (location.protocol === "https:" ? "; Secure" : "");
    } catch (e) {}
  }

  function rand() {
    return "s_" + Math.random().toString(36).slice(2) + Date.now().toString(36);
  }
  var ID = /^s_[a-z0-9]{6,40}$/;

  function hostOf(u) {
    try {
      return new URL(u).hostname.toLowerCase();
    } catch (e) {
      return "";
    }
  }
  var hereHost = String(location.hostname || location.host || "").toLowerCase();
  function internalHost(h) {
    if (!h) return false;
    if (h === hereHost || linkerHosts.indexOf(h) !== -1) return true;
    return (
      !!cookieDomain && (h === cookieDomain || h.slice(-cookieDomain.length - 1) === "." + cookieDomain)
    );
  }
  function queryParams() {
    var out = {};
    var raw = String(location.search || "").replace(/^\?/, "");
    if (!raw) return out;
    var parts = raw.split("&");
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf("=");
      var k = eq === -1 ? parts[i] : parts[i].slice(0, eq);
      var v = eq === -1 ? "" : parts[i].slice(eq + 1);
      try {
        k = decodeURIComponent(k.replace(/\+/g, " "));
        v = decodeURIComponent(v.replace(/\+/g, " "));
      } catch (e) {
        continue;
      }
      if (!(k in out)) out[k] = v;
    }
    return out;
  }

  // ─── cross-domain handoff (incoming) ────────────────────────────────
  // Runs before history is patched so removing _apa is not a route change.
  // The parameter is only honoured for a few minutes and only when the
  // referrer is absent or an approved host, so a shared link cannot merge
  // strangers into one visitor.
  var handoff = null;
  (function () {
    var raw = queryParams()._apa;
    if (raw === undefined) return;
    var parts = String(raw).split(".");
    var ref = document.referrer ? hostOf(document.referrer) : "";
    var at = parseInt(parts[2] || "", 36);
    if (
      parts.length === 3 &&
      ID.test(parts[0]) &&
      ID.test(parts[1]) &&
      Math.abs(Date.now() - at) < 5 * 60 * 1000 &&
      (!ref || linkerHosts.indexOf(ref) !== -1)
    )
      handoff = { visitor: parts[0], session: parts[1] };
    if (history.replaceState) {
      try {
        var search = String(location.search || "")
          .replace(/^\?/, "")
          .split("&")
          .filter(function (p) {
            return p && p.split("=")[0] !== "_apa";
          })
          .join("&");
        history.replaceState(
          history.state,
          "",
          location.pathname + (search ? "?" + search : "") + (location.hash || ""),
        );
      } catch (e) {}
    }
  })();

  // ─── visitor + session ──────────────────────────────────────────────
  // v2 sessions expire after 30 minutes of inactivity. Legacy apa_sid is
  // retained as visitor identity so historical sessions are not rewritten.
  var visitor = (handoff && handoff.visitor) || load("apa_vid") || load("apa_sid") || rand();
  var session = handoff ? { id: handoff.session, last: Date.now() } : { id: rand(), last: 0 };
  function saveVisitor() {
    save("apa_vid", visitor);
    if (persist && useCookie) writeCookie(visitor, 400 * 24 * 60 * 60);
  }
  saveVisitor();
  if (handoff) save("apa_session_v2", JSON.stringify(session));
  function sessionID() {
    var now = Date.now();
    var shared = loadJSON("apa_session_v2");
    if (shared && typeof shared.id === "string" && Number.isFinite(shared.last)) session = shared;
    if (!session.last || now - session.last >= 30 * 60 * 1000 || now < session.last)
      session = { id: rand(), last: now };
    session.last = now;
    save("apa_session_v2", JSON.stringify(session));
    return session.id;
  }

  // ─── attribution ────────────────────────────────────────────────────
  var UTM = ["source", "medium", "campaign", "term", "content"];
  var touches = loadJSON("apa_attr_v1") || {};
  function fresh(t, now) {
    return t && typeof t === "object" && Number.isFinite(t.ts) && now - t.ts < attributionDays * DAY && now >= t.ts
      ? t
      : null;
  }
  // withReferrer is true only for the landing page: document.referrer does
  // not change on SPA route changes, so later routes only read UTMs.
  function observeTouch(withReferrer) {
    var now = Date.now();
    var q = queryParams();
    var t = {},
      found = false;
    for (var i = 0; i < UTM.length; i++) {
      var v = q["utm_" + UTM[i]];
      if (v) {
        t[UTM[i]] = String(v).slice(0, 200);
        found = true;
      }
    }
    var ref = withReferrer && document.referrer ? hostOf(document.referrer) : "";
    if (ref && !internalHost(ref)) {
      t.referrer = ref;
      found = true;
    }
    var first = fresh(touches.first, now),
      last = fresh(touches.last, now);
    if (found) {
      t.landing = location.pathname;
      t.ts = now;
      last = t;
      if (!first) first = t;
    }
    touches = {};
    if (first) touches.first = first;
    if (last) touches.last = last;
    save("apa_attr_v1", JSON.stringify(touches));
  }
  function attributionProps() {
    var out = {};
    function put(prefix, t) {
      if (!t) return;
      for (var i = 0; i < UTM.length; i++)
        if (t[UTM[i]]) out[prefix + "utm_" + UTM[i]] = t[UTM[i]];
      var p = prefix || "touch_";
      if (t.referrer) out[p + "referrer"] = t.referrer;
      if (t.landing) out[p + "landing"] = t.landing;
      out[p + (prefix ? "touch_ts" : "ts")] = t.ts;
    }
    put("", touches.last);
    put("first_", touches.first);
    return out;
  }
  observeTouch(true);

  // ─── device ─────────────────────────────────────────────────────────
  function device() {
    var ua = navigator.userAgent || "";
    if (/iPad|Tablet/i.test(ua)) return "tablet";
    if (/Mobi|Android|iPhone|iPod/i.test(ua)) return "mobile";
    return "desktop";
  }
  function platform() {
    var ua = navigator.userAgent || "";
    if (/Android/i.test(ua)) return "android";
    if (/iPhone|iPad|iPod/i.test(ua)) return "ios";
    return "web";
  }

  function anchorFromTarget(target) {
    var node = target;
    while (node && node !== document) {
      if (node.tagName && String(node.tagName).toLowerCase() === "a") return node;
      node = node.parentNode;
    }
    return null;
  }

  function textOf(anchor) {
    var text = anchor && (anchor.textContent || anchor.innerText || "");
    return String(text).replace(/\s+/g, " ").trim().slice(0, 200);
  }

  // ─── delivery + retry queue ─────────────────────────────────────────
  var QUEUE_MAX = 50;
  function enqueue(url) {
    if (!persist) return;
    var q = loadJSON("apa_queue_v1");
    if (!Array.isArray(q)) q = [];
    q.push({ u: url, t: Date.now() });
    save("apa_queue_v1", JSON.stringify(q.slice(-QUEUE_MAX)));
  }
  function deliver(url, queuedAt) {
    // A retried event carries its age so the server can date it correctly
    // without trusting the device clock.
    var target = queuedAt ? url + "&age=" + Math.max(0, Date.now() - queuedAt) : url;
    function failed() {
      enqueue(url);
    }
    function fallback() {
      try {
        var img = new Image();
        img.onerror = failed;
        img.src = target;
      } catch (e) {
        failed();
      }
    }
    try {
      fetch(target, {
        method: "GET",
        mode: "no-cors",
        keepalive: true,
        credentials: "omit",
      }).catch(fallback);
    } catch (e) {
      fallback();
    }
  }
  function flushQueue() {
    var q = loadJSON("apa_queue_v1");
    if (!Array.isArray(q) || !q.length) return;
    save("apa_queue_v1", "[]");
    var now = Date.now();
    for (var i = 0; i < q.length; i++) {
      if (q[i] && typeof q[i].u === "string" && now - q[i].t < DAY && now >= q[i].t) deliver(q[i].u, q[i].t);
    }
  }

  function here() {
    return {
      url: location.origin + location.pathname,
      host: location.host,
      path: location.pathname,
    };
  }

  // loc pins the page a deferred event belongs to; it defaults to the current one.
  function send(event, props, loc) {
    if (!key) return;
    loc = loc || here();
    var params = {
      k: key,
      e: event || "page_view",
      sid: sessionID(),
      eid: rand(),
      url: loc.url,
      host: loc.host,
      path: loc.path,
      ref: document.referrer ? hostOf(document.referrer) : "",
      title: document.title || "",
      lang: navigator.language || "",
      device: device(),
      platform: platform(),
      screen: (screen.width || 0) + "x" + (screen.height || 0),
    };
    var extra = attributionProps();
    if (props && typeof props === "object") {
      for (var pk in props) if (Object.prototype.hasOwnProperty.call(props, pk)) extra[pk] = props[pk];
    }
    extra.visitor_id = visitor;
    extra.session_version = 2;
    try {
      params.p = JSON.stringify(extra);
    } catch (e) {
      params.p = JSON.stringify({ visitor_id: visitor, session_version: 2 });
    }
    var qs = [];
    for (var k in params) {
      if (params[k] === "" || params[k] == null) continue;
      qs.push(encodeURIComponent(k) + "=" + encodeURIComponent(params[k]));
    }
    var url = COLLECT + "?" + qs.join("&");
    if (url.length > 16000) return;
    deliver(url, 0);
  }

  // ─── consent ────────────────────────────────────────────────────────
  function consent(state) {
    if (state === "granted") {
      if (persist) return;
      persist = true;
      saveVisitor();
      save("apa_session_v2", JSON.stringify(session));
      save("apa_attr_v1", JSON.stringify(touches));
    } else if (state === "denied") {
      persist = false;
      for (var i = 0; i < KEYS.length; i++) {
        try {
          localStorage.removeItem(KEYS[i]);
        } catch (e) {}
      }
      // Persisted values may outlive this page, so start over in memory too.
      visitor = rand();
      session = { id: rand(), last: 0 };
      if (useCookie) writeCookie("", 0);
    }
  }

  // Public API. Drains any queued calls (apa.q) made before load.
  var queued = window.apa && window.apa.q ? window.apa.q : [];
  window.apa = function (event, props) {
    if (event === "consent") return consent(props);
    send(event, props);
  };
  window.apa.loaded = true;
  window.apa.visitorId = function () {
    return visitor;
  };
  window.apa.attribution = function () {
    return JSON.parse(JSON.stringify(touches));
  };
  for (var i = 0; i < queued.length; i++) {
    try {
      window.apa(queued[i][0], queued[i][1]);
    } catch (e) {}
  }

  // Auto page_view: initial + SPA route changes. Routers call pushState
  // before rendering the new page, so a route change's page_view waits briefly
  // for document.title to catch up. The URL is captured at the route change;
  // a newer route change or page exit sends the pending one immediately.
  send("page_view");
  flushQueue();
  window.addEventListener("online", flushQueue);
  var lastPath = location.pathname + location.search;
  var pendingPV = null,
    pendingTimer = null;
  function flushPV() {
    if (!pendingPV) return;
    var loc = pendingPV;
    pendingPV = null;
    if (pendingTimer && typeof clearTimeout === "function") clearTimeout(pendingTimer);
    pendingTimer = null;
    send("page_view", null, loc);
  }
  function maybePV() {
    var p = location.pathname + location.search;
    if (p === lastPath) return;
    lastPath = p;
    flushPV();
    observeTouch(false);
    pendingPV = here();
    if (typeof setTimeout === "function") pendingTimer = setTimeout(flushPV, 100);
    else flushPV();
  }
  window.addEventListener("pagehide", flushPV);
  var push = history.pushState;
  if (push)
    history.pushState = function () {
      push.apply(this, arguments);
      maybePV();
    };
  var replace = history.replaceState;
  if (replace)
    history.replaceState = function () {
      replace.apply(this, arguments);
      maybePV();
    };
  window.addEventListener("popstate", maybePV);

  // GA4-style Enhanced Measurement for outbound links. Delegation means
  // dynamically rendered links are covered too, and capture phase lets us
  // observe a click even when another handler stops propagation. The event
  // is deliberately generic; callers can filter by link_domain or link_url
  // (for example, go.example.com for affiliate redirects).
  function outboundClick(event) {
    var anchor = anchorFromTarget(event.target);
    if (!anchor || anchor.hasAttribute("data-apa-ignore")) return;

    var href = anchor.href || anchor.getAttribute("href") || "";
    var destination;
    try {
      destination = new URL(href, location.href);
    } catch (e) {
      return;
    }
    if (destination.protocol !== "http:" && destination.protocol !== "https:") return;
    if (destination.origin === location.origin) return;

    // Query strings and fragments on outbound links often carry tokens
    // (affiliate IDs, signed URLs, magic links), so only origin + path leave.
    send("outbound_click", {
      link_url: destination.origin + destination.pathname,
      link_domain: destination.hostname,
      link_path: destination.pathname,
      link_text: textOf(anchor),
      link_target: anchor.getAttribute("target") || "",
      link_download: anchor.hasAttribute("download"),
    });

    // Approved cross-domain handoff: the click navigates with the current
    // visitor and session so the receiving site continues the same journey.
    if (linkerHosts.indexOf(destination.hostname.toLowerCase()) !== -1) {
      destination.searchParams.set("_apa", visitor + "." + sessionID() + "." + Date.now().toString(36));
      try {
        anchor.href = destination.href;
      } catch (e) {}
    }
  }
  if (document && document.addEventListener) document.addEventListener("click", outboundClick, true);
})();
