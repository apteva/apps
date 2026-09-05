var n=14400000;function i(e,t=Date.now()){if(!e||e.status==="failed")return!1;if(!e.updated_at)return!0;let r=Date.parse(e.updated_at);return!Number.isFinite(r)||t-r>=14400000}function s(e){return Object.entries(e||{}).filter(([t])=>!t.startsWith("_")&&!t.startsWith("total_")&&!t.endsWith("_total")).map(([t,r])=>({name:t,points:r})).filter(({points:t})=>t.length>=2&&t.some((r)=>Number(r.value)!==0))}
export{n as D,i as E,s as F};

//# debugId=BFFA9289D97D36AF64756E2164756E21
