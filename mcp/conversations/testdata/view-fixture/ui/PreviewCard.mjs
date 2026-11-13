import {createElement as h,useEffect,useState} from "react";
export default function PreviewCard({record_id,title,projectId,installId,presentation}) {
 const [value,setValue]=useState(null),[local,setLocal]=useState(0);
 useEffect(()=>{let active=true;let busy=false;const refresh=async()=>{if(busy)return;busy=true;try{const r=await fetch(`/api/apps/view-fixture/records/${encodeURIComponent(record_id)}?project_id=${encodeURIComponent(projectId)}&install_id=${installId}`,{credentials:"same-origin"});if(r.ok){const record=await r.json();if(active)setValue(record.value)}}finally{busy=false}};if(presentation?.active!==false)void refresh();const timer=setInterval(()=>{if(presentation?.active!==false)void refresh()},500);return()=>{active=false;clearInterval(timer)}},[record_id,projectId,installId,presentation?.active]);
 return h("section",{"data-live-resource":record_id,style:{padding:12,border:"1px solid #555",borderRadius:8}},
 h("h3",null,title),h("p",null,`Value: ${value??"loading"}`),h("p",null,`Layout: ${presentation?.mode??"inline"}`),
 h("button",{onClick:()=>setLocal(n=>n+1)},`Local count: ${local}`),
 h("iframe",{title:"Resource frame",src:`/api/apps/view-fixture/frame?project_id=${encodeURIComponent(projectId)}&install_id=${installId}`,style:{display:"block",width:"100%",height:presentation?.height?Math.max(80,presentation.height-160):120,border:0,background:"white"}}));
}
