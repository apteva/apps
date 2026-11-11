package testbrowser

// locatorScript resolves a strict locator in the current document. The generated
// token is used only by this serialized command and is refreshed before input.
const locatorScript = `(function(l){
 const norm=s=>String(s||'').replace(/\s+/g,' ').trim();
 const visible=e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>0&&r.height>0&&s.display!=='none'&&s.visibility!=='hidden'&&s.opacity!=='0'};
 const role=e=>e.getAttribute('role')||({BUTTON:'button',A:e.hasAttribute('href')?'link':'',TEXTAREA:'textbox',SELECT:e.multiple?'listbox':'combobox',IMG:'img',H1:'heading',H2:'heading',H3:'heading',H4:'heading',OPTION:'option'}[e.tagName])||(e.tagName==='INPUT'?({checkbox:'checkbox',radio:'radio',button:'button',submit:'button',range:'slider',number:'spinbutton'}[e.type]||'textbox'):'');
 const name=e=>norm(e.getAttribute('aria-label')||(e.getAttribute('aria-labelledby')||'').split(/\s+/).map(id=>document.getElementById(id)?.textContent||'').join(' ')||(e.labels?.length?Array.from(e.labels).map(x=>x.textContent).join(' '):'')||e.getAttribute('alt')||((e.tagName==='INPUT'&&['submit','button'].includes(e.type))?e.value:'')||e.textContent);
 const nodes=Array.from(document.querySelectorAll('body *'));
 if(nodes.length>20000)return {error:'document exceeds test locator limit'};
 const matches=nodes.filter(e=>{
  if(l.test_id)return e.getAttribute('data-testid')===l.test_id;
  if(l.role)return role(e)===l.role&&(!l.name||name(e)===norm(l.name));
  if(l.text){if(norm(e.textContent)!==norm(l.text))return false;return !Array.from(e.children).some(c=>norm(c.textContent)===norm(l.text))}
  return false;
 });
 const out={count:matches.length,visible:false,enabled:false,text:'',sensitive:false};
 if(matches.length!==1)return out;
 const e=matches[0];
 out.visible=visible(e);
 out.enabled=!e.disabled&&e.getAttribute('aria-disabled')!=='true'&&!e.closest('[inert]');
 out.sensitive=e.matches('input[type=password],[data-test-sensitive]')||!!e.closest('[data-test-sensitive]');
 if(!out.sensitive){
  out.text=norm(e.textContent).slice(0,4096);
  if('value'in e&&typeof e.value==='string')out.value=e.value.slice(0,4096);
 }
 if('checked'in e)out.checked=!!e.checked;
 else if(e.hasAttribute('aria-checked'))out.checked=e.getAttribute('aria-checked')==='true';
 document.querySelectorAll('[data-apteva-test-target]').forEach(x=>x.removeAttribute('data-apteva-test-target'));
 e.setAttribute('data-apteva-test-target','active');
 return out;
})`
