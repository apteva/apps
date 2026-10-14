export type Audience = {
 audience_type?: "public" | "restricted" | "private" | "unknown";
 access_policy?: string; group_scope?: "all_eligible" | "selected" | "unknown";
 group_ids?: string[]; group_labels?: Record<string,string>;
 publisher_ref?: { app: string; install_id?: number; record_id: string } | null;
};
export type PublicationScope = { destination?: string; account_ref?: string; audience_type?: string; access_policy?: string; group_scope?: string; group_id?: string };
export type PublicationSummary = Audience & { id?: string; destination: string; account_ref?: string; status: string; historically_verified?: boolean; recorded_audiences?: Audience[]; verified_audiences?: Audience[] };
export function audienceLabel(a: Audience): string {
 if(a.audience_type === "public") return "Public";
 if(a.audience_type === "private") return "Private";
 if(a.audience_type !== "restricted") return "Audience unknown";
 if(a.group_scope === "selected") return "Selected groups";
 if(a.group_scope === "all_eligible") return a.access_policy === "membership" ? "All members" : "All eligible groups";
 return a.access_policy === "membership" ? "Members" : a.access_policy ? `Restricted · ${a.access_policy}` : "Restricted";
}
export function audienceMatches(a: Audience, f: PublicationScope, uncertain = false): boolean {
 const equal=(actual:string|undefined,target:string|undefined,unknown:string)=> !target || (actual || unknown)===target || (uncertain && (actual || unknown)===unknown);
 return equal(a.audience_type,f.audience_type,"unknown") && equal(a.access_policy,f.access_policy,"") && equal(a.group_scope,f.group_scope,"unknown") && (!f.group_id || a.group_scope === "all_eligible" || (a.group_scope === "selected" && (a.group_ids || []).includes(f.group_id)) || (uncertain && (!a.group_scope || a.group_scope === "unknown")));
}
export function identityMatches(p: PublicationSummary,f: PublicationScope):boolean{return (!f.destination || p.destination.toLowerCase()===f.destination.toLowerCase()) && (!f.account_ref || p.account_ref===f.account_ref);}
export function publicationMatches(p: PublicationSummary,f: PublicationScope):boolean{return identityMatches(p,f) && audienceMatches(p,f);}
export function verifiedHere(p: PublicationSummary,f: PublicationScope,uncertain=false):boolean{return identityMatches(p,f) && ((p.status === "verified_published" && audienceMatches(p,f,uncertain)) || (p.verified_audiences || []).some(a=>audienceMatches(a,f,uncertain)) || (p.historically_verified === true && !p.verified_audiences?.length && !f.audience_type && !f.access_policy && !f.group_scope && !f.group_id));}
export function matchesPublicationState(posts: PublicationSummary[],state:string,f:PublicationScope):boolean{
 const matching=posts.filter(p=>publicationMatches(p,f));
 const scoped=Object.values(f).some(Boolean);
 if(state==="any") return !scoped || matching.length>0;
 if(state==="none") return matching.length===0 && !posts.some(p=>identityMatches(p,f) && ((p.recorded_audiences || []).some(a=>audienceMatches(a,f)) || verifiedHere(p,f)));
 if(state==="not_verified") return !posts.some(p=>verifiedHere(p,f));
 if(state==="verified") return posts.some(p=>verifiedHere(p,f));
 if(state==="reported") return matching.some(p=>p.status==="provider_reported_published");
 if(state==="scheduled") return matching.some(p=>["planned","scheduled","submitted"].includes(p.status));
 if(state==="failed") return matching.some(p=>p.status==="failed");
 if(state==="ready") return !posts.some(p=>identityMatches(p,p.account_ref?f:{...f,account_ref:""}) && ((!["failed","removed"].includes(p.status) && audienceMatches(p,f,true)) || verifiedHere(p,p.account_ref?f:{...f,account_ref:""},true)));
 return true;
}

export function publicationDateInput(value?:string):string{if(!value)return "";const date=new Date(value);return Number.isNaN(date.valueOf())?"":new Date(date.valueOf()-date.getTimezoneOffset()*60000).toISOString().slice(0,16);}
export function publicationDateSubmit(value:string,original?:string):string{if(!value)return "";return original && value===publicationDateInput(original)?original:new Date(value).toISOString();}
