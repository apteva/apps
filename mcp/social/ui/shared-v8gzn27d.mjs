var r=14400000;function i(e,t=Date.now()){if(!e||e.status==="failed")return!1;if(!e.updated_at)return!0;let n=Date.parse(e.updated_at);return!Number.isFinite(n)||t-n>=14400000}function s(e){return Object.entries(e||{}).filter(([t])=>!t.startsWith("_")&&!t.startsWith("total_")&&!t.endsWith("_total")).map(([t,n])=>({name:t,points:n})).filter(({points:t})=>t.length>=2&&t.some((n)=>Number(n.value)!==0))}
export{r as M,i as N,s as O};

//# debugId=38552B360D3B863764756E2164756E21
