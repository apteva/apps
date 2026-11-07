import {expect,test} from "bun:test";
import {audienceLabel,audienceMatches,matchesPublicationState,verifiedHere,type PublicationSummary} from "./publication";
const members={destination:"youtube",account_ref:"channel",audience_type:"restricted",access_policy:"membership",group_id:"gold"};
const unknown:PublicationSummary={destination:"youtube",account_ref:"channel",status:"verified_published",audience_type:"restricted",access_policy:"membership",group_scope:"unknown"};
test("unknown group scope never claims all members or exact group coverage",()=>{
 expect(audienceLabel(unknown)).toBe("Members");expect(audienceLabel({...unknown,group_scope:"all_eligible"})).toBe("All members");
 expect(audienceMatches(unknown,members)).toBe(false);expect(matchesPublicationState([unknown],"verified",members)).toBe(false);expect(matchesPublicationState([unknown],"ready",members)).toBe(false);
});
test("public, platform, account and selected groups have separate availability",()=>{
 const publicPost:PublicationSummary={...unknown,audience_type:"public",access_policy:"",group_scope:"unknown"};
 expect(matchesPublicationState([publicPost],"ready",members)).toBe(true);
 expect(matchesPublicationState([{...unknown,group_scope:"selected",group_ids:["silver"]}],"ready",members)).toBe(true);
 expect(matchesPublicationState([{...unknown,group_scope:"selected",group_ids:["gold"]}],"ready",members)).toBe(false);
 expect(matchesPublicationState([{...unknown,group_scope:"all_eligible"}],"verified",members)).toBe(true);
 expect(matchesPublicationState([unknown],"ready",{...members,destination:"patreon"})).toBe(true);
 expect(matchesPublicationState([unknown],"ready",{...members,account_ref:"other"})).toBe(true);
});
test("historical evidence keeps its original audience after current audience changes",()=>{
 const changed:PublicationSummary={...unknown,status:"removed",historically_verified:true,verified_audiences:[{audience_type:"public",group_scope:"unknown",group_ids:[]}]};
 expect(verifiedHere(changed,{destination:"youtube",audience_type:"public"})).toBe(true);
 expect(verifiedHere(changed,members)).toBe(false);
 expect(audienceLabel({audience_type:"private"})).toBe("Private");expect(audienceLabel({})).toBe("Audience unknown");
});

test("editing audience preserves publication time and unknown date",async()=>{
 const {publicationDateInput,publicationDateSubmit}=await import("./publication");
 const original="2023-08-23T07:29:55Z";
 expect(publicationDateSubmit(publicationDateInput(original),original)).toBe(original);
 expect(publicationDateInput("")).toBe("");expect(publicationDateSubmit("",original)).toBe("");
});

test("unknown account needs review before automatic target availability",()=>{
 expect(matchesPublicationState([{...unknown,account_ref:"",audience_type:"unknown",access_policy:""}],"ready",members)).toBe(false);
 const changed:PublicationSummary={...unknown,status:"removed",recorded_audiences:[{audience_type:"public"}]};expect(matchesPublicationState([changed],"none",{destination:"youtube",audience_type:"public"})).toBe(false);
});
