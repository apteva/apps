import {chromium} from '@playwright/test';
const origin=process.env.TELEPHONY_BENCHMARK_ORIGIN!;
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw new Error('Benchmark permits loopback fixtures only');
const browser=await chromium.launch({headless:true,args:['--autoplay-policy=no-user-gesture-required','--use-fake-device-for-media-stream','--use-fake-ui-for-media-stream']});
try{
 const page=await browser.newPage();const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(origin);await page.waitForFunction(()=>typeof(window as any).runBenchmark==='function');
 const result=await page.evaluate(async()=>{const config=await(await fetch('/config')).json();return (window as any).runBenchmark(config)});
 result.page_errors=errors;result.browser_version=browser.version();await Bun.write(process.env.TELEPHONY_BENCHMARK_BROWSER_RESULT!,JSON.stringify(result,null,2));
}finally{await browser.close();}
