import {createRoot} from "react-dom/client";
import OutboundTransportDialog from "../../ui/OutboundTransportDialog";
const provider=new URL(location.href).searchParams.get("provider")||"telnyx";
const requests:Array<Record<string,unknown>>=[];
Object.assign(window,{setupRequests:requests});
createRoot(document.getElementById("root")!).render(<OutboundTransportDialog provider={provider} connection={10} number="+33123456780" request={async body=>{
 requests.push(body);
 if(body.transport||body.inherit){document.getElementById("result")!.textContent=JSON.stringify(body);return {outbound_transport:{transport:"sip_direct",scope:"number",credentials_configured:true}};}
 return {outbound_transport:{transport:"programmable_websocket",scope:"default",credentials_configured:false},setup_options:{automatic_supported:true,profiles:[{id:"profile-1",name:"France",enabled:true,destinations:["FR"],daily_spend_limit_enabled:true},{id:"profile-2",name:"Spain",enabled:true,destinations:["ES"],daily_spend_limit_enabled:true}]}};
}} onClose={()=>document.getElementById("closed")!.textContent="closed"}/>);
