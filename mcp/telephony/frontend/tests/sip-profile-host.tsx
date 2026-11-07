import {createRoot} from "react-dom/client";
import SIPAudioProfileControls from "../../ui/SIPAudioProfileControls";
const request=async(body:Record<string,unknown>)=>{const r=await fetch('/profile',{method:'POST',body:JSON.stringify(body)});const data=await r.json();if(!r.ok)throw new Error(data.error);return data};
createRoot(document.getElementById('root')!).render(<SIPAudioProfileControls target={{direction:'outbound',carrier_connection_id:10,phone_number:'+12025550100'}} request={request}/>);
