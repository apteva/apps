import {createRoot} from 'react-dom/client';
import CodePanel from '../CodePanel';
const listeners=new Set<(event:unknown)=>void>();
Object.assign(window,{
 __aptevaAppEvents:{subscribe(_app:string,_project:string,listener:(event:unknown)=>void){listeners.add(listener);return()=>listeners.delete(listener)}},
 emitCodeEvent(topic:string,data:unknown){for(const listener of listeners)listener({app:'code',project_id:'test',topic,data,seq:Date.now()})}
});
createRoot(document.getElementById('root')!).render(<CodePanel appName="code" projectId="test" installId={1}/>);
