export const PANEL_STYLES = `
.aw { min-height:100%; background:var(--bg,#0a0a0a); color:var(--text,#e8e8e8); font-family:var(--font-base,ui-sans-serif,system-ui); font-size:12px; }
.aw * { box-sizing:border-box; }
.aw button,.aw select,.aw input { font:inherit; }
.aw button { cursor:pointer; }
.aw button:disabled { cursor:not-allowed; opacity:.45; }
.aw button:focus-visible,.aw select:focus-visible,.aw input:focus-visible { outline:2px solid var(--accent,#f97316); outline-offset:2px; }
.aw-header { display:flex; align-items:center; justify-content:space-between; gap:20px; padding:21px 28px; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-title-row { display:flex; align-items:center; gap:10px; }
.aw-mark { display:grid; place-items:center; width:29px; height:29px; border:1px solid var(--accent,#f97316); border-radius:var(--radius-sm,3px); color:var(--accent,#f97316); font-size:20px; line-height:1; }
.aw h1 { margin:0; font-size:18px; line-height:1.3; font-weight:700; letter-spacing:-.03em; }
.aw-beta { padding:3px 5px; border:1px solid var(--success,#22c55e); border-radius:var(--radius-sm,3px); color:var(--success,#22c55e); font-size:9px; font-weight:700; letter-spacing:.07em; }
.aw-heading p { margin:5px 0 0 39px; color:var(--text-dim,#888); font-size:11px; }
.aw-header-actions,.aw-toolbar-actions { display:flex; align-items:center; gap:8px; }
.aw-button { min-height:32px; padding:7px 12px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); color:var(--text,#e8e8e8); font-size:11px; font-weight:600; white-space:nowrap; }
.aw-button:hover:not(:disabled) { background:var(--bg-hover,#1c1c1c); border-color:var(--border-strong,#4a4a4a); }
.aw-main { max-width:1600px; margin:auto; padding:20px 28px 36px; }
.aw-toolbar { display:flex; align-items:end; gap:12px; padding:14px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); }
.aw-field { display:flex; flex-direction:column; gap:6px; min-width:0; color:var(--text-dim,#888); font-size:10px; font-weight:700; text-transform:uppercase; letter-spacing:.06em; }
.aw-source { flex:1; }.aw-agent { width:240px; }
.aw-field select { width:100%; height:33px; padding:0 10px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-sm,3px); background:var(--bg-input,#111); color:var(--text,#e8e8e8); font-size:11px; font-weight:500; letter-spacing:normal; text-transform:none; }
.aw-live.is-live { border-color:var(--success,#22c55e); color:var(--success,#22c55e); background:var(--bg-subtle,#101010); }
.aw-dot,.aw-stage-indicator { display:inline-block; width:6px; height:6px; margin-right:7px; border-radius:50%; background:currentColor; vertical-align:1px; }
.aw-error { margin-top:14px; padding:10px 12px; border:1px solid var(--error,#ef4444); border-radius:var(--radius-md,4px); color:var(--error,#ef4444); }
.aw-workspace { margin-top:20px; }
.aw-workspace-bar { display:flex; align-items:end; justify-content:space-between; gap:15px; margin-bottom:13px; }
.aw-eyebrow { color:var(--accent,#f97316); font-size:10px; font-weight:700; letter-spacing:.12em; }
.aw-workspace-bar h2 { margin:4px 0 2px; font-size:16px; line-height:1.3; font-weight:700; }
.aw-workspace-bar p { margin:0; color:var(--text-dim,#888); font-size:11px; }
.aw-portrait { display:flex; align-items:center; gap:7px; color:var(--text-muted,#aaa); white-space:nowrap; font-size:11px; }
.aw-portrait input,.aw-replay input { accent-color:var(--accent,#f97316); }
.aw-presets { display:flex; overflow-x:auto; gap:2px; padding:4px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); }
.aw-presets button { flex:1; min-width:max-content; padding:9px 14px; border:0; border-radius:var(--radius-sm,3px); background:transparent; color:var(--text-dim,#888); font-size:11px; font-weight:600; text-align:left; }
.aw-presets button:hover { background:var(--bg-hover,#1c1c1c); color:var(--text,#e8e8e8); }
.aw-presets button.is-active { background:var(--bg-hover,#1c1c1c); color:var(--accent,#f97316); box-shadow:inset 0 -2px var(--accent,#f97316); }
.aw-preset-num { margin-right:9px; opacity:.55; font-size:9px; }
.aw-content { display:grid; grid-template-columns:minmax(0,1fr) 265px; gap:14px; margin-top:14px; align-items:start; }
.aw-stage-card,.aw-panel,.aw-replay { overflow:hidden; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); box-shadow:var(--shadow-card,none); }
.aw-stage-card.is-portrait { max-width:540px; width:100%; margin-inline:auto; }
.aw-stage-top,.aw-stage-footer { display:flex; justify-content:space-between; align-items:center; gap:12px; padding:10px 14px; color:var(--text-dim,#888); font-size:10px; }
.aw-stage-top { border-bottom:1px solid var(--border,#2a2a2a); }
.aw-stage-label { color:var(--text,#e8e8e8); font-weight:700; }
.aw-stage-indicator { color:var(--success,#22c55e); }
.aw-stage { aspect-ratio:16/10; background:var(--bg-subtle,#101010); }
.aw-stage-portrait { aspect-ratio:9/16; }
.aw-stage svg { display:block; }
.aw-loading { display:grid; place-items:center; height:100%; color:var(--text-dim,#888); }
.aw-stage-footer { border-top:1px solid var(--border,#2a2a2a); }
.aw-aside { display:flex; flex-direction:column; gap:14px; }
.aw-panel-heading { display:flex; justify-content:space-between; align-items:center; gap:5px; padding:13px 14px; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-panel-heading h3 { margin:0; font-size:12px; font-weight:700; }.aw-panel-heading span { color:var(--text-dim,#888); font-size:9px; letter-spacing:.08em; }
.aw-metrics { display:grid; grid-template-columns:repeat(2,1fr); gap:1px; background:var(--border,#2a2a2a); }
.aw-metric { display:flex; flex-direction:column; gap:4px; padding:15px; background:var(--bg-card,#141414); }.aw-metric strong { color:var(--text,#e8e8e8); font-size:22px; line-height:1; font-weight:700; }.aw-metric span { color:var(--text-dim,#888); font-size:10px; }
.aw-activity-list { max-height:415px; overflow:auto; }.aw-event { display:flex; gap:9px; padding:11px 14px; border-bottom:1px solid var(--border,#2a2a2a); }.aw-event:last-child { border-bottom:0; }.aw-event-mark { flex:none; width:6px; height:6px; margin-top:5px; border-radius:50%; }.aw-event-line { overflow:hidden; color:var(--text-muted,#aaa); font-size:11px; line-height:1.5; overflow-wrap:anywhere; }.aw-event-line b { color:var(--text,#e8e8e8); }.aw-event-meta { margin-top:4px; color:var(--text-dim,#888); font-size:9px; }
.aw-empty { display:flex; align-items:center; flex-direction:column; gap:7px; padding:35px 15px; text-align:center; color:var(--text-dim,#888); font-size:10px; }.aw-empty-icon { color:var(--border-strong,#4a4a4a); font-size:28px; line-height:1; }.aw-empty strong { color:var(--text-muted,#aaa); font-size:11px; }
.aw-replay { margin-top:14px; padding:12px 14px; }.aw-replay div { display:flex; justify-content:space-between; margin-bottom:8px; color:var(--text-dim,#888); font-size:10px; }.aw-replay strong { color:var(--text,#e8e8e8); }.aw-replay input { width:100%; }
@media(max-width:900px) { .aw-content { grid-template-columns:1fr; }.aw-aside { display:grid; grid-template-columns:1fr 1fr; }.aw-stage-card.is-portrait { margin-inline:auto; } }
@media(max-width:620px) { .aw-header { align-items:flex-start; flex-direction:column; padding:18px; }.aw-main { padding:16px; }.aw-toolbar { flex-wrap:wrap; }.aw-source,.aw-agent { width:calc(50% - 6px); flex:auto; }.aw-toolbar-actions { width:100%; }.aw-toolbar-actions .aw-button { flex:1; }.aw-aside { grid-template-columns:1fr; }.aw-stage-footer span:first-child { display:none; }.aw-presets button { padding:9px; }.aw-preset-num { display:none; } }
`;
