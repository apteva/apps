export const PANEL_STYLES = `
.aw { display:flex; flex-direction:column; width:100%; min-height:100%; background:var(--bg,#0a0a0a); color:var(--text,#e8e8e8); font-family:var(--font-base,ui-sans-serif,system-ui); font-size:12px; }
.aw * { box-sizing:border-box; }
.aw button,.aw select,.aw input { font:inherit; }
.aw button { cursor:pointer; }
.aw button:disabled { cursor:not-allowed; opacity:.45; }
.aw button:focus-visible,.aw select:focus-visible,.aw input:focus-visible { outline:2px solid var(--accent,#f97316); outline-offset:2px; }
.aw-header { display:flex; align-items:center; justify-content:space-between; gap:20px; padding:16px 24px; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-title-row { display:flex; align-items:center; gap:9px; }
.aw-mark { display:grid; place-items:center; width:28px; height:28px; border:1px solid var(--accent,#f97316); border-radius:var(--radius-sm,3px); color:var(--accent,#f97316); font-size:19px; line-height:1; }
.aw h1 { margin:0; font-size:17px; line-height:1.3; font-weight:700; letter-spacing:-.025em; }
.aw-beta { padding:2px 5px; border:1px solid var(--success,#22c55e); border-radius:var(--radius-sm,3px); color:var(--success,#22c55e); font-size:9px; font-weight:700; letter-spacing:.07em; }
.aw-heading p { margin:3px 0 0 37px; color:var(--text-dim,#888); font-size:11px; }
.aw-header-actions,.aw-control-actions { display:flex; align-items:center; gap:8px; }
.aw-button { min-height:32px; padding:7px 11px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); color:var(--text,#e8e8e8); font-size:11px; font-weight:600; white-space:nowrap; }
.aw-button:hover:not(:disabled) { background:var(--bg-hover,#1c1c1c); border-color:var(--border-strong,#4a4a4a); }
.aw-main { flex:1; min-width:0; width:100%; padding:16px 20px 24px; }
.aw-frame { overflow:hidden; width:100%; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-md,4px); background:var(--bg-card,#141414); box-shadow:var(--shadow-card,none); }
.aw-controls { display:flex; align-items:end; gap:12px; padding:12px 16px; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-field { display:flex; flex-direction:column; gap:5px; min-width:0; color:var(--text-dim,#888); font-size:10px; font-weight:700; text-transform:uppercase; letter-spacing:.06em; }
.aw-source { width:min(33%,400px); }.aw-agent { width:min(23%,280px); }
.aw-field select { width:100%; height:33px; padding:0 10px; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-sm,3px); background:var(--bg-input,#111); color:var(--text,#e8e8e8); font-size:11px; font-weight:500; letter-spacing:normal; text-transform:none; }
.aw-control-actions { margin-left:auto; }
.aw-live.is-live { border-color:var(--success,#22c55e); color:var(--success,#22c55e); background:var(--bg-subtle,#101010); }
.aw-dot,.aw-stage-indicator { display:inline-block; width:6px; height:6px; margin-right:7px; border-radius:50%; background:currentColor; vertical-align:1px; }
.aw-portrait { display:flex; align-items:center; gap:7px; margin-left:9px; color:var(--text-muted,#aaa); white-space:nowrap; font-size:11px; }
.aw-portrait input,.aw-replay input { accent-color:var(--accent,#f97316); }
.aw-error { margin:12px 16px; padding:10px 12px; border:1px solid var(--error,#ef4444); border-radius:var(--radius-md,4px); color:var(--error,#ef4444); }
.aw-presets { display:flex; gap:0; overflow-x:auto; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-presets button { min-width:max-content; padding:12px 18px; border:0; border-bottom:2px solid transparent; background:transparent; color:var(--text-dim,#888); font-size:11px; font-weight:700; }
.aw-presets button:hover { background:var(--bg-hover,#1c1c1c); color:var(--text,#e8e8e8); }
.aw-presets button.is-active { border-bottom-color:var(--accent,#f97316); color:var(--accent,#f97316); background:var(--bg-subtle,#101010); }
.aw-summary { display:flex; align-items:center; gap:0; min-height:48px; padding:0 16px; border-bottom:1px solid var(--border,#2a2a2a); background:var(--bg-subtle,#101010); }
.aw-summary>div { display:flex; align-items:baseline; gap:6px; min-width:100px; padding-right:17px; border-right:1px solid var(--border,#2a2a2a); margin-right:17px; }
.aw-summary strong { color:var(--text,#e8e8e8); font-size:17px; line-height:1; }.aw-summary span { color:var(--text-dim,#888); font-size:10px; }
.aw-summary-time { margin-left:auto; }
.aw-content { display:grid; grid-template-columns:minmax(0,1fr) 300px; min-width:0; }
.aw-scene { min-width:0; display:flex; flex-direction:column; }
.aw-scene-head { display:flex; justify-content:space-between; align-items:center; gap:14px; min-height:49px; padding:9px 16px; border-bottom:1px solid var(--border,#2a2a2a); }
.aw-scene-head>div:first-child { display:flex; align-items:baseline; gap:12px; min-width:0; }.aw-scene-head strong { font-size:12px; }.aw-scene-head span { color:var(--text-dim,#888); font-size:10px; }
.aw-scene-source { color:var(--text-muted,#aaa); white-space:nowrap; font-size:10px; }.aw-stage-indicator { color:var(--success,#22c55e); }
.aw-canvas { display:grid; place-items:center; width:100%; aspect-ratio:16/7; max-height:650px; min-height:430px; background:var(--bg-subtle,#101010); }
.aw-canvas svg { display:block; width:100%; height:100%; }
.aw-content.is-portrait .aw-canvas { height:auto; min-height:0; max-height:none; aspect-ratio:auto; padding:18px; }
.aw-content.is-portrait .aw-canvas svg { width:min(100%,540px); height:auto; aspect-ratio:9/16; border:1px solid var(--border,#2a2a2a); }
.aw-scene-foot { display:flex; justify-content:space-between; align-items:center; gap:12px; min-height:42px; padding:9px 16px; border-top:1px solid var(--border,#2a2a2a); color:var(--text-dim,#888); font-size:10px; }
.aw-resources { display:flex; align-items:center; justify-content:flex-end; gap:5px; overflow:hidden; }.aw-resources>span:first-child { margin-right:3px; }.aw-resource { display:inline-block; max-width:135px; padding:3px 6px; overflow:hidden; border:1px solid var(--border,#2a2a2a); border-radius:var(--radius-sm,3px); color:var(--text-muted,#aaa); text-overflow:ellipsis; white-space:nowrap; }.aw-resources em { font-style:normal; }
.aw-loading { display:grid; place-items:center; height:100%; color:var(--text-dim,#888); }
.aw-sidebar { display:flex; min-height:0; flex-direction:column; border-left:1px solid var(--border,#2a2a2a); }
.aw-side-agents { flex:none; }.aw-side-activity { display:flex; min-height:0; flex:1; flex-direction:column; border-top:1px solid var(--border,#2a2a2a); }
.aw-side-head { display:flex; justify-content:space-between; align-items:center; min-height:42px; padding:10px 14px; border-bottom:1px solid var(--border,#2a2a2a); }.aw-side-head h3 { margin:0; font-size:11px; font-weight:700; }.aw-side-head span { color:var(--text-dim,#888); font-size:10px; }
.aw-side-destinations { border-bottom:1px solid var(--border,#2a2a2a); }.aw-destination-list { max-height:230px; overflow:auto; }.aw-destination-row { display:flex; align-items:center; gap:9px; width:100%; min-height:46px; padding:6px 12px; border:0; border-bottom:1px solid var(--border,#2a2a2a); background:transparent; color:var(--text,#e8e8e8); text-align:left; }.aw-destination-row:hover,.aw-destination-row.is-selected { background:var(--bg-hover,#1c1c1c); }.aw-destination-row.is-selected { box-shadow:inset 2px 0 var(--accent,#f97316); }.aw-destination-icon { display:grid; flex:none; place-items:center; width:27px; height:27px; border:1px solid var(--border-strong,#4a4a4a); color:var(--accent,#f97316); font-size:16px; }.aw-destination-icon.is-integration { color:var(--info,#3b82f6); }.aw-destination-row>span:nth-child(2) { display:flex; min-width:0; flex:1; flex-direction:column; gap:3px; }.aw-destination-row strong,.aw-destination-row small { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.aw-destination-row strong { font-size:10px; }.aw-destination-row small { color:var(--text-dim,#888); font-size:9px; }.aw-destination-row em { color:var(--text-dim,#888); font-size:10px; font-style:normal; }.aw-destination-detail { display:flex; flex-direction:column; gap:6px; padding:12px 14px; color:var(--text-muted,#aaa); font-size:10px; overflow-wrap:anywhere; }.aw-destination-detail strong { color:var(--text,#e8e8e8); font-size:11px; }.aw-destination-detail small { color:var(--text-dim,#888); font-size:9px; }
.aw-agent-list { max-height:300px; overflow:auto; }.aw-agent-row { display:flex; align-items:center; gap:9px; width:100%; padding:9px 13px; border:0; border-bottom:1px solid var(--border,#2a2a2a); background:transparent; color:var(--text,#e8e8e8); text-align:left; }.aw-agent-row:hover,.aw-agent-row.is-selected { background:var(--bg-hover,#1c1c1c); }.aw-agent-row.is-selected { box-shadow:inset 2px 0 var(--accent,#f97316); }
.aw-agent-avatar { display:grid; flex:none; place-items:center; width:27px; height:27px; border:1px solid var(--border-strong,#4a4a4a); border-radius:50%; color:var(--accent,#f97316); font-size:11px; font-weight:700; }.aw-agent-name { display:flex; min-width:0; flex:1; flex-direction:column; gap:2px; }.aw-agent-name strong { overflow:hidden; font-size:10px; font-weight:700; text-overflow:ellipsis; white-space:nowrap; }.aw-agent-name small { overflow:hidden; color:var(--text-dim,#888); font-size:9px; text-overflow:ellipsis; white-space:nowrap; }.aw-agent-status { width:6px; height:6px; flex:none; border-radius:50%; }
.aw-activity-list { min-height:80px; max-height:320px; overflow:auto; }.aw-event { display:flex; gap:9px; padding:11px 14px; border-bottom:1px solid var(--border,#2a2a2a); }.aw-event:last-child { border-bottom:0; }.aw-event-mark { flex:none; width:6px; height:6px; margin-top:5px; border-radius:50%; }.aw-event-line { overflow:hidden; color:var(--text-muted,#aaa); font-size:10px; line-height:1.5; overflow-wrap:anywhere; }.aw-event-line b { color:var(--text,#e8e8e8); }.aw-event-meta { margin-top:4px; color:var(--text-dim,#888); font-size:9px; }
.aw-plain-empty { padding:18px 14px; color:var(--text-dim,#888); font-size:10px; }
.aw-replay { padding:12px 16px; border-top:1px solid var(--border,#2a2a2a); }.aw-replay div { display:flex; justify-content:space-between; margin-bottom:8px; color:var(--text-dim,#888); font-size:10px; }.aw-replay strong { color:var(--text,#e8e8e8); }.aw-replay input { width:100%; }
@media(max-width:1100px) { .aw-content { grid-template-columns:1fr; }.aw-sidebar { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); border-left:0; border-top:1px solid var(--border,#2a2a2a); }.aw-side-destinations,.aw-side-agents { border-right:1px solid var(--border,#2a2a2a); border-bottom:0; }.aw-side-activity { border-top:0; }.aw-agent-list,.aw-activity-list,.aw-destination-list { max-height:240px; } }
@media(max-width:720px) { .aw-header { align-items:flex-start; flex-direction:column; padding:14px 16px; }.aw-main { padding:10px; }.aw-controls { flex-wrap:wrap; }.aw-source,.aw-agent { width:calc(50% - 6px); }.aw-control-actions { width:100%; justify-content:flex-start; margin-left:0; }.aw-portrait { margin-left:auto; }.aw-summary { flex-wrap:wrap; gap:7px; padding:9px 12px; }.aw-summary>div { min-width:auto; padding-right:8px; margin-right:4px; }.aw-summary-time { display:none; }.aw-scene-head span { display:none; }.aw-scene-source { display:block; }.aw-canvas { min-height:330px; }.aw-scene-foot>span:first-child { display:none; }.aw-resources { justify-content:flex-start; }.aw-sidebar { grid-template-columns:1fr; }.aw-side-activity { border-top:1px solid var(--border,#2a2a2a); border-left:0; } }
`;
