import test from 'node:test';
import assert from 'node:assert/strict';
import { exportDefaults, exportExplanation, mountExport } from './export.js';
import { createBookController } from './controller.js';
import { mountBookEditor } from './view.js';
import { installAudioStub } from './audio-test-helper.js';
import { parseHTML } from 'linkedom';
const view = (revision=7) => ({revision,project:{id:'book',metadata:{title:'Book'},export:{...exportDefaults}},chapterRows:[],durationUs:0});
function fixture(start, recoveryStatus = 'succeeded') {
 let event, log, revision=7, seq=0; const calls=[];
 const controller=createBookController({makeRequestId:()=>`r${++seq}`,api:{onOperation:fn=>(event=fn,()=>{}),onLog:fn=>(log=fn,()=>{}),call:async(method,...args)=>{
 calls.push([method,...args]);if(method==='OpenProject')return view();if(method==='StartExport')return start(args,event);if(method==='GetOperation')return {requestId:args[0],projectId:'book',revision:7,kind:'export',taskId:1,status:recoveryStatus,paths:['book.m4b'],normalizationReport:'Preserve fallback: edited timeline'};if(method==='ApplyEdit'||method==='SaveProject')return view(++revision);if(method==='SelectExportTarget')return 'book.m4b'; }} });
 return {controller,calls,event:s=>event(s),log:s=>log?.(s)};
}
test('defaults and explanation cover every format/mode without disabling valid combinations',()=>{
 assert.deepEqual(exportDefaults,{profile:'standard',channels:'auto',normalize:false,outputMode:'book',format:'m4b'});
 for(const format of ['m4b','m4a','flac','wav'])for(const outputMode of ['book','chapters','both']) {
  const text=exportExplanation({...exportDefaults,format,outputMode});
  if(['flac','wav'].includes(format))assert.match(text,/bitrate.*not applicable/i);
  if(format==='m4b'&&outputMode!=='book')assert.match(text,/\.m4a/);
 }
 assert.match(exportExplanation({...exportDefaults,profile:'preserve'}),/fallback|re-encod/i);
 assert.match(exportExplanation(exportDefaults),/64.*96.*44.1/);
});
test('early terminal recovery ends export idle and preserves renderer report',async()=>{
 const f=fixture(async()=>({requestId:'r2',taskId:1,revision:7,projectId:'book'}));await f.controller.open('book');await f.controller.exportProject('book.m4b');
 assert.ok(f.calls.find(c=>c[0]==='GetOperation'));assert.equal(f.controller.getState().exportOperation,null);assert.equal(f.controller.getState().exportResult.normalizationReport,'Preserve fallback: edited timeline');
});
test('export keeps its snapshot revision while edits and saves proceed; stale events ignored',async()=>{
 let resolve;const gate=new Promise(r=>resolve=r);
 const g=fixture(async()=>gate);await g.controller.open('book');const pending=g.controller.exportProject('book.m4b');await Promise.resolve();await Promise.resolve();
 await g.controller.edit({kind:'cover-remove'});await g.controller.save();assert.equal(g.controller.getState().view.revision,9);assert.equal(g.controller.getState().exportOperation.revision,7);
 await assert.rejects(g.controller.open('other'),/operation already running/);await assert.rejects(g.controller.exportProject('second.m4b'),/operation already running/);
 g.event({requestId:'old',kind:'export',status:'failed',error:'stale'});assert.equal(g.controller.getState().error,'');
 await g.controller.cancel();assert.ok(g.calls.find(c=>c[0]==='CancelOperation'&&c[1]==='r2'));
 resolve({requestId:'r2',taskId:1,revision:7,projectId:'book'});await pending;
 assert.equal(g.controller.getState().exportOperation,null);assert.equal(g.controller.getState().view.revision,9);
});
test('export UI renders all choices and actual reports safely',async()=>{
 const {document}=parseHTML('<main></main>');const root=document.querySelector('main');const f=fixture(async()=>({requestId:'r2',taskId:1,revision:7,projectId:'book'}));const dispose=mountExport(root,f.controller);await f.controller.open('book');
 assert.equal(root.querySelectorAll('[name="format"] option').length,4);assert.equal(root.querySelectorAll('[name="outputMode"] option').length,3);
 await f.controller.exportProject('book.m4b');assert.match(root.textContent,/Preserve fallback: edited timeline/);dispose();
});

test('failed recovery or cancel RPC keeps active export reserved until a terminal event',async()=>{
 let emit;const controller=createBookController({makeRequestId:()=> 'request',api:{onOperation:fn=>(emit=fn,()=>{}),call:async(method)=>{
  if(method==='OpenProject')return view();
  if(method==='StartExport')return {requestId:'request',taskId:7,projectId:'book',revision:7};
  throw Error('RPC temporarily unavailable');
 }}});
 await controller.open('book');await assert.rejects(controller.exportProject('book.m4b'),/temporarily unavailable/);
 assert.equal(controller.getState().exportOperation.requestId,'request');
 await assert.rejects(controller.cancel(),/temporarily unavailable/);assert.equal(controller.getState().exportOperation.requestId,'request');
 emit({requestId:'request',taskId:7,kind:'export',projectId:'book',revision:7,status:'succeeded',paths:['published.m4b'],normalizationReport:'Actual −18 LUFS'});
 assert.equal(controller.getState().exportOperation,null);assert.deepEqual(controller.getState().exportResult.paths,['published.m4b']);
});

test('terminal event before StartExport acknowledgement is never replaced by subsequent recovery',async()=>{
 const f=fixture(async(args,emit)=>{emit({requestId:args[0],taskId:1,kind:'export',projectId:'book',revision:7,status:'succeeded',paths:['published.m4b'],normalizationReport:'Actual −18 LUFS'});return {requestId:args[0],taskId:1,projectId:'book',revision:7}});
 await f.controller.open('book');await f.controller.exportProject('book.m4b');
 assert.equal(f.controller.getState().exportOperation,null);assert.deepEqual(f.controller.getState().exportResult.paths,['published.m4b']);
});


test('mounted editor allows changes during export and blocks switch and another render',async()=>{
 const {document}=parseHTML('<main></main>');installAudioStub(document);
 const f=fixture(async()=>({requestId:'r2',taskId:1,projectId:'book',revision:7}),'running');
 const root=document.querySelector('main');const dispose=mountBookEditor(root,f.controller);
 await f.controller.open('book');await f.controller.exportProject('book.m4b');
 assert.equal(root.querySelector('[data-action="save"]').disabled,false);
 assert.equal(root.querySelector('[data-action="cover"]').disabled,false);
 assert.equal(root.querySelector('[data-action="folder"]').disabled,true);
 assert.equal(root.querySelector('[data-action="projects"]').disabled,true);
 assert.equal(root.querySelector('[data-action="export"]').disabled,true);
 assert.equal(root.querySelector('[data-action="analyze"]').disabled,true);
 for(const key of ['profile','channels','format','outputMode'])assert.equal(root.querySelector(`[name="${key}"]`).disabled,false);
 await f.controller.edit({kind:'cover-remove'});
 assert.match(root.querySelector('.book-export').textContent,/snapshot revision 7 \(current revision 8\)/);
 f.event({requestId:'r2',projectId:'book',revision:8,kind:'export',taskId:1,status:'succeeded'});
 assert.ok(f.controller.getState().exportOperation);
 f.event({requestId:'r2',projectId:'book',revision:7,kind:'export',taskId:99,status:'failed'});
 assert.ok(f.controller.getState().exportOperation);
 f.event({requestId:'r2',projectId:'book',revision:7,kind:'export',taskId:1,status:'cancelled',error:'cancelled',paths:[]});
 assert.equal(root.querySelector('[data-action="export"]').disabled,false);
 assert.match(root.querySelector('.book-export').textContent,/cancelled/);
 dispose();f.controller.dispose();
});

// Exercise real change listeners: busy-state rendering must not replace the
// requested control value before the controller captures its edit snapshot.
for (const [name, value] of [
  ['profile', 'high'], ['channels', 'mono'], ['outputMode', 'both'],
  ['format', 'flac'], ['normalize', true],
]) {
  test(`changing ${name} submits the requested complete settings and renders the returned view`, async () => {
    const { document, window } = parseHTML('<main></main>');
    const root = document.querySelector('main');
    const submitted = [];
    let currentView = view();
    const controller = createBookController({
      makeRequestId: () => 'edit',
      api: {
        onOperation: () => () => {},
        call: async (method, ...args) => {
          if (method === 'OpenProject') return currentView;
          assert.equal(method, 'ApplyEdit');
          submitted.push(args);
          currentView = {
            ...currentView, revision: currentView.revision + 1,
            project: { ...currentView.project, export: args[1].export },
          };
          return currentView;
        },
      },
    });
    const dispose = mountExport(root, controller);
    try {
      await controller.open('book');
      const control = root.querySelector(`[name="${name}"]`);
      if (name === 'normalize') control.checked = value;
      else [...control.options].find(option => option.value === value).selected = true;
      control.dispatchEvent(new window.Event('change'));
      await new Promise(resolve => setImmediate(resolve));
      const requested = { ...exportDefaults, [name]: value };
      assert.deepEqual(submitted, [[7, { kind: 'export', export: requested }]]);
      assert.deepEqual(controller.getState().view.project.export, requested);
      assert.equal(controller.getState().view.revision, 8);
      assert.equal(name === 'normalize' ? control.checked : control.value, value);
      assert.equal(control.disabled, false);
      assert.equal(root.querySelector('[role="alert"]').textContent, '');
    } finally {
      dispose();
      controller.dispose();
    }
  });
}

test('rejected control change preserves committed settings, shows error, and allows retry', async () => {
  const { document, window } = parseHTML('<main></main>');
  const root = document.querySelector('main');
  const submitted = [];
  let reject = true;
  const controller = createBookController({
    makeRequestId: () => 'edit',
    api: {
      onOperation: () => () => {},
      call: async (method, ...args) => {
        if (method === 'OpenProject') return view();
        assert.equal(method, 'ApplyEdit');
        submitted.push(args);
        if (reject) throw Error('Settings could not be saved');
        const updated = view(8);
        updated.project.export = args[1].export;
        return updated;
      },
    },
  });
  const dispose = mountExport(root, controller);
  try {
    await controller.open('book');
    const control = root.querySelector('[name="normalize"]');
    control.checked = true;
    control.dispatchEvent(new window.Event('change'));
    await new Promise(resolve => setImmediate(resolve));
    const expected = [7, { kind: 'export', export: { ...exportDefaults, normalize: true } }];
    assert.deepEqual(submitted, [expected]);
    assert.deepEqual(controller.getState().view.project.export, exportDefaults);
    assert.equal(controller.getState().view.revision, 7);
    assert.equal(control.checked, false);
    assert.equal(control.disabled, false);
    assert.match(root.querySelector('[role="alert"]').textContent, /Settings could not be saved/);
    reject = false;
    control.checked = true;
    control.dispatchEvent(new window.Event('change'));
    await new Promise(resolve => setImmediate(resolve));
    assert.deepEqual(submitted, [expected, expected]);
    assert.equal(controller.getState().view.project.export.normalize, true);
    assert.equal(control.checked, true);
    assert.equal(root.querySelector('[role="alert"]').textContent, '');
  } finally {
    dispose();
    controller.dispose();
  }
});

test('export folder name is proposed from title and author and falls back safely', async () => {
  const {suggestedExportName} = await import('./export.js');
  assert.equal(suggestedExportName({title:'Monster Makers', artist:'E.A. Winters'}), 'Monster Makers-E.A. Winters');
  assert.equal(suggestedExportName({title:'Monster Makers', artist:''}), 'Monster Makers');
  assert.equal(suggestedExportName({artist:'E.A. Winters'}), 'E.A. Winters');
  assert.equal(suggestedExportName({title:'  Part 1: The/End?  ', artist:'A\tB'}), 'Part 1 The End-A B');
  assert.equal(suggestedExportName({title:'...'}), 'Book export');
  assert.equal(suggestedExportName(), 'Book export');
});
