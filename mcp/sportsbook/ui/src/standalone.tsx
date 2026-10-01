import {createRoot} from 'react-dom/client';
import Sportsbook from './Sportsbook';
const params = new URLSearchParams(location.search);
const path = location.pathname;
const prefix = path.includes('/ui/') ? path.slice(0,path.indexOf('/ui/')) : '';
createRoot(document.getElementById('root')!).render(<Sportsbook projectId={params.get('project_id')||''} installId={Number(params.get('install_id')||0)} apiBase={prefix}/>);
