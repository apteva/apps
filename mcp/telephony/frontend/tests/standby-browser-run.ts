import {chromium} from "@playwright/test";
const origin=process.env.TELEPHONY_STANDBY_BROWSER_ORIGIN!;
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw new Error("Only loopback fixtures are permitted");
const browser=await chromium.launch({headless:true,args:["--autoplay-policy=no-user-gesture-required","--use-fake-device-for-media-stream","--use-fake-ui-for-media-stream"]});
try {
 const page=await browser.newPage({permissions:["microphone"]});
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 await page.goto(origin);await page.waitForFunction(()=>typeof(window as any).runStandbyTest==="function");
 const result=await page.evaluate(async()=>{const {standby_url}=await(await fetch("/config")).json();return await(window as any).runStandbyTest(standby_url);});
 if(errors.length)throw new Error("Unexpected browser page errors: "+errors.join(","));
 console.log(JSON.stringify({browser:browser.version(),...result}));
}finally{await browser.close();}
