import React from 'react';
import {createRoot} from 'react-dom/client';
import BillingPanel from '../../ui/BillingPanel';
(window as any).__aptevaAppEvents={subscribe:()=>()=>{}};
function Harness(){const [install,setInstall]=React.useState(23);return <><button id="switch-install" onClick={()=>setInstall(x=>x+1)}>Switch test install</button><div style={{height:'95vh'}}><BillingPanel appName="billing" projectId="qa-project" installId={install}/></div></>}
createRoot(document.getElementById('root')!).render(<Harness/>);
