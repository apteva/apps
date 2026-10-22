import {chromium} from "@playwright/test";
const origin=process.env.TELEPHONY_TURN_BROWSER_ORIGIN!;
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw new Error("Only loopback TURN fixtures are permitted");
const browser=await chromium.launch({headless:true,args:["--autoplay-policy=no-user-gesture-required","--use-fake-device-for-media-stream","--use-fake-ui-for-media-stream"]});
try {
 const page=await browser.newPage({permissions:["microphone"]});
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 await page.goto(origin);await page.waitForFunction(()=>typeof(window as any).runTURNTest==="function");
 const result=await page.evaluate(async()=>{const {media_url}=await(await fetch("/config")).json();return await(window as any).runTURNTest(media_url);});
 if(errors.length)throw new Error("Unexpected browser page errors: "+errors.join(","));
 console.log(JSON.stringify({browser:browser.version(),...result}));
}finally{await browser.close();}
