import { createRoot } from 'react-dom/client';
import { useState } from 'react';
import StoragePanel from '/api/apps/storage/ui/StoragePanel.mjs';
import FileCard from '/api/apps/storage/ui/FileCard.mjs';
import {uploadResumable} from '../../ui/uploadResumable';
const subscribers = new Set<(ev: unknown)=>void>();
Object.assign(window, {__aptevaAppEvents:{subscribe:(_a: string,_p: string,handler:(ev:unknown)=>void)=>{subscribers.add(handler);return()=>subscribers.delete(handler)}},fireStorageEvent:(event:unknown)=>subscribers.forEach(fn=>fn(event)),uploadResumable});
function Harness(){const [id,setID]=useState(1);Object.assign(window,{selectCard:setID});return location.search.includes('card')?<FileCard file_id={id} projectId="p1" installId={42}/>:<StoragePanel appName="storage" projectId="p1" installId={42}/>}
createRoot(document.getElementById('root')!).render(<Harness/>);
