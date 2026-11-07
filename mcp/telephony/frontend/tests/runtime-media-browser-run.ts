import {chromium} from '@playwright/test';
const origin=process.env.TELEPHONY_RUNTIME_BROWSER_ORIGIN!;
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw Error('Only local fixtures allowed');
const browser=await chromium.launch({headless:true});
try {
 const page=await browser.newPage();const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));page.on('websocket',s=>s.on('socketerror',e=>errors.push(e)));
 await page.goto(origin);await page.waitForFunction(()=>typeof (window as any).runManagedMedia==='function');
 const result=await page.evaluate(()=>(window as any).runManagedMedia()).catch(e=>{throw Error(JSON.stringify({message:e.message,errors}));});
 if(errors.length||result.echoes!==2||!result.remoteMedia)throw Error(JSON.stringify({errors,result}));
 console.log(JSON.stringify(result));
}finally{await browser.close();}
