import {test,expect,mock} from 'bun:test';
import React from './node_modules/react/index.js';
import * as jsx from './node_modules/react/jsx-runtime.js';
import {create,act} from './node_modules/react-test-renderer/index.js';
const build=await Bun.build({entrypoints:[import.meta.dir+'/../../ui/BillingPanel.tsx'],target:'bun',format:'esm',external:['react','react/jsx-runtime','react/jsx-dev-runtime'],outdir:import.meta.dir+'/.generated',naming:'panel.mjs'});if(!build.success)throw new Error(String(build.logs));
const {default:Panel}=await import('./.generated/panel.mjs');
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;
(globalThis as any).window={__aptevaAppEvents:{subscribe:()=>()=>{}}};
const invoice=(id:number)=>({id,number:`QA-${id}`,customer_id:1,customer_name:'Buyer',status:'open',currency:'USD',total_cents:1200,amount_paid_cents:0,line_items:[],updated_at:'2026-09-05'});
function text(n:any):string{return typeof n==='string'?n:Array.isArray(n)?n.map(text).join(''):n?.children?.map(text).join('')||''}
function button(tree:any,label:string){return tree.root.findAllByType('button').find((b:any)=>text(b.toJSON?.()||{children:b.children})===label)!}
function allText(tree:any){return text(tree.toJSON())}

test('selection ignores reversed responses, actions carry install identity, pagination and install reset work',async()=>{
 const requests:any[]=[];let aResolve:(value:any)=>void=()=>{};
 globalThis.fetch=async(input:any,init:any={})=>{const url=new URL(String(input),'http://fixture');requests.push({url,init});if(url.pathname==='/api/apps/billing/invoices'){const offset=Number(url.searchParams.get('offset')||0);return Response.json({invoices:Array.from({length:offset?15:50},(_,n)=>invoice(offset+n+1))})}if(url.pathname==='/api/apps/billing/invoices/1')return new Promise(resolve=>{aResolve=resolve});if(url.pathname.endsWith('/history'))return Response.json({payments:[],audit_log:[]});if(url.pathname==='/api/apps/billing/invoices/2')return Response.json({invoice:invoice(2)});if(url.pathname.endsWith('/mcp'))return Response.json({result:{content:[{type:'text',text:'{"url":"https://checkout.stripe.com/test-only"}'}]}});return Response.json({})};
 let tree:any;await act(async()=>{tree=create(<Panel appName="billing" projectId="p" installId={23}/>)});
 const rows=tree.root.findAllByType('li');await act(async()=>{rows[0].props.onClick();rows[1].props.onClick()});await act(async()=>{aResolve(Response.json({invoice:invoice(1)}))});expect(tree.root.findByType('h1').children.map((n:any)=>typeof n==='string'?n:'').join('')).toContain('QA-2');
 await act(async()=>{button(tree,'Send payment link').props.onClick()});expect(allText(tree)).toContain('Stripe payment link for QA-2');expect(requests.some(r=>r.url.pathname.endsWith('/mcp')&&r.url.searchParams.get('install_id')==='23')).toBe(true);
 await act(async()=>{button(tree,'Close').props.onClick()});
 await act(async()=>{button(tree,'Next').props.onClick()});expect(allText(tree)).toContain('QA-51');expect(allText(tree)).toContain('Page 2');
 await act(async()=>{tree.update(<Panel appName="billing" projectId="p" installId={24}/>)});expect(allText(tree)).toContain('Select an invoice');expect(allText(tree)).not.toContain('Stripe payment link');expect(requests.at(-1).url.searchParams.get('install_id')).toBe('24');
 await act(async()=>{tree.unmount()});
});

test('editing a draft preserves catalog references and metadata',async()=>{
 let saved:any;const draft={...invoice(1),status:'draft',number:'',line_items:[{id:10,description:'Catalog item',quantity:1,unit_price_cents:900,amount_cents:900,tax_rate_bps:0,price_id:88,product_id:77,metadata:{contract:'keep-me'}}],currency:'EUR',tax_treatment:'standard'};
 globalThis.fetch=async(input:any,init:any={})=>{const u=new URL(String(input),'http://fixture');if(init.method==='PATCH'){saved=JSON.parse(init.body);return Response.json({invoice:{...draft,...saved}})};if(u.pathname.endsWith('/history'))return Response.json({payments:[],audit_log:[]});if(u.pathname.endsWith('/invoices'))return Response.json({invoices:[draft]});if(u.pathname.endsWith('/invoices/1'))return Response.json({invoice:draft});if(u.pathname.endsWith('/customers/1'))return Response.json({customer:{id:1,name:'Buyer',email:'buyer@example.com',currency:'EUR'}});return Response.json({})};
 let tree:any;await act(async()=>{tree=create(<Panel appName="billing" projectId="p" installId={23}/>)});await act(async()=>{tree.root.findByType('li').props.onClick()});await act(async()=>{button(tree,'Edit').props.onClick()});await act(async()=>{button(tree,'Save changes').props.onClick()});expect(saved.line_items[0].price_id).toBe(88);expect(saved.line_items[0].product_id).toBe(77);expect(saved.line_items[0].metadata).toEqual({contract:'keep-me'});expect(saved.currency).toBe('EUR');await act(async()=>tree.unmount());
});
