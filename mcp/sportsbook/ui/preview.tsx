// Mount the shipped ESM panel exactly as the dashboard does.
import {createRoot} from 'react-dom/client';
import SportsbookPanel from './SportsbookPanel.mjs';
const params=new URLSearchParams(location.search);
document.documentElement.dataset.theme=params.get('theme')==='clean'?'clean':'terminal';
document.documentElement.dataset.mode=params.get('mode')==='light'?'light':'dark';
createRoot(document.getElementById('root')!).render(<SportsbookPanel
 appName="sportsbook" projectId={params.get('project_id')||'sportsbook-preview'}
 installId={Number(params.get('install_id')||0)}/>);

export default SportsbookPanel;
