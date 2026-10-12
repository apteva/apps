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
