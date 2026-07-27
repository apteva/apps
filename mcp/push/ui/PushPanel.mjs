import{useCallback as c,useEffect as U,useState as u}from"react";import{jsx as a,jsxs as r}from"react/jsx-runtime";var C="/api/apps/push",F={provider_ready:!1,active_devices:0,sent_today:0,failed_today:0,recent:[]},O={configured:!1,hostname:"",domains_bound:!1,dns:{managed:!1}};function H({projectId:n=""}){let[o,p]=u(F),[b,$]=u([]),[t,x]=u(O),[m,g]=u(k()),[v,f]=u(!1),[w,_]=u(!0),[R,i]=u(""),d=c((e)=>{if(!n)return`${C}${e}`;let s=e.includes("?")?"&":"?";return`${C}${e}${s}project_id=${encodeURIComponent(n)}`},[n]),l=c(async()=>{_(!0);try{let[e,s,h]=await Promise.all([fetch(d("/stats"),{credentials:"same-origin"}),fetch(d("/devices?limit=20"),{credentials:"same-origin"}),fetch(d("/admin/relay-domain"),{credentials:"same-origin"})]);if(!e.ok||!s.ok||!h.ok){let L=!e.ok?e.status:!s.ok?s.status:h.status;throw Error(`Push returned ${L}`)}p(await e.json());let A=await s.json();$(A.devices||[]);let S=await h.json();x(S),g(S.hostname||k()),i("")}catch(e){i(e instanceof Error?e.message:"Could not load Push")}finally{_(!1)}},[d]);U(()=>{l()},[l]);let P=c(async(e)=>{i("Sending test…");let s=await fetch(d(`/admin/devices/${encodeURIComponent(e)}/test`),{method:"POST",credentials:"same-origin"});if(!s.ok){let h=await s.json().catch(()=>({}));i(h.error||`Test failed (${s.status})`);return}i("Test notification sent"),await l()},[d,l]),E=c(async(e)=>{let s=await fetch(d(`/admin/devices/${encodeURIComponent(e)}`),{method:"DELETE",credentials:"same-origin"});if(!s.ok){i(`Could not revoke device (${s.status})`);return}i("Device revoked"),await l()},[d,l]),T=c(async()=>{f(!0),i("Configuring relay domain…");try{let e=await fetch(d("/admin/relay-domain"),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({hostname:m.trim(),project_id:n,auto_dns:!0})}),s=await e.json().catch(()=>({}));if(!e.ok)throw Error(s.error||`Relay setup failed (${e.status})`);x(s),g(s.hostname),i(s.dns.error?`Relay route configured. ${s.dns.error}`:"Relay domain configured")}catch(e){i(e instanceof Error?e.message:"Could not configure relay domain")}finally{f(!1)}},[d,n,m]),M=c(async()=>{if(!window.confirm(`Detach ${t.hostname}? Its DNS record will be retained.`))return;f(!0),i("Detaching relay domain…");try{let e=await fetch(d("/admin/relay-domain"),{method:"DELETE",credentials:"same-origin"}),s=await e.json().catch(()=>({}));if(!e.ok)throw Error(s.error||`Relay detach failed (${e.status})`);x(s),g(s.hostname||k()),i("Relay domain detached. Its DNS record was retained.")}catch(e){i(e instanceof Error?e.message:"Could not detach relay domain")}finally{f(!1)}},[d,t.hostname]);return r("div",{className:"push-panel",children:[a("style",{children:B}),r("header",{className:"push-header",children:[r("div",{children:[a("div",{className:"push-eyebrow",children:"MOBILE DELIVERY"}),a("h1",{children:"Push"}),a("p",{children:"Private notifications for connected Apteva devices."})]}),a("button",{className:"push-refresh",onClick:l,disabled:w,children:w?"Loading…":"Refresh"})]}),r("section",{className:"push-metrics","aria-label":"Push status",children:[a(y,{label:"APNs",value:o.provider_ready?"Connected":"Not connected",tone:o.provider_ready?"good":"muted"}),a(y,{label:"Active devices",value:String(o.active_devices)}),a(y,{label:"Sent today",value:String(o.sent_today)}),a(y,{label:"Failed today",value:String(o.failed_today),tone:o.failed_today?"warn":"muted"})]}),R&&a("div",{className:"push-message",role:"status",children:R}),r("section",{className:"push-card push-relay",children:[r("div",{className:"push-card-heading",children:[r("div",{children:[a("h2",{children:"Relay domain"}),a("p",{children:"Public HTTPS endpoint used by connected Apteva instances."})]}),t.configured&&r("div",{className:"push-actions",children:[a("button",{onClick:l,disabled:w||v,children:"Refresh"}),a("button",{className:"danger",onClick:M,disabled:v,children:"Detach"})]})]}),t.configured?r("div",{className:"push-relay-body",children:[r("div",{className:"push-relay-url",children:[a("div",{className:"push-relay-mark","aria-hidden":"true",children:"↗"}),r("div",{children:[a("span",{children:"Relay URL"}),a("a",{href:`${t.relay_url}/health`,target:"_blank",rel:"noreferrer",children:t.relay_url})]})]}),r("div",{className:"push-relay-statuses",children:[a(N,{label:"Route",value:t.route?.status||"Unavailable",active:t.route?.status==="active"}),a(N,{label:"TLS",value:t.route?.certificate?.status||(t.route?"Pending":"Unavailable"),active:t.route?.certificate?.status==="live"}),a(N,{label:"DNS",value:t.dns.managed?"Managed":"Manual",active:t.dns.managed})]}),(t.dns.type||t.dns.value)&&r("div",{className:"push-dns-record",children:[a("span",{children:"DNS record"}),r("code",{children:[t.dns.name||"@"," ",t.dns.type||"—"," ",t.dns.value||"target required"]})]}),(t.route_error||t.dns.error||t.route?.certificate?.error)&&a("div",{className:"push-relay-note",children:t.route_error||t.dns.error||t.route?.certificate?.error})]}):r("div",{className:"push-relay-setup",children:[a("label",{htmlFor:"push-relay-hostname",children:"Hostname"}),r("div",{className:"push-relay-field",children:[a("span",{children:"https://"}),a("input",{id:"push-relay-hostname",value:m,onChange:(e)=>g(e.target.value),placeholder:"push.example.com",autoCapitalize:"none",autoCorrect:"off",spellCheck:!1}),a("button",{className:"push-primary",onClick:T,disabled:v||!m.trim(),children:v?"Configuring…":"Configure relay"})]}),a("p",{children:t.domains_bound?"Domains will publish DNS automatically. Apteva ingress manages the route and TLS certificate.":"Bind Domains for automatic DNS, or configure now to receive the exact record to publish manually."})]})]}),r("div",{className:"push-grid",children:[r("section",{className:"push-card",children:[r("div",{className:"push-card-heading",children:[r("div",{children:[a("h2",{children:"Devices"}),a("p",{children:"iPhones registered with this relay."})]}),a("span",{className:"push-count",children:b.length})]}),r("div",{className:"push-list",children:[b.length===0&&a(D,{title:"No devices yet",detail:"A device appears here after the iOS app enables notifications."}),b.map((e)=>r("div",{className:"push-device",children:[a("div",{className:"push-device-icon","aria-hidden":"true",children:a("span",{})}),r("div",{className:"push-device-copy",children:[a("strong",{children:e.user_ref||"iOS device"}),r("span",{children:[e.bundle_id," · ",e.environment,e.app_version?` · v${e.app_version}`:""," · ",z(e.last_seen_at)]})]}),a("span",{className:`push-status ${e.status}`,children:e.status}),e.status==="active"&&r("div",{className:"push-actions",children:[a("button",{onClick:()=>P(e.id),children:"Test"}),a("button",{className:"danger",onClick:()=>E(e.id),children:"Revoke"})]})]},e.id))]})]}),r("section",{className:"push-card",children:[a("div",{className:"push-card-heading",children:r("div",{children:[a("h2",{children:"Recent delivery"}),a("p",{children:"Latest messages accepted or rejected by APNs."})]})}),r("div",{className:"push-list",children:[o.recent.length===0&&a(D,{title:"Nothing sent yet",detail:"Delivery history will appear here."}),o.recent.map((e)=>r("div",{className:"push-delivery",children:[a("div",{className:`push-dot ${e.status}`}),r("div",{className:"push-delivery-copy",children:[a("strong",{children:I(e.type)}),a("span",{children:z(e.created_at)}),e.error&&a("small",{title:e.error,children:e.error})]}),a("span",{className:`push-status ${e.status}`,children:e.status})]},e.id))]})]})]})]})}function y({label:n,value:o,tone:p=""}){return r("div",{className:`push-metric ${p}`,children:[a("span",{children:n}),a("strong",{children:o})]})}function D({title:n,detail:o}){return r("div",{className:"push-empty",children:[a("strong",{children:n}),a("span",{children:o})]})}function N({label:n,value:o,active:p}){return r("div",{className:"push-relay-status",children:[a("span",{children:n}),a("strong",{className:p?"active":"",children:o.replaceAll("_"," ")})]})}function k(){return""}function I(n){if(n==="approval")return"Approval";if(n==="alert")return"Alert";if(n==="report")return"Report";return"Test notification"}function z(n){let o=new Date(n);if(Number.isNaN(o.getTime()))return"Recently";let p=Math.max(0,Math.floor((Date.now()-o.getTime())/1000));if(p<60)return"Just now";if(p<3600)return`${Math.floor(p/60)}m ago`;if(p<86400)return`${Math.floor(p/3600)}h ago`;return`${Math.floor(p/86400)}d ago`}var B=`
  .push-panel {
    --push-orange: #ff6b1a;
    --push-border: color-mix(in srgb, currentColor 12%, transparent);
    --push-muted: color-mix(in srgb, currentColor 58%, transparent);
    color: var(--text-primary, inherit);
    max-width: 1120px;
    margin: 0 auto;
    padding: 28px;
  }
  .push-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 24px;
  }
  .push-eyebrow {
    color: var(--push-orange);
    font: 700 11px/1.2 ui-monospace, SFMono-Regular, Menlo, monospace;
    letter-spacing: .13em;
  }
  .push-header h1 {
    margin: 5px 0 4px;
    font-size: 32px;
    letter-spacing: -.04em;
  }
  .push-header p, .push-card-heading p {
    color: var(--push-muted);
    margin: 0;
    font-size: 14px;
  }
  .push-refresh, .push-actions button {
    appearance: none;
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 5%, transparent);
    color: inherit;
    border-radius: 10px;
    padding: 9px 13px;
    font: inherit;
    font-size: 13px;
    cursor: pointer;
  }
  .push-refresh:hover, .push-actions button:hover {
    border-color: color-mix(in srgb, var(--push-orange) 55%, transparent);
  }
  .push-refresh:disabled, .push-actions button:disabled { opacity: .5; cursor: default; }
  .push-metrics {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
    margin-bottom: 16px;
  }
  .push-metric {
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 3%, transparent);
    border-radius: 14px;
    padding: 15px 16px;
  }
  .push-metric span {
    color: var(--push-muted);
    display: block;
    font-size: 12px;
    margin-bottom: 6px;
  }
  .push-metric strong { font-size: 17px; }
  .push-metric.good strong { color: #24b477; }
  .push-metric.warn strong { color: var(--push-orange); }
  .push-metric.muted strong { color: var(--push-muted); }
  .push-message {
    border: 1px solid color-mix(in srgb, var(--push-orange) 30%, transparent);
    background: color-mix(in srgb, var(--push-orange) 9%, transparent);
    border-radius: 11px;
    color: color-mix(in srgb, var(--push-orange) 85%, currentColor);
    font-size: 13px;
    margin: 0 0 16px;
    padding: 10px 13px;
  }
  .push-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.15fr) minmax(0, .85fr);
    gap: 16px;
  }
  .push-card {
    border: 1px solid var(--push-border);
    background: color-mix(in srgb, currentColor 2.5%, transparent);
    border-radius: 18px;
    min-width: 0;
    overflow: hidden;
  }
  .push-relay { margin-bottom: 16px; }
  .push-card-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 18px 19px 15px;
  }
  .push-card-heading h2 {
    font-size: 16px;
    margin: 0 0 4px;
  }
  .push-count {
    background: color-mix(in srgb, currentColor 7%, transparent);
    border-radius: 999px;
    color: var(--push-muted);
    font-size: 12px;
    padding: 4px 8px;
  }
  .push-list { border-top: 1px solid var(--push-border); }
  .push-device, .push-delivery {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 66px;
    padding: 10px 18px;
  }
  .push-device + .push-device, .push-delivery + .push-delivery {
    border-top: 1px solid var(--push-border);
  }
  .push-device-icon {
    align-items: center;
    background: color-mix(in srgb, var(--push-orange) 14%, transparent);
    border-radius: 11px;
    color: var(--push-orange);
    display: flex;
    height: 38px;
    justify-content: center;
    width: 38px;
  }
  .push-device-icon span {
    border: 1.7px solid currentColor;
    border-radius: 3px;
    box-sizing: border-box;
    display: block;
    height: 20px;
    position: relative;
    width: 12px;
  }
  .push-device-icon span::after {
    background: currentColor;
    border-radius: 50%;
    bottom: 1.5px;
    content: "";
    height: 1.5px;
    left: 50%;
    position: absolute;
    transform: translateX(-50%);
    width: 1.5px;
  }
  .push-device-copy, .push-delivery-copy {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
  }
  .push-device-copy strong, .push-delivery-copy strong { font-size: 14px; }
  .push-device-copy span, .push-delivery-copy span, .push-delivery-copy small {
    color: var(--push-muted);
    font-size: 12px;
    margin-top: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .push-status {
    border-radius: 999px;
    color: var(--push-muted);
    font-size: 11px;
    padding: 4px 7px;
    text-transform: capitalize;
  }
  .push-status.active, .push-status.sent {
    background: color-mix(in srgb, #24b477 12%, transparent);
    color: #24b477;
  }
  .push-status.invalid, .push-status.failed {
    background: color-mix(in srgb, var(--push-orange) 12%, transparent);
    color: var(--push-orange);
  }
  .push-actions { display: flex; gap: 6px; }
  .push-actions button { padding: 6px 9px; }
  .push-actions .danger { color: var(--push-orange); }
  .push-relay-body, .push-relay-setup {
    border-top: 1px solid var(--push-border);
    padding: 18px 19px;
  }
  .push-relay-url {
    align-items: center;
    display: flex;
    gap: 12px;
  }
  .push-relay-mark {
    align-items: center;
    background: color-mix(in srgb, var(--push-orange) 14%, transparent);
    border-radius: 11px;
    color: var(--push-orange);
    display: flex;
    font-size: 18px;
    height: 40px;
    justify-content: center;
    width: 40px;
  }
  .push-relay-url > div:last-child {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .push-relay-url span, .push-dns-record span, .push-relay-setup label {
    color: var(--push-muted);
    font-size: 11px;
    margin-bottom: 3px;
  }
  .push-relay-url a {
    color: inherit;
    font-size: 15px;
    font-weight: 650;
    overflow: hidden;
    text-decoration: none;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .push-relay-url a:hover { color: var(--push-orange); }
  .push-relay-statuses {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    margin-top: 16px;
  }
  .push-relay-status {
    background: color-mix(in srgb, currentColor 3%, transparent);
    border: 1px solid var(--push-border);
    border-radius: 11px;
    display: flex;
    flex-direction: column;
    padding: 10px 12px;
  }
  .push-relay-status span {
    color: var(--push-muted);
    font-size: 11px;
    margin-bottom: 4px;
  }
  .push-relay-status strong {
    color: var(--push-muted);
    font-size: 13px;
    text-transform: capitalize;
  }
  .push-relay-status strong.active { color: #24b477; }
  .push-dns-record {
    align-items: flex-start;
    border-top: 1px solid var(--push-border);
    display: flex;
    flex-direction: column;
    margin-top: 16px;
    padding-top: 14px;
  }
  .push-dns-record code {
    font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
    overflow-wrap: anywhere;
  }
  .push-relay-note {
    background: color-mix(in srgb, var(--push-orange) 8%, transparent);
    border-radius: 9px;
    color: color-mix(in srgb, var(--push-orange) 82%, currentColor);
    font-size: 12px;
    margin-top: 12px;
    padding: 9px 11px;
  }
  .push-relay-setup > label {
    display: block;
    font-weight: 600;
  }
  .push-relay-field {
    align-items: center;
    background: color-mix(in srgb, currentColor 3%, transparent);
    border: 1px solid var(--push-border);
    border-radius: 12px;
    display: flex;
    margin-top: 7px;
    padding: 5px;
  }
  .push-relay-field > span {
    color: var(--push-muted);
    font-size: 13px;
    padding-left: 8px;
  }
  .push-relay-field input {
    background: transparent;
    border: 0;
    color: inherit;
    flex: 1;
    font: 14px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace;
    min-width: 120px;
    outline: none;
    padding: 8px 4px;
  }
  .push-primary {
    appearance: none;
    background: var(--push-orange);
    border: 0;
    border-radius: 9px;
    color: white;
    cursor: pointer;
    font: inherit;
    font-size: 13px;
    font-weight: 650;
    padding: 9px 13px;
  }
  .push-primary:disabled { cursor: default; opacity: .5; }
  .push-relay-setup > p {
    color: var(--push-muted);
    font-size: 12px;
    line-height: 1.5;
    margin: 9px 0 0;
  }
  .push-dot {
    background: var(--push-muted);
    border-radius: 50%;
    height: 8px;
    width: 8px;
  }
  .push-dot.sent { background: #24b477; }
  .push-dot.failed { background: var(--push-orange); }
  .push-empty {
    align-items: center;
    color: var(--push-muted);
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 36px 22px;
    text-align: center;
  }
  .push-empty strong { color: inherit; font-size: 14px; }
  .push-empty span { font-size: 13px; }
  @media (max-width: 800px) {
    .push-panel { padding: 18px; }
    .push-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .push-grid { grid-template-columns: 1fr; }
  }
  @media (max-width: 540px) {
    .push-header { align-items: flex-start; flex-direction: column; }
    .push-device { align-items: flex-start; flex-wrap: wrap; }
    .push-device-copy { min-width: 160px; }
    .push-actions { margin-left: 50px; width: 100%; }
    .push-card-heading .push-actions { margin-left: 0; width: auto; }
    .push-relay-statuses { grid-template-columns: 1fr; }
    .push-relay-field { align-items: stretch; flex-wrap: wrap; }
    .push-relay-field input { min-width: 160px; }
    .push-primary { width: 100%; }
  }
`;export{H as default};

//# debugId=C2A01E8B673EF5B064756E2164756E21
