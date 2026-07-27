import{useCallback as c,useEffect as _,useState as u}from"react";import{jsx as t,jsxs as r}from"react/jsx-runtime";var b="/api/apps/push",C={provider_ready:!1,active_devices:0,sent_today:0,failed_today:0,recent:[]};function $({projectId:s=""}){let[a,n]=u(C),[h,w]=u([]),[m,v]=u(!0),[f,o]=u(""),p=c((e)=>{if(!s)return`${b}${e}`;let i=e.includes("?")?"&":"?";return`${b}${e}${i}project_id=${encodeURIComponent(s)}`},[s]),d=c(async()=>{v(!0);try{let[e,i]=await Promise.all([fetch(p("/stats"),{credentials:"same-origin"}),fetch(p("/devices?limit=20"),{credentials:"same-origin"})]);if(!e.ok||!i.ok)throw Error(`Push returned ${e.ok?i.status:e.status}`);n(await e.json());let g=await i.json();w(g.devices||[]),o("")}catch(e){o(e instanceof Error?e.message:"Could not load Push")}finally{v(!1)}},[p]);_(()=>{d()},[d]);let N=c(async(e)=>{o("Sending test…");let i=await fetch(p(`/admin/devices/${encodeURIComponent(e)}/test`),{method:"POST",credentials:"same-origin"});if(!i.ok){let g=await i.json().catch(()=>({}));o(g.error||`Test failed (${i.status})`);return}o("Test notification sent"),await d()},[p,d]),k=c(async(e)=>{let i=await fetch(p(`/admin/devices/${encodeURIComponent(e)}`),{method:"DELETE",credentials:"same-origin"});if(!i.ok){o(`Could not revoke device (${i.status})`);return}o("Device revoked"),await d()},[p,d]);return r("div",{className:"push-panel",children:[t("style",{children:D}),r("header",{className:"push-header",children:[r("div",{children:[t("div",{className:"push-eyebrow",children:"MOBILE DELIVERY"}),t("h1",{children:"Push"}),t("p",{children:"Private notifications for connected Apteva devices."})]}),t("button",{className:"push-refresh",onClick:d,disabled:m,children:m?"Loading…":"Refresh"})]}),r("section",{className:"push-metrics","aria-label":"Push status",children:[t(l,{label:"APNs",value:a.provider_ready?"Connected":"Not connected",tone:a.provider_ready?"good":"muted"}),t(l,{label:"Active devices",value:String(a.active_devices)}),t(l,{label:"Sent today",value:String(a.sent_today)}),t(l,{label:"Failed today",value:String(a.failed_today),tone:a.failed_today?"warn":"muted"})]}),f&&t("div",{className:"push-message",role:"status",children:f}),r("div",{className:"push-grid",children:[r("section",{className:"push-card",children:[r("div",{className:"push-card-heading",children:[r("div",{children:[t("h2",{children:"Devices"}),t("p",{children:"iPhones registered with this relay."})]}),t("span",{className:"push-count",children:h.length})]}),r("div",{className:"push-list",children:[h.length===0&&t(y,{title:"No devices yet",detail:"A device appears here after the iOS app enables notifications."}),h.map((e)=>r("div",{className:"push-device",children:[t("div",{className:"push-device-icon","aria-hidden":"true",children:t("span",{})}),r("div",{className:"push-device-copy",children:[t("strong",{children:e.user_ref||"iOS device"}),r("span",{children:[e.bundle_id," · ",e.environment,e.app_version?` · v${e.app_version}`:""," · ",x(e.last_seen_at)]})]}),t("span",{className:`push-status ${e.status}`,children:e.status}),e.status==="active"&&r("div",{className:"push-actions",children:[t("button",{onClick:()=>N(e.id),children:"Test"}),t("button",{className:"danger",onClick:()=>k(e.id),children:"Revoke"})]})]},e.id))]})]}),r("section",{className:"push-card",children:[t("div",{className:"push-card-heading",children:r("div",{children:[t("h2",{children:"Recent delivery"}),t("p",{children:"Latest messages accepted or rejected by APNs."})]})}),r("div",{className:"push-list",children:[a.recent.length===0&&t(y,{title:"Nothing sent yet",detail:"Delivery history will appear here."}),a.recent.map((e)=>r("div",{className:"push-delivery",children:[t("div",{className:`push-dot ${e.status}`}),r("div",{className:"push-delivery-copy",children:[t("strong",{children:P(e.type)}),t("span",{children:x(e.created_at)}),e.error&&t("small",{title:e.error,children:e.error})]}),t("span",{className:`push-status ${e.status}`,children:e.status})]},e.id))]})]})]})]})}function l({label:s,value:a,tone:n=""}){return r("div",{className:`push-metric ${n}`,children:[t("span",{children:s}),t("strong",{children:a})]})}function y({title:s,detail:a}){return r("div",{className:"push-empty",children:[t("strong",{children:s}),t("span",{children:a})]})}function P(s){if(s==="approval")return"Approval";if(s==="alert")return"Alert";if(s==="report")return"Report";return"Test notification"}function x(s){let a=new Date(s);if(Number.isNaN(a.getTime()))return"Recently";let n=Math.max(0,Math.floor((Date.now()-a.getTime())/1000));if(n<60)return"Just now";if(n<3600)return`${Math.floor(n/60)}m ago`;if(n<86400)return`${Math.floor(n/3600)}h ago`;return`${Math.floor(n/86400)}d ago`}var D=`
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
  .push-refresh:disabled { opacity: .5; cursor: default; }
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
  }
`;export{$ as default};

//# debugId=72EAD2EE1755159C64756E2164756E21
