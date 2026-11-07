// Camera, canvas and overlay rules shared by the page and the dashboard widget.
export const CANVAS_STYLES = `
.aw-canvas {position:relative;overflow:hidden;width:100%;height:calc(100dvh - 140px);min-height:360px;background:var(--bg-subtle,#101010);touch-action:none;cursor:grab;user-select:none;isolation:isolate}
.aw-pixel {display:block;width:100%;height:100%;image-rendering:pixelated}
.aw-sr {position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap}
.aw-canvas.is-dragging,.aw-canvas.is-dragging * {cursor:grabbing!important}.aw-canvas > svg {display:block;width:100%;height:100%}
.aw-canvas-status,.aw-camera-hint {position:absolute;pointer-events:none;color:var(--text-muted,#aaa);background:var(--bg-card,#141414);border:1px solid var(--border,#2a2a2a);border-radius:4px;font-size:10px;padding:6px 8px;opacity:.9}
.aw-canvas-status {top:12px;left:12px}.aw-camera-hint {bottom:12px;left:12px}
.aw-camera-controls {position:absolute;right:12px;bottom:12px;display:flex;border:1px solid var(--border,#2a2a2a);border-radius:5px;overflow:hidden;background:var(--bg-card,#141414)}
.aw-camera-controls button {height:34px;min-width:36px;border:0;border-right:1px solid var(--border,#2a2a2a);background:transparent;color:var(--text,#e8e8e8);font-size:16px}.aw-camera-controls button:last-child {border-right:0}.aw-camera-controls button:nth-child(2) {font-size:11px;min-width:55px}.aw-camera-controls button:hover {background:var(--bg-hover,#252525)}
`;

export const PANEL_STYLES = `
.aw {container:agent-worlds/inline-size;display:flex;flex-direction:column;width:100%;min-height:100%;background:var(--bg,#0a0a0a);color:var(--text,#e8e8e8);font:12px var(--font-base,ui-sans-serif,system-ui)}
.aw * {box-sizing:border-box}.aw button,.aw select,.aw input {font:inherit}.aw button {cursor:pointer}.aw button:disabled {opacity:.4;cursor:default}
.aw button:focus-visible,.aw select:focus-visible,.aw input:focus-visible,.aw-canvas:focus-visible {outline:2px solid var(--accent,#f97316);outline-offset:-2px}
.aw-toolbar {display:flex;align-items:center;gap:8px;height:52px;padding:8px 12px;border-bottom:1px solid var(--border,#2a2a2a);position:relative;z-index:5;flex:none;white-space:nowrap}
.aw-toolbar-main {display:flex;align-items:center;gap:16px;flex:1;min-width:0;overflow-x:auto;scrollbar-width:thin}.aw h1 {margin:0;font-size:14px;letter-spacing:-.02em;flex:none}
.aw-button,.aw-visualization {height:32px;padding:6px 10px;border:1px solid var(--border,#2a2a2a);border-radius:4px;background:var(--bg-card,#141414);color:var(--text,#e8e8e8);font-size:11px;white-space:nowrap;flex:none}
.aw-button:hover:not(:disabled),.aw-button[aria-pressed=true] {border-color:var(--accent,#f97316)}.aw-visualization {max-width:155px;cursor:pointer}
.aw-live.is-live {color:var(--success,#22c55e)}.aw-dot {display:inline-block;width:5px;height:5px;margin-right:6px;border-radius:50%;background:currentColor;vertical-align:1px}
.aw-breadcrumbs {display:flex;align-items:center;gap:8px;font-size:11px;color:var(--text-dim,#888);flex:none}
.aw-breadcrumbs button {border:0;background:transparent;color:var(--accent,#f97316);padding:5px 0;max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.aw-breadcrumbs button[aria-current] {color:var(--text,#e8e8e8)}.aw-breadcrumbs .aw-zoom-out {font-size:10px;border:1px solid var(--border,#2a2a2a);padding:4px 7px;border-radius:3px}
.aw-options {position:relative;flex:none}.aw-options summary {list-style:none;cursor:pointer;min-width:32px;text-align:center}.aw-options summary::-webkit-details-marker {display:none}
.aw-options-menu {position:absolute;right:0;top:40px;display:grid;gap:12px;width:260px;padding:16px;border:1px solid var(--border,#2a2a2a);border-radius:6px;background:var(--bg-card,#141414);box-shadow:0 8px 32px #0008;white-space:normal}
.aw-options-menu small {color:var(--text-dim,#888);font-size:10px;line-height:1.8}.aw-field {display:grid;gap:6px;color:var(--text-dim,#888);font-size:10px}
.aw-field select {width:100%;height:32px;padding:0 8px;border:1px solid var(--border,#2a2a2a);border-radius:3px;background:var(--bg-input,#111);color:var(--text,#e8e8e8);font-size:11px}
.aw-portrait {display:flex;align-items:center;gap:7px;color:var(--text-muted,#aaa);font-size:11px}.aw input {accent-color:var(--accent,#f97316)}
.aw-main,.aw-frame,.aw-scene {width:100%;min-width:0}.aw-content {position:relative}.aw-scene {position:relative}
${CANVAS_STYLES}
.aw-pages {position:absolute;left:12px;bottom:56px;display:flex;align-items:center;gap:8px;padding:5px;background:var(--bg-card,#141414);border:1px solid var(--border,#2a2a2a);border-radius:4px;font-size:11px}
.aw-loading {display:grid;place-items:center;height:100%;color:var(--text-dim,#888)}.aw-error,.aw-notice {padding:8px 12px;font-size:11px;color:var(--text-muted,#aaa);border-bottom:1px solid var(--border,#2a2a2a)}.aw-error {color:var(--error,#ef4444)}
.aw-scene svg g[data-selected=true]>rect:first-of-type,.aw-scene svg g[data-selected=true]>ellipse:first-of-type {stroke:var(--accent,#f97316);stroke-width:3}
.aw-side-agents { flex:none; }.aw-side-activity { display:flex; min-height:0; flex:1; flex-direction:column; border-top:1px solid var(--border,#2a2a2a); }
.aw-float {position:absolute;top:12px;right:12px;bottom:12px;z-index:4;display:flex;flex-direction:column;max-width:calc(100% - 24px);background:color-mix(in srgb,var(--bg-card,#141414) 90%,transparent);backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px);border:1px solid var(--border,#2a2a2a);border-radius:8px;box-shadow:0 12px 36px #0007;overflow:hidden}
.aw-float-resize {position:absolute;left:0;top:0;bottom:0;width:6px;cursor:ew-resize;z-index:1;touch-action:none}.aw-float-resize:hover {background:color-mix(in srgb,var(--accent,#f97316) 35%,transparent)}
.aw-float-head {display:flex;align-items:center;gap:4px;padding:8px 8px 8px 14px;border-bottom:1px solid var(--border,#2a2a2a);flex:none}
.aw-float-head h2 {flex:1;min-width:0;margin:0 0 0 2px;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.aw-float-head .aw-dot {margin-right:2px}
.aw-icon-button {display:grid;place-items:center;flex:none;width:26px;height:26px;padding:0;border:1px solid transparent;border-radius:4px;background:transparent;color:var(--text-muted,#aaa)}
.aw-icon-button:hover:not(:disabled) {border-color:var(--border,#2a2a2a);color:var(--text,#e8e8e8)}.aw-icon-button[aria-pressed=true] {border-color:var(--accent,#f97316);color:var(--accent,#f97316)}
.aw-float-body {flex:1;min-height:0;overflow:auto;padding-bottom:8px}
.aw-float-tab {position:absolute;top:12px;right:12px;z-index:4;display:flex;align-items:center;gap:6px;max-width:220px;padding:8px 12px;border:1px solid var(--border,#2a2a2a);border-radius:6px;background:var(--bg-card,#141414);color:var(--text,#e8e8e8);box-shadow:0 6px 20px #0006;font-size:11px}
.aw-float-tab span:last-child {overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.aw-block {padding:12px 14px;border-bottom:1px solid var(--border,#2a2a2a)}.aw-block:last-of-type {border-bottom:0}
.aw-block-head {display:flex;align-items:center;gap:6px;min-height:22px;margin-bottom:8px}.aw-block-head h3 {flex:1;margin:0;font-size:10px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;color:var(--text-dim,#888)}
.aw-block-tools {display:flex;align-items:center;gap:2px}.aw-block-tools select {height:26px;padding:0 4px;border:1px solid var(--border,#2a2a2a);border-radius:4px;background:var(--bg-input,#111);color:var(--text,#e8e8e8);font-size:10px}
.aw-block-body {min-width:0}.aw-block-note {padding:4px 0;color:var(--text-dim,#888);font-size:11px;line-height:1.5}
.aw-block-settings {display:grid;gap:8px;margin-bottom:10px;padding:10px;border:1px dashed var(--border,#2a2a2a);border-radius:4px}.aw-block-settings input:not([type=checkbox]) {height:30px;padding:0 8px;border:1px solid var(--border,#2a2a2a);border-radius:3px;background:var(--bg-input,#111);color:var(--text,#e8e8e8);font-size:11px}
.aw-block-add {display:flex;gap:8px;padding:12px 14px}.aw-block-add select {flex:1;min-width:0;height:32px;padding:0 8px;border:1px solid var(--border,#2a2a2a);border-radius:4px;background:var(--bg-input,#111);color:var(--text,#e8e8e8);font-size:11px}
.aw-facts {display:grid;grid-template-columns:auto 1fr;gap:7px 14px;margin:0;font-size:11px}.aw-facts dt {color:var(--text-dim,#888)}.aw-facts dd {margin:0;min-width:0;text-transform:none}.aw-facts small {color:var(--text-dim,#888)}
.aw-link {padding:0;border:0;background:none;color:var(--accent,#f97316);font:inherit;cursor:pointer}.aw-link:hover {text-decoration:underline}
.aw-content:has(>.aw-float) .aw-camera-controls {right:calc(var(--aw-float-width,360px) + 24px)}
.aw-side-head { display:flex; justify-content:space-between; align-items:center; min-height:42px; padding:10px 14px; border-bottom:1px solid var(--border,#2a2a2a); }.aw-side-head h3 { margin:0; font-size:11px; font-weight:700; }.aw-side-head span { color:var(--text-dim,#888); font-size:10px; }
.aw-side-destinations { border-bottom:1px solid var(--border,#2a2a2a); }.aw-destination-list { max-height:230px; overflow:auto; }.aw-destination-row { display:flex; align-items:center; gap:9px; width:100%; min-height:46px; padding:6px 12px; border:0; border-bottom:1px solid var(--border,#2a2a2a); background:transparent; color:var(--text,#e8e8e8); text-align:left; }.aw-destination-row:hover,.aw-destination-row.is-selected { background:var(--bg-hover,#1c1c1c); }.aw-destination-row.is-selected { box-shadow:inset 2px 0 var(--accent,#f97316); }.aw-destination-icon { display:grid; flex:none; place-items:center; width:27px; height:27px; border:1px solid var(--border-strong,#4a4a4a); color:var(--accent,#f97316); font-size:16px; }.aw-destination-icon.is-integration { color:var(--info,#3b82f6); }.aw-destination-row>span:nth-child(2) { display:flex; min-width:0; flex:1; flex-direction:column; gap:3px; }.aw-destination-row strong,.aw-destination-row small { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.aw-destination-row strong { font-size:10px; }.aw-destination-row small { color:var(--text-dim,#888); font-size:9px; }.aw-destination-row em { color:var(--text-dim,#888); font-size:10px; font-style:normal; }.aw-destination-detail { display:flex; flex-direction:column; gap:6px; padding:12px 14px; color:var(--text-muted,#aaa); font-size:10px; overflow-wrap:anywhere; }.aw-destination-detail strong { color:var(--text,#e8e8e8); font-size:11px; }.aw-destination-detail small { color:var(--text-dim,#888); font-size:9px; }
.aw-agent-list { max-height:300px; overflow:auto; }.aw-agent-row { display:flex; align-items:center; gap:9px; width:100%; padding:9px 13px; border:0; border-bottom:1px solid var(--border,#2a2a2a); background:transparent; color:var(--text,#e8e8e8); text-align:left; }.aw-agent-row:hover,.aw-agent-row.is-selected { background:var(--bg-hover,#1c1c1c); }.aw-agent-row.is-selected { box-shadow:inset 2px 0 var(--accent,#f97316); }
.aw-agent-avatar { display:grid; flex:none; place-items:center; width:27px; height:27px; border:1px solid var(--border-strong,#4a4a4a); border-radius:50%; color:var(--accent,#f97316); font-size:11px; font-weight:700; }.aw-agent-name { display:flex; min-width:0; flex:1; flex-direction:column; gap:2px; }.aw-agent-name strong { overflow:hidden; font-size:10px; font-weight:700; text-overflow:ellipsis; white-space:nowrap; }.aw-agent-name small { overflow:hidden; color:var(--text-dim,#888); font-size:9px; text-overflow:ellipsis; white-space:nowrap; }.aw-agent-status { width:6px; height:6px; flex:none; border-radius:50%; }
.aw-activity-list { min-height:80px; max-height:320px; overflow:auto; }.aw-event { display:flex; gap:9px; padding:11px 14px; border-bottom:1px solid var(--border,#2a2a2a); }.aw-event:last-child { border-bottom:0; }.aw-event-mark { flex:none; width:6px; height:6px; margin-top:5px; border-radius:50%; }.aw-event-line { overflow:hidden; color:var(--text-muted,#aaa); font-size:10px; line-height:1.5; overflow-wrap:anywhere; }.aw-event-line b { color:var(--text,#e8e8e8); }.aw-event-meta { margin-top:4px; color:var(--text-dim,#888); font-size:9px; }
.aw-plain-empty { padding:18px 14px; color:var(--text-dim,#888); font-size:10px; }
.aw-replay { padding:12px 16px; border-top:1px solid var(--border,#2a2a2a); }.aw-replay div { display:flex; justify-content:space-between; margin-bottom:8px; color:var(--text-dim,#888); font-size:10px; }.aw-replay strong { color:var(--text,#e8e8e8); }.aw-replay input { width:100%; }

@container agent-worlds (max-width:600px) {.aw-toolbar {gap:5px;padding:6px;height:46px}.aw-toolbar-main {gap:10px}.aw h1 {font-size:12px}.aw-button {padding:5px 7px}.aw-camera-hint {font-size:9px;max-width:170px}.aw-canvas {height:calc(100dvh - 134px)}.aw-float {top:auto;left:12px;width:auto!important;height:min(60%,520px)}.aw-float-resize {display:none}.aw-content:has(>.aw-float) .aw-camera-controls {display:none}}
`;
export const WORLD_NAV_STYLES = `
.aw-thread-row {display:flex;flex-direction:column;gap:4px;padding:9px 14px;border-bottom:1px solid var(--border,#2a2a2a);font-size:11px;overflow-wrap:anywhere}.aw-thread-row span {color:var(--text-dim,#888)}
.aw-scene svg [role=button]:focus-visible {outline:2px solid var(--accent,#f97316);outline-offset:3px}
@media(prefers-reduced-motion:reduce) {.aw-scene * {animation:none!important;transition:none!important}}
`;
