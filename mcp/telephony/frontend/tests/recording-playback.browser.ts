import {test,expect} from "@playwright/test";
test("balanced playback recovers to original with a visible explanation",async({page})=>{
 await page.goto("/");await expect(page.locator("audio")).toHaveJSProperty("readyState",4);
 await page.locator("audio").evaluate(el=>el.dispatchEvent(new Event("error")));
 await expect(page.locator("audio")).toHaveAttribute("src","/audio/original");
 await expect(page.getByRole("status")).toHaveText("Balanced playback unavailable. Playing the original channels.");
 await expect(page.getByRole("button",{name:"Original",exact:true})).toHaveClass(/font-medium/);
});
for(const track of ["Caller","Agent"])test(`${track}-only errors never switch to both channels`,async({page})=>{
 await page.goto("/");await page.getByRole("button",{name:track,exact:true}).click();await expect(page.locator("audio")).toHaveJSProperty("readyState",4);
 await page.locator("audio").evaluate(el=>el.dispatchEvent(new Event("error")));
 await expect(page.locator("audio")).toHaveAttribute("src",`/audio/${track.toLowerCase()}`);
 await expect(page.getByRole("status")).toHaveText(`${track}-only playback unavailable. Select Original to play both sides.`);
 await page.getByRole("button",{name:"Original",exact:true}).click();await expect(page.getByRole("status")).toHaveCount(0);
});

test("bundled recording MP3 plays and seeks in Chromium",async({page})=>{
 await page.goto("/");
 await page.locator("audio").evaluate(el=>{const audio=el as HTMLAudioElement;audio.src="/audio/mp3";audio.load();});
 await expect.poll(()=>page.locator("audio").evaluate(el=>(el as HTMLAudioElement).readyState)).toBeGreaterThanOrEqual(2);
 const duration=await page.locator("audio").evaluate(el=>(el as HTMLAudioElement).duration);
 expect(duration).toBeGreaterThan(1.9);expect(duration).toBeLessThan(2.2);
 await page.locator("audio").evaluate(async el=>{const audio=el as HTMLAudioElement;audio.currentTime=1.2;await audio.play();});
 await expect.poll(()=>page.locator("audio").evaluate(el=>(el as HTMLAudioElement).currentTime)).toBeGreaterThan(1.3);
 await expect(page.locator("audio")).toHaveJSProperty("error",null);
});
