import{useCallback as sa,useEffect as Ne,useMemo as ca,useRef as Ke,useState as b}from"react";import{forwardRef as oa,createElement as ra}from"react";var Ge=(i)=>i.replace(/([a-z0-9])([A-Z])/g,"$1-$2").toLowerCase(),Ve=(...i)=>i.filter((n,o,l)=>{return Boolean(n)&&l.indexOf(n)===o}).join(" ");import{forwardRef as ia,createElement as We}from"react";var Ye={xmlns:"http://www.w3.org/2000/svg",width:24,height:24,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:2,strokeLinecap:"round",strokeLinejoin:"round"};var Ze=ia(({color:i="currentColor",size:n=24,strokeWidth:o=2,absoluteStrokeWidth:l,className:d="",children:s,iconNode:c,...r},g)=>{return We("svg",{ref:g,...Ye,width:n,height:n,stroke:i,strokeWidth:l?Number(o)*24/Number(n):o,className:Ve("lucide",d),...r},[...c.map(([u,w])=>We(u,w)),...Array.isArray(s)?s:[s]])});var p=(i,n)=>{let o=oa(({className:l,...d},s)=>ra(Ze,{ref:s,iconNode:n,className:Ve(`lucide-${Ge(i)}`,l),...d}));return o.displayName=`${i}`,o};var Ae=p("Activity",[["path",{d:"M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2",key:"169zse"}]]);var j=p("ArrowRight",[["path",{d:"M5 12h14",key:"1ays0h"}],["path",{d:"m12 5 7 7-7 7",key:"xquz4c"}]]);var ee=p("ArrowUpRight",[["path",{d:"M7 7h10v10",key:"1tivn9"}],["path",{d:"M7 17 17 7",key:"1vkiza"}]]);var W=p("Bot",[["path",{d:"M12 8V4H8",key:"hb8ula"}],["rect",{width:"16",height:"12",x:"4",y:"8",rx:"2",key:"enze0r"}],["path",{d:"M2 14h2",key:"vft8re"}],["path",{d:"M20 14h2",key:"4cs60a"}],["path",{d:"M15 13v2",key:"1xurst"}],["path",{d:"M9 13v2",key:"rq6x2g"}]]);var ce=p("Check",[["path",{d:"M20 6 9 17l-5-5",key:"1gmf2c"}]]);var pe=p("ChevronLeft",[["path",{d:"m15 18-6-6 6-6",key:"1wnfg3"}]]);var V=p("ChevronRight",[["path",{d:"m9 18 6-6-6-6",key:"mthhwq"}]]);var ze=p("Clock",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["polyline",{points:"12 6 12 12 16 14",key:"68esgv"}]]);var Te=p("Copy",[["rect",{width:"14",height:"14",x:"8",y:"8",rx:"2",ry:"2",key:"17jyea"}],["path",{d:"M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2",key:"zix9uf"}]]);var Se=p("ExternalLink",[["path",{d:"M15 3h6v6",key:"1q9fwt"}],["path",{d:"M10 14 21 3",key:"gplh6r"}],["path",{d:"M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6",key:"a6xqqp"}]]);var me=p("FileText",[["path",{d:"M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z",key:"1rqfz7"}],["path",{d:"M14 2v4a2 2 0 0 0 2 2h4",key:"tnqrlb"}],["path",{d:"M10 9H8",key:"b1mrlr"}],["path",{d:"M16 13H8",key:"t4e002"}],["path",{d:"M16 17H8",key:"z1uh3a"}]]);var ge=p("GitBranch",[["line",{x1:"6",x2:"6",y1:"3",y2:"15",key:"17qcm7"}],["circle",{cx:"18",cy:"6",r:"3",key:"1h7g24"}],["circle",{cx:"6",cy:"18",r:"3",key:"fqmcym"}],["path",{d:"M18 9a9 9 0 0 1-9 9",key:"n2h4wq"}]]);var ue=p("LayoutGrid",[["rect",{width:"7",height:"7",x:"3",y:"3",rx:"1",key:"1g98yp"}],["rect",{width:"7",height:"7",x:"14",y:"3",rx:"1",key:"6d4xhi"}],["rect",{width:"7",height:"7",x:"14",y:"14",rx:"1",key:"nxv5o0"}],["rect",{width:"7",height:"7",x:"3",y:"14",rx:"1",key:"1bb6yr"}]]);var Ee=p("Link2",[["path",{d:"M9 17H7A5 5 0 0 1 7 7h2",key:"8i5ue5"}],["path",{d:"M15 7h2a5 5 0 1 1 0 10h-2",key:"1b9ql8"}],["line",{x1:"8",x2:"16",y1:"12",y2:"12",key:"1jonct"}]]);var ae=p("MessageSquare",[["path",{d:"M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z",key:"1lielz"}]]);var te=p("Network",[["rect",{x:"16",y:"16",width:"6",height:"6",rx:"1",key:"4q2zg0"}],["rect",{x:"2",y:"16",width:"6",height:"6",rx:"1",key:"8cvhb9"}],["rect",{x:"9",y:"2",width:"6",height:"6",rx:"1",key:"1egb70"}],["path",{d:"M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3",key:"1jsf9p"}],["path",{d:"M12 12V8",key:"2874zd"}]]);var ne=p("Plus",[["path",{d:"M5 12h14",key:"1ays0h"}],["path",{d:"M12 5v14",key:"s699le"}]]);var ve=p("RefreshCw",[["path",{d:"M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8",key:"v9h5vc"}],["path",{d:"M21 3v5h-5",key:"1q7to0"}],["path",{d:"M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16",key:"3uifl3"}],["path",{d:"M8 16H3v5",key:"1cv678"}]]);var be=p("Search",[["circle",{cx:"11",cy:"11",r:"8",key:"4ej97u"}],["path",{d:"m21 21-4.3-4.3",key:"1qie3q"}]]);var H=p("Server",[["rect",{width:"20",height:"8",x:"2",y:"2",rx:"2",ry:"2",key:"ngkwjq"}],["rect",{width:"20",height:"8",x:"2",y:"14",rx:"2",ry:"2",key:"iecqi9"}],["line",{x1:"6",x2:"6.01",y1:"6",y2:"6",key:"16zg32"}],["line",{x1:"6",x2:"6.01",y1:"18",y2:"18",key:"nzw8ys"}]]);var fe=p("Shield",[["path",{d:"M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z",key:"oel41y"}]]);var $e=p("Unplug",[["path",{d:"m19 5 3-3",key:"yk6iyv"}],["path",{d:"m2 22 3-3",key:"19mgm9"}],["path",{d:"M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z",key:"goz73y"}],["path",{d:"M7.5 13.5 10 11",key:"7xgeeb"}],["path",{d:"M10.5 16.5 13 14",key:"10btkg"}],["path",{d:"m12 6 6 6 2.3-2.3a2.4 2.4 0 0 0 0-3.4l-2.6-2.6a2.4 2.4 0 0 0-3.4 0Z",key:"1snsnr"}]]);var he=p("X",[["path",{d:"M18 6 6 18",key:"1bl5f8"}],["path",{d:"m6 6 12 12",key:"d8bk6v"}]]);var A=p("CircleAlert",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["line",{x1:"12",x2:"12",y1:"8",y2:"12",key:"1pkeuh"}],["line",{x1:"12",x2:"12.01",y1:"16",y2:"16",key:"4dfq90"}]]);var J=p("CircleCheck",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m9 12 2 2 4-4",key:"dzmm74"}]]);var q=p("Earth",[["path",{d:"M21.54 15H17a2 2 0 0 0-2 2v4.54",key:"1djwo0"}],["path",{d:"M7 3.34V5a3 3 0 0 0 3 3a2 2 0 0 1 2 2c0 1.1.9 2 2 2a2 2 0 0 0 2-2c0-1.1.9-2 2-2h3.17",key:"1tzkfa"}],["path",{d:"M11 21.95V18a2 2 0 0 0-2-2a2 2 0 0 1-2-2v-1a2 2 0 0 0-2-2H2.05",key:"14pb5j"}],["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}]]);var O=p("LoaderCircle",[["path",{d:"M21 12a9 9 0 1 1-6.219-8.56",key:"13zald"}]]);import{useEffect as la,useRef as da}from"react";function Fe(i,n,o){let l=da(o);l.current=o,la(()=>{if(!i||!n)return;let d=(k)=>l.current(k),s=window.__aptevaAppEvents;if(s)return s.subscribe(i,n,d);let c=0,r=null,g=!1,u=null,w=()=>{if(g)return;let k=`/api/app-events/${encodeURIComponent(i)}?project_id=${encodeURIComponent(n)}`+(c>0?`&since=${c}`:"");r=new EventSource(k,{withCredentials:!0}),r.onmessage=(E)=>{try{let _=JSON.parse(E.data);if(_.seq<=c)return;c=_.seq,l.current(_)}catch{}},r.onerror=()=>{if(r&&r.readyState===EventSource.CLOSED){if(u)window.clearTimeout(u);u=window.setTimeout(w,2000)}}};return w(),()=>{if(g=!0,u)window.clearTimeout(u);if(r)r.close()}},[i,n])}var Ue=`
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
.a2a .organigram {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.a2a .organigram-root {
  align-self: center;
  display: inline-flex;
  align-items: center;
  gap: 9px;
  border: 1px solid var(--a-accent);
  border-radius: var(--radius-md, 4px);
  padding: 9px 14px;
  background: color-mix(in srgb, var(--a-accent) 7%, var(--a-card));
}
.a2a .organigram-root span,
.a2a .organigram-node span:not(.avatar) {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.a2a .organigram-root small,
.a2a .organigram-node small {
  color: var(--a-muted);
  font-size: 10px;
  font-weight: 400;
}
.a2a .organigram-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 10px;
  position: relative;
  padding-top: 18px;
}
.a2a .organigram-grid:before {
  content: "";
  position: absolute;
  top: 0;
  left: 10%;
  right: 10%;
  border-top: 1px solid var(--a-border);
}
.a2a .organigram-node {
  color: inherit;
  text-align: left;
  padding: 11px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.a2a .organigram-node:hover,
.a2a .organigram-node.selected {
  border-color: var(--a-accent);
}
.a2a .org-relations {
  display: flex;
  flex-direction: column;
  gap: 5px;
  border-top: 1px solid var(--a-border);
  padding-top: 8px;
}
.a2a .org-relations > small {
  text-transform: uppercase;
  letter-spacing: .35px;
  font-size: 9px;
}
.a2a .access-editor {
  align-items: flex-end;
}
.a2a .access-field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 200px;
  flex: 1;
}
.a2a .access-field > span {
  color: var(--a-muted);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: .35px;
}
.a2a .access-modes {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
}
.a2a .access-mode {
  display: flex;
  flex-direction: column;
  gap: 4px;
  text-align: left;
  padding: 10px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
  color: inherit;
  background: var(--a-card);
}
.a2a .access-mode:hover,
.a2a .access-mode.selected {
  border-color: var(--a-accent);
  background: color-mix(in srgb, var(--a-accent) 7%, var(--a-card));
}
.a2a .access-mode small {
  color: var(--a-muted);
  font-size: 10px;
}
.a2a .access-subjects {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 7px;
}
.a2a .access-check {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 9px;
  border: 1px solid var(--a-border);
  border-radius: var(--radius-sm, 2px);
}
.a2a .access-check span {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.a2a .access-check small {
  color: var(--a-muted);
  font-size: 10px;
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
  .a2a .access-modes {
    grid-template-columns: 1fr 1fr;
  }
  .a2a .organigram-grid {
    grid-template-columns: 1fr;
  }
  .a2a .sheet {
    padding: 20px;
  }
}
`;import{jsx as e,jsxs as a,Fragment as C}from"react/jsx-runtime";var pa="/api/apps/a2a",Ie={q:"",status:"",peer:"",agent_address:"",from:"",to:""},ie=[{name:"Overview",icon:ue},{name:"Agents",icon:W},{name:"Access",icon:ge},{name:"Exchanges",icon:ae},{name:"Connections",icon:Ee}];function ma(i,n){return`${pa}${n}${n.includes("?")?"&":"?"}project_id=${encodeURIComponent(i)}`}async function De(i,n,o){let l=await fetch(ma(i,n),{credentials:"same-origin",...o});if(!l.ok){let d=await l.text();throw Error(d.length<300&&!d.includes("<html")?d:`Request failed (${l.status}).`)}return l.json()}function K(i,n,o=0){let[l,d]=b({error:"",loading:!0,key:""}),[s,c]=b(0),r=`${i}:${n}`;return Ne(()=>{if(n===null)return;let g=new AbortController;return d((u)=>({data:u.key===r?u.data:void 0,error:"",loading:!0,key:r})),De(i,n,{signal:g.signal}).then((u)=>{if(!g.signal.aborted)d({data:u,loading:!1,error:"",key:r})}).catch((u)=>{if(!g.signal.aborted)d((w)=>({...w,loading:!1,error:u.message}))}),()=>g.abort()},[i,n,r,o,s]),{...l,data:l.key===r?l.data:void 0,loading:n!==null&&(l.loading||l.key!==r),reload:()=>c((g)=>g+1)}}function ye(i){if(!i)return"Never";let n=new Date(i);return Number.isNaN(+n)?"Unknown":n.toLocaleString()}function Qe(i){if(!i)return"Never";let n=Date.now()-new Date(i).getTime();if(!Number.isFinite(n))return"Unknown";let o=Math.max(0,Math.floor(n/60000));return o<1?"Just now":o<60?`${o}m ago`:o<1440?`${Math.floor(o/60)}h ago`:`${Math.floor(o/1440)}d ago`}function we(i,n){let o=n==="from"?i.direction==="inbound":i.direction==="outbound";return i[`${n}_agent_name`]||(o?"Remote agent":`Agent ${i[`${n}_agent_id`]}`)}function Le(i,n){return!i.peer_id?"Local exchange":n.find((o)=>o.id===i.peer_id)?.name||i.peer_id}function ga(i,n){return i?.length?i.map((o)=>o==="*"?"All exposed agents (all projects)":n.find((l)=>l.kind==="local"&&(String(l.id)===o||l.name===o))?.name||o).join(", "):"No inbound access"}function qe({status:i}){let n=["completed","running"].includes(i)?"good":i==="failed"?"bad":i==="input_required"?"warn":["working","submitted"].includes(i)?"active":"";return e("span",{className:`badge ${n}`,children:i.replaceAll("_"," ")})}function Q({title:i,children:n,action:o}){return a("div",{className:"empty",children:[e(te,{size:28,strokeWidth:1.4}),e("h3",{children:i}),e("p",{children:n}),o]})}function P({message:i,retry:n}){return a("div",{role:"alert",className:"notice error",children:[e(A,{size:16}),e("span",{className:"grow",children:i}),n&&e("button",{className:"btn quiet",onClick:n,children:"Retry"})]})}function oe(){return a("div",{className:"empty",role:"status",children:[e(O,{className:"spin",size:20}),"Loading…"]})}function ua({value:i}){let[n,o]=b("");return a("button",{className:"btn quiet",title:i,onClick:async()=>{try{await navigator.clipboard.writeText(i),o("Copied")}catch{o("Copy unavailable")}},children:[n==="Copied"?e(ce,{size:13}):e(Te,{size:13}),n||"Copy address"]})}function Me({title:i,onClose:n,children:o}){let l=Ke(null),d=Ke(n);return d.current=n,Ne(()=>{let s=document.activeElement;return l.current?.querySelector("button")?.focus(),()=>s?.focus()},[]),e("div",{className:"overlay",onMouseDown:(s)=>{if(s.target===s.currentTarget)n()},children:a("div",{ref:l,className:"sheet",role:"dialog","aria-modal":"true","aria-label":i,onKeyDown:(s)=>{if(s.key==="Escape")s.stopPropagation(),d.current();if(s.key==="Tab"){let c=[...l.current?.querySelectorAll('button:not(:disabled),a[href],input,select,textarea,[tabindex="0"]')||[]],r=c[0],g=c.at(-1);if(s.shiftKey&&document.activeElement===r)s.preventDefault(),g?.focus();else if(!s.shiftKey&&document.activeElement===g)s.preventDefault(),r?.focus()}},children:[a("div",{className:"row between",children:[e("h2",{children:i}),e("button",{className:"icon-btn","aria-label":"Close panel",onClick:n,children:e(he,{size:20})})]}),o]})})}function Je({task:i,connections:n,selected:o,onClick:l}){return a("button",{className:`list-row ${o?"selected":""}`,onClick:l,"aria-pressed":o,children:[a("div",{className:"row between",children:[a("span",{className:"row grow",children:[e("strong",{className:"truncate",children:we(i,"from")}),e(j,{size:12}),e("strong",{className:"truncate",children:we(i,"to")})]}),e(qe,{status:i.status})]}),e("p",{children:i.preview||(i.kind==="ask"?"Request awaiting its first recorded message":"One-way message")}),a("div",{className:"row wrap",children:[e("small",{children:Le(i,n)}),a("small",{children:["· #",i.id]}),!!i.pending_delivery&&e("span",{className:"badge warn",children:"Delivery pending"}),!i.pending_delivery&&!i.overdue&&["submitted","working","input_required"].includes(i.status)&&e("span",{className:"badge",children:i.status==="submitted"?"Awaiting response":i.status==="working"?"Response in progress":"Awaiting input"}),!!i.overdue&&["working","submitted","input_required"].includes(i.status)&&e("span",{className:"badge warn",children:"Overdue · awaiting response"}),!!i.poll_failures&&["working","submitted","input_required"].includes(i.status)&&e("span",{className:"badge warn",children:"Sync retrying"}),e("small",{style:{marginLeft:"auto"},title:ye(i.updated_at),children:Qe(i.updated_at)})]})]})}function va({bytes:i,name:n}){let[o,l]=b("");return a(C,{children:[a("button",{className:"btn quiet",onClick:()=>{try{let s=atob(i),c=Uint8Array.from(s,(u)=>u.charCodeAt(0)),r=URL.createObjectURL(new Blob([c],{type:"application/octet-stream"})),g=document.createElement("a");g.href=r,g.download=n,g.click(),setTimeout(()=>URL.revokeObjectURL(r),1000)}catch{l("This embedded file could not be decoded. Its original data is available below.")}},children:[e(me,{size:13}),"Download ",n]}),o&&e("p",{className:"muted",children:o})]})}function ba({value:i,index:n}){let o=i&&typeof i==="object"?i:{},l=Array.isArray(o.parts)?o.parts:[];return a("article",{className:"panel pad stack gap-sm",children:[a("div",{className:"row",children:[e(me,{size:16}),e("strong",{children:typeof o.name==="string"?o.name:`Artifact ${n+1}`})]}),typeof o.description==="string"&&e("p",{className:"muted",children:o.description}),l.map((d,s)=>{if(!d||typeof d!=="object")return null;let c=d.url||d.file?.uri,r=typeof c==="string"&&/^https?:\/\//i.test(c),g=typeof d.text==="string"?d.text:null;return a("div",{children:[g&&e("p",{className:"message-body",children:g}),d.data&&e("pre",{children:JSON.stringify(d.data,null,2)}),r&&a("a",{className:"btn quiet",href:c,target:"_blank",rel:"noopener noreferrer",children:[e(Se,{size:13}),typeof d.file?.name==="string"?d.file.name:"Open returned file"]}),typeof(d.raw||d.file?.bytes)==="string"&&e(va,{bytes:d.raw||d.file.bytes,name:typeof d.file?.name==="string"?d.file.name:"artifact.bin"})]},s)}),a("details",{children:[e("summary",{className:"muted",children:"Artifact JSON"}),e("pre",{children:JSON.stringify(i,null,2)})]})]})}function fa({project:i,task:n,revision:o,connections:l}){let d=K(i,`/tasks/${n.id}/messages`,o),s={...n,...d.data?.task};return a("section",{className:"panel",children:[a("div",{className:"section-head stack gap-sm",children:[a("div",{className:"row between",children:[a("span",{className:"eyebrow",children:["Exchange #",n.id]}),e(qe,{status:s.status})]}),a("h2",{children:[we(n,"from")," ",e("span",{className:"muted",children:"→"})," ",we(n,"to")]}),a("p",{className:"muted",style:{fontSize:12},children:[s.kind==="ask"?"Request with reply":"One-way message"," ·"," ",Le(n,l)]})]}),a("div",{className:"detail-meta",children:[a("span",{className:"row",children:[e(ze,{size:13}),"Started ",ye(n.created_at)]}),["from","to"].map((c)=>{let r=c==="from"?n.direction!=="inbound":n.direction!=="outbound",g=n[`${c}_agent_id`];return r&&g>0&&a("a",{href:`/agents/${g}?thread=${encodeURIComponent(s[`${c}_thread_id`]||"main")}`,className:"row",children:[we(n,c)," thread ",e(ee,{size:13})]},c)})]}),!!n.pending_delivery&&e("div",{className:"pad",children:a("div",{className:"notice",children:[e(A,{size:15}),"A recorded result is waiting to be delivered. Automatic retries are active."]})}),!!s.overdue&&!n.pending_delivery&&e("div",{className:"pad",children:a("div",{className:"notice",children:[e(A,{size:15})," This request is overdue. Local requests are failed automatically if no reply or progress update is recorded."]})}),n.direction==="outbound"&&a("div",{className:"detail-meta",children:["Remote sync: ",ye(s.last_synced_at),!!n.poll_failures&&a(C,{children:[" · ",n.poll_failures," failed attempts; retrying automatically"]})]}),d.error?e("div",{className:"pad",children:e(P,{message:`Could not load this exchange: ${d.error}`,retry:d.reload})}):d.loading&&!d.data?e(oe,{}):e("div",{className:"timeline",children:!d.data?.messages.length?e(Q,{title:"No messages recorded",children:"This exchange has no recorded message content yet."}):d.data.messages.map((c)=>a("article",{className:"timeline-item",children:[a("div",{className:"row wrap",children:[e("strong",{children:we(n,c.from_agent_id===n.from_agent_id?"from":"to")}),c.status_after&&e(qe,{status:c.status_after}),e("small",{className:"muted",title:ye(c.created_at),children:ye(c.created_at)})]}),e("p",{className:"message-body",children:c.body})]},c.id))}),!!s.artifacts?.length&&a("div",{className:"artifacts stack",children:[a("h3",{children:["Returned artifacts"," ",a("span",{className:"muted",children:["(",s.artifacts.length,")"]})]}),s.artifacts.map((c,r)=>e(ba,{value:c,index:r},r))]})]})}function ha({project:i,agent:n,onClose:o,onExchanges:l}){let d=n.kind!=="local",s=K(i,d?`/network/card?address=${encodeURIComponent(n.address)}`:null),c=s.data?.card||n.card;return a(Me,{title:n.name,onClose:o,children:[a("div",{className:"row",children:[e("div",{className:"avatar",children:e(W,{size:21})}),a("div",{children:[e("strong",{children:n.peer_name}),e("p",{className:"muted",children:d?"Remote agent":"Local agent"})]}),e(qe,{status:n.status})]}),e("p",{children:n.description||c?.description||"This agent has no published description."}),a("div",{className:"row wrap",children:[e(ua,{value:n.address}),a("button",{className:"btn",onClick:()=>l(n),children:[e(ae,{size:13}),"View exchanges"]}),n.id&&a("a",{className:"btn",href:`/agents/${n.id}`,children:["Open agent ",e(ee,{size:13})]})]}),d&&e("p",{className:"notice",children:"Remote directory entries are cached. Check the connection to refresh discovery; a published card does not prove task execution is available."}),s.error&&e(P,{message:s.error,retry:s.reload}),d&&s.loading&&a("p",{className:"muted row",children:[e(O,{size:14,className:"spin"}),"Retrieving current Agent Card…"]}),a("div",{className:"stack",children:[e("h3",{children:"Capabilities"}),c?.skills?.length?c.skills.map((r)=>a("div",{className:"panel pad",children:[e("strong",{children:r.name||r.id}),e("p",{className:"subtitle",children:r.description||"No description provided."}),r.examples?.map((g,u)=>a("p",{className:"message-body muted",children:["“",g,"”"]},u))]},r.id)):n.skills?.length?e("div",{className:"chips",children:n.skills.map((r)=>e("span",{className:"badge",children:r},r))}):e("p",{className:"muted",children:"No capabilities published yet."})]}),c&&a(C,{children:[a("dl",{className:"kv",children:[e("dt",{children:"Provider"}),e("dd",{children:c.provider?.organization||"Not specified"}),e("dt",{children:"Version"}),e("dd",{children:c.version||"Not specified"}),e("dt",{children:"Input"}),e("dd",{children:c.defaultInputModes?.join(", ")||"Not specified"}),e("dt",{children:"Output"}),e("dd",{children:c.defaultOutputModes?.join(", ")||"Not specified"}),e("dt",{children:"Streaming"}),e("dd",{children:c.capabilities?.streaming?"Supported":"Not advertised"})]}),a("details",{children:[e("summary",{children:"Agent Card JSON"}),e("pre",{children:JSON.stringify(c,null,2)})]})]})]})}function xe({title:i,value:n,onChange:o,agents:l}){let[d,s]=b(n.join(", ")),c=(r)=>{s(r.join(", ")),o(r)};return a("div",{className:"stack gap-sm",children:[e("h3",{children:i}),e("div",{className:"checks",children:l.filter((r)=>r.kind==="local").map((r)=>{let g=String(r.id);return a("label",{children:[e("input",{type:"checkbox",checked:n.includes(g)||n.includes(r.name),disabled:n.includes("*"),onChange:(u)=>c(u.target.checked?[...n,g]:n.filter((w)=>w!==g&&w!==r.name))}),r.name]},r.address)})}),a("label",{className:"field",children:["Agent names or IDs",e("input",{className:"input","aria-label":i,placeholder:"None — no inbound access",value:d,onChange:(r)=>{s(r.target.value),o(r.target.value.split(",").map((g)=>g.trim()).filter(Boolean))}}),e("small",{children:"Empty grants deny access. * grants every exposed agent across this installation, including other projects."})]})]})}function ya({project:i,agents:n,edit:o,onClose:l,onSaved:d}){let[s,c]=b(o?1:0),[r,g]=b(o?.kind||"agent_card"),[u,w]=b({card_url:"",id:"",name:"",base_url:"",token:""}),[k,E]=b(o?.discover_agents||[]),[_,M]=b(o?.invoke_agents||[]),[$,D]=b(!1),[z,X]=b(""),G=!!o||(r==="agent_card"?!!u.card_url.trim():!!u.id.trim()&&!!u.base_url.trim()&&!!u.token.trim()),m=async()=>{D(!0),X("");try{await De(i,o?`/connections/${encodeURIComponent(o.id)}`:"/connections",{method:o?"PATCH":"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(o?{discover_agents:k,invoke_agents:_}:{...u,kind:r,discover_agents:k,invoke_agents:_})}),d()}catch(v){X(v instanceof Error?v.message:String(v))}finally{D(!1)}},f=(v,y,re,le="text")=>a("label",{className:"field",children:[y,e("input",{className:"input",type:le,autoComplete:"off",value:u[v],placeholder:re,onChange:(ke)=>w((Z)=>({...Z,[v]:ke.target.value}))})]});return a(Me,{title:o?`Access · ${o.name}`:"Add connection",onClose:()=>{if(!$)l()},children:[e("p",{className:"muted",children:"Connections are shared across this A2A installation. Local agents and exchanges remain scoped to the current project."}),!o&&e("div",{className:"steps",children:["01 · Choose type","02 · Configure","03 · Review"].map((v,y)=>e("span",{className:`step ${s===y?"current":""}`,children:v},v))}),s===0&&e("div",{className:"stack",children:[{kind:"agent_card",icon:q,title:"Public agent",detail:"Connect an external agent using its Agent Card URL."},{kind:"node",icon:H,title:"Apteva installation",detail:"Connect a node and discover the agents it shares."}].map((v)=>a("button",{className:"panel option","aria-pressed":r===v.kind,onClick:()=>g(v.kind),children:[e(v.icon,{size:24}),a("div",{children:[e("strong",{children:v.title}),e("p",{className:"subtitle",children:v.detail})]}),r===v.kind&&e(J,{size:18})]},v.kind))}),s===1&&a("div",{className:"stack",children:[!o&&(r==="agent_card"?a(C,{children:[f("card_url","Agent Card URL","https://agent.example/.well-known/agent-card.json","url"),f("token","Bearer token (optional)","Leave empty for anonymous discovery","password")]}):a(C,{children:[f("name","Display name","e.g. Research team"),f("id","Connection ID","research-team"),f("base_url","A2A base URL","https://node.example/api/apps/a2a","url"),f("token","Pairing token","Reciprocal pairing token","password")]})),r==="node"&&a(C,{children:[a("div",{className:"notice",children:[e(fe,{size:17}),e("span",{children:"These grants control which of your agents this remote node may discover and invoke. Access to its agents is controlled on the remote node."})]}),e(xe,{title:"Agents this node may discover",agents:n,value:k,onChange:E}),e(xe,{title:"Agents this node may invoke",agents:n,value:_,onChange:M})]})]}),s===2&&a("div",{className:"stack",children:[a("div",{className:"panel pad",children:[e("h3",{children:r==="node"?u.name||u.id:"Public agent"}),e("p",{className:"subtitle break",children:r==="node"?u.base_url:u.card_url})]}),a("dl",{className:"kv",children:[e("dt",{children:"Authentication"}),e("dd",{children:u.token?"Bearer token provided":"Anonymous"}),r==="node"&&a(C,{children:[e("dt",{children:"Discovery grants"}),e("dd",{children:k.join(", ")||"No inbound access"}),e("dt",{children:"Invocation grants"}),e("dd",{children:_.join(", ")||"No inbound access"})]})]}),e("p",{className:"notice",children:r==="agent_card"?"The Agent Card will be validated before saving. No message will be sent.":"Save the pairing, then run Check connection to verify remote discovery. No message will be sent."})]}),z&&e(P,{message:z}),a("div",{className:"row between",style:{marginTop:"auto"},children:[s>0&&!o?a("button",{className:"btn",disabled:$,onClick:()=>c((v)=>v-1),children:[e(pe,{size:14}),"Back"]}):e("span",{}),s<2&&!o?a("button",{className:"btn primary",disabled:s===1&&!G,onClick:()=>c((v)=>v+1),children:["Continue ",e(V,{size:14})]}):a("button",{className:"btn primary",disabled:$||!G,onClick:m,children:[$?e(O,{size:14,className:"spin"}):e(ce,{size:14}),o?"Save access":"Save connection"]})]})]})}function wa({project:i,data:n,reload:o}){let l=(n?.agents||[]).filter((m)=>m.attached),[d,s]=b(l[0]?.id||0),[c,r]=b("invoke"),[g,u]=b("compatibility"),[w,k]=b([]),[E,_]=b(!1),[M,$]=b(""),D=n?.policies.find((m)=>m.target_agent_id===d&&m.action===c);Ne(()=>{if(!l.some((m)=>m.id===d))s(l[0]?.id||0)},[l,d]),Ne(()=>{u(D?.mode||"compatibility"),k(D?.subject_ids||[])},[D?.mode,JSON.stringify(D?.subject_ids||[]),d,c]);let z=(m)=>l.find((f)=>f.id===m)?.name||`Agent ${m}`,X=async()=>{if(!d)return;_(!0),$("");try{await De(i,"/access",{method:"PATCH",body:JSON.stringify({target_agent_id:d,action:c,mode:g,subject_ids:g==="selected"?w:[]})}),o()}catch(m){$(m instanceof Error?m.message:String(m))}finally{_(!1)}},G=(m,f)=>n?.edges.find((v)=>v.from_agent_id===m&&v.to_agent_id===f)?.allowed;return a(C,{children:[a("div",{className:"row between wrap",children:[a("div",{children:[e("h2",{children:"Access map"}),e("p",{className:"subtitle",children:"Compose the communication organigram for this project. Rules answer who may reach each target agent."})]}),a("span",{className:"badge",children:[l.length," attached local agent",l.length===1?"":"s"]})]}),a("section",{className:"panel pad stack",children:[a("div",{className:"section-head row between",style:{margin:"-12px -16px 0"},children:[a("div",{children:[e("h2",{children:"Communication organigram"}),e("p",{className:"subtitle",children:"Arrows show allowed new work. Select a target below to edit its access."})]}),e(ge,{size:18,className:"muted"})]}),l.length?a("div",{className:"organigram",children:[a("div",{className:"organigram-root",children:[e(H,{size:17}),a("span",{children:[e("strong",{children:"Project communication"}),e("small",{children:"Target-owned access rules"})]})]}),e("div",{className:"organigram-grid",children:l.map((m)=>{let f=l.filter((y)=>y.id!==m.id&&G(m.id,y.id)),v=l.filter((y)=>y.id!==m.id&&G(y.id,m.id));return a("button",{type:"button",className:`panel organigram-node ${d===m.id?"selected":""}`,onClick:()=>s(m.id),children:[a("div",{className:"row",children:[e("span",{className:"avatar",children:e(W,{size:18})}),a("span",{className:"grow",children:[e("strong",{children:m.name}),e("small",{className:"muted",children:m.status||"unknown"})]}),e(V,{size:14,className:"muted"})]}),a("div",{className:"org-relations",children:[e("small",{children:"Can reach"}),e("div",{className:"chips",children:f.length?f.map((y)=>e("span",{className:"badge good",children:y.name},y.id)):e("span",{className:"muted",children:"Nobody"})}),e("small",{children:"Reachable from"}),e("div",{className:"chips",children:v.length?v.map((y)=>e("span",{className:"badge active",children:y.name},y.id)):e("span",{className:"muted",children:"Nobody"})})]})]},m.id)})})]}):e(Q,{title:"No attached local agents",children:"Attach A2A to agents before composing the communication organigram."})]}),l.length>0&&a("section",{className:"panel pad stack",children:[a("div",{children:[e("h2",{children:"Edit target access"}),e("p",{className:"subtitle",children:"The target owns this decision. Existing tasks can still finish after new work is revoked."})]}),a("div",{className:"toolbar access-editor",children:[a("label",{className:"access-field",children:[e("span",{children:"Target agent"}),e("select",{className:"input",value:d,onChange:(m)=>s(Number(m.target.value)),children:l.map((m)=>e("option",{value:m.id,children:m.name},m.id))})]}),a("label",{className:"access-field",children:[e("span",{children:"Action"}),a("select",{className:"input",value:c,onChange:(m)=>r(m.target.value),children:[e("option",{value:"discover",children:"Discover"}),e("option",{value:"invoke",children:"Start new work"}),e("option",{value:"message",children:"One-way message"}),e("option",{value:"continue",children:"Continue a task"})]})]})]}),e("div",{className:"access-modes",role:"group","aria-label":"Access mode",children:[["compatibility","Compatibility default","All attached agents until you set a rule"],["all","All attached agents","Explicitly allow every attached agent"],["selected","Selected agents","Choose exactly who may use this action"],["none","Nobody","Close this action for the target"]].map(([m,f,v])=>a("button",{type:"button",className:`access-mode ${g===m?"selected":""}`,"aria-pressed":g===m,onClick:()=>u(m),children:[e("strong",{children:f}),e("small",{children:v})]},m))}),g==="selected"&&e("div",{className:"access-subjects",children:l.filter((m)=>m.id!==d).map((m)=>a("label",{className:"access-check",children:[e("input",{type:"checkbox",checked:w.includes(m.id),onChange:(f)=>k((v)=>f.target.checked?[...new Set([...v,m.id])]:v.filter((y)=>y!==m.id))}),a("span",{children:[e("strong",{children:m.name}),a("small",{children:[z(m.id)," may use ",c]})]})]},m.id))}),M&&a("div",{className:"notice",children:[e(A,{size:15}),M]}),a("div",{className:"row between wrap",children:[a("small",{className:"muted",children:["Current rule: ",D?.mode||"compatibility default"]}),e("button",{type:"button",className:"btn primary",disabled:E,onClick:X,children:E?"Saving…":"Save access"})]})]})]})}function Na(i){return e(ka,{...i},`${i.projectId}:${i.installId}`)}function ka({projectId:i}){let[n,o]=b("Overview"),[l,d]=b(0),[s,c]=b(null),[r,g]=b(null),[u,w]=b(!1),[k,E]=b(null),[_,M]=b(""),[$,D]=b(!1),[z,X]=b({}),[G,m]=b({}),[f,v]=b(Ie),[y,re]=b(0),[le,ke]=b(""),[Z,Oe]=b(""),[Pe,de]=b("directory"),[je,Xe]=b("");Ne(()=>{let t=setTimeout(()=>Xe(f.q),250);return()=>clearTimeout(t)},[f.q]);let T=K(i,"/network",l),R=K(i,"/connections",l),F=K(i,"/access",l),Re=K(i,"/overview",l),U=K(i,"/tasks?limit=5",l),I=K(i,"/tasks?status=attention&limit=4",l),ea=new URLSearchParams({...f,q:je,limit:"30",offset:String(y)}),N=K(i,n==="Exchanges"?`/tasks?${ea}`:null,l),S=R.data?.connections||[],B=T.data?.agents||[],Ce=sa(()=>d((t)=>t+1),[]),_e=Ke(null);Ne(()=>()=>{if(_e.current)clearTimeout(_e.current)},[]),Fe("a2a",i,(t)=>{if(t.topic==="task.created"||t.topic==="task.updated"){if(_e.current)clearTimeout(_e.current);_e.current=setTimeout(Ce,200)}});let x=(t={},h)=>{v({...Ie,...t}),Xe(t.q||""),re(0),g(h||null),o("Exchanges")},se=(t,h)=>{v((Y)=>({...Y,[t]:h})),re(0),g(null)},Be=r&&(N.data?.tasks.find((t)=>t.id===r.id)||r),He=ca(()=>B.filter((t)=>(!Z||t.peer_id===Z)&&`${t.name} ${t.description} ${t.peer_name} ${t.skills?.join(" ")||""}`.toLowerCase().includes(le.toLowerCase())),[B,Z,le]),aa=async(t)=>{m((h)=>({...h,[t.id]:!0}));try{let h=await De(i,`/connections/${encodeURIComponent(t.id)}/check`,{method:"POST"});if(X((Y)=>({...Y,[t.id]:h})),h.ok)Ce()}catch(h){X((Y)=>({...Y,[t.id]:{ok:!1,message:h instanceof Error?h.message:String(h),checked_at:new Date().toISOString(),latency_ms:0}}))}finally{m((h)=>({...h,[t.id]:!1}))}},ta=async()=>{if(!k)return;D(!0),M("");try{await De(i,`/connections/${encodeURIComponent(k.id)}`,{method:"DELETE"}),E(null),Ce()}catch(t){M(t instanceof Error?t.message:String(t))}finally{D(!1)}},L=Re.data;return a("div",{className:"a2a",children:[e("style",{children:Ue}),a("header",{className:"header",children:[a("div",{className:"row between title-line",children:[a("div",{children:[e("h1",{children:"Agent to Agent"}),e("p",{className:"subtitle",children:"Manage agents, exchanges, and connections."})]}),a("div",{className:"row",children:[e("button",{className:"btn quiet","aria-label":"Refresh workspace",onClick:Ce,children:e(ve,{size:14})}),a("button",{className:"btn primary",onClick:()=>w("new"),children:[e(ne,{size:14}),"Connect"]})]})]}),e("nav",{className:"tabs",role:"tablist","aria-label":"A2A views",onKeyDown:(t)=>{if(!["ArrowLeft","ArrowRight","Home","End"].includes(t.key))return;t.preventDefault();let h=ie.findIndex((na)=>na.name===n),Y=t.key==="Home"?0:t.key==="End"?ie.length-1:(h+(t.key==="ArrowRight"?1:-1)+ie.length)%ie.length;o(ie[Y].name),document.getElementById(`a2a-tab-${ie[Y].name}`)?.focus()},children:ie.map(({name:t,icon:h})=>a("button",{role:"tab",id:`a2a-tab-${t}`,"aria-controls":`a2a-view-${t}`,"aria-selected":n===t,tabIndex:n===t?0:-1,className:"tab",onClick:()=>o(t),children:[e(h,{size:15}),t,t==="Agents"&&T.data&&e("span",{className:"badge",children:B.length}),t==="Connections"&&R.data&&e("span",{className:"badge",children:S.length})]},t))})]}),a("main",{className:"main stack",id:`a2a-view-${n}`,role:"tabpanel","aria-labelledby":`a2a-tab-${n}`,children:[T.error&&e(P,{message:`Agent directory: ${T.error}`,retry:T.reload}),R.error&&e(P,{message:`Connections: ${R.error}`,retry:R.reload}),F.error&&n==="Access"&&e(P,{message:`Access policy: ${F.error}`,retry:F.reload}),T.data?.warnings?.map((t)=>a("div",{className:"notice",children:[e(A,{size:15}),t]},t)),n==="Overview"&&a(C,{children:[a("div",{className:"row between",children:[a("div",{children:[e("h2",{children:"Overview"}),e("p",{className:"subtitle",children:"Project exchanges · all time"})]}),a("small",{className:"muted",children:["Updated ",Qe(L?.as_of)]})]}),Re.error?e(P,{message:Re.error,retry:Re.reload}):e("div",{className:"metrics",children:[{label:"Active exchanges",value:L?.active,note:"Submitted or working",icon:Ae,status:"active"},{label:"Waiting for input",value:L?.input_required,note:"An agent has a question",icon:ae,status:"input_required"},{label:"Needs attention",value:L?.attention,note:"Failures, input, or delivery retries",icon:A,status:"attention"},{label:"Completed",value:L?.completed,note:`${L?.total??"—"} exchange${L?.total===1?"":"s"} in total`,icon:J,status:"completed"}].map((t)=>a("button",{className:"panel metric",onClick:()=>x({status:t.status}),children:[a("div",{className:"row",children:[e("span",{children:t.label}),e(t.icon,{size:16})]}),e("div",{className:"metric-value",children:t.value??"—"}),e("small",{children:t.note})]},t.label))}),a("div",{className:"overview-grid",children:[a("section",{className:"panel",children:[a("div",{className:"section-head row between",children:[e("h2",{children:"Recent exchanges"}),a("button",{className:"btn quiet",onClick:()=>x(),children:["View all ",e(j,{size:13})]})]}),U.error?e("div",{className:"pad",children:e(P,{message:U.error,retry:U.reload})}):U.loading&&!U.data?e(oe,{}):U.data?.tasks.length?U.data.tasks.map((t)=>e(Je,{task:t,connections:S,onClick:()=>x({},t)},t.id)):e(Q,{title:"The first exchange starts with an agent",children:"Attach A2A to two agents and ask one to collaborate with the other. Their work will appear here."})]}),a("div",{className:"stack",children:[a("section",{className:"panel",children:[a("div",{className:"section-head row between",children:[e("h2",{children:"Needs attention"}),e(A,{size:16,className:"muted"})]}),I.error?e("div",{className:"pad",children:e(P,{message:I.error,retry:I.reload})}):I.loading&&!I.data?e(oe,{}):I.data?.tasks.length?I.data.tasks.map((t)=>e(Je,{task:t,connections:S,onClick:()=>x({status:"attention"},t)},t.id)):e(Q,{title:"Nothing needs attention",children:"No failed exchanges, overdue requests, unanswered input requests, or pending delivery retries."})]}),a("section",{className:"panel pad stack",children:[a("div",{className:"row between",children:[e("h3",{children:"Connected network"}),e(te,{size:17,className:"muted"})]}),a("div",{className:"row wrap",children:[a("span",{className:"badge",children:[T.data?B.filter((t)=>t.kind==="local").length:"—"," ","local agents"]}),a("span",{className:"badge",children:[T.data?B.filter((t)=>t.kind!=="local").length:"—"," ","cached remote agents"]})]}),a("p",{className:"muted",children:[R.data?S.length:"—"," external connections ·"," ",T.data?.node?.display_name||"This installation"]}),a("button",{className:"btn",onClick:()=>{o("Agents"),de("map")},children:["Explore network ",e(j,{size:13})]})]})]})]})]}),n==="Agents"&&a(C,{children:[a("div",{className:"row between wrap",children:[a("div",{children:[e("h2",{children:"Agent directory"}),e("p",{className:"subtitle",children:"Local agents in this project and remote agents discovered by this installation."})]}),a("div",{className:"segmented",children:[a("button",{"aria-pressed":Pe==="directory",onClick:()=>de("directory"),children:[e(ue,{size:14}),"Directory"]}),a("button",{"aria-pressed":Pe==="map",onClick:()=>de("map"),children:[e(te,{size:14}),"Map"]})]})]}),Pe==="map"&&a("section",{className:"panel",children:[a("div",{className:"map",children:[a("div",{className:"map-hub",children:[e(H,{size:28,style:{margin:"0 auto 12px"}}),e("h3",{children:T.data?.node?.display_name||"This installation"}),a("p",{className:"subtitle",children:[B.filter((t)=>t.kind==="local").length," local agents in this project"]}),e("button",{className:"btn quiet",style:{marginTop:14},onClick:()=>{Oe("local"),de("directory")},children:"View agents"})]}),e("div",{className:"map-lines"}),e("div",{className:"map-peers",children:S.length?S.map((t)=>e("div",{className:"map-peer",children:a("button",{className:"panel",onClick:()=>{Oe(t.id),de("directory"),ke("")},children:[t.kind==="node"?e(H,{size:18}):e(q,{size:18}),a("span",{className:"grow",children:[e("strong",{children:t.name}),a("p",{className:"subtitle",children:[t.agents?.length||0," cached agents ·"," ",t.kind==="node"?"Installation":"Public agent"]})]}),e(V,{size:16})]})},t.id)):e(Q,{title:"Extend your network",action:e("button",{className:"btn",onClick:()=>w("new"),children:"Add connection"}),children:"Connect another installation or a public agent."})})]}),e("p",{className:"pad muted",style:{fontSize:12,borderTop:"1px solid var(--a-border)"},children:"Lines show configured connections. Select a node to see its agents and open their exchanges. Availability is verified from Connections."})]}),Pe==="directory"&&a(C,{children:[a("div",{className:"toolbar",children:[a("label",{className:"search",children:[e(be,{size:15}),e("input",{"aria-label":"Search agents",className:"input",placeholder:"Search names, capabilities, or installations…",value:le,onChange:(t)=>ke(t.target.value)})]}),a("select",{className:"input","aria-label":"Agent installation",value:Z,onChange:(t)=>Oe(t.target.value),children:[e("option",{value:"",children:"All installations"}),e("option",{value:"local",children:"This installation"}),S.map((t)=>e("option",{value:t.id,children:t.name},t.id))]}),a("small",{className:"muted",children:[He.length," agents"]})]}),T.loading&&!T.data?e(oe,{}):He.length?e("div",{className:"agent-grid",children:He.map((t)=>a("button",{className:"panel agent-card",onClick:()=>c(t),children:[a("div",{className:"row",children:[e("div",{className:"avatar",children:t.kind==="local"?e(W,{size:20}):e(q,{size:20})}),a("div",{className:"grow",children:[e("h3",{className:"truncate",children:t.name}),e("small",{className:"muted",children:t.peer_name})]}),e(V,{size:14,className:"muted"})]}),e("p",{children:t.description||"Open this agent to inspect its published capabilities."}),a("div",{className:"chips",children:[t.skills?.slice(0,3).map((h)=>e("span",{className:"badge",children:h},h)),(t.skills?.length||0)>3&&a("span",{className:"badge",children:["+",t.skills.length-3]})]}),a("div",{className:"row between",style:{marginTop:"auto"},children:[e(qe,{status:t.status||"unknown"}),e("small",{className:"muted",children:t.kind==="local"?"This project":`Cached ${Qe(t.fetched_at).toLowerCase()}`})]})]},t.address))}):!T.error&&e(Q,{title:le||Z?"No matching agents":"No agents discovered yet",action:e("button",{className:"btn",onClick:()=>o("Connections"),children:"Manage connections"}),children:"Attach A2A to local agents, or check a connection to discover remote agents."})]})]}),n==="Access"&&(F.loading&&!F.data?e(oe,{}):e(wa,{project:i,data:F.data,reload:F.reload})),n==="Exchanges"&&a(C,{children:[a("div",{children:[e("h2",{children:"Exchanges"}),e("p",{className:"subtitle",children:"Search the full exchange history, inspect replies, and open the originating threads."})]}),a("div",{className:"toolbar",children:[a("label",{className:"search",children:[e(be,{size:15}),e("input",{"aria-label":"Search exchanges",className:"input",placeholder:"Search agents or message content…",value:f.q,onChange:(t)=>se("q",t.target.value)})]}),e("select",{"aria-label":"Exchange status",className:"input",value:f.status,onChange:(t)=>se("status",t.target.value),children:[["","All statuses"],["open","Open"],["active","Active"],["attention","Needs attention"],["working","Working"],["submitted","Submitted"],["input_required","Waiting for input"],["completed","Completed"],["failed","Failed"],["canceled","Canceled"]].map(([t,h])=>e("option",{value:t,children:h},t))}),a("select",{"aria-label":"Exchange connection",className:"input",value:f.peer,onChange:(t)=>se("peer",t.target.value),children:[e("option",{value:"",children:"All connections"}),e("option",{value:"local",children:"Local only"}),S.map((t)=>e("option",{value:t.id,children:t.name},t.id))]})]}),a("div",{className:"toolbar",children:[a("select",{"aria-label":"Exchange agent",className:"input",value:f.agent_address,onChange:(t)=>se("agent_address",t.target.value),children:[e("option",{value:"",children:"All agents"}),B.map((t)=>e("option",{value:t.address,children:t.name},t.address))]}),a("label",{children:["From (UTC)",e("input",{className:"input",type:"date",value:f.from,onChange:(t)=>se("from",t.target.value)})]}),a("label",{children:["To (UTC)",e("input",{className:"input",type:"date",value:f.to,onChange:(t)=>se("to",t.target.value)})]}),Object.values(f).some(Boolean)&&a("button",{className:"btn quiet",onClick:()=>x(),children:["Clear filters ",e(he,{size:12})]})]}),N.error&&e(P,{message:N.error,retry:N.reload}),a("div",{className:"exchange-grid",children:[a("section",{className:"panel",children:[a("div",{className:"section-head row between",children:[a("h3",{children:["Exchanges"," ",e("span",{className:"muted",children:N.data?.total??"—"})]}),e("span",{className:"eyebrow",children:"Latest first"})]}),e("div",{className:"exchange-list",children:N.loading&&!N.data?e(oe,{}):N.data?.tasks.length?N.data.tasks.map((t)=>e(Je,{task:t,connections:S,selected:Be?.id===t.id,onClick:()=>g(t)},t.id)):!N.error&&e(Q,{title:"No exchanges found",children:"Try changing the filters, or start a collaboration between agents."})}),a("div",{className:"pagination",children:[e("span",{children:N.data?.total?`${y+1}–${y+N.data.tasks.length} of ${N.data.total}`:"0 exchanges"}),a("div",{className:"row",children:[e("button",{className:"btn quiet","aria-label":"Previous page",disabled:!y||N.loading,onClick:()=>{re((t)=>Math.max(0,t-30)),g(null)},children:e(pe,{size:14})}),e("button",{className:"btn quiet","aria-label":"Next page",disabled:!N.data||y+30>=N.data.total||N.loading,onClick:()=>{re((t)=>t+30),g(null)},children:e(V,{size:14})})]})]})]}),Be?e(fa,{task:Be,project:i,revision:l,connections:S},Be.id):e("section",{className:"panel",children:e(Q,{title:"Select an exchange",children:"Read the request and replies, inspect returned artifacts, and trace the work back to each agent’s thread."})})]})]}),n==="Connections"&&a(C,{children:[a("div",{className:"row between wrap",children:[a("div",{children:[e("h2",{children:"Connected installations & public agents"}),e("p",{className:"subtitle",children:"Shared across this A2A installation. Checks verify discovery access without sending tasks."})]}),a("button",{className:"btn",onClick:()=>w("new"),children:[e(ne,{size:14}),"Add connection"]})]}),R.loading&&!R.data?e(oe,{}):S.length?e("div",{className:"connection-grid",children:S.map((t)=>a("article",{className:"panel connection",children:[a("div",{className:"row",children:[e("div",{className:"avatar",children:t.kind==="node"?e(H,{size:20}):e(q,{size:20})}),a("div",{className:"grow",children:[e("h3",{children:t.name}),e("small",{className:"muted",children:t.kind==="node"?"Apteva installation":"Public agent"})]}),e("span",{className:`badge ${z[t.id]?z[t.id].ok?"good":"bad":""}`,children:z[t.id]?z[t.id].ok?"Discovery verified":"Check failed":"Not checked"})]}),e("p",{className:"muted break",style:{fontSize:12},children:t.card_url||t.base_url}),a("dl",{className:"kv",children:[e("dt",{children:"Authentication"}),e("dd",{children:t.authenticated?"Bearer token configured":"Anonymous"}),e("dt",{children:"Managed by"}),e("dd",{children:t.managed_by==="operator"?"You":t.managed_by==="app"?"Another app":t.managed_by==="config"?"Installation configuration":"Agent discovery"}),t.protocol_version&&a(C,{children:[e("dt",{children:"Protocol"}),a("dd",{children:["A2A ",t.protocol_version]})]}),t.kind==="node"&&a(C,{children:[e("dt",{children:"May discover"}),e("dd",{children:t.discover_agents?.join(", ")||"No inbound access"}),e("dt",{children:"May invoke"}),e("dd",{children:ga(t.invoke_agents,B)})]})]}),a("div",{className:"stack gap-sm",children:[a("div",{className:"row between",children:[e("small",{className:"muted",children:"Discovered agents"}),e("button",{className:"icon-btn","aria-label":`View agents on ${t.name}`,onClick:()=>{Oe(t.id),ke(""),de("directory"),o("Agents")},children:e(ee,{size:14})})]}),e("div",{className:"chips",children:t.agents?.length?t.agents.map((h)=>e("span",{className:"badge",children:h},h)):e("small",{className:"muted",children:"Run a check to refresh this directory."})})]}),z[t.id]&&e("div",{className:"notice",role:"status",children:a("span",{children:[z[t.id].message,e("br",{}),a("small",{children:[ye(z[t.id].checked_at)," ·"," ",z[t.id].latency_ms," ms"]})]})}),a("div",{className:"connection-footer",children:[a("button",{className:"btn",disabled:G[t.id],onClick:()=>aa(t),children:[G[t.id]?e(O,{size:13,className:"spin"}):e(ve,{size:13}),"Check connection"]}),e("button",{className:"btn quiet",onClick:()=>x({peer:t.id}),children:"Exchanges"}),["operator","agent"].includes(t.managed_by)&&a(C,{children:[t.kind==="node"&&a("button",{className:"btn quiet",onClick:()=>w(t),children:[e(fe,{size:13}),"Edit access"]}),e("button",{className:"icon-btn danger","aria-label":`Remove ${t.name}`,onClick:()=>{M(""),E(t)},children:e($e,{size:15})})]})]})]},t.id))}):!R.error&&e("section",{className:"panel",children:e(Q,{title:"Bring another agent into the conversation",action:a("button",{className:"btn primary",onClick:()=>w("new"),children:[e(ne,{size:14}),"Add your first connection"]}),children:"Connect an Apteva installation or paste a public Agent Card URL. Your agents can then discover its capabilities."})})]})]}),s&&e(ha,{project:i,agent:s,onClose:()=>c(null),onExchanges:(t)=>{c(null),x(t.id?{agent_address:t.address}:{peer:t.peer_id,agent_address:t.address})}},s.address),u&&e(ya,{project:i,agents:B,edit:u==="new"?void 0:u,onClose:()=>w(!1),onSaved:()=>{w(!1),X({}),o("Connections"),Ce()}}),k&&a(Me,{title:`Remove ${k.name}?`,onClose:()=>{if(!$)E(null)},children:[e("p",{children:"This removes the connection from the whole A2A installation. Existing exchange history stays available; active remote exchanges may no longer synchronize."}),_&&e(P,{message:_}),a("div",{className:"row",children:[e("button",{disabled:$,className:"btn",onClick:()=>E(null),children:"Keep connection"}),e("button",{disabled:$,className:"btn danger",onClick:ta,children:"Remove connection"})]})]})]})}export{Na as default};

//# debugId=0F4048201D33013A64756E2164756E21
