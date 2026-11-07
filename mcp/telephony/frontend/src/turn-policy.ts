export interface TURNPolicy {source:"existing"|"app";transport:"all"|"udp"|"tcp"|"tls";ice_transport_policy:"all"|"relay";tls_port?:number;ttl_seconds:number;allow_user_overrides?:boolean}
export interface TURNIdentity {issuer_app:string;issuer_install_id:string;subject_type:string;subject_id:string;organization_id:string}
export interface TURNPolicyView {policy:TURNPolicy;effective:TURNPolicy;override:boolean;allow_user_overrides:boolean;turn_bound:boolean;scope:"project"|"adviser";advisers?:TURNIdentity[]}
export type TURNPolicyRequest=(args:Record<string,unknown>)=>Promise<TURNPolicyView>;
export function validateTURNPolicy(p:TURNPolicy){
 if(!["existing","app"].includes(p.source)||!["all","udp","tcp","tls"].includes(p.transport)||!["all","relay"].includes(p.ice_transport_policy)||!Number.isInteger(p.ttl_seconds)||p.ttl_seconds<60||p.ttl_seconds>86400||p.tls_port!==undefined&&(!Number.isInteger(p.tls_port)||p.tls_port<1||p.tls_port>65535||p.transport!=="tls"))throw new Error("Invalid TURN settings");return {...p};
}

/** Project override permission is never writable through an adviser policy. */
export function turnPolicyUpdate(policy:TURNPolicy,scope:TURNPolicyView["scope"]):TURNPolicy {
 const value=validateTURNPolicy(policy);
 if(scope==="adviser")delete value.allow_user_overrides;
 return value;
}
