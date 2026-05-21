/* Apteva Analytics — static-site tag (v0.4).
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
        if (els[i].src && els[i].src.indexOf("/ui/tag.js") !== -1) return els[i];
      }
      return null;
    })();
  if (!self) return;

  var key = self.getAttribute("data-key") || "";
  var base = self.src.split("?")[0].replace(/\/ui\/tag\.js$/, "");
  var COLLECT = base + "/collect";
  if (!key) {
    try { console.warn("[apteva-analytics] missing data-key on tag.js"); } catch (e) {}
  }

  function rand() {
    return "s_" + Math.random().toString(36).slice(2) + Date.now().toString(36);
  }

  // First-party session id (no cookies). Falls back to in-memory if
  // storage is unavailable (private mode, etc.).
  var sid;
  try {
    sid = localStorage.getItem("apa_sid");
    if (!sid) { sid = rand(); localStorage.setItem("apa_sid", sid); }
  } catch (e) { sid = rand(); }

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
    try { return new URL(u).hostname; } catch (e) { return ""; }
  }

  function send(event, props) {
    if (!key) return;
    var params = {
      k: key,
      e: event || "page_view",
      sid: sid,
      url: location.href,
      host: location.host,
      path: location.pathname + location.search,
      ref: document.referrer ? hostOf(document.referrer) : "",
      title: document.title || "",
      lang: navigator.language || "",
      device: device(),
      platform: platform(),
      screen: (screen.width || 0) + "x" + (screen.height || 0),
    };
    if (props && typeof props === "object") {
      try { params.p = JSON.stringify(props); } catch (e) {}
    }
    var qs = [];
    for (var k in params) {
      if (params[k] === "" || params[k] == null) continue;
      qs.push(encodeURIComponent(k) + "=" + encodeURIComponent(params[k]));
    }
    var url = COLLECT + "?" + qs.join("&");
    try {
      fetch(url, { method: "GET", mode: "no-cors", keepalive: true, credentials: "omit" });
    } catch (e) {
      try { new Image().src = url; } catch (e2) {}
    }
  }

  // Public API. Drains any queued calls (apa.q) made before load.
  var queued = window.apa && window.apa.q ? window.apa.q : [];
  window.apa = function (event, props) { send(event, props); };
  for (var i = 0; i < queued.length; i++) {
    try { send(queued[i][0], queued[i][1]); } catch (e) {}
  }

  // Auto page_view: initial + SPA route changes.
  send("page_view");
  var lastPath = location.pathname + location.search;
  function maybePV() {
    var p = location.pathname + location.search;
    if (p !== lastPath) { lastPath = p; send("page_view"); }
  }
  var push = history.pushState;
  if (push) history.pushState = function () { push.apply(this, arguments); maybePV(); };
  var replace = history.replaceState;
  if (replace) history.replaceState = function () { replace.apply(this, arguments); maybePV(); };
  window.addEventListener("popstate", maybePV);
})();
