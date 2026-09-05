/* Apteva Analytics — static-site tag (v0.8.7).
 *
 * Drop on any website:
 *   <script async src="https://YOUR-APTEVA/api/apps/analytics/ui/tag.js"
 *           data-key="wk_live_..."></script>
 *
 * Auto-fires a page_view on load and on SPA route changes. Custom events:
 *   apa("signup", { plan: "pro" });
 *
 * Sends GET /collect (no-cors, keepalive) so it works cross-origin with
 * no CORS preflight; falls back to an image pixel. The write key is the
 * only credential and is write-only — safe to ship in page source.
 */
(function () {
  "use strict";

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

  var key = self.getAttribute("data-key") || "";
  var base = self.src.split("?")[0].replace(/\/ui\/tag\.js$/, "");
  var COLLECT = base + "/collect";
  if (!key) {
    try {
      console.warn("[apteva-analytics] missing data-key on tag.js");
    } catch (e) {}
  }

  function rand() {
    return "s_" + Math.random().toString(36).slice(2) + Date.now().toString(36);
  }

  // v2 sessions expire after 30 minutes of inactivity. Legacy apa_sid is
  // retained as visitor identity so historical sessions are not rewritten.
  var visitor = rand(),
    session = { id: rand(), last: 0 };
  try {
    visitor =
      localStorage.getItem("apa_vid") ||
      localStorage.getItem("apa_sid") ||
      visitor;
    localStorage.setItem("apa_vid", visitor);
  } catch (e) {}
  function sessionID() {
    var now = Date.now();
    try {
      var shared = JSON.parse(localStorage.getItem("apa_session_v2") || "null");
      if (
        shared &&
        typeof shared.id === "string" &&
        Number.isFinite(shared.last)
      )
        session = shared;
    } catch (e) {}
    if (
      !session.last ||
      now - session.last >= 30 * 60 * 1000 ||
      now < session.last
    )
      session = { id: rand(), last: now };
    session.last = now;
    try {
      localStorage.setItem("apa_session_v2", JSON.stringify(session));
    } catch (e) {}
    return session.id;
  }

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
  function hostOf(u) {
    try {
      return new URL(u).hostname;
    } catch (e) {
      return "";
    }
  }

  function send(event, props) {
    if (!key) return;
    var params = {
      k: key,
      e: event || "page_view",
      sid: sessionID(),
      eid: rand(),
      url: location.origin + location.pathname,
      host: location.host,
      path: location.pathname,
      ref: document.referrer ? hostOf(document.referrer) : "",
      title: document.title || "",
      lang: navigator.language || "",
      device: device(),
      platform: platform(),
      screen: (screen.width || 0) + "x" + (screen.height || 0),
    };
    if (props && typeof props === "object") {
      try {
        params.p = JSON.stringify(
          Object.assign({}, props, { visitor_id: visitor, session_version: 2 }),
        );
      } catch (e) {}
    }
    if (!params.p)
      params.p = JSON.stringify({ visitor_id: visitor, session_version: 2 });
    var qs = [];
    for (var k in params) {
      if (params[k] === "" || params[k] == null) continue;
      qs.push(encodeURIComponent(k) + "=" + encodeURIComponent(params[k]));
    }
    var url = COLLECT + "?" + qs.join("&");
    if (url.length > 16000) return;
    function fallback() {
      try {
        new Image().src = url;
      } catch (e) {}
    }
    try {
      fetch(url, {
        method: "GET",
        mode: "no-cors",
        keepalive: true,
        credentials: "omit",
      }).catch(fallback);
    } catch (e) {
      fallback();
    }
  }

  // Public API. Drains any queued calls (apa.q) made before load.
  var queued = window.apa && window.apa.q ? window.apa.q : [];
  window.apa = function (event, props) {
    send(event, props);
  };
  for (var i = 0; i < queued.length; i++) {
    try {
      send(queued[i][0], queued[i][1]);
    } catch (e) {}
  }

  // Auto page_view: initial + SPA route changes.
  send("page_view");
  var lastPath = location.pathname + location.search;
  function maybePV() {
    var p = location.pathname + location.search;
    if (p !== lastPath) {
      lastPath = p;
      send("page_view");
    }
  }
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
})();
