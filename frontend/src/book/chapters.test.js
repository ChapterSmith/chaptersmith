import test from 'node:test';
import assert from 'node:assert/strict';
import { parseHTML } from 'linkedom';
import { createBookController } from './controller.js';
import { mountChapters } from './chapters.js';
const tick=()=>new Promise(r=>setImmediate(r));
const view=()=>({project:{id:'p',export:{normalize:false}},revision:7,chapterRows:[{id:'c1',title:'First',startUs:0,durationUs:10000000},{id:'c2',title:'Second',startUs:10000000,durationUs:10000000}]});
function fixture(reply) {
 const calls=[];let seq=0, event;
 const controller=createBookController({api:{onOperation:fn=>{event=fn;return ()=>{}},call:async(method,...args)=>{calls.push([method,...args]);return reply?reply(method,...args):view()}},makeRequestId:()=>String(++seq)});
 return {controller,calls,event:status=>event(status)};
}
test('suggestions require matching request/revision and edits invalidate them and preview',async()=>{
 let clears=0;
 const f=fixture(async(method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:args[1],proposals:[{chapterId:'c2',oldStartUs:10000000,newStartUs:13000000}]}:{...view(),revision:method==='ApplyEdit'?8:7});
 f.controller.attachPlayer({clear:()=>clears++,dispose(){},load(){}});
 await f.controller.open('p');await f.controller.suggestBoundaries();
 assert.equal(f.controller.getState().suggestions.proposals.length,1);
 await f.controller.edit({kind:'rename',chapterId:'c2',title:'Next'});
 assert.equal(f.controller.getState().suggestions,null);assert.ok(clears>=2);
 await assert.rejects(f.controller.applyBoundaries([{chapterId:'c2',oldStartUs:10000000,newStartUs:13000000}]),/suggest|revision/i);
});
test('cancelled or mismatched suggestion reply is never offered for acceptance',async()=>{
 let resolve;const pending=new Promise(r=>resolve=r);
 const f=fixture(method=>method==='SuggestBoundaries'?pending:view());await f.controller.open('p');
 const run=f.controller.suggestBoundaries();await tick();await f.controller.cancel();
 resolve({requestId:'2',projectId:'p',revision:7,proposals:[]});await assert.rejects(run,/stale|cancel/i);
 assert.equal(f.controller.getState().suggestions,null);
 const g=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:'wrong',projectId:'p',revision:7,proposals:[]}:view());await g.controller.open('p');await assert.rejects(g.controller.suggestBoundaries(),/stale/i);
});
test('chapter buttons and drag dispatch same move; first boundary stays disabled; silence suggestions never normalize or apply',async()=>{
 const f=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:7,proposals:[{chapterId:'c2',oldStartUs:10000000,newStartUs:13000000}]}:view());await f.controller.open('p');
 const {document,window}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);
 const click=s=>root.querySelector(s).click();
 assert.equal(root.querySelector('[data-chapter-id="c1"] [data-action="boundary"]').disabled,true);
 click('[data-chapter-id="c2"] [data-action="up"]');await tick();
 const move=f.calls.find(c=>c[0]==='ApplyEdit');assert.deepEqual(move.slice(1),[7,{kind:'move',chapterId:'c2',destination:0}]);
 const drag=new window.Event('dragstart',{bubbles:true});let dragged='';drag.dataTransfer={setData:(_,v)=>dragged=v};root.querySelector('[data-chapter-id="c2"]').dispatchEvent(drag);
 const drop=new window.Event('drop',{bubbles:true,cancelable:true});drop.dataTransfer={getData:()=>dragged};root.querySelector('[data-chapter-id="c1"]').dispatchEvent(drop);await tick();assert.deepEqual(f.calls.filter(c=>c[0]==='ApplyEdit')[1],move);
 click('[data-chapter-id="c1"] [data-action="down"]');await tick();assert.deepEqual(f.calls.filter(c=>c[0]==='ApplyEdit')[2].slice(1),[7,{kind:'move',chapterId:'c1',destination:1}]);
 click('[data-action="suggest"]');await tick();
 assert.match(root.textContent,/\+00:00:03\.000000/);
 assert.equal(f.calls.filter(c=>c[0]==='ApplyBoundaries').length,0);
 assert.equal(f.controller.getState().view.project.export.normalize,false);
 assert.deepEqual(f.calls.find(c=>c[0]==='SuggestBoundaries').slice(2),[7,{windowUs:5000000,thresholdDb:-40,minSilenceUs:500000}]);
 dispose();f.controller.dispose();
});

test('manual boundary and silence controls reject empty/crossing/backward-remove times; acceptance sends only selected proposal', async()=>{
 const f=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:7,proposals:[{chapterId:'c2',oldStartUs:10000000,newStartUs:8000000}]}:view());await f.controller.open('p');
 const {document,window}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);
 const row=root.querySelector('[data-chapter-id="c2"]'), start=row.querySelector('[name="boundary-start"]'), mode=row.querySelector('[name="boundary-mode"]'), apply=row.querySelector('[data-action="boundary"]');
 for (const value of ['00:00:00.000000','00:00:20.000000','bad']) {start.value=value;start.dispatchEvent(new window.Event('input'));assert.equal(apply.disabled,true);}
 start.value='00:00:08.000000';mode.querySelector('[value="remove"]').selected=true;mode.dispatchEvent(new window.Event('change'));assert.equal(apply.disabled,true);
 mode.querySelector('[value="transfer"]').selected=true;mode.dispatchEvent(new window.Event('change'));assert.equal(apply.disabled,false);
 apply.click();await tick();assert.deepEqual(f.calls.find(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'boundary',chapterId:'c2',newStartUs:8000000,mode:'transfer'}]);
 root.querySelector('[data-action="suggest"]').click();await tick();
 assert.match(root.textContent,/−00:00:02.000000/);const accept=root.querySelector('[data-action="accept-boundaries"]');assert.equal(accept.disabled,true);
 const check=root.querySelector('[type="checkbox"]');check.checked=true;check.dispatchEvent(new window.Event('change'));assert.equal(accept.disabled,false);accept.click();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='ApplyBoundaries').slice(1),[7,[{chapterId:'c2',oldStartUs:10000000,newStartUs:8000000}]]);
 assert.equal(f.controller.getState().suggestions,null);dispose();f.controller.dispose();
});
test('boundary preview uses original suggestion revision and releases stale media after an edit',async()=>{
 let resolve;const pending=new Promise(r=>resolve=r);const loaded=[];
 const proposal={chapterId:'c2',oldStartUs:10000000,newStartUs:13000000};
 const f=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:7,proposals:[proposal]}:method==='PreviewBoundary'?pending:method==='ApplyEdit'?{...view(),revision:8}:view());
 f.controller.attachPlayer({clear(){},dispose(){},load:r=>loaded.push(r)});await f.controller.open('p');await f.controller.suggestBoundaries();
 const preview=f.controller.previewBoundary(proposal);await tick();const edit=f.controller.edit({kind:'rename',chapterId:'c2',title:'Changed'});
 resolve({requestId:'3',projectId:'p',revision:7,mediaId:'token',url:'/_media/token',durationUs:13000000,originUs:5000000,oldMarkerUs:5000000,newMarkerUs:8000000});await Promise.all([preview,edit]);
 assert.equal(loaded.length,0);assert.deepEqual(f.calls.filter(c=>c[0]==='ReleaseMedia'),[['ReleaseMedia','token']]);
 await assert.rejects(f.controller.previewBoundary(proposal),/suggest|revision/i);
});
test('empty suggestions report no changes and rejecting proposals preserves the project',async()=>{
 let proposals=[];const f=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:7,proposals}:view());await f.controller.open('p');
 const {document}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);const old=f.controller.getState().view;
 root.querySelector('[data-action="suggest"]').click();await tick();assert.match(root.textContent,/No suggestions/);assert.equal(f.controller.getState().view,old);
 proposals=[{chapterId:'c2',oldStartUs:10000000,newStartUs:13000000}];root.querySelector('[data-action="suggest"]').click();await tick();root.querySelector('[data-action="reject-boundaries"]').click();await tick();
 assert.equal(f.controller.getState().view,old);assert.equal(f.controller.getState().suggestions,null);assert.equal(f.calls.some(c=>c[0]==='ApplyBoundaries'),false);dispose();f.controller.dispose();
});
test('a single chapter can be searched for silence and answers inside its own editor',async()=>{
 const proposal={chapterId:'c2',oldStartUs:10000000,newStartUs:10400000};
 let found=[proposal];
 const f=fixture((method,...args)=>method==='SuggestBoundaries'?{requestId:args[0],projectId:'p',revision:7,proposals:found}:view());await f.controller.open('p');
 const {document}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);
 assert.equal(root.querySelector('[data-chapter-id="c1"] [data-action="suggest-chapter"]').disabled,true);
 const row=()=>root.querySelector('[data-chapter-id="c2"]');
 row().querySelector('[data-action="suggest-chapter"]').click();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='SuggestBoundaries').slice(2),[7,{windowUs:5000000,thresholdDb:-40,minSilenceUs:500000,chapterId:'c2'}]);
 assert.match(row().querySelector('.chapter-suggestion').textContent,/\+00:00:00\.400000/);
 assert.equal(root.querySelector('.boundary-proposals').textContent,'');
 row().querySelector('[data-action="accept-boundaries"]').click();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='ApplyBoundaries').slice(1),[7,[proposal]]);
 found=[];row().querySelector('[data-action="suggest-chapter"]').click();await tick();
 assert.match(row().querySelector('.chapter-suggestion').textContent,/No silence found/);
 dispose();f.controller.dispose();
});
test('chapter breaks found from pauses start selected and become one split edit; rows can split and merge by hand',async()=>{
 const breaks=[{atUs:4000000,silenceUs:2500000},{atUs:15000000,silenceUs:3000000}];
 const f=fixture((method,...args)=>method==='SuggestSplits'?{requestId:args[0],projectId:'p',revision:7,proposals:breaks}:method==='PreviewPosition'?{requestId:args[0],projectId:'p',revision:7,mediaId:'m',url:'/_media/m'}:method==='ReleaseMedia'?null:view());await f.controller.open('p');
 const {document,window}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);
 root.querySelector('[data-action="suggest-splits"]').click();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='SuggestSplits').slice(2),[7,{minSilenceUs:2000000,thresholdDb:-40,minChapterUs:300000000}]);
 const accept=()=>root.querySelector('[data-action="accept-splits"]');
 assert.equal(accept().textContent,'Create 2 chapters');assert.match(root.textContent,/New chapter at 00:00:04 · 2\.5 s pause/);
 const check=root.querySelector('.boundary-proposal input');check.checked=false;check.dispatchEvent(new window.Event('change'));
 assert.equal(accept().textContent,'Create 1 chapter');
 root.querySelector('[data-action="preview-split"]').click();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='PreviewPosition').slice(2),[7,4000000]);
 accept().click();await tick();
 assert.deepEqual(f.calls.findLast(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'split',positionsUs:[15000000]}]);
 const row=root.querySelector('[data-chapter-id="c2"]'),at=row.querySelector('[name="split-at"]'),split=row.querySelector('[data-action="split"]');
 assert.equal(at.value,'00:00:15.000000');assert.equal(split.disabled,false);
 for(const value of ['00:00:10.000000','00:00:20.000000','bad']){at.value=value;at.dispatchEvent(new window.Event('input'));assert.equal(split.disabled,true);}
 at.value='00:00:12.000000';at.dispatchEvent(new window.Event('input'));split.click();await tick();
 assert.deepEqual(f.calls.findLast(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'split',positionsUs:[12000000]}]);
 assert.equal(root.querySelector('[data-chapter-id="c1"] [data-action="merge"]').disabled,true);
 root.querySelector('[data-chapter-id="c2"] [data-action="merge"]').click();await tick();
 assert.deepEqual(f.calls.findLast(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'merge',chapterId:'c2'}]);
 dispose();f.controller.dispose();
});
test('one pause length is set between chapters, replacing measured silence unless told only to add',async()=>{
 const measured=[{chapterId:'c2',beforeUs:1000000,afterUs:500000}];
 const f=fixture((method,...args)=>method==='MeasureChapterPauses'?{requestId:args[0],projectId:'p',revision:7,pauses:measured}:view());await f.controller.open('p');
 const {document,window}=parseHTML('<main></main>');const root=document.querySelector('main');const dispose=mountChapters(root,f.controller);
 const length=root.querySelector('[name="pause-length"]'),replace=root.querySelector('[name="pause-replace"]'),set=root.querySelector('[data-action="set-pauses"]');
 assert.equal(length.value,'00:00:02.000000');assert.equal(!!replace.checked,true);assert.equal(set.disabled,false);
 set.click();await tick();await tick();
 assert.deepEqual(f.calls.find(c=>c[0]==='MeasureChapterPauses').slice(2),[7,{thresholdDb:-40,windowUs:5000000}]);
 assert.deepEqual(f.calls.findLast(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'pauses',pauses:measured,durationUs:2000000}]);
 assert.match(root.textContent,/Pause set at 1 chapter start; existing silence replaced at 1\./);
 const before=f.calls.filter(c=>c[0]==='MeasureChapterPauses').length;
 replace.checked=false;length.value='00:00:03.500000';set.click();await tick();await tick();
 assert.equal(f.calls.filter(c=>c[0]==='MeasureChapterPauses').length,before);
 assert.deepEqual(f.calls.findLast(c=>c[0]==='ApplyEdit').slice(1),[7,{kind:'pauses',pauses:[],durationUs:3500000}]);
 const edits=f.calls.filter(c=>c[0]==='ApplyEdit').length;
 length.value='00:00:00.000000';set.click();await tick();await tick();
 assert.equal(f.calls.filter(c=>c[0]==='ApplyEdit').length,edits);assert.match(root.querySelector('[role="alert"]').textContent,/longer than zero/);
 dispose();f.controller.dispose();
});
