import test from 'node:test';
import assert from 'node:assert/strict';
import { parseHTML } from 'linkedom';
import { createAnalysisController, respondFixPrompt } from './analysis-controller.js';
import { mountAnalysis } from './analysis-view.js';
import { createPlayer } from './player.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve; const promise = new Promise(r => resolve = r); return {promise, resolve}; };
const result = (origin = 'book', id = 'report', revision = 7) => ({origin, projectId: 'p', revision, report: {
  id, detectorVersion: 'repair-analysis-v1', durationUs: 30000000, scannedUs: 25000000, repeatScannedUs: 12000000,
  sources: [{sourceId: 's', name: '<img src=x onerror=alert(1)>', sha256: 'abc', sizeBytes: '999'}],
  coverage: [{detector: 'repetition', complete: false, scannedUs: 12000000}], counts: {repetition: 1, quiet: 1}, limitations: ['pair limit'],
  findings: [{id: 'f', kind: 'repetition', severity: 'info', startUs: 0, endUs: 10000000, secondStartUs: 15000000, secondEndUs: 25000000, verification: 'pcm16-exact', detail: '<script>bad</script>', provenance: [{sourceId: 's', sourceStartUs: 5000000, sourceEndUs: 15000000, outputStartUs: 0, outputEndUs: 10000000}, {synthetic: true, outputStartUs: 10000000, outputEndUs: 11000000}]},
    {id: 'source', kind: 'decode_error', severity: 'warning', startUs: null, endUs: null, sourceId: 's', sourceStartUs: 27000000, sourceEndUs: 28000000}],
}});
function fixture(overrides = {}) {
  const calls = [], handlers = new Set(), operations = new Set(), errors = [];
  let current = null;
  const player = {clear: async () => {calls.push(['clear']); const old = current; current = null; if (old) await old.release();}, load: (value, release) => {current = {value, release}; calls.push(['load', value.mediaId]);}, seek: value => calls.push(['seek', value]), play: async () => {}, dispose() {}};
  const api = Object.fromEntries(['analyzeBook', 'getRepair', 'previewBook', 'previewRepair', 'confirmRepair', 'releaseBook', 'releaseRepairMedia', 'releaseBookMedia', 'cancelBook', 'cancelRepair', 'respondFixPrompt'].map(name => [name, async (...args) => {calls.push([name, ...args]); return overrides[name]?.(...args);} ]));
  api.onRepair = fn => {handlers.add(fn); return () => handlers.delete(fn);};
  api.onOperation = fn => {operations.add(fn); return () => operations.delete(fn);};
  const controller = createAnalysisController(api, player, s => {if(s.error) errors.push(s.error);});
  return {api, controller, calls, player, handlers, operations, errors, event: s => handlers.forEach(fn => fn(s))};
}
async function book(f) { f.api.analyzeBook = async (requestId, revision) => {f.calls.push(['analyzeBook', requestId, revision]); return {...result(), requestId};}; await f.controller.analyzeBook(7, 'p'); }
async function repair(f) {f.api.getRepair = async id => ({reportId: id, taskId: 1, status: 'awaitingDecision', result: result('repair', id)}); await f.controller.loadRepair('repair');}

test('book analysis uses current revision and no repair action or automatic edit/proceed', async () => {
  const f = fixture(); await book(f);
  assert.equal(f.calls.find(c => c[0] === 'analyzeBook')[2], 7);
  assert.equal(f.controller.getState().origin, 'book');
  await assert.rejects(f.controller.confirmRepair(true));
  assert.equal(f.calls.some(c => /confirmRepair|edit/i.test(c[0])), false);
  await f.controller.dismiss();
  assert.equal(f.calls.filter(c => c[0] === 'releaseBook').length, 1);
});

test('checked repair subscribes then recovers report after early event; unchecked fix skips analysis', async () => {
  const f = fixture({respondFixPrompt: (promptId, choice, analyze) => {assert.equal(f.handlers.size, 1); if(analyze) f.event({reportId: 'repair', status: 'awaitingDecision', result: result('repair', 'repair')}); return analyze ? 'repair' : '';}, getRepair: id => ({reportId:id, taskId: 1, status:'awaitingDecision', result: result('repair',id)})});
  await respondFixPrompt(f.api, f.controller, 'prompt', 'fix', false);
  assert.equal(f.calls.some(c => c[0] === 'getRepair'), false);
  await respondFixPrompt(f.api, f.controller, 'prompt2', 'fix', true);
  assert.equal(f.controller.getState().reportId, 'repair');
  assert.equal(f.controller.getState().status, 'awaitingDecision');
  assert.ok(f.calls.some(c => c[0] === 'getRepair'));
  f.event({reportId:'old', status:'failed', error:'stale'});
  assert.equal(f.controller.getState().reportId, 'repair');
});

test('late analysis revision and preview cannot replace current report; stale token released once', async () => {
  const late = deferred(), f = fixture({previewBook: () => late.promise}); await book(f);
  const pending = f.controller.preview('f', 1); await tick();
  await f.controller.invalidate();
  late.resolve({reportId:'report', requestId:f.calls.find(c=>c[0]==='previewBook')[1], mediaId:'late', url:'/_media/late'});
  await pending;
  assert.equal(f.calls.filter(c=>c[0]==='releaseBookMedia' && c[1]==='late').length, 1);
  assert.equal(f.calls.some(c=>c[0]==='load'), false);
  const delayed = deferred(); f.api.analyzeBook = () => delayed.promise;
  const scan = f.controller.analyzeBook(8,'p'); await tick(); await f.controller.invalidate();
  delayed.resolve({...result('book','stale',7), requestId:'stale'}); await scan;
  assert.equal(f.controller.getState().report, null);
  assert.ok(f.calls.some(c=>c[0]==='releaseBook' && c[1]==='stale'));
});

test('candidate audition selects A and B; only finding IDs and occurrence go to API', async () => {
  const f = fixture({previewBook: (requestId, revision, reportId) => ({requestId, reportId, mediaId:Math.random().toString(), url:'/_media/opaque', originUs:1000000, markerUs:5000000})}); await book(f);
  await f.controller.preview('f',0); await f.controller.preview('f',1);
  assert.deepEqual(f.calls.filter(c=>c[0]==='previewBook').map(c=>c.slice(2)), [[7,'report','f',0],[7,'report','f',1]]);
  assert.ok(f.calls.some(c=>c[0]==='seek' && c[1]===5));
  await assert.rejects(f.controller.preview('source',0));
  await f.controller.dismiss();
  assert.equal(f.calls.filter(c=>c[0]==='releaseBookMedia').length,2);
});

for (const proceed of [true,false]) test(`repair decision ${proceed} stops player and releases before decision; no later acceptance`, async () => {
  const f = fixture({previewRepair: reportId => ({reportId, mediaId:'owned', url:'/_media/owned'})}); await repair(f);
  await f.controller.preview('f',0); await f.controller.confirmRepair(proceed);
  const release = f.calls.findIndex(c=>c[0]==='releaseRepairMedia'), confirm = f.calls.findIndex(c=>c[0]==='confirmRepair');
  assert.ok(release >= 0 && release < confirm);
  assert.deepEqual(f.calls[release],['releaseRepairMedia','repair','owned']);
  await assert.rejects(f.controller.preview('f',0));
  f.event({reportId:'repair', taskId:1, status:'awaitingDecision', result:result('repair','repair')});
  await assert.rejects(f.controller.preview('f',0));
});

test('cleanup removes subscriptions and panel remount does not duplicate handlers', async () => {
  const f = fixture(), {document} = parseHTML('<main></main>'), root=document.querySelector('main');
  const clean = mountAnalysis(root,f.controller); clean(); const clean2=mountAnalysis(root,f.controller);
  assert.equal(f.handlers.size,1); clean2(); await f.controller.dispose(); await f.controller.dispose();
  assert.equal(f.handlers.size,0); assert.equal(f.operations.size,0);
});

test('report renders source/output timing, safe text, synthetic silence and incomplete coverage distinctly', async () => {
  const f=fixture(); await book(f); const {document}=parseHTML('<main></main>'),root=document.querySelector('main'); const clean=mountAnalysis(root,f.controller);
  assert.match(root.textContent,/00:00:05.000000/); assert.match(root.textContent,/only the first 00:00:12 of 00:00:30 was checked/); assert.match(root.textContent,/Silence you inserted ·/); assert.match(root.textContent,/pcm16-exact/); assert.doesNotMatch(root.textContent,/No repetitions were found/);
  assert.match(root.textContent,/<script>bad/); assert.equal(root.querySelector('script,img'),null);
  assert.deepEqual([...root.querySelectorAll('[data-occurrence]')].map(n=>n.textContent),['Listen to A','Listen to B','Listen']);
  assert.equal(root.querySelector('[data-finding="source"]').disabled,true);
  assert.equal(root.querySelector('[data-action="proceed"]'),null); clean();
});

test('repair media cleanup failure prevents frontend proceed and remains visible', async () => {
  const f=fixture({previewRepair: reportId=>({reportId,mediaId:'owned',url:'/_media/owned'}), releaseRepairMedia:()=>{throw Error('release failed');}}); await repair(f); await f.controller.preview('f',0);
  await assert.rejects(f.controller.confirmRepair(true),/release failed/);
  assert.equal(f.calls.some(c=>c[0]==='confirmRepair'),false);
  assert.match(f.controller.getState().error,/release failed/);
});

test('double audition click admits only one renderer and dismiss during player clear cannot restart it', async () => {
  const wait=deferred(), f=fixture({previewBook:()=>wait.promise}); await book(f);
  const first=f.controller.preview('f',0); const second=f.controller.preview('f',1).catch(error=>error);
  await tick(); assert.equal(f.calls.filter(c=>c[0]==='previewBook').length,1);
  await f.controller.dismiss(); wait.resolve({reportId:'report', mediaId:'late'}); await first; assert.ok(await second instanceof Error);
});

test('cancel remains possible after media cleanup failure and a second repair accepts its new task ID', async () => {
  const f=fixture({previewRepair:reportId=>({reportId,mediaId:'owned',url:'/_media/owned'}),releaseRepairMedia:()=>{throw Error('cleanup');}});
  await repair(f); await f.controller.preview('f',0);await assert.rejects(f.controller.confirmRepair(true));
  await f.controller.dismiss().catch(()=>{});assert.ok(f.calls.some(c=>c[0]==='cancelRepair'));
  const clean=fixture();await repair(clean);await clean.controller.confirmRepair(false);await clean.controller.dismiss();
  clean.api.getRepair=async id=>({reportId:id,taskId:2,status:'awaitingDecision',result:result('repair',id)});
  await clean.controller.loadRepair('second'); assert.equal(clean.controller.getState().status,'awaitingDecision');assert.equal(clean.controller.getState().taskId,2);
});

test('dismiss during awaited media release prevents a pending proceed RPC', async () => {
  const released=deferred(),f=fixture({previewRepair:reportId=>({reportId,mediaId:'owned',url:'/_media/owned'}),releaseRepairMedia:()=>released.promise});await repair(f);await f.controller.preview('f',0);
  const decision=f.controller.confirmRepair(true);await tick();const closing=f.controller.dismiss();released.resolve();await Promise.all([decision,closing]);
  assert.equal(f.calls.some(c=>c[0]==='confirmRepair'),false);
});

test('rejected audition is visible and a cancelled scan cannot resume after shared player cleanup', async () => {
  const f=fixture({previewBook:(requestId,revision,reportId)=>({requestId,reportId,mediaId:'m',url:'/_media/m'})});await book(f);
  f.player.play=async()=>{throw Error('gesture required');};await assert.rejects(f.controller.preview('f',0));assert.match(f.controller.getState().error,/gesture required/);
  const release=deferred();f.player.clear=()=>release.promise;f.api.analyzeBook=async()=>assert.fail('late scan started');
  const pending=f.controller.analyzeBook(7,'p');const dismissed=f.controller.dismiss();release.resolve();await Promise.all([pending,dismissed]);
});

for (const failureAt of ['media', 'report']) test(`book analysis recovers after ${failureAt} cleanup retires the old report`, async () => {
  let retainedReport = '', scans = 0;
  const f = fixture({
    analyzeBook: (requestId, revision) => {
      retainedReport = `report-${++scans}`;
      return {...result('book', retainedReport, revision), requestId};
    },
    previewBook: (requestId, revision, reportId) => ({requestId, reportId, mediaId:'owned', url:'/_media/owned'}),
    releaseBookMedia: () => {
      if (failureAt === 'media') throw Error('audio disposal failed');
    },
    releaseBook: reportId => {
      if (reportId !== retainedReport) throw Error('unknown audio report');
      retainedReport = '';
      // ReleaseAudioAnalysis also retires its report when final disposal fails.
      if (failureAt === 'report') throw Error('report disposal failed');
    },
  });
  await f.controller.analyzeBook(7, 'p');
  await f.controller.preview('f', 0);
  await assert.rejects(f.controller.dismiss(), /disposal failed/);
  assert.equal(retainedReport, '');
  assert.equal(f.controller.getState().reportId, '');
  assert.equal(f.controller.getState().report, null);
  assert.match(f.controller.getState().error, /disposal failed/);
  assert.equal(f.controller.getState().status, 'failed');
  await f.controller.analyzeBook(7, 'p');
  assert.equal(scans, 2);
  assert.equal(f.controller.getState().reportId, 'report-2');
  assert.equal(f.controller.getState().status, 'ready');
  assert.equal(f.calls.filter(call => call[0] === 'releaseBook' && call[1] === 'report-1').length, 1);
});

test('remaining time appears only after steady progress and resets with each step', async () => {
  const {createEtaEstimator} = await import('./analysis-view.js');
  let clock = 0; const eta = createEtaEstimator(() => clock);
  const at = (phase, current, total = 100) => eta({status:'analyzing', phase, current, total});
  assert.equal(at('decode', 0), '');
  clock = 2000; assert.equal(at('decode', 5), '');
  clock = 10000; assert.equal(at('decode', 10), 'about 2 min left');
  clock = 60000; assert.equal(at('decode', 90), 'less than a minute left');
  clock = 61000; assert.equal(at('repeat-verify', 50), '');
  assert.equal(eta({status:'ready', phase:'decode', current:1, total:2}), '');
  assert.equal(eta({status:'analyzing', phase:'decode', current:0, total:0}), '');
});
