'use strict';
// Local design prototype only. No API calls, uploads, authentication or publication.
const $ = s => document.querySelector(s);
const key = 'human-worth-submit-prototype-v1';
const sides = ['human', 'agent'];
const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const emptyDraft = () => ({title:'',summary:'',description:'',category:'',attachments:[],works:{human:[],agent:[]},state:'draft'});
function exampleDraft(){
  const d=emptyDraft();
  Object.assign(d,{title:'A library is closing. What do its readers need to know?',summary:'A neighbourhood library needs a clear, reassuring notice for its readers.',description:'The library closes from 12–16 November and reopens on 17 November. The outside book return stays open. No overdue fees accrue during the closure. Write a notice in no more than 80 words.',category:'Communication'});
  const work=(title,body,source,model='')=>({title,body,source,model,prompt:'',harness:'',url:'',cloud:'no',files:[]});
  d.works.human=[work('A small pause, a warm welcome back.','Your library is taking a short break for repairs, 12–16 November.\n\nYou can still return books through the outside book drop, day or night. There will be no overdue fees during the closure.\n\nWe look forward to welcoming you back on 17 November.','Example community librarian'),work('The essentials, at a glance.','Library closed: 12–16 November\nReopening: 17 November\n\nThe outside book return remains open. No overdue fees will be charged during the closure. Thank you for your patience while we improve your library.','Example human contributor')];
  d.works.agent=[work('A notice for every reader.','Our library will be closed for repairs from 12 to 16 November, reopening on 17 November.\n\nThe outside book return will remain available throughout. No overdue fees will accrue during this period.\n\nThank you for your understanding. We look forward to seeing you again.','Illustrative Agent output','Example model'),work('Plan your next visit.','Planning a visit? The library is closed for repairs, 12–16 November. Please come back on 17 November.\n\nReturning a book? Use the outside book drop at any time.\n\nWorried about fees? No overdue fees accrue while we are closed.','Illustrative Agent output','Example model')];
  return d;
}
let draft=emptyDraft(), positions={human:0,agent:0}, signed=true, dirty=false, busy=false, savedAt='', editing=null, editFiles=[];
try{const saved=JSON.parse(localStorage.getItem(key));if(saved?.draft?.works && Array.isArray(saved.draft.works.human)&&Array.isArray(saved.draft.works.agent)){draft=saved.draft;savedAt=saved.savedAt;draft.attachments.forEach(f=>f.available=false);sides.forEach(side=>draft.works[side].forEach(w=>w.files.forEach(f=>f.available=false)));}}catch{ /* Storage may be unavailable; the editor remains usable. */ }
const locked=()=>draft.state==='pending_review';
function validWork(w,side){return !!(w.title.trim()&&w.source.trim()&&(w.body.trim()||w.files.some(f=>f.available))&&w.files.every(f=>f.available)&&['yes','no'].includes(w.cloud)&&(side!=='agent'||w.model.trim())&&(!w.url||/^https?:\/\/[^\s]+$/i.test(w.url)));}
function chapterComplete(d,side){return d.works[side].length>0&&d.works[side].every(w=>validWork(w,side));}
function healthComplete(data){return !!(data&&data.status==='ok'&&typeof data.revision==='string'&&data.revision.trim().length&&data.deploymentRevision===data.revision&&Array.isArray(data.deployedServices)&&['identity','content','asset','moderation','gateway'].every(s=>data.deployedServices.includes(s)));}
function checks(d=draft){return [
  ['Task title, introduction and instructions',!!(d.title.trim()&&d.summary.trim()&&d.description.trim())],
  ['At least one complete Human work',chapterComplete(d,'human')],
  ['At least one complete Agent work',chapterComplete(d,'agent')],
  ['Work content, sources, models and permissions',sides.every(side=>chapterComplete(d,side))],
  [d.attachments.length?'Shared files selected locally, with permissions chosen':'Shared files — optional, none added',d.attachments.length?d.attachments.every(f=>f.available&&['yes','no'].includes(f.cloud)):null]
];}
function fitTitle(){const el=$('#task-title');el.style.height='auto';el.style.height=el.scrollHeight+'px';}
function changed(){fitTitle();dirty=true;if(draft.state==='rejected')draft.state='draft';$('#feedback').textContent='';renderReview();renderState();fitTitle();}
function renderState(){
  $('#draft-state').textContent=locked()?'In review · simulated':dirty?'Unsaved changes':draft.state==='rejected'?'Returned · simulated':savedAt?'Saved locally':'New draft';
  $('#saved-at').textContent=savedAt?`Saved in this browser at ${savedAt}`:'Not saved in this browser';
  $('#session').textContent=signed?'Demo signed in':'Demo signed out';
  $('#state-banner').hidden=!locked()&&draft.state!=='rejected';
  $('#state-banner').textContent=locked()?'Local simulation: this task is in review. Editing is paused. Nothing has been sent or published. Use Explore states to return to an editable example.':'Local simulation: returned for changes. Example reviewer note: clarify when the library reopens. Edit the draft, then send it for review again.';
  $('#task-fields').disabled=locked()||busy;
  document.querySelectorAll('[data-add]').forEach(b=>b.disabled=locked()||busy);
  $('#save').disabled=locked()||busy;$('#send').disabled=locked()||busy;
}
function renderReview(){
  $('#review-title').textContent=draft.title||'Your task starts here.';
  $('#review-summary').textContent=draft.summary||'Frame the situation, then bring both perspectives.';
  $('#review-counts').textContent=`${draft.works.human.length} Human · ${draft.works.agent.length} Agent · ${draft.attachments.length} shared files`;
  const result=checks();
  $('#checklist').innerHTML=result.map(([label,ok])=>`<li class="${ok===true?'complete':ok===null?'optional':''}">${escapeHTML(label)}<span class="sr-only"> — ${ok===null?'not required':ok?'complete locally':'incomplete'}</span></li>`).join('');
  // Local input readiness is separate from server acknowledgement. A simulated
  // review state must never award a submission-complete checkmark.
  const steps=[result[0][1]&&result[4][1]!==false,sides.every(s=>chapterComplete(draft,s)),false];
  document.querySelectorAll('.margin-index nav a').forEach((a,i)=>{a.classList.toggle('step-complete',steps[i]);a.querySelector('.step-status').textContent=steps[i]?'✓ Complete locally':i===2&&locked()?'Simulated only':'';});
  sides.forEach(side=>{const el=$(`#${side}-readiness`);if(el){el.classList.toggle('complete',chapterComplete(draft,side));el.textContent=chapterComplete(draft,side)?`✓ ${draft.works[side].length} complete in this draft`:'At least one complete work needed';}});
}
function renderStack(side,animate=false){
  const works=draft.works[side];positions[side]=Math.max(0,Math.min(positions[side],works.length-1));const w=works[positions[side]],name=side==='human'?'Human':'Agent';
  $(`#${side}-count`).textContent=`${works.length} ${works.length===1?'work':'works'}`;
  $(`#${side}-stack`).innerHTML=`<div class="work-register ${animate?'turning':''}">${works.length?works.map((work,i)=>`<div class="register-row"><span class="folio-number">${String(i+1).padStart(2,'0')}</span><div><button class="work-title-button" data-edit="${side}" data-index="${i}" ${locked()?'disabled':''} aria-label="Edit ${escapeHTML(work.title)}">${escapeHTML(work.title)}</button><p class="small">${work.files.length?`${work.files.length} locally selected files`:`${work.body.trim().split(/\s+/).filter(Boolean).length} words`} · ${validWork(work,side)?'Complete locally':'Needs attention'}</p></div></div>`).join(''):`<div class="empty-register"><h4>This chapter is empty.</h4><p>Add your ${name.toLowerCase()} work below.<br>A draft can wait; review needs both perspectives.</p></div>`}</div>`;
  document.querySelectorAll(`[data-edit="${side}"]`).forEach(b=>b.addEventListener('click',()=>{positions[side]=Number(b.dataset.index);openWork(side,positions[side]);}));
}
function renderFiles(){
  $('#task-file-list').innerHTML=draft.attachments.map((f,i)=>`<li>${escapeHTML(f.name)} ${f.available?'':'— reselect file after reopening'} <select aria-label="Cloud-use permission for ${escapeHTML(f.name)}" data-permission="${i}"><option value="">Choose permission</option><option value="yes" ${f.cloud==='yes'?'selected':''}>Allow cloud use</option><option value="no" ${f.cloud==='no'?'selected':''}>Do not allow cloud use</option></select><button data-remove-file="${i}" type="button">Remove</button></li>`).join('');
  document.querySelectorAll('[data-permission]').forEach(el=>el.onchange=()=>{draft.attachments[Number(el.dataset.permission)].cloud=el.value;changed();});
  document.querySelectorAll('[data-remove-file]').forEach(el=>el.onclick=()=>{draft.attachments.splice(Number(el.dataset.removeFile),1);renderFiles();changed();});
}
function renderAll(){for(const name of ['title','summary','description','category'])$(`#task-${name}`).value=draft[name];renderFiles();sides.forEach(s=>renderStack(s));renderReview();renderState();fitTitle();}
for(const name of ['title','summary','description','category'])$(`#task-${name}`).addEventListener('input',e=>{draft[name]=e.target.value;changed();});
$('#task-files').onchange=e=>{for(const f of e.target.files){const old=draft.attachments.find(x=>x.name===f.name&&!x.available);if(old)old.available=true;else draft.attachments.push({name:f.name,available:true,cloud:''});}e.target.value='';renderFiles();changed();};
document.querySelectorAll('[data-add]').forEach(b=>b.onclick=()=>openWork(b.dataset.add));
function renderEditFiles(){
  $('#work-file-list').innerHTML=editFiles.map((f,i)=>`<li>${escapeHTML(f.name)}${f.available?'':' — reselect file'} <button type="button" data-remove-edit="${i}">Remove</button></li>`).join('');
  document.querySelectorAll('[data-remove-edit]').forEach(b=>b.onclick=()=>{editFiles.splice(Number(b.dataset.removeEdit),1);renderEditFiles();});
}
function openWork(side,index=null){
  if(locked()||busy)return;editing={side,index};const form=$('#work-form');form.reset();const w=index===null?null:draft.works[side][index];
  $('#work-kind').textContent=side==='human'?'Human work':'Agent work';$('#work-dialog-title').textContent=w?'A closer look.':'Add a work.';
  for(const name of ['title','body','source','url','model','prompt','harness'])form.elements[name].value=w?.[name]||'';
  if(w?.cloud)form.elements.cloud.value=w.cloud;
  editFiles=w?structuredClone(w.files):[];renderEditFiles();$('#agent-fields').hidden=side!=='agent';form.elements.model.required=side==='agent';$('#remove-work').hidden=!w;$('#work-error').textContent='';$('#work-dialog').showModal();
}
$('#work-form').elements.files.onchange=e=>{for(const f of e.target.files){const old=editFiles.find(x=>x.name===f.name&&!x.available);if(old)old.available=true;else editFiles.push({name:f.name,available:true});}e.target.value='';renderEditFiles();};
$('#work-form').onsubmit=e=>{
  e.preventDefault();const form=e.target;const w={files:structuredClone(editFiles)};
  for(const name of ['title','body','source','url','model','prompt','harness','cloud'])w[name]=form.elements[name].value.trim();
  if(!validWork(w,editing.side)){$('#work-error').textContent='Add a title, actual text or available file, source, model if applicable, a valid supplementary link and a cloud-use choice.';return;}
  if(w.url&&!/^https?:\/\//i.test(w.url)){$('#work-error').textContent='Use an http:// or https:// supplementary link.';return;}
  const list=draft.works[editing.side];if(editing.index===null){list.push(w);positions[editing.side]=list.length-1;}else list[editing.index]=w;
  renderStack(editing.side,true);changed();$('#work-dialog').close();
};
$('#remove-work').onclick=()=>{if(!confirm('Remove this work from the local draft?'))return;draft.works[editing.side].splice(editing.index,1);renderStack(editing.side,true);changed();$('#work-dialog').close();};
document.querySelectorAll('[data-close]').forEach(b=>b.onclick=()=>{if(b.closest('dialog').id==='work-dialog'&&!confirm('Close the editor? Changes not kept in the draft will be discarded.'))return;b.closest('dialog').close();});
$('#work-dialog').addEventListener('cancel',e=>{if(!confirm('Close the editor and discard changes not kept in the draft?'))e.preventDefault();});
function showPreview(){
  $('#preview-content').innerHTML=`<h2 id="preview-title">${escapeHTML(draft.title||'Untitled task')}</h2><p>${escapeHTML(draft.summary)}</p><div class="preview-work"><h3>The shared brief</h3><p>${escapeHTML(draft.description)}</p><p class="small">${escapeHTML(draft.attachments.map(f=>f.name).join(' · '))}</p></div>`+sides.map(side=>draft.works[side].map(w=>`<article class="preview-work"><p class="eyebrow">${side} work</p><h3>${escapeHTML(w.title)}</h3><p class="small">${escapeHTML(w.source)}${side==='agent'?' / '+escapeHTML(w.model):''}</p><p class="full-body">${escapeHTML(w.body)}</p><p class="small">${escapeHTML(w.files.map(f=>f.name).join(' · '))}</p>${w.url?`<p>Supplementary link: ${escapeHTML(w.url)}</p>`:''}<p class="small">Cloud use: ${w.cloud==='yes'?'allowed':'not allowed'}</p></article>`).join('')).join('');$('#preview-dialog').showModal();
}
$('#preview').onclick=showPreview;
$('#session').onclick=()=>{if(busy)return;signed=!signed;renderState();$('#feedback').textContent=signed?'Demo session enabled. No real sign-in took place.':'Demo signed out. Your draft remains here.';};
async function save(send=false){
  if(busy||locked())return;
  if(!signed){$('#feedback').textContent='Enable the demo session at the top of the page, then try again. Your input is retained.';return;}
  if(!draft.title.trim()){$('#feedback').textContent='Give the task a title before saving a draft.';$('#task-title').focus();return;}
  if(send&&checks().some(([,ok])=>ok===false)){$('#feedback').textContent='Before review, complete the unchecked items above. You can still save this draft.';return;}
  busy=true;renderState();$('#feedback').textContent=send?'Simulating a review request…':'Saving this draft in your browser…';
  const snapshot=structuredClone(draft);const failure=$('#fail').checked;$('#fail').checked=false;
  // ponytail: a local delay models pending/error feedback; no backend transport exists here.
  await new Promise(r=>setTimeout(r,550));
  if(failure){busy=false;renderState();$('#feedback').textContent='Simulated failure. Your work is retained; try again.';return;}
  if(send)snapshot.state='pending_review';const time=new Date().toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  try{localStorage.setItem(key,JSON.stringify({draft:snapshot,savedAt:time}));savedAt=time;if(send)draft.state='pending_review';dirty=JSON.stringify(draft)!==JSON.stringify(snapshot);$('#feedback').textContent=send?'Local review simulation complete. Nothing was submitted or published.':'Draft saved in this browser only. Selected files must be reselected after reopening.';}catch{$('#feedback').textContent='Browser storage is unavailable or full. Your draft remains in this tab; it has not been saved.';}
  busy=false;renderAll();
}
$('#save').onclick=()=>save();$('#send').onclick=()=>save(true);
document.querySelectorAll('[data-state]').forEach(b=>b.onclick=()=>{if(busy)return;if(dirty&&!confirm('Replace unsaved changes with this example state?'))return;draft=b.dataset.state==='empty'?emptyDraft():exampleDraft();if(b.dataset.state==='missing')draft.works.agent=[];if(b.dataset.state==='rejected')draft.state='rejected';if(b.dataset.state==='pending')draft.state='pending_review';positions={human:0,agent:0};savedAt='';dirty=true;$('#feedback').textContent='Example state loaded. Your previously saved browser draft is unchanged.';$('.prototype-bar details').open=false;renderAll();});
window.addEventListener('beforeunload',e=>{if(dirty){e.preventDefault();e.returnValue='';}});
renderAll();


window.addEventListener('resize',fitTitle);

// Read-only same-origin probe. Health is never authorization or proof a write succeeded.
async function checkConnection(){
  const button=$('#api-retry'),status=$('#api-status');button.disabled=true;status.textContent='Checking API availability…';
  try{
    const response=await fetch('/api/health',{credentials:'omit',cache:'no-store',signal:AbortSignal.timeout(5000)});
    if(!response.ok){status.textContent=response.status===404?'This preview has no API route. Continue with the local draft; live submission is not connected.':`API unavailable (HTTP ${response.status}). Your local draft is retained; live submission is not connected.`;return;}
    if(!response.headers.get('content-type')?.includes('application/json'))throw Error('non-json');
    const data=await response.json();
    status.textContent=healthComplete(data)?'API health is available. This design prototype still saves locally; authentication, uploads and submission are not connected.':'API health data is incomplete or the required modules are not confirmed. Local editing remains available; no server completion is assumed.';
  }catch{status.textContent='API connection could not be verified. Continue editing locally; nothing will be sent or published.';}
  finally{button.disabled=false;}
}
$('#api-retry').onclick=checkConnection;
checkConnection();
