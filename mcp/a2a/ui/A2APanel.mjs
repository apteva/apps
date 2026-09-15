import{useCallback as la,useEffect as Pe,useMemo as da,useRef as He,useState as u}from"react";import{forwardRef as na,createElement as ia}from"react";var Me=(i)=>i.replace(/([a-z0-9])([A-Z])/g,"$1-$2").toLowerCase(),qe=(...i)=>i.filter((n,o,p)=>{return Boolean(n)&&p.indexOf(n)===o}).join(" ");import{forwardRef as ta,createElement as Ge}from"react";var Xe={xmlns:"http://www.w3.org/2000/svg",width:24,height:24,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:2,strokeLinecap:"round",strokeLinejoin:"round"};var Ye=ta(({color:i="currentColor",size:n=24,strokeWidth:o=2,absoluteStrokeWidth:p,className:d="",children:l,iconNode:s,...r},m)=>{return Ge("svg",{ref:m,...Xe,width:n,height:n,stroke:i,strokeWidth:p?Number(o)*24/Number(n):o,className:qe("lucide",d),...r},[...s.map(([g,h])=>Ge(g,h)),...Array.isArray(l)?l:[l]])});var c=(i,n)=>{let o=na(({className:p,...d},l)=>ia(Ye,{ref:l,iconNode:n,className:qe(`lucide-${Me(i)}`,p),...d}));return o.displayName=`${i}`,o};var ke=c("Activity",[["path",{d:"M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2",key:"169zse"}]]);var W=c("ArrowRight",[["path",{d:"M5 12h14",key:"1ays0h"}],["path",{d:"m12 5 7 7-7 7",key:"xquz4c"}]]);var Z=c("ArrowUpRight",[["path",{d:"M7 7h10v10",key:"1tivn9"}],["path",{d:"M7 17 17 7",key:"1vkiza"}]]);var F=c("Bot",[["path",{d:"M12 8V4H8",key:"hb8ula"}],["rect",{width:"16",height:"12",x:"4",y:"8",rx:"2",key:"enze0r"}],["path",{d:"M2 14h2",key:"vft8re"}],["path",{d:"M20 14h2",key:"4cs60a"}],["path",{d:"M15 13v2",key:"1xurst"}],["path",{d:"M9 13v2",key:"rq6x2g"}]]);var ie=c("Check",[["path",{d:"M20 6 9 17l-5-5",key:"1gmf2c"}]]);var oe=c("ChevronLeft",[["path",{d:"m15 18-6-6 6-6",key:"1wnfg3"}]]);var V=c("ChevronRight",[["path",{d:"m9 18 6-6-6-6",key:"mthhwq"}]]);var Ne=c("Clock",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["polyline",{points:"12 6 12 12 16 14",key:"68esgv"}]]);var Ce=c("Copy",[["rect",{width:"14",height:"14",x:"8",y:"8",rx:"2",ry:"2",key:"17jyea"}],["path",{d:"M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2",key:"zix9uf"}]]);var _e=c("ExternalLink",[["path",{d:"M15 3h6v6",key:"1q9fwt"}],["path",{d:"M10 14 21 3",key:"gplh6r"}],["path",{d:"M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6",key:"a6xqqp"}]]);var re=c("FileText",[["path",{d:"M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z",key:"1rqfz7"}],["path",{d:"M14 2v4a2 2 0 0 0 2 2h4",key:"tnqrlb"}],["path",{d:"M10 9H8",key:"b1mrlr"}],["path",{d:"M16 13H8",key:"t4e002"}],["path",{d:"M16 17H8",key:"z1uh3a"}]]);var le=c("LayoutGrid",[["rect",{width:"7",height:"7",x:"3",y:"3",rx:"1",key:"1g98yp"}],["rect",{width:"7",height:"7",x:"14",y:"3",rx:"1",key:"6d4xhi"}],["rect",{width:"7",height:"7",x:"14",y:"14",rx:"1",key:"nxv5o0"}],["rect",{width:"7",height:"7",x:"3",y:"14",rx:"1",key:"1bb6yr"}]]);var Ae=c("Link2",[["path",{d:"M9 17H7A5 5 0 0 1 7 7h2",key:"8i5ue5"}],["path",{d:"M15 7h2a5 5 0 1 1 0 10h-2",key:"1b9ql8"}],["line",{x1:"8",x2:"16",y1:"12",y2:"12",key:"1jonct"}]]);var U=c("MessageSquare",[["path",{d:"M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z",key:"1lielz"}]]);var I=c("Network",[["rect",{x:"16",y:"16",width:"6",height:"6",rx:"1",key:"4q2zg0"}],["rect",{x:"2",y:"16",width:"6",height:"6",rx:"1",key:"8cvhb9"}],["rect",{x:"9",y:"2",width:"6",height:"6",rx:"1",key:"1egb70"}],["path",{d:"M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3",key:"1jsf9p"}],["path",{d:"M12 12V8",key:"2874zd"}]]);var x=c("Plus",[["path",{d:"M5 12h14",key:"1ays0h"}],["path",{d:"M12 5v14",key:"s699le"}]]);var de=c("RefreshCw",[["path",{d:"M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8",key:"v9h5vc"}],["path",{d:"M21 3v5h-5",key:"1q7to0"}],["path",{d:"M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16",key:"3uifl3"}],["path",{d:"M8 16H3v5",key:"1cv678"}]]);var se=c("Search",[["circle",{cx:"11",cy:"11",r:"8",key:"4ej97u"}],["path",{d:"m21 21-4.3-4.3",key:"1qie3q"}]]);var H=c("Server",[["rect",{width:"20",height:"8",x:"2",y:"2",rx:"2",ry:"2",key:"ngkwjq"}],["rect",{width:"20",height:"8",x:"2",y:"14",rx:"2",ry:"2",key:"iecqi9"}],["line",{x1:"6",x2:"6.01",y1:"6",y2:"6",key:"16zg32"}],["line",{x1:"6",x2:"6.01",y1:"18",y2:"18",key:"nzw8ys"}]]);var ce=c("Shield",[["path",{d:"M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z",key:"oel41y"}]]);var ze=c("Unplug",[["path",{d:"m19 5 3-3",key:"yk6iyv"}],["path",{d:"m2 22 3-3",key:"19mgm9"}],["path",{d:"M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z",key:"goz73y"}],["path",{d:"M7.5 13.5 10 11",key:"7xgeeb"}],["path",{d:"M10.5 16.5 13 14",key:"10btkg"}],["path",{d:"m12 6 6 6 2.3-2.3a2.4 2.4 0 0 0 0-3.4l-2.6-2.6a2.4 2.4 0 0 0-3.4 0Z",key:"1snsnr"}]]);var pe=c("X",[["path",{d:"M18 6 6 18",key:"1bl5f8"}],["path",{d:"m6 6 12 12",key:"d8bk6v"}]]);var _=c("CircleAlert",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["line",{x1:"12",x2:"12",y1:"8",y2:"12",key:"1pkeuh"}],["line",{x1:"12",x2:"12.01",y1:"16",y2:"16",key:"4dfq90"}]]);var q=c("CircleCheck",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m9 12 2 2 4-4",key:"dzmm74"}]]);var T=c("Earth",[["path",{d:"M21.54 15H17a2 2 0 0 0-2 2v4.54",key:"1djwo0"}],["path",{d:"M7 3.34V5a3 3 0 0 0 3 3a2 2 0 0 1 2 2c0 1.1.9 2 2 2a2 2 0 0 0 2-2c0-1.1.9-2 2-2h3.17",key:"1tzkfa"}],["path",{d:"M11 21.95V18a2 2 0 0 0-2-2a2 2 0 0 1-2-2v-1a2 2 0 0 0-2-2H2.05",key:"14pb5j"}],["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}]]);var S=c("LoaderCircle",[["path",{d:"M21 12a9 9 0 1 1-6.219-8.56",key:"13zald"}]]);import{useEffect as oa,useRef as ra}from"react";function We(i,n,o){let p=ra(o);p.current=o,oa(()=>{if(!i||!n)return;let d=(k)=>p.current(k),l=window.__aptevaAppEvents;if(l)return l.subscribe(i,n,d);let s=0,r=null,m=!1,g=null,h=()=>{if(m)return;let k=`/api/app-events/${encodeURIComponent(i)}?project_id=${encodeURIComponent(n)}`+(s>0?`&since=${s}`:"");r=new EventSource(k,{withCredentials:!0}),r.onmessage=(P)=>{try{let A=JSON.parse(P.data);if(A.seq<=s)return;s=A.seq,p.current(A)}catch{}},r.onerror=()=>{if(r&&r.readyState===EventSource.CLOSED){if(g)window.clearTimeout(g);g=window.setTimeout(h,2000)}}};return h(),()=>{if(m=!0,g)window.clearTimeout(g);if(r)r.close()}},[i,n])}var Ze=`
.a2a {
  --a-bg: var(--bg);
  --a-card: var(--bg-card);
  --a-text: var(--text);
  --a-muted: var(--text-muted);
  --a-border: var(--border);
  --a-accent: var(--accent);
  color: var(--a-text);
  background: var(--a-bg);
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  font-family: var(--font-base, inherit);
  font-size: 12px;
  line-height: 1.5;
  color-scheme: dark;
}
[data-mode="light"] .a2a {
  color-scheme: light;
}
.a2a * {
  box-sizing: border-box;
}
.a2a button,
.a2a input,
.a2a select,
.a2a textarea {
  font: inherit;
  color: inherit;
}
.a2a button,
.a2a a {
  touch-action: manipulation;
}
.a2a button {
  cursor: pointer;
}
.a2a button:disabled {
  opacity: 0.5;
  cursor: wait;
}
.a2a :focus-visible {
  outline: 2px solid var(--a-accent);
  outline-offset: 3px;
}
.a2a h1,
.a2a h2,
.a2a h3,
.a2a p {
  margin: 0;
}
.a2a h1 {
  font-size: 18px;
  font-weight: 700;
  letter-spacing: normal;
  line-height: 1.5;
}
.a2a h2 {
  font-size: 12px;
  font-weight: 700;
  letter-spacing: normal;
}
.a2a h3 {
  font-size: 12px;
  font-weight: 600;
}
.a2a a {
  color: var(--a-accent);
  text-decoration: none;
}
.a2a a:hover {
  text-decoration: underline;
}
.a2a svg {
  flex-shrink: 0;
}
.a2a small {
  font-size: 10px;
}
.a2a pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: 11px/1.65 var(--font-mono-fixed, monospace);
  background: var(--a-bg);
  padding: 14px;
  border-radius: var(--radius-sm, 2px);
  max-height: 360px;
  overflow: auto;
}
.a2a summary {
  cursor: pointer;
}
.a2a .muted {
  color: var(--a-muted);
}
.a2a .eyebrow {
  font-size: 10px;
  letter-spacing: 0.5px;
  text-transform: uppercase;
  font-weight: 650;
  color: var(--a-muted);
}
.a2a .row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.a2a .wrap {
  flex-wrap: wrap;
}
.a2a .between {
  justify-content: space-between;
}
.a2a .grow {
  flex: 1;
  min-width: 0;
}
.a2a .stack {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .gap-sm {
  gap: 8px;
}
.a2a .truncate {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a2a .break {
  overflow-wrap: anywhere;
}
.a2a .header {
  padding: 16px 24px 0;
  flex-shrink: 0;
  border-bottom: 1px solid var(--a-border);
  background: var(--a-bg);
}
.a2a .title-line {
  margin: 0 0 12px;
  flex-wrap: wrap;
  gap: 12px;
}
.a2a .subtitle {
  margin-top: 4px;
  color: var(--text-dim, var(--a-muted));
  font-size: 12px;
}
.a2a .tabs {
  display: flex;
  gap: 4px;
  overflow-x: auto;
}
.a2a .tab {
  display: flex;
  align-items: center;
  gap: 6px;
  border: 0;
  border-bottom: 2px solid transparent;
  padding: 6px 12px;
  background: none;
  color: var(--a-muted);
  white-space: nowrap;
  font-size: 12px;
}
.a2a .tab[aria-selected="true"] {
  border-bottom-color: var(--a-accent);
  color: var(--a-accent);
}
.a2a .main {
  padding: 16px 24px;
  flex: 1;
  min-height: 0;
  overflow: auto;
  width: 100%;
  max-width: none;
  margin: 0;
}
.a2a .btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  padding: 6px 10px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  background: var(--a-card);
  font-size: 12px;
  white-space: nowrap;
  text-decoration: none;
}
.a2a .btn:hover {
  border-color: var(--a-muted);
  text-decoration: none;
}
.a2a .primary:hover {
  background: color-mix(in srgb, var(--a-accent) 20%, transparent);
}
.a2a .primary {
  background: color-mix(in srgb, var(--a-accent) 10%, transparent);
  border-color: var(--a-accent);
  color: var(--a-accent);
  font-weight: 700;
}
.a2a .quiet {
  background: transparent;
}
.a2a .danger {
  color: var(--error);
}
.a2a .icon-btn {
  border: 0;
  background: none;
  padding: 6px;
  display: inline-flex;
  align-items: center;
  border-radius: var(--radius-sm, 2px);
  color: var(--a-muted);
}
.a2a .panel {
  box-shadow: var(--shadow-card, none);
  border: 1px solid var(--a-border);
  border-radius: var(--radius-md, 4px);
  background: var(--a-card);
  overflow: hidden;
}
.a2a .pad {
  padding: 12px 16px;
}
.a2a .section-head {
  padding: 10px 12px;
  border-bottom: 1px solid var(--a-border);
}
.a2a .metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.a2a .metric {
  padding: 12px 14px;
  text-align: left;
  color: inherit;
  display: block;
}
.a2a .metric:hover {
  border-color: var(--a-accent);
}
.a2a .metric-value {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.25;
  letter-spacing: normal;
  margin: 6px 0 2px;
}
.a2a .metric .row {
  justify-content: space-between;
  color: var(--a-muted);
  font-size: 12px;
}
.a2a .metric small {
  color: var(--a-muted);
}
.a2a .overview-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.7fr) minmax(270px, 1fr);
  gap: 16px;
  align-items: start;
}
.a2a .list-row {
  width: 100%;
  padding: 10px 12px;
  background: none;
  border: 0;
  border-bottom: 1px solid var(--a-border);
  text-align: left;
  color: inherit;
  display: block;
}
.a2a .list-row:last-child {
  border-bottom: 0;
}
.a2a .list-row:hover,
.a2a .list-row.selected {
  background: var(--bg-hover, var(--a-bg));
}
.a2a .list-row.selected {
  box-shadow: inset 3px 0 var(--a-accent);
}
.a2a .list-row p {
  margin: 4px 0;
  color: var(--a-muted);
  font-size: 12px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  overflow-wrap: anywhere;
}
.a2a .list-row small {
  font-size: 11px;
  color: var(--a-muted);
}
.a2a .badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 10px;
  line-height: 1.5;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  padding: 3px 7px;
  white-space: nowrap;
  color: var(--a-muted);
}
.a2a .badge.good {
  color: var(--success);
  border-color: color-mix(in srgb, var(--success) 30%, transparent);
  background: color-mix(in srgb, var(--success) 7%, transparent);
}
.a2a .badge.warn {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 30%, transparent);
  background: color-mix(in srgb, var(--warn) 7%, transparent);
}
.a2a .badge.bad {
  color: var(--error);
  border-color: color-mix(in srgb, var(--error) 30%, transparent);
}
.a2a .badge.active {
  color: var(--info);
}
.a2a .avatar {
  width: 28px;
  height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--a-border);
  background: var(--a-bg);
  border-radius: var(--radius-sm, 2px);
  color: var(--a-accent);
  flex-shrink: 0;
}
.a2a .agent-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.a2a .agent-card {
  padding: 12px;
  text-align: left;
  color: inherit;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .agent-card:hover {
  border-color: var(--a-accent);
}
.a2a .agent-card p {
  font-size: 12px;
  color: var(--a-muted);
  min-height: 40px;
}
.a2a .chips {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.a2a .toolbar {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
}
.a2a .input {
  width: 100%;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  background: var(--bg-input, var(--a-bg));
  padding: 6px 10px;
  font-size: 12px;
  min-width: 0;
}
.a2a .input::placeholder {
  color: var(--a-muted);
  opacity: 0.7;
}
.a2a .search {
  position: relative;
  flex: 1;
  min-width: 190px;
}
.a2a .search svg {
  position: absolute;
  top: 9px;
  left: 10px;
  color: var(--a-muted);
}
.a2a .search .input {
  padding-left: 32px;
}
.a2a .toolbar select {
  width: auto;
  max-width: 220px;
}
.a2a .toolbar label {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--a-muted);
  font-size: 11px;
}
.a2a .toolbar input[type="date"] {
  width: 142px;
}
.a2a .segmented {
  display: flex;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  padding: 3px;
  gap: 3px;
}
.a2a .segmented button {
  background: none;
  border: 0;
  border-radius: var(--radius-sm, 2px);
  padding: 6px 9px;
  color: var(--a-muted);
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.a2a .segmented button[aria-pressed="true"] {
  background: var(--a-card);
  color: var(--a-text);
}
.a2a .exchange-grid {
  display: grid;
  grid-template-columns: minmax(280px, 0.8fr) minmax(0, 1.2fr);
  align-items: start;
  gap: 16px;
}
.a2a .exchange-list {
  max-height: 720px;
  overflow: auto;
}
.a2a .timeline {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.a2a .timeline-item {
  position: relative;
  border-left: 1px solid var(--a-border);
  padding-left: 20px;
  margin-left: 7px;
}
.a2a .timeline-item:before {
  content: "";
  position: absolute;
  left: -4px;
  top: 7px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--a-accent);
}
.a2a .message-body {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 12px;
  line-height: 1.75;
  margin-top: 9px !important;
}
.a2a .detail-meta {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  padding: 10px 12px;
  border-bottom: 1px solid var(--a-border);
  font-size: 12px;
  color: var(--a-muted);
}
.a2a .artifacts {
  border-top: 1px solid var(--a-border);
  padding: 12px;
}
.a2a .empty {
  padding: 24px 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 8px;
  text-align: center;
  color: var(--a-muted);
}
.a2a .empty h3 {
  color: var(--a-text);
}
.a2a .empty p {
  max-width: 390px;
  font-size: 12px;
}
.a2a .notice {
  padding: 12px 15px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
  overflow-wrap: anywhere;
}
.a2a .notice.error {
  border-color: var(--error);
  color: var(--error);
}
.a2a .pagination {
  padding: 12px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-top: 1px solid var(--a-border);
  font-size: 12px;
  color: var(--a-muted);
}
.a2a .connection-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}
.a2a .connection {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .kv {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  font-size: 12px;
  gap: 10px;
}
.a2a .kv dt {
  color: var(--a-muted);
}
.a2a .kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.a2a .connection-footer {
  border-top: 1px solid var(--a-border);
  padding-top: 16px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.a2a .overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: var(--bg-overlay, rgb(0 0 0 / 0.5));
  display: flex;
  justify-content: flex-end;
}
.a2a .sheet {
  background: var(--a-bg);
  border-left: 1px solid var(--a-border);
  width: min(560px, 100%);
  height: 100%;
  overflow: auto;
  box-shadow: var(--shadow-popover, none);
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 24px;
}
.a2a .field {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
  font-weight: 550;
}
.a2a .field small {
  color: var(--a-muted);
  font-weight: 400;
}
.a2a .steps {
  display: flex;
  gap: 8px;
}
.a2a .step {
  flex: 1;
  padding: 8px 0;
  border-bottom: 2px solid var(--a-border);
  color: var(--a-muted);
  font-size: 11px;
}
.a2a .step.current {
  border-color: var(--a-accent);
  color: var(--a-text);
}
.a2a .option {
  padding: 20px;
  display: flex;
  gap: 14px;
  text-align: left;
  color: inherit;
  align-items: center;
  width: 100%;
}
.a2a .option[aria-pressed="true"] {
  border-color: var(--a-accent);
}
.a2a .checks {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.a2a .checks label {
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.a2a input[type="checkbox"] {
  accent-color: var(--a-accent);
}
.a2a .map {
  display: grid;
  grid-template-columns: 220px minmax(40px, 1fr) minmax(240px, 1.5fr);
  align-items: center;
  padding: 20px;
  min-height: 260px;
}
.a2a .map-hub {
  border: 1px solid var(--a-accent);
  border-radius: var(--radius-md, 4px);
  padding: 16px;
  text-align: center;
  background: var(--a-bg);
}
.a2a .map-lines {
  align-self: stretch;
  position: relative;
}
.a2a .map-lines:before {
  content: "";
  position: absolute;
  top: 50%;
  width: 100%;
  border-top: 1px solid var(--a-border);
}
.a2a .map-peers {
  border-left: 1px solid var(--a-border);
  padding-left: 24px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.a2a .map-peer {
  position: relative;
}
.a2a .map-peer:before {
  content: "";
  position: absolute;
  left: -24px;
  width: 24px;
  top: 50%;
  border-top: 1px solid var(--a-border);
}
.a2a .map-peer button {
  width: 100%;
  text-align: left;
  padding: 12px;
  color: inherit;
  display: flex;
  align-items: center;
  gap: 12px;
}
.a2a .spin {
  animation: a2a-spin 1s linear infinite;
}
@keyframes a2a-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .a2a .spin {
    animation: none;
  }
}
@media (max-width: 1000px) {
  .a2a .overview-grid,
  .a2a .exchange-grid {
    grid-template-columns: 1fr;
  }
  .a2a .exchange-list {
    max-height: 390px;
  }
  .a2a .metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .a2a .header {
    padding: 12px 16px 0;
  }
  .a2a .main {
    padding: 16px;
  }
  .a2a .title-line {
    align-items: flex-start;
  }
  .a2a h1 {
    font-size: 18px;
  }
  .a2a .tabs {
    gap: 0;
  }
  .a2a .tab svg {
    display: none;
  }
  .a2a .metrics {
    gap: 8px;
  }
  .a2a .metric {
    padding: 14px;
  }
  .a2a .metric-value {
    font-size: 22px;
  }
  .a2a .connection-grid {
    grid-template-columns: 1fr;
  }
  .a2a .map {
    grid-template-columns: 1fr;
    padding: 20px;
    gap: 24px;
  }
  .a2a .map-lines {
    display: none;
  }
  .a2a .map-peers {
    padding-left: 15px;
  }
  .a2a .map-peer:before {
    left: -15px;
    width: 15px;
  }
  .a2a .sheet {
    padding: 20px;
  }
}
`;import{jsx as e,jsxs as t,Fragment as w}from"react/jsx-runtime";var sa="/api/apps/a2a",Fe={q:"",status:"",peer:"",agent_address:"",from:"",to:""},L=[{name:"Overview",icon:le},{name:"Agents",icon:F},{name:"Exchanges",icon:U},{name:"Connections",icon:Ae}];function ca(i,n){return`${sa}${n}${n.includes("?")?"&":"?"}project_id=${encodeURIComponent(i)}`}async function Re(i,n,o){let p=await fetch(ca(i,n),{credentials:"same-origin",...o});if(!p.ok){let d=await p.text();throw Error(d.length<300&&!d.includes("<html")?d:`Request failed (${p.status}).`)}return p.json()}function K(i,n,o=0){let[p,d]=u({error:"",loading:!0,key:""}),[l,s]=u(0),r=`${i}:${n}`;return Pe(()=>{if(n===null)return;let m=new AbortController;return d((g)=>({data:g.key===r?g.data:void 0,error:"",loading:!0,key:r})),Re(i,n,{signal:m.signal}).then((g)=>{if(!m.signal.aborted)d({data:g,loading:!1,error:"",key:r})}).catch((g)=>{if(!m.signal.aborted)d((h)=>({...h,loading:!1,error:g.message}))}),()=>m.abort()},[i,n,r,o,l]),{...p,data:p.key===r?p.data:void 0,loading:n!==null&&(p.loading||p.key!==r),reload:()=>s((m)=>m+1)}}function ge(i){if(!i)return"Never";let n=new Date(i);return Number.isNaN(+n)?"Unknown":n.toLocaleString()}function Je(i){if(!i)return"Never";let n=Date.now()-new Date(i).getTime();if(!Number.isFinite(n))return"Unknown";let o=Math.max(0,Math.floor(n/60000));return o<1?"Just now":o<60?`${o}m ago`:o<1440?`${Math.floor(o/60)}h ago`:`${Math.floor(o/1440)}d ago`}function ue(i,n){let o=n==="from"?i.direction==="inbound":i.direction==="outbound";return i[`${n}_agent_name`]||(o?"Remote agent":`Agent ${i[`${n}_agent_id`]}`)}function Ie(i,n){return!i.peer_id?"Local exchange":n.find((o)=>o.id===i.peer_id)?.name||i.peer_id}function pa(i,n){return i?.length?i.map((o)=>o==="*"?"All exposed agents (all projects)":n.find((p)=>p.kind==="local"&&(String(p.id)===o||p.name===o))?.name||o).join(", "):"No inbound access"}function Te({status:i}){let n=["completed","running"].includes(i)?"good":i==="failed"?"bad":i==="input_required"?"warn":["working","submitted"].includes(i)?"active":"";return e("span",{className:`badge ${n}`,children:i.replaceAll("_"," ")})}function J({title:i,children:n,action:o}){return t("div",{className:"empty",children:[e(I,{size:28,strokeWidth:1.4}),e("h3",{children:i}),e("p",{children:n}),o]})}function $({message:i,retry:n}){return t("div",{role:"alert",className:"notice error",children:[e(_,{size:16}),e("span",{className:"grow",children:i}),n&&e("button",{className:"btn quiet",onClick:n,children:"Retry"})]})}function me(){return t("div",{className:"empty",role:"status",children:[e(S,{className:"spin",size:20}),"Loading…"]})}function ma({value:i}){let[n,o]=u("");return t("button",{className:"btn quiet",title:i,onClick:async()=>{try{await navigator.clipboard.writeText(i),o("Copied")}catch{o("Copy unavailable")}},children:[n==="Copied"?e(ie,{size:13}):e(Ce,{size:13}),n||"Copy address"]})}function Ke({title:i,onClose:n,children:o}){let p=He(null),d=He(n);return d.current=n,Pe(()=>{let l=document.activeElement;return p.current?.querySelector("button")?.focus(),()=>l?.focus()},[]),e("div",{className:"overlay",onMouseDown:(l)=>{if(l.target===l.currentTarget)n()},children:t("div",{ref:p,className:"sheet",role:"dialog","aria-modal":"true","aria-label":i,onKeyDown:(l)=>{if(l.key==="Escape")l.stopPropagation(),d.current();if(l.key==="Tab"){let s=[...p.current?.querySelectorAll('button:not(:disabled),a[href],input,select,textarea,[tabindex="0"]')||[]],r=s[0],m=s.at(-1);if(l.shiftKey&&document.activeElement===r)l.preventDefault(),m?.focus();else if(!l.shiftKey&&document.activeElement===m)l.preventDefault(),r?.focus()}},children:[t("div",{className:"row between",children:[e("h2",{children:i}),e("button",{className:"icon-btn","aria-label":"Close panel",onClick:n,children:e(pe,{size:20})})]}),o]})})}function Ve({task:i,connections:n,selected:o,onClick:p}){return t("button",{className:`list-row ${o?"selected":""}`,onClick:p,"aria-pressed":o,children:[t("div",{className:"row between",children:[t("span",{className:"row grow",children:[e("strong",{className:"truncate",children:ue(i,"from")}),e(W,{size:12}),e("strong",{className:"truncate",children:ue(i,"to")})]}),e(Te,{status:i.status})]}),e("p",{children:i.preview||(i.kind==="ask"?"Request awaiting its first recorded message":"One-way message")}),t("div",{className:"row wrap",children:[e("small",{children:Ie(i,n)}),t("small",{children:["· #",i.id]}),!!i.pending_delivery&&e("span",{className:"badge warn",children:"Delivery pending"}),!!i.poll_failures&&["working","submitted","input_required"].includes(i.status)&&e("span",{className:"badge warn",children:"Sync retrying"}),e("small",{style:{marginLeft:"auto"},title:ge(i.updated_at),children:Je(i.updated_at)})]})]})}function ga({bytes:i,name:n}){let[o,p]=u("");return t(w,{children:[t("button",{className:"btn quiet",onClick:()=>{try{let l=atob(i),s=Uint8Array.from(l,(g)=>g.charCodeAt(0)),r=URL.createObjectURL(new Blob([s],{type:"application/octet-stream"})),m=document.createElement("a");m.href=r,m.download=n,m.click(),setTimeout(()=>URL.revokeObjectURL(r),1000)}catch{p("This embedded file could not be decoded. Its original data is available below.")}},children:[e(re,{size:13}),"Download ",n]}),o&&e("p",{className:"muted",children:o})]})}function ua({value:i,index:n}){let o=i&&typeof i==="object"?i:{},p=Array.isArray(o.parts)?o.parts:[];return t("article",{className:"panel pad stack gap-sm",children:[t("div",{className:"row",children:[e(re,{size:16}),e("strong",{children:typeof o.name==="string"?o.name:`Artifact ${n+1}`})]}),typeof o.description==="string"&&e("p",{className:"muted",children:o.description}),p.map((d,l)=>{if(!d||typeof d!=="object")return null;let s=d.url||d.file?.uri,r=typeof s==="string"&&/^https?:\/\//i.test(s),m=typeof d.text==="string"?d.text:null;return t("div",{children:[m&&e("p",{className:"message-body",children:m}),d.data&&e("pre",{children:JSON.stringify(d.data,null,2)}),r&&t("a",{className:"btn quiet",href:s,target:"_blank",rel:"noopener noreferrer",children:[e(_e,{size:13}),typeof d.file?.name==="string"?d.file.name:"Open returned file"]}),typeof(d.raw||d.file?.bytes)==="string"&&e(ga,{bytes:d.raw||d.file.bytes,name:typeof d.file?.name==="string"?d.file.name:"artifact.bin"})]},l)}),t("details",{children:[e("summary",{className:"muted",children:"Artifact JSON"}),e("pre",{children:JSON.stringify(i,null,2)})]})]})}function va({project:i,task:n,revision:o,connections:p}){let d=K(i,`/tasks/${n.id}/messages`,o),l={...n,...d.data?.task};return t("section",{className:"panel",children:[t("div",{className:"section-head stack gap-sm",children:[t("div",{className:"row between",children:[t("span",{className:"eyebrow",children:["Exchange #",n.id]}),e(Te,{status:l.status})]}),t("h2",{children:[ue(n,"from")," ",e("span",{className:"muted",children:"→"})," ",ue(n,"to")]}),t("p",{className:"muted",style:{fontSize:12},children:[l.kind==="ask"?"Request with reply":"One-way message"," ·"," ",Ie(n,p)]})]}),t("div",{className:"detail-meta",children:[t("span",{className:"row",children:[e(Ne,{size:13}),"Started ",ge(n.created_at)]}),["from","to"].map((s)=>{let r=s==="from"?n.direction!=="inbound":n.direction!=="outbound",m=n[`${s}_agent_id`];return r&&m>0&&t("a",{href:`/agents/${m}?thread=${encodeURIComponent(l[`${s}_thread_id`]||"main")}`,className:"row",children:[ue(n,s)," thread ",e(Z,{size:13})]},s)})]}),!!n.pending_delivery&&e("div",{className:"pad",children:t("div",{className:"notice",children:[e(_,{size:15}),"A recorded result is waiting to be delivered. Automatic retries are active."]})}),n.direction==="outbound"&&t("div",{className:"detail-meta",children:["Remote sync: ",ge(l.last_synced_at),!!n.poll_failures&&t(w,{children:[" · ",n.poll_failures," failed attempts; retrying automatically"]})]}),d.error?e("div",{className:"pad",children:e($,{message:`Could not load this exchange: ${d.error}`,retry:d.reload})}):d.loading&&!d.data?e(me,{}):e("div",{className:"timeline",children:!d.data?.messages.length?e(J,{title:"No messages recorded",children:"This exchange has no recorded message content yet."}):d.data.messages.map((s)=>t("article",{className:"timeline-item",children:[t("div",{className:"row wrap",children:[e("strong",{children:ue(n,s.from_agent_id===n.from_agent_id?"from":"to")}),s.status_after&&e(Te,{status:s.status_after}),e("small",{className:"muted",title:ge(s.created_at),children:ge(s.created_at)})]}),e("p",{className:"message-body",children:s.body})]},s.id))}),!!l.artifacts?.length&&t("div",{className:"artifacts stack",children:[t("h3",{children:["Returned artifacts"," ",t("span",{className:"muted",children:["(",l.artifacts.length,")"]})]}),l.artifacts.map((s,r)=>e(ua,{value:s,index:r},r))]})]})}function ba({project:i,agent:n,onClose:o,onExchanges:p}){let d=n.kind!=="local",l=K(i,d?`/network/card?address=${encodeURIComponent(n.address)}`:null),s=l.data?.card||n.card;return t(Ke,{title:n.name,onClose:o,children:[t("div",{className:"row",children:[e("div",{className:"avatar",children:e(F,{size:21})}),t("div",{children:[e("strong",{children:n.peer_name}),e("p",{className:"muted",children:d?"Remote agent":"Local agent"})]}),e(Te,{status:n.status})]}),e("p",{children:n.description||s?.description||"This agent has no published description."}),t("div",{className:"row wrap",children:[e(ma,{value:n.address}),t("button",{className:"btn",onClick:()=>p(n),children:[e(U,{size:13}),"View exchanges"]}),n.id&&t("a",{className:"btn",href:`/agents/${n.id}`,children:["Open agent ",e(Z,{size:13})]})]}),d&&e("p",{className:"notice",children:"Remote directory entries are cached. Check the connection to refresh discovery; a published card does not prove task execution is available."}),l.error&&e($,{message:l.error,retry:l.reload}),d&&l.loading&&t("p",{className:"muted row",children:[e(S,{size:14,className:"spin"}),"Retrieving current Agent Card…"]}),t("div",{className:"stack",children:[e("h3",{children:"Capabilities"}),s?.skills?.length?s.skills.map((r)=>t("div",{className:"panel pad",children:[e("strong",{children:r.name||r.id}),e("p",{className:"subtitle",children:r.description||"No description provided."}),r.examples?.map((m,g)=>t("p",{className:"message-body muted",children:["“",m,"”"]},g))]},r.id)):n.skills?.length?e("div",{className:"chips",children:n.skills.map((r)=>e("span",{className:"badge",children:r},r))}):e("p",{className:"muted",children:"No capabilities published yet."})]}),s&&t(w,{children:[t("dl",{className:"kv",children:[e("dt",{children:"Provider"}),e("dd",{children:s.provider?.organization||"Not specified"}),e("dt",{children:"Version"}),e("dd",{children:s.version||"Not specified"}),e("dt",{children:"Input"}),e("dd",{children:s.defaultInputModes?.join(", ")||"Not specified"}),e("dt",{children:"Output"}),e("dd",{children:s.defaultOutputModes?.join(", ")||"Not specified"}),e("dt",{children:"Streaming"}),e("dd",{children:s.capabilities?.streaming?"Supported":"Not advertised"})]}),t("details",{children:[e("summary",{children:"Agent Card JSON"}),e("pre",{children:JSON.stringify(s,null,2)})]})]})]})}function Ue({title:i,value:n,onChange:o,agents:p}){let[d,l]=u(n.join(", ")),s=(r)=>{l(r.join(", ")),o(r)};return t("div",{className:"stack gap-sm",children:[e("h3",{children:i}),e("div",{className:"checks",children:p.filter((r)=>r.kind==="local").map((r)=>{let m=String(r.id);return t("label",{children:[e("input",{type:"checkbox",checked:n.includes(m)||n.includes(r.name),disabled:n.includes("*"),onChange:(g)=>s(g.target.checked?[...n,m]:n.filter((h)=>h!==m&&h!==r.name))}),r.name]},r.address)})}),t("label",{className:"field",children:["Agent names or IDs",e("input",{className:"input","aria-label":i,placeholder:"None — no inbound access",value:d,onChange:(r)=>{l(r.target.value),o(r.target.value.split(",").map((m)=>m.trim()).filter(Boolean))}}),e("small",{children:"Empty grants deny access. * grants every exposed agent across this installation, including other projects."})]})]})}function ha({project:i,agents:n,edit:o,onClose:p,onSaved:d}){let[l,s]=u(o?1:0),[r,m]=u(o?.kind||"agent_card"),[g,h]=u({card_url:"",id:"",name:"",base_url:"",token:""}),[k,P]=u(o?.discover_agents||[]),[A,ve]=u(o?.invoke_agents||[]),[R,be]=u(!1),[z,j]=u(""),he=!!o||(r==="agent_card"?!!g.card_url.trim():!!g.id.trim()&&!!g.base_url.trim()&&!!g.token.trim()),Se=async()=>{be(!0),j("");try{await Re(i,o?`/connections/${encodeURIComponent(o.id)}`:"/connections",{method:o?"PATCH":"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(o?{discover_agents:k,invoke_agents:A}:{...g,kind:r,discover_agents:k,invoke_agents:A})}),d()}catch(b){j(b instanceof Error?b.message:String(b))}finally{be(!1)}},f=(b,E,ee,ae="text")=>t("label",{className:"field",children:[E,e("input",{className:"input",type:ae,autoComplete:"off",value:g[b],placeholder:ee,onChange:(fe)=>h((Q)=>({...Q,[b]:fe.target.value}))})]});return t(Ke,{title:o?`Access · ${o.name}`:"Add connection",onClose:()=>{if(!R)p()},children:[e("p",{className:"muted",children:"Connections are shared across this A2A installation. Local agents and exchanges remain scoped to the current project."}),!o&&e("div",{className:"steps",children:["01 · Choose type","02 · Configure","03 · Review"].map((b,E)=>e("span",{className:`step ${l===E?"current":""}`,children:b},b))}),l===0&&e("div",{className:"stack",children:[{kind:"agent_card",icon:T,title:"Public agent",detail:"Connect an external agent using its Agent Card URL."},{kind:"node",icon:H,title:"Apteva installation",detail:"Connect a node and discover the agents it shares."}].map((b)=>t("button",{className:"panel option","aria-pressed":r===b.kind,onClick:()=>m(b.kind),children:[e(b.icon,{size:24}),t("div",{children:[e("strong",{children:b.title}),e("p",{className:"subtitle",children:b.detail})]}),r===b.kind&&e(q,{size:18})]},b.kind))}),l===1&&t("div",{className:"stack",children:[!o&&(r==="agent_card"?t(w,{children:[f("card_url","Agent Card URL","https://agent.example/.well-known/agent-card.json","url"),f("token","Bearer token (optional)","Leave empty for anonymous discovery","password")]}):t(w,{children:[f("name","Display name","e.g. Research team"),f("id","Connection ID","research-team"),f("base_url","A2A base URL","https://node.example/api/apps/a2a","url"),f("token","Pairing token","Reciprocal pairing token","password")]})),r==="node"&&t(w,{children:[t("div",{className:"notice",children:[e(ce,{size:17}),e("span",{children:"These grants control which of your agents this remote node may discover and invoke. Access to its agents is controlled on the remote node."})]}),e(Ue,{title:"Agents this node may discover",agents:n,value:k,onChange:P}),e(Ue,{title:"Agents this node may invoke",agents:n,value:A,onChange:ve})]})]}),l===2&&t("div",{className:"stack",children:[t("div",{className:"panel pad",children:[e("h3",{children:r==="node"?g.name||g.id:"Public agent"}),e("p",{className:"subtitle break",children:r==="node"?g.base_url:g.card_url})]}),t("dl",{className:"kv",children:[e("dt",{children:"Authentication"}),e("dd",{children:g.token?"Bearer token provided":"Anonymous"}),r==="node"&&t(w,{children:[e("dt",{children:"Discovery grants"}),e("dd",{children:k.join(", ")||"No inbound access"}),e("dt",{children:"Invocation grants"}),e("dd",{children:A.join(", ")||"No inbound access"})]})]}),e("p",{className:"notice",children:r==="agent_card"?"The Agent Card will be validated before saving. No message will be sent.":"Save the pairing, then run Check connection to verify remote discovery. No message will be sent."})]}),z&&e($,{message:z}),t("div",{className:"row between",style:{marginTop:"auto"},children:[l>0&&!o?t("button",{className:"btn",disabled:R,onClick:()=>s((b)=>b-1),children:[e(oe,{size:14}),"Back"]}):e("span",{}),l<2&&!o?t("button",{className:"btn primary",disabled:l===1&&!he,onClick:()=>s((b)=>b+1),children:["Continue ",e(V,{size:14})]}):t("button",{className:"btn primary",disabled:R||!he,onClick:Se,children:[R?e(S,{size:14,className:"spin"}):e(ie,{size:14}),o?"Save access":"Save connection"]})]})]})}function fa(i){return e(ya,{...i},`${i.projectId}:${i.installId}`)}function ya({projectId:i}){let[n,o]=u("Overview"),[p,d]=u(0),[l,s]=u(null),[r,m]=u(null),[g,h]=u(!1),[k,P]=u(null),[A,ve]=u(""),[R,be]=u(!1),[z,j]=u({}),[he,Se]=u({}),[f,b]=u(Fe),[E,ee]=u(0),[ae,fe]=u(""),[Q,$e]=u(""),[Ee,te]=u("directory"),[xe,Qe]=u("");Pe(()=>{let a=setTimeout(()=>Qe(f.q),250);return()=>clearTimeout(a)},[f.q]);let N=K(i,"/network",p),D=K(i,"/connections",p),De=K(i,"/overview",p),M=K(i,"/tasks?limit=5",p),X=K(i,"/tasks?status=attention&limit=4",p),Le=new URLSearchParams({...f,q:xe,limit:"30",offset:String(E)}),y=K(i,n==="Exchanges"?`/tasks?${Le}`:null,p),C=D.data?.connections||[],O=N.data?.agents||[],ye=la(()=>d((a)=>a+1),[]),we=He(null);Pe(()=>()=>{if(we.current)clearTimeout(we.current)},[]),We("a2a",i,(a)=>{if(a.topic==="task.created"||a.topic==="task.updated"){if(we.current)clearTimeout(we.current);we.current=setTimeout(ye,200)}});let G=(a={},v)=>{b({...Fe,...a}),Qe(a.q||""),ee(0),m(v||null),o("Exchanges")},ne=(a,v)=>{b((B)=>({...B,[a]:v})),ee(0),m(null)},Oe=r&&(y.data?.tasks.find((a)=>a.id===r.id)||r),Be=da(()=>O.filter((a)=>(!Q||a.peer_id===Q)&&`${a.name} ${a.description} ${a.peer_name} ${a.skills?.join(" ")||""}`.toLowerCase().includes(ae.toLowerCase())),[O,Q,ae]),je=async(a)=>{Se((v)=>({...v,[a.id]:!0}));try{let v=await Re(i,`/connections/${encodeURIComponent(a.id)}/check`,{method:"POST"});if(j((B)=>({...B,[a.id]:v})),v.ok)ye()}catch(v){j((B)=>({...B,[a.id]:{ok:!1,message:v instanceof Error?v.message:String(v),checked_at:new Date().toISOString(),latency_ms:0}}))}finally{Se((v)=>({...v,[a.id]:!1}))}},ea=async()=>{if(!k)return;be(!0),ve("");try{await Re(i,`/connections/${encodeURIComponent(k.id)}`,{method:"DELETE"}),P(null),ye()}catch(a){ve(a instanceof Error?a.message:String(a))}finally{be(!1)}},Y=De.data;return t("div",{className:"a2a",children:[e("style",{children:Ze}),t("header",{className:"header",children:[t("div",{className:"row between title-line",children:[t("div",{children:[e("h1",{children:"Agent to Agent"}),e("p",{className:"subtitle",children:"Manage agents, exchanges, and connections."})]}),t("div",{className:"row",children:[e("button",{className:"btn quiet","aria-label":"Refresh workspace",onClick:ye,children:e(de,{size:14})}),t("button",{className:"btn primary",onClick:()=>h("new"),children:[e(x,{size:14}),"Connect"]})]})]}),e("nav",{className:"tabs",role:"tablist","aria-label":"A2A views",onKeyDown:(a)=>{if(!["ArrowLeft","ArrowRight","Home","End"].includes(a.key))return;a.preventDefault();let v=L.findIndex((aa)=>aa.name===n),B=a.key==="Home"?0:a.key==="End"?L.length-1:(v+(a.key==="ArrowRight"?1:-1)+L.length)%L.length;o(L[B].name),document.getElementById(`a2a-tab-${L[B].name}`)?.focus()},children:L.map(({name:a,icon:v})=>t("button",{role:"tab",id:`a2a-tab-${a}`,"aria-controls":`a2a-view-${a}`,"aria-selected":n===a,tabIndex:n===a?0:-1,className:"tab",onClick:()=>o(a),children:[e(v,{size:15}),a,a==="Agents"&&N.data&&e("span",{className:"badge",children:O.length}),a==="Connections"&&D.data&&e("span",{className:"badge",children:C.length})]},a))})]}),t("main",{className:"main stack",id:`a2a-view-${n}`,role:"tabpanel","aria-labelledby":`a2a-tab-${n}`,children:[N.error&&e($,{message:`Agent directory: ${N.error}`,retry:N.reload}),D.error&&e($,{message:`Connections: ${D.error}`,retry:D.reload}),N.data?.warnings?.map((a)=>t("div",{className:"notice",children:[e(_,{size:15}),a]},a)),n==="Overview"&&t(w,{children:[t("div",{className:"row between",children:[t("div",{children:[e("h2",{children:"Overview"}),e("p",{className:"subtitle",children:"Project exchanges · all time"})]}),t("small",{className:"muted",children:["Updated ",Je(Y?.as_of)]})]}),De.error?e($,{message:De.error,retry:De.reload}):e("div",{className:"metrics",children:[{label:"Active exchanges",value:Y?.active,note:"Submitted or working",icon:ke,status:"active"},{label:"Waiting for input",value:Y?.input_required,note:"An agent has a question",icon:U,status:"input_required"},{label:"Needs attention",value:Y?.attention,note:"Failures, input, or delivery retries",icon:_,status:"attention"},{label:"Completed",value:Y?.completed,note:`${Y?.total??"—"} exchange${Y?.total===1?"":"s"} in total`,icon:q,status:"completed"}].map((a)=>t("button",{className:"panel metric",onClick:()=>G({status:a.status}),children:[t("div",{className:"row",children:[e("span",{children:a.label}),e(a.icon,{size:16})]}),e("div",{className:"metric-value",children:a.value??"—"}),e("small",{children:a.note})]},a.label))}),t("div",{className:"overview-grid",children:[t("section",{className:"panel",children:[t("div",{className:"section-head row between",children:[e("h2",{children:"Recent exchanges"}),t("button",{className:"btn quiet",onClick:()=>G(),children:["View all ",e(W,{size:13})]})]}),M.error?e("div",{className:"pad",children:e($,{message:M.error,retry:M.reload})}):M.loading&&!M.data?e(me,{}):M.data?.tasks.length?M.data.tasks.map((a)=>e(Ve,{task:a,connections:C,onClick:()=>G({},a)},a.id)):e(J,{title:"The first exchange starts with an agent",children:"Attach A2A to two agents and ask one to collaborate with the other. Their work will appear here."})]}),t("div",{className:"stack",children:[t("section",{className:"panel",children:[t("div",{className:"section-head row between",children:[e("h2",{children:"Needs attention"}),e(_,{size:16,className:"muted"})]}),X.error?e("div",{className:"pad",children:e($,{message:X.error,retry:X.reload})}):X.loading&&!X.data?e(me,{}):X.data?.tasks.length?X.data.tasks.map((a)=>e(Ve,{task:a,connections:C,onClick:()=>G({status:"attention"},a)},a.id)):e(J,{title:"Nothing needs attention",children:"No failed exchanges, unanswered input requests, or pending delivery retries."})]}),t("section",{className:"panel pad stack",children:[t("div",{className:"row between",children:[e("h3",{children:"Connected network"}),e(I,{size:17,className:"muted"})]}),t("div",{className:"row wrap",children:[t("span",{className:"badge",children:[N.data?O.filter((a)=>a.kind==="local").length:"—"," ","local agents"]}),t("span",{className:"badge",children:[N.data?O.filter((a)=>a.kind!=="local").length:"—"," ","cached remote agents"]})]}),t("p",{className:"muted",children:[D.data?C.length:"—"," external connections ·"," ",N.data?.node?.display_name||"This installation"]}),t("button",{className:"btn",onClick:()=>{o("Agents"),te("map")},children:["Explore network ",e(W,{size:13})]})]})]})]})]}),n==="Agents"&&t(w,{children:[t("div",{className:"row between wrap",children:[t("div",{children:[e("h2",{children:"Agent directory"}),e("p",{className:"subtitle",children:"Local agents in this project and remote agents discovered by this installation."})]}),t("div",{className:"segmented",children:[t("button",{"aria-pressed":Ee==="directory",onClick:()=>te("directory"),children:[e(le,{size:14}),"Directory"]}),t("button",{"aria-pressed":Ee==="map",onClick:()=>te("map"),children:[e(I,{size:14}),"Map"]})]})]}),Ee==="map"&&t("section",{className:"panel",children:[t("div",{className:"map",children:[t("div",{className:"map-hub",children:[e(H,{size:28,style:{margin:"0 auto 12px"}}),e("h3",{children:N.data?.node?.display_name||"This installation"}),t("p",{className:"subtitle",children:[O.filter((a)=>a.kind==="local").length," local agents in this project"]}),e("button",{className:"btn quiet",style:{marginTop:14},onClick:()=>{$e("local"),te("directory")},children:"View agents"})]}),e("div",{className:"map-lines"}),e("div",{className:"map-peers",children:C.length?C.map((a)=>e("div",{className:"map-peer",children:t("button",{className:"panel",onClick:()=>{$e(a.id),te("directory"),fe("")},children:[a.kind==="node"?e(H,{size:18}):e(T,{size:18}),t("span",{className:"grow",children:[e("strong",{children:a.name}),t("p",{className:"subtitle",children:[a.agents?.length||0," cached agents ·"," ",a.kind==="node"?"Installation":"Public agent"]})]}),e(V,{size:16})]})},a.id)):t(J,{title:"Extend your network",children:["Connect another installation or a public agent.",e("button",{className:"btn",style:{marginTop:12},onClick:()=>h("new"),children:"Add connection"})]})})]}),e("p",{className:"pad muted",style:{fontSize:12,borderTop:"1px solid var(--a-border)"},children:"Lines show configured connections. Select a node to see its agents and open their exchanges. Availability is verified from Connections."})]}),Ee==="directory"&&t(w,{children:[t("div",{className:"toolbar",children:[t("label",{className:"search",children:[e(se,{size:15}),e("input",{"aria-label":"Search agents",className:"input",placeholder:"Search names, capabilities, or installations…",value:ae,onChange:(a)=>fe(a.target.value)})]}),t("select",{className:"input","aria-label":"Agent installation",value:Q,onChange:(a)=>$e(a.target.value),children:[e("option",{value:"",children:"All installations"}),e("option",{value:"local",children:"This installation"}),C.map((a)=>e("option",{value:a.id,children:a.name},a.id))]}),t("small",{className:"muted",children:[Be.length," agents"]})]}),N.loading&&!N.data?e(me,{}):Be.length?e("div",{className:"agent-grid",children:Be.map((a)=>t("button",{className:"panel agent-card",onClick:()=>s(a),children:[t("div",{className:"row",children:[e("div",{className:"avatar",children:a.kind==="local"?e(F,{size:20}):e(T,{size:20})}),t("div",{className:"grow",children:[e("h3",{className:"truncate",children:a.name}),e("small",{className:"muted",children:a.peer_name})]}),e(V,{size:14,className:"muted"})]}),e("p",{children:a.description||"Open this agent to inspect its published capabilities."}),t("div",{className:"chips",children:[a.skills?.slice(0,3).map((v)=>e("span",{className:"badge",children:v},v)),(a.skills?.length||0)>3&&t("span",{className:"badge",children:["+",a.skills.length-3]})]}),t("div",{className:"row between",style:{marginTop:"auto"},children:[e(Te,{status:a.status||"unknown"}),e("small",{className:"muted",children:a.kind==="local"?"This project":`Cached ${Je(a.fetched_at).toLowerCase()}`})]})]},a.address))}):!N.error&&t(J,{title:ae||Q?"No matching agents":"No agents discovered yet",children:["Attach A2A to local agents, or check a connection to discover remote agents.",e("button",{className:"btn",style:{marginTop:12},onClick:()=>o("Connections"),children:"Manage connections"})]})]})]}),n==="Exchanges"&&t(w,{children:[t("div",{children:[e("h2",{children:"Exchanges"}),e("p",{className:"subtitle",children:"Search the full exchange history, inspect replies, and open the originating threads."})]}),t("div",{className:"toolbar",children:[t("label",{className:"search",children:[e(se,{size:15}),e("input",{"aria-label":"Search exchanges",className:"input",placeholder:"Search agents or message content…",value:f.q,onChange:(a)=>ne("q",a.target.value)})]}),e("select",{"aria-label":"Exchange status",className:"input",value:f.status,onChange:(a)=>ne("status",a.target.value),children:[["","All statuses"],["open","Open"],["active","Active"],["attention","Needs attention"],["working","Working"],["submitted","Submitted"],["input_required","Waiting for input"],["completed","Completed"],["failed","Failed"],["canceled","Canceled"]].map(([a,v])=>e("option",{value:a,children:v},a))}),t("select",{"aria-label":"Exchange connection",className:"input",value:f.peer,onChange:(a)=>ne("peer",a.target.value),children:[e("option",{value:"",children:"All connections"}),e("option",{value:"local",children:"Local only"}),C.map((a)=>e("option",{value:a.id,children:a.name},a.id))]})]}),t("div",{className:"toolbar",children:[t("select",{"aria-label":"Exchange agent",className:"input",value:f.agent_address,onChange:(a)=>ne("agent_address",a.target.value),children:[e("option",{value:"",children:"All agents"}),O.map((a)=>e("option",{value:a.address,children:a.name},a.address))]}),t("label",{children:["From (UTC)",e("input",{className:"input",type:"date",value:f.from,onChange:(a)=>ne("from",a.target.value)})]}),t("label",{children:["To (UTC)",e("input",{className:"input",type:"date",value:f.to,onChange:(a)=>ne("to",a.target.value)})]}),Object.values(f).some(Boolean)&&t("button",{className:"btn quiet",onClick:()=>G(),children:["Clear filters ",e(pe,{size:12})]})]}),y.error&&e($,{message:y.error,retry:y.reload}),t("div",{className:"exchange-grid",children:[t("section",{className:"panel",children:[t("div",{className:"section-head row between",children:[t("h3",{children:["Exchanges"," ",e("span",{className:"muted",children:y.data?.total??"—"})]}),e("span",{className:"eyebrow",children:"Latest first"})]}),e("div",{className:"exchange-list",children:y.loading&&!y.data?e(me,{}):y.data?.tasks.length?y.data.tasks.map((a)=>e(Ve,{task:a,connections:C,selected:Oe?.id===a.id,onClick:()=>m(a)},a.id)):!y.error&&e(J,{title:"No exchanges found",children:"Try changing the filters, or start a collaboration between agents."})}),t("div",{className:"pagination",children:[e("span",{children:y.data?.total?`${E+1}–${E+y.data.tasks.length} of ${y.data.total}`:"0 exchanges"}),t("div",{className:"row",children:[e("button",{className:"btn quiet","aria-label":"Previous page",disabled:!E||y.loading,onClick:()=>{ee((a)=>Math.max(0,a-30)),m(null)},children:e(oe,{size:14})}),e("button",{className:"btn quiet","aria-label":"Next page",disabled:!y.data||E+30>=y.data.total||y.loading,onClick:()=>{ee((a)=>a+30),m(null)},children:e(V,{size:14})})]})]})]}),Oe?e(va,{task:Oe,project:i,revision:p,connections:C},Oe.id):e("section",{className:"panel",children:e(J,{title:"Select an exchange",children:"Read the request and replies, inspect returned artifacts, and trace the work back to each agent’s thread."})})]})]}),n==="Connections"&&t(w,{children:[t("div",{className:"row between wrap",children:[t("div",{children:[e("h2",{children:"Connected installations & public agents"}),e("p",{className:"subtitle",children:"Shared across this A2A installation. Checks verify discovery access without sending tasks."})]}),t("button",{className:"btn",onClick:()=>h("new"),children:[e(x,{size:14}),"Add connection"]})]}),D.loading&&!D.data?e(me,{}):C.length?e("div",{className:"connection-grid",children:C.map((a)=>t("article",{className:"panel connection",children:[t("div",{className:"row",children:[e("div",{className:"avatar",children:a.kind==="node"?e(H,{size:20}):e(T,{size:20})}),t("div",{className:"grow",children:[e("h3",{children:a.name}),e("small",{className:"muted",children:a.kind==="node"?"Apteva installation":"Public agent"})]}),e("span",{className:`badge ${z[a.id]?z[a.id].ok?"good":"bad":""}`,children:z[a.id]?z[a.id].ok?"Discovery verified":"Check failed":"Not checked"})]}),e("p",{className:"muted break",style:{fontSize:12},children:a.card_url||a.base_url}),t("dl",{className:"kv",children:[e("dt",{children:"Authentication"}),e("dd",{children:a.authenticated?"Bearer token configured":"Anonymous"}),e("dt",{children:"Managed by"}),e("dd",{children:a.managed_by==="operator"?"You":a.managed_by==="app"?"Another app":a.managed_by==="config"?"Installation configuration":"Agent discovery"}),a.protocol_version&&t(w,{children:[e("dt",{children:"Protocol"}),t("dd",{children:["A2A ",a.protocol_version]})]}),a.kind==="node"&&t(w,{children:[e("dt",{children:"May discover"}),e("dd",{children:a.discover_agents?.join(", ")||"No inbound access"}),e("dt",{children:"May invoke"}),e("dd",{children:pa(a.invoke_agents,O)})]})]}),t("div",{className:"stack gap-sm",children:[t("div",{className:"row between",children:[e("small",{className:"muted",children:"Discovered agents"}),e("button",{className:"icon-btn","aria-label":`View agents on ${a.name}`,onClick:()=>{$e(a.id),fe(""),te("directory"),o("Agents")},children:e(Z,{size:14})})]}),e("div",{className:"chips",children:a.agents?.length?a.agents.map((v)=>e("span",{className:"badge",children:v},v)):e("small",{className:"muted",children:"Run a check to refresh this directory."})})]}),z[a.id]&&e("div",{className:"notice",role:"status",children:t("span",{children:[z[a.id].message,e("br",{}),t("small",{children:[ge(z[a.id].checked_at)," ·"," ",z[a.id].latency_ms," ms"]})]})}),t("div",{className:"connection-footer",children:[t("button",{className:"btn",disabled:he[a.id],onClick:()=>je(a),children:[he[a.id]?e(S,{size:13,className:"spin"}):e(de,{size:13}),"Check connection"]}),e("button",{className:"btn quiet",onClick:()=>G({peer:a.id}),children:"Exchanges"}),["operator","agent"].includes(a.managed_by)&&t(w,{children:[a.kind==="node"&&t("button",{className:"btn quiet",onClick:()=>h(a),children:[e(ce,{size:13}),"Edit access"]}),e("button",{className:"icon-btn danger","aria-label":`Remove ${a.name}`,onClick:()=>{ve(""),P(a)},children:e(ze,{size:15})})]})]})]},a.id))}):!D.error&&e("section",{className:"panel",children:t(J,{title:"Bring another agent into the conversation",children:["Connect an Apteva installation or paste a public Agent Card URL. Your agents can then discover its capabilities.",t("button",{className:"btn primary",style:{marginTop:16},onClick:()=>h("new"),children:[e(x,{size:14}),"Add your first connection"]})]})})]})]}),l&&e(ba,{project:i,agent:l,onClose:()=>s(null),onExchanges:(a)=>{s(null),G(a.id?{agent_address:a.address}:{peer:a.peer_id,agent_address:a.address})}},l.address),g&&e(ha,{project:i,agents:O,edit:g==="new"?void 0:g,onClose:()=>h(!1),onSaved:()=>{h(!1),j({}),o("Connections"),ye()}}),k&&t(Ke,{title:`Remove ${k.name}?`,onClose:()=>{if(!R)P(null)},children:[e("p",{children:"This removes the connection from the whole A2A installation. Existing exchange history stays available; active remote exchanges may no longer synchronize."}),A&&e($,{message:A}),t("div",{className:"row",children:[e("button",{disabled:R,className:"btn",onClick:()=>P(null),children:"Keep connection"}),e("button",{disabled:R,className:"btn danger",onClick:ea,children:"Remove connection"})]})]})]})}export{fa as default};

//# debugId=817CBDEDB556D26B64756E2164756E21
