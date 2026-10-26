import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { RankedPosts, ReportNotice, visibleMetricNames, type AnalyticsReport } from "./analyticsReporting";
import SocialPanel from "./SocialPanel";

const browser=new Window({url:"http://localhost:3000"});
const originalFetch=globalThis.fetch;
let root:ReturnType<typeof createRoot>|undefined;
function globals(){Object.assign(globalThis,{window:browser,document:browser.document,navigator:browser.navigator,localStorage:browser.localStorage,HTMLElement:browser.HTMLElement,EventSource: class { close(){} },IS_REACT_ACT_ENVIRONMENT:true})}
afterEach(async()=>{globals();if(root)await act(async()=>root!.unmount());root=undefined;browser.document.body.innerHTML="";globalThis.fetch=originalFetch;browser.history.replaceState(null,"","/");browser.localStorage.clear();await browser.happyDOM.abort()});
async function render(node:React.ReactNode){globals();if(!root){const host=browser.document.createElement("div");browser.document.body.append(host);root=createRoot(host)}await act(async()=>root!.render(node))}
const report:AnalyticsReport={scope:"destination",entity_id:"board",identity:"pinterest:board",source:"post_aggregation",window:{kind:"range",start_date:"2026-10-01",end_date:"2026-10-07"},metric_info:{impressions:{kind:"range",additive:true},outbound_clicks:{kind:"range",additive:true},clicks:{kind:"range",alias_of:"outbound_clicks"}},coverage:{inventory:"current_destination_posts",discovered:3,included:2,failed:1,complete:false,truncated:false,note:"Current board membership"}};

test("report notice exposes partial coverage and real dates",async()=>{await render(<ReportNotice report={report} comparison={{status:"partial",window:{start_date:"2026-09-24",end_date:"2026-09-30"}}}/>);const text=browser.document.body.textContent!;expect(text).toContain("Selected destination");expect(text).toContain("Calculated from individual posts");expect(text).toContain("Partial coverage");expect(text).toContain("2/3 posts included");expect(text).toContain("1 failed");expect(text).toContain("2026-09-24");expect(text).toContain("Current board membership")});
test("metric availability retains zero and removes compatibility aliases",()=>{expect(visibleMetricNames(["impressions","outbound_clicks","clicks"],report)).toEqual(["impressions","outbound_clicks"])});
test("post ranking preserves genuine zeros and missing values",async()=>{let selected="";await render(<RankedPosts metric="outbound_clicks" onMetric={value=>selected=value} metrics={["outbound_clicks","impressions"]} rows={[{id:"missing",title:"Missing",url:"https://pinterest.com/pin/1",status:"failed",error:"No access",metrics:{}},{id:"zero",title:"Zero clicks",url:"https://pinterest.com/pin/2",status:"ok",metrics:{outbound_clicks:0},report}]}/>);const rows=browser.document.querySelectorAll("tbody tr");expect(rows[0].textContent).toContain("Zero clicks");expect(rows[0].textContent).toContain("0");expect(rows[0].textContent).toContain("Unavailable");expect(rows[1].textContent).toContain("No access");const select=browser.document.querySelector("select")!;await act(async()=>{select.value="impressions";select.dispatchEvent(new browser.Event("change",{bubbles:true}))});expect(selected).toBe("impressions")});
test("Social metrics forwards scope, date window and ranking while zero cards stay visible",async()=>{
 globals();browser.history.replaceState(null,"","/?tab=metrics");const requests:URL[]=[];
 globalThis.fetch=(async(input)=>{const url=new URL(String(input),"http://localhost");requests.push(url);
 if(url.pathname.endsWith("/profiles"))return Response.json({profiles:[]});
 if(url.pathname.endsWith("/accounts"))return Response.json({accounts:[{id:42,platform:"pinterest",display_name:"Ideas",status:"active"}]});
 if(url.pathname.endsWith("/platforms"))return Response.json({platforms:[]});
 if(url.pathname.endsWith("/posts"))return Response.json({posts:[]});
 if(url.pathname.includes("/metrics"))return Response.json({social_account_id:42,platform:"pinterest",display_name:"Ideas",status:"ok",updated_at:new Date().toISOString(),impressions:100,saves:0,available:["impressions","saves"],outbound_clicks:null,report:{...report,requested_metrics:["impressions","saves","outbound_clicks"],metric_info:{...report.metric_info,saves:{kind:"range",additive:true}}},capabilities:{scope_labels:{destination:"Board",provider_account:"Pinterest account"},metrics:["impressions","saves"]},post_rows:[{id:"a",title:"A Pin",url:"https://pinterest.com/pin/a",status:"ok",metrics:{impressions:100,saves:0},report}]});
 return Response.json({items:[],unread:0});}) as typeof fetch;
 await render(<SocialPanel projectId="test-project"/>);
 expect(browser.document.body.textContent).toContain("Post ranking");expect(browser.document.body.textContent).toContain("saves");expect(browser.document.body.textContent).toContain("Unavailable");
 const metrics=requests.filter(url=>url.pathname.includes("/metrics"));expect(metrics.length).toBeGreaterThan(0);expect(metrics.every(url=>url.searchParams.get("scope")==="provider_account"&&url.searchParams.get("project_id")==="test-project"&&url.searchParams.get("range")==="28d")).toBe(true);
 const scope=browser.document.querySelector('select[aria-label="Analytics scope"]') as HTMLSelectElement;expect(scope.textContent).toContain("Board");await act(async()=>{scope.value="destination";scope.dispatchEvent(new browser.Event("change",{bubbles:true}))});expect(requests.at(-1)?.searchParams.get("scope")).toBe("destination");
});

test("generic report freshness exposes timezone, pending dates and provider estimates",async()=>{
 await render(<ReportNotice report={{...report,identity:"another-channel:account",window:{...report.window,timezone:"UTC"},freshness:{observed_at:"2026-10-26T13:00:00Z",processed_through:"2026-10-24",pending_dates:["2026-10-25","2026-10-26"],estimated_dates:["2026-10-23"],unavailable_dates:["2026-10-22"]}}}/>);
 const text=browser.document.body.textContent!;
 expect(text).toContain("UTC");expect(text).toContain("Processed through");expect(text).toContain("Pending:");expect(text).toContain("provider estimates");expect(text).toContain("not real-time");expect(text).toContain("gaps are not zero activity");
});
