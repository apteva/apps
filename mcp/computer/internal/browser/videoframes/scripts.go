package videoframes

// The chosen video and its saved playback state live in this page global
// between calls. Each script is a function taking one JSON argument.
const stateKey = "__aptevaVideoFrames"

// listFunction enumerates videos in the top document and same-origin frames,
// in a stable document order shared with prepareFunction.
const listFunction = `function(args){
  const videos = [];
  const walk = (doc, inFrame) => {
    let nodes = [];
    try { nodes = doc.querySelectorAll('video'); } catch (e) { return; }
    for (const v of nodes) videos.push({ v, inFrame });
    let frames = [];
    try { frames = doc.querySelectorAll('iframe,frame'); } catch (e) {}
    for (const f of frames) {
      let d = null;
      try { d = f.contentDocument; } catch (e) {}
      if (d) walk(d, true);
    }
  };
  walk(document, false);
  const offset = (v) => {
    let x = 0, y = 0, w = v.ownerDocument.defaultView;
    while (w && w !== window && w.frameElement) {
      const r = w.frameElement.getBoundingClientRect();
      x += r.left; y += r.top; w = w.parent;
    }
    return { x, y };
  };
  const nearby = (v) => {
    let n = v.parentElement;
    for (let i = 0; n && i < 8; i++, n = n.parentElement) {
      const t = (n.innerText || '').replace(/\s+/g, ' ').trim();
      if (t) return t.slice(0, 120);
    }
    return '';
  };
  const vw = window.innerWidth, vh = window.innerHeight;
  const candidates = videos.map(({ v, inFrame }, index) => {
    const r = v.getBoundingClientRect();
    const o = offset(v);
    const left = Math.max(0, r.left + o.x), top = Math.max(0, r.top + o.y);
    const right = Math.min(vw, r.right + o.x), bottom = Math.min(vh, r.bottom + o.y);
    const visible = Math.max(0, right - left) * Math.max(0, bottom - top);
    const d = v.duration;
    const live = d === Infinity;
    const src = v.srcObject ? 'media_stream' : (String(v.currentSrc || v.src || '').startsWith('blob:') ? 'blob' : ((v.currentSrc || v.src) ? 'url' : 'none'));
    return {
      index,
      on_screen_width: Math.round(r.width), on_screen_height: Math.round(r.height),
      visible_area: Math.round(visible),
      video_width: v.videoWidth || 0, video_height: v.videoHeight || 0,
      duration: (live || !isFinite(d)) ? 0 : Math.round(d * 1000) / 1000,
      live,
      drm: !!v.mediaKeys,
      playing: !v.paused && !v.ended && v.readyState > 2 && v.currentTime > 0,
      ready_state: v.readyState,
      source: src,
      in_frame: inFrame,
      nearby_text: nearby(v),
    };
  });
  let selectorIndex = -1;
  if (args.selector) {
    let matches;
    try { matches = document.querySelectorAll(args.selector); }
    catch (e) { return { code: 'invalid_target', message: 'invalid CSS selector: ' + String(e && e.message || e), candidates }; }
    if (matches.length !== 1) return { code: 'invalid_target', message: 'selector matched ' + matches.length + ' elements; it must match exactly one <video>', candidates };
    selectorIndex = videos.findIndex((x) => x.v === matches[0]);
    if (selectorIndex < 0) return { code: 'invalid_target', message: 'selector matched a <' + matches[0].tagName.toLowerCase() + '>, not a <video>', candidates };
  }
  return { candidates, selector_index: selectorIndex };
}`

// prepareFunction saves the chosen video's state, then pauses and mutes it.
const prepareFunction = `function(args){
  const videos = [];
  const walk = (doc) => {
    try { for (const v of doc.querySelectorAll('video')) videos.push(v); } catch (e) { return; }
    let frames = [];
    try { frames = doc.querySelectorAll('iframe,frame'); } catch (e) {}
    for (const f of frames) { let d = null; try { d = f.contentDocument; } catch (e) {} if (d) walk(d); }
  };
  walk(document);
  const v = videos[args.index];
  if (!v) return { code: 'video_not_found', message: 'the chosen video left the page' };
  const prior = window['` + stateKey + `'];
  if (prior && prior.v === v) {
    // A previous call ended without restoring; keep its original state.
  } else {
    window['` + stateKey + `'] = { v, saved: { t: v.currentTime, paused: v.paused, muted: v.muted, rate: v.playbackRate } };
  }
  v.muted = true;
  if (!v.paused) v.pause();
  return {};
}`

// captureFunction seeks, waits for the frame, and encodes it at max_edge.
const captureFunction = `async function(a){
  const st = window['` + stateKey + `'];
  if (!st || !st.v || !st.v.isConnected) return { code: 'video_detached', message: 'the video left the page during capture' };
  const v = st.v;
  if (!v.paused) v.pause();
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const once = (ev, ms) => new Promise((res) => {
    let done = false;
    const fin = (ok) => { if (!done) { done = true; v.removeEventListener(ev, on); res(ok); } };
    const on = () => fin(true);
    v.addEventListener(ev, on);
    setTimeout(() => fin(false), ms);
  });
  const deadline = Date.now() + a.seekTimeoutMs;
  // Some site players move the position themselves while data loads (a
  // jump to 6 s can land at 8.5 s), so check where it landed and jump again.
  const offBy = () => Math.abs(v.currentTime - a.t);
  for (let attempt = 0; ; attempt++) {
    if (offBy() > 0.0005 || v.readyState < 2) {
      const left = deadline - Date.now();
      if (left <= 0) return { code: 'seek_timeout', message: 'the jump to ' + a.t + 's did not finish' };
      const seeked = once('seeked', left);
      v.currentTime = a.t;
      const ok = await seeked;
      if (!ok && v.seeking) return { code: 'seek_timeout', message: 'the jump to ' + a.t + 's did not finish' };
    }
    while (v.readyState < 2 && Date.now() < deadline) await sleep(50);
    if (v.readyState < 2) return { code: 'seek_timeout', message: 'no frame data at ' + a.t + 's' };
    if (!v.paused) v.pause();
    if (offBy() <= 0.25) break;
    if (attempt >= 2) return { code: 'seek_timeout', message: 'the page kept moving the video away from ' + a.t + 's (it landed at ' + v.currentTime.toFixed(2) + 's)' };
  }
  // requestVideoFrameCallback can be throttled in background tabs; never
  // rely on it alone.
  await new Promise((res) => {
    let done = false;
    const fin = () => { if (!done) { done = true; res(); } };
    try { if (v.requestVideoFrameCallback) v.requestVideoFrameCallback(fin); } catch (e) {}
    setTimeout(fin, 300);
  });
  const vw = v.videoWidth, vh = v.videoHeight;
  if (!vw || !vh) return { code: 'seek_timeout', message: 'the video has no decoded size yet' };
  const scale = Math.min(1, a.maxEdge / Math.max(vw, vh));
  const w = Math.max(1, Math.round(vw * scale)), h = Math.max(1, Math.round(vh * scale));
  const c = document.createElement('canvas');
  c.width = w; c.height = h;
  const g = c.getContext('2d');
  let url;
  try {
    g.drawImage(v, 0, 0, w, h);
    url = c.toDataURL(a.mime, a.quality);
  } catch (e) {
    if (e && e.name === 'SecurityError') return { code: 'frame_capture_blocked', message: 'the video comes from another site that does not allow copying its frames' };
    throw e;
  }
  const comma = url.indexOf(',');
  return { actual: v.currentTime, vw, vh, w, h, mime: url.slice(5, url.indexOf(';')), data: url.slice(comma + 1) };
}`

// restoreFunction puts back time, speed, mute and play state.
const restoreFunction = `async function(){
  const st = window['` + stateKey + `'];
  if (!st || !st.v) return { restored: false, reason: 'saved state was lost' };
  const v = st.v, s = st.saved;
  delete window['` + stateKey + `'];
  try { v.currentTime = s.t; } catch (e) {}
  v.playbackRate = s.rate;
  v.muted = s.muted;
  if (s.paused) { if (!v.paused) v.pause(); return { restored: true }; }
  try {
    await Promise.race([v.play(), new Promise((_, rej) => setTimeout(() => rej(new Error('play() did not settle within 2s')), 2000))]);
    return { restored: true };
  } catch (e) {
    return { restored: false, reason: 'the browser did not resume playback: ' + String(e && e.message || e) };
  }
}`
