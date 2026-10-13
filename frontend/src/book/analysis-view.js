import { element } from './metadata.js';
import { displayTimeUs, formatTimeUs } from './state.js';
import { icon } from './icons.js';

const labels = {repetition:'Possible repetition', clipping:'Possible clipping', quiet:'Long silence or very quiet passage', decode_error:'Decoding error', timestamp_discontinuity:'Timestamp discontinuity', missing_timestamp:'Missing timestamp'};
const limitationText = {
  repeat_pcm_limit: 'The repetition check stopped early: comparing the whole book needs more temporary disk space than the check allows itself (4 GB), so only the first part was compared. Everything else was checked for the whole book.',
  repeat_index_limit: 'The repetition check stopped early because its index reached its size limit; only the first part of the book was compared.',
  repeat_pair_limit: 'More repeated passages were found than the report keeps; only the first ones are listed.',
  repeat_occurrence_limit: 'A passage repeats more often than the report keeps; only its first occurrences are listed.',
  finding_limit: 'More findings were detected than the report shows; the counts above include all of them.',
  duration_limit: 'Only the first 24 hours of audio were analyzed.',
  decode_incomplete: 'Decoding stopped before the end of the book, so the rest was not checked.',
};
const statuses = {analyzing:'Analyzing…', ready:'Report ready', awaitingDecision:'Waiting for your decision', deciding:'Applying your decision…', failed:'Analysis failed', cancelled:'Analysis cancelled'};
const phases = {'hash-before':'Checking source files', decode:'Scanning audio', 'repeat-verify':'Verifying repetitions', 'hash-after':'Confirming source files'};
const phaseOrder = Object.keys(phases);
// Progress of a running scan: a readable step and, when the service reports
// totals, how far that step has come.
export function analysisProgress(state) {
  const step = phaseOrder.indexOf(state?.phase);
  const fraction = state?.total > 0 ? Math.min(1, Math.max(0, state.current / state.total)) : null;
  return {
    label: phases[state?.phase] || state?.phase || 'Starting',
    step: step < 0 ? '' : `Step ${step + 1} of ${phaseOrder.length}`,
    fraction,
    // One decimal early on, so a long book visibly moves from the start.
    percent: fraction === null ? '' : fraction < 0.1 ? `${(fraction * 100).toFixed(1)}%` : `${Math.round(fraction * 100)}%`,
  };
}
// Remaining time for the current step, from how fast it has advanced so far.
// Returns "" until there is enough evidence for a stable figure.
export function createEtaEstimator(now = () => Date.now()) {
  let phase = null, startedAt = 0, startFraction = 0;
  return state => {
    const { fraction } = analysisProgress(state);
    if (state?.status !== 'analyzing' || fraction === null) { phase = null; return ''; }
    if (state.phase !== phase || fraction < startFraction) { phase = state.phase; startedAt = now(); startFraction = fraction; return ''; }
    const elapsed = (now() - startedAt) / 1000, advanced = fraction - startFraction;
    if (elapsed < 3 || advanced < 0.01) return '';
    const seconds = (elapsed / advanced) * (1 - fraction);
    if (seconds < 50) return 'less than a minute left';
    const minutes = Math.round(seconds / 60);
    return minutes < 60 ? `about ${minutes} min left` : `about ${Math.floor(minutes / 60)} h ${minutes % 60} min left`;
  };
}
export function progressBar(document, fraction) {
  const track = element(document, 'div', '', 'progress-track');
  const fill = element(document, 'div', '', 'progress-fill');
  if (fraction === null) track.classList.add('indeterminate');
  else fill.style.width = `${fraction * 100}%`;
  track.append(fill);
  return track;
}
const time = value => Number.isSafeInteger(value) && value >= 0 ? formatTimeUs(value) : 'no time';
// Reports that are still filling in can lack totals; never throw while rendering.
const span = value => Number.isSafeInteger(value) && value >= 0 ? displayTimeUs(value) : 'unknown';
const interval = (start,end) => `${time(start)} – ${time(end)}`;

// One engine and one report layout serve both the open book and a file about
// to be repaired; `origin` restricts a mounted view to one of the two.
export function mountAnalysis(root, controller, book = null, { origin = '' } = {}) {
  const document = root.ownerDocument;
  let active = true, queue = null, playRun = 0;
  const eta = createEtaEstimator();
  // Plays every previewable finding in turn; any single Listen or Stop ends it.
  async function listenAll(findings) {
    const run = ++playRun;
    queue = {current: ''};
    try {
      for (const finding of findings) for (const occurrence of finding.secondStartUs != null ? [0, 1] : [0]) {
        if (!active || run !== playRun) return;
        queue = {current: `${finding.id}:${occurrence}`};
        await controller.preview(finding.id, occurrence);
        await book.previewEnded();
      }
    } finally {
      if (run === playRun) { queue = null; if (active) render(controller.getState()); }
    }
  }
  async function stopAll() { queue = null; playRun++; await controller.stopPreview(); if (active) render(controller.getState()); }
  function render(state) {
    const remaining = eta(state);
    const shown = !!state.origin && (!origin || state.origin === origin);
    root.hidden = !shown;
    root.replaceChildren();
    if (!shown) return;
    const add = (tag,text,cls='',parent=root) => {const node=element(document,tag,text,cls); parent.append(node); return node;};
    const head=add('div','','report-head');
    add('h2', state.origin === 'repair' ? 'Analysis before repair' : 'Analysis report','',head);
    const running = state.status === 'analyzing', progress = analysisProgress(state);
    const status=add('p',running ? `${statuses.analyzing} ${progress.percent}`.trim() : `${statuses[state.status] || state.status}${state.phase && state.origin === 'repair' ? ` · ${phases[state.phase] || state.phase}` : ''}`,'report-status',head); status.setAttribute('role','status');
    status.dataset.status=state.status || '';
    if (running) {
      const live=add('div','','report-progress');
      add('p',[progress.label, progress.step, remaining].filter(Boolean).join(' · '),'report-line',live);
      live.append(progressBar(document, progress.fraction));
    }
    const error=add('p',state.error || '', 'book-error banner'); error.setAttribute('role','alert');
    const bookState = book?.getState();
    const blocked = state.origin === 'book' && (bookState?.operation || bookState?.exportOperation || bookState?.repairBusy);
    const report=state.report;
    if (state.origin === 'repair') add('p','Re-encoding can repair a stream or container that can still be decoded. It does not remove repetitions, recover missing samples, or necessarily fix clipping. The report never changes the book on its own.','hint-box');
    const playable=(report?.findings || []).filter(f=>f.startUs != null);
    const canPreview=!blocked && !state.previewing && ['ready','awaitingDecision'].includes(state.status);
    const actions=add('div','','action-row report-actions');
    function button(text,action,fn,disabled=false,variant='btn-ghost',glyph='') {const node=element(document,'button',text,`btn ${variant}`); node.type='button';node.dataset.action=action;node.disabled=disabled;if(glyph)node.prepend(icon(document,glyph));node.addEventListener('click',()=>Promise.resolve(fn()).catch(showError));actions.append(node);return node;}
    if (state.origin === 'repair') {
      if (state.status === 'awaitingDecision' || state.status === 'deciding') {
        button('Continue repair','proceed',()=>controller.confirmRepair(true),state.status !== 'awaitingDecision','btn-accent');
        button('Cancel','cancel',()=>controller.confirmRepair(false),state.status !== 'awaitingDecision');
      } else if (state.status === 'analyzing') button('Cancel','cancel',()=>controller.dismiss());
      else button('Close','dismiss',()=>controller.dismiss());
    }
    if (playable.length > 1 && book?.previewEnded) {
      if (queue) button('Stop listening','stop-all',stopAll,false,'btn-ghost','pause');
      else button(`Listen to all ${playable.length} findings`,'listen-all',()=>listenAll(playable),!canPreview,state.origin === 'book' ? 'btn-accent' : 'btn-ghost','play');
    }
    if (state.origin === 'book') {
      const sources=bookState?.view?.project?.sources || [];
      if (report && sources.length === 1) button('Repair this file…','repair-from-report',()=>{
        for (const [name,detail] of [['app-show-tab','repair'],['app-focus-repair','']]) {const event=new document.defaultView.Event(name,{bubbles:true});event.tab=detail;root.dispatchEvent(event);}
      },false,'btn-ghost','wrench');
      button(state.status === 'analyzing' ? 'Cancel' : 'Close report','dismiss',()=>controller.dismiss());
    }
    if (state.media) add('p',`Preview (up to 30 s): starts at ${time(state.media.originUs)} in the book · marker at ${time(state.media.markerUs)} in the preview. Use the player at the bottom of the window.`,'hint-box');
    if (report) {
      const count=report.findings?.length || 0, coverage=report.coverage || [];
      const partial=coverage.filter(c=>!c.complete);
      // The method note describes how the scan works, not something that went wrong.
      const limits=(report.limitations || []).filter(l=>!l.startsWith('repeat_method'));
      const verdict=add('p',count ? `${count === 1 ? '1 finding' : `${count} findings`} to review — listen before deciding whether anything is wrong.` : partial.length || limits.length ? 'No problems found in the part of the book that was checked.' : 'No problems found.','report-verdict');
      verdict.dataset.tone=count || partial.length || limits.length ? 'warning' : 'success';
      const stats=add('div','','report-stats');
      for (const [name,value] of [['Book length',span(report.durationUs)],['Checked',span(report.scannedUs)],['Checked for repetitions',span(report.repeatScannedUs)],['Findings',String(count)]]) {
        const stat=add('div','','report-stat',stats); add('span',name,'stat-label',stat); add('span',value,'stat-value',stat);
      }
      const checks=add('ul','','report-checks');
      for (const item of coverage) {
        const line=add('li',`${labels[item.detector] || item.detector}: ${item.complete ? 'whole book checked' : `only the first ${span(item.scannedUs)} of ${span(report.durationUs)} was checked`}${report.counts?.[item.detector] ? ` · ${report.counts[item.detector]} found` : ''}`,'',checks);
        line.dataset.tone=!item.complete ? 'warning' : report.counts?.[item.detector] ? 'found' : 'clear';
      }
      for (const limitation of limits) {
        const code=limitation.split(':')[0];
        add('p',limitationText[code] || limitation.replace(/^[a-z_]+:\s*/,''),'report-warning');
      }
      const names = new Map((report.sources || []).map(s=>[s.sourceId,s.name]));
      for (const finding of report.findings || []) {
        const row=add('article','','analysis-finding');
        row.classList.toggle('is-playing', !!queue && queue.current.startsWith(`${finding.id}:`));
        const title=element(document,'h3',`${labels[finding.kind] || finding.kind} · ${finding.severity}`); title.dataset.severity=finding.severity || '';
        row.append(title,element(document,'p',finding.detail || ''));
        row.append(element(document,'p',finding.startUs == null ? 'Source observation — it has no position on the book timeline, so it cannot be previewed.' : `Book: ${interval(finding.startUs,finding.endUs)}`));
        if (finding.secondStartUs != null) row.append(element(document,'p',`B — book: ${interval(finding.secondStartUs,finding.secondEndUs)}`));
        if (finding.sourceId) row.append(element(document,'p',`Source: ${names.get(finding.sourceId) || finding.sourceId} · ${interval(finding.sourceStartUs,finding.sourceEndUs)}`));
        if (finding.originalTimestampUs != null) row.append(element(document,'p',`Original timestamp: ${finding.originalTimestampUs} µs (does not by itself mean audible damage)`));
        if (finding.verification) row.append(element(document,'p',`Verification: ${finding.verification}`));
        for (const span of finding.provenance || []) row.append(element(document,'p',span.synthetic ? `Silence you inserted · book ${interval(span.outputStartUs,span.outputEndUs)}` : `Source ${names.get(span.sourceId) || span.sourceId}: ${interval(span.sourceStartUs,span.sourceEndUs)} → book ${interval(span.outputStartUs,span.outputEndUs)}`,'finding-origin'));
        const occurrences = finding.secondStartUs != null ? [0,1] : [0];
        for (const occurrence of occurrences) {
          const node=element(document,'button',occurrences.length === 2 ? `Listen to ${occurrence === 0 ? 'A' : 'B'}` : 'Listen','btn btn-ghost btn-sm');
          node.prepend(icon(document,'play'));
          node.type='button'; node.dataset.finding=finding.id; node.dataset.occurrence=String(occurrence);
          node.disabled=!!blocked || finding.startUs == null || state.previewing || !['ready','awaitingDecision'].includes(state.status);
          node.addEventListener('click',()=>{queue=null;playRun++;controller.preview(finding.id,occurrence).catch(showError);}); row.append(node);
        }
      }
      const notes=add('details','','report-notes'); add('summary','How to read this report','',notes);
      add('p','Silence you inserted yourself is skipped when looking for silence and repetitions; it is never reported as damage.','',notes);
      add('p','The analysis is diagnostic. No findings does not guarantee that the file is correct. Possible clipping is judged at 44.1 kHz; resampling and legitimate peaks can produce the same result. A long silence or a very quiet passage is not necessarily a fault.','',notes);
      add('p','Repetitions: exact PCM comparison after quantizing to 16 bits (pcm16-exact), a 250 ms grid, at least 8 s and at most 60 s per pair. The scan can miss a change in volume, similar passages that were encoded separately, offsets that fall off the grid, and tails shorter than 8 s. Repeated intros, music and refrains can be intentional; decide after listening.','',notes);
      for (const source of report.sources || []) add('p',`Source: ${source.name} · ${source.sizeBytes} B · SHA-256: ${source.sha256}`,'analysis-source',notes);
      add('p',`Detector version: ${report.detectorVersion}`,'analysis-source',notes);
    }
    function showError(err) {if(active) error.textContent=err?.message || String(err);}
  }
  const stop=controller.subscribe(render);
  const stopBook=book?.subscribe(()=>render(controller.getState()));
  return ()=>{active=false;stop();stopBook?.();root.replaceChildren();};
}

// A separate source-file repair launcher; it never loads or edits the open book.
export function mountRepair(root, book, api) {
  const document=root.ownerDocument;
  let active=true, busy=false, started=false, promptId='', responding=false, epoch=0, lastTaskId=0;
  const promptsSeen = new Set();
  const add=(tag,text,cls='',parent=root)=>{const node=element(document,tag,text,cls);parent.append(node);return node;};
  const intro=add('div','','page-head');
  add('h2','Repair a file','',intro);
  add('p','Re-encodes one audiobook into a clean copy, keeping its chapters, tags and cover. Use it for files that stop or skip during playback. The original file is kept, and an open book keeps its changes.','',intro);
  const card=add('div','','card repair-card');
  // When the open book is a single file, offer it directly instead of asking
  // the user to find the same file again.
  const openBook=add('button','','btn btn-accent',card);openBook.type='button';openBook.dataset.action='repair-open';openBook.hidden=true;
  const launch=add('button','Choose a file to repair…','btn btn-accent',card);launch.type='button';launch.dataset.action='repair-file';
  launch.prepend(icon(document,'wrench'));
  const openHint=add('p','Repairs the original file of the open book. Edits you made in Book are not part of the repaired copy.','repair-hint',card);openHint.hidden=true;
  let openPath='';
  const baseName=path=>path.replace(/^.*[/\\]/,'');
  const cancel=add('button','Cancel repair','btn btn-ghost',card);cancel.type='button';cancel.hidden=true;
  const status=add('p','','repair-status',card);status.setAttribute('role','status');
  const error=add('p','','book-error banner',card);error.setAttribute('role','alert');
  const prompt=add('section','','repair-prompt',card);prompt.hidden=true;
  const filename=element(document,'p','','repair-filename');
  const label=element(document,'label','','check-row');
  const checkbox=element(document,'input');checkbox.type='checkbox';checkbox.checked=false;
  label.append(checkbox,element(document,'span','Analyze the file before repairing','check-title'));
  const choices=element(document,'div','','action-row');prompt.append(filename,label,choices);
  const buttons=[];
  const reportHost=add('section','','analysis-report card');
  const stopReport = book.analysis?.subscribe ? mountAnalysis(reportHost, book.analysis, book, {origin:'repair'}) : () => {};
  const showError=err=>{if(active)error.textContent=err?.message || String(err);};
  const focusOffer=()=>{(openBook.hidden ? launch : openBook).focus();};
  document.addEventListener('app-focus-repair',focusOffer);
  const stopBusy=()=>{busy=false;started=false;promptId='';prompt.hidden=true;cancel.hidden=true;book.setRepairBusy(false);};
  const showState=state=>{
    const sources=state.view?.project?.sources || [];
    const path=sources.length === 1 ? sources[0].path || '' : '';
    if(path !== openPath) {
      openPath=path;
      openBook.replaceChildren(icon(document,'wrench'),document.createTextNode(`Repair “${baseName(path)}”`));
      openBook.hidden=openHint.hidden=!path;
      launch.textContent=path ? 'Choose another file…' : 'Choose a file to repair…';
      if(!path) launch.prepend(icon(document,'wrench'));
      launch.className=path ? 'btn btn-ghost' : 'btn btn-accent';
    }
    openBook.disabled=launch.disabled=busy || !!state.operation || !!state.exportOperation || !!state.repairBusy || !!state.analysis?.previewing || ['analyzing','awaitingDecision','deciding'].includes(state.analysis?.status);
  };
  const unsubscribers=[book.subscribe(showState), api.onPrompt(data=>{
    if(!active || !started || !data?.promptId || promptsSeen.has(data.promptId))return;
    promptsSeen.add(data.promptId);
    promptId=data.promptId;responding=false;checkbox.checked=false;checkbox.disabled=false;
    filename.textContent=`Single file: ${data.filename}`;prompt.hidden=false;
    for(const node of buttons)node.disabled=false;
  }),api.onProgress(data=>{if(active && started)status.textContent=data?.message || data?.phase || 'Repairing…';}),api.onDone(data=>{
    if(!active || !started || (data?.taskID && data.taskID <= lastTaskId))return;
    if(data?.taskID)lastTaskId=data.taskID;
    epoch++;status.textContent=data?.success ? 'Done.' : 'The operation did not finish successfully.';
    error.textContent=data?.error || '';stopBusy();
  })];
  for(const [choice,text,variant] of [['fix','Repair','btn-accent'],['continue','Continue without repair','btn-ghost'],['stop','Stop','btn-quiet']]) {
    const node=element(document,'button',text,`btn ${variant}`);node.type='button';node.dataset.choice=choice;buttons.push(node);choices.append(node);
    node.addEventListener('click',async()=>{
      if(responding || !promptId || !started)return;
      const id=promptId, attempt=epoch;responding=true;checkbox.disabled=true;
      for(const item of buttons)item.disabled=true;
      try {
        // Subscription was installed at controller construction, before StartMerge.
        await book.analysis.invalidate();
        if(!active || attempt!==epoch)return;
        const reportId=await api.respondFixPrompt(id,choice,choice==='fix' && checkbox.checked===true);
        if(!active || attempt!==epoch)return;
        prompt.hidden=true;
        if(reportId)await book.analysis.loadRepair(reportId);
      } catch(err) {showError(err);}
    });
  }
  // known is the open book's file; without it the user picks the input.
  async function start(known='') {
    busy=true;const attempt=++epoch;book.setRepairBusy(true);error.textContent='';status.textContent=known ? 'Choose where to save the repaired copy…' : 'Choose a file…';
    try {
      await book.analysis.invalidate();await book.clearPreview();
      const input=known || await api.selectInput();if(!active || attempt!==epoch)return;
      if(!input){stopBusy();status.textContent='';return;}
      const name=input.replace(/^.*[/\\]/,'').replace(/\.[^.]*$/,'');
      const output=await api.selectOutput(`${name}_fixed.m4b`);if(!active || attempt!==epoch)return;
      if(!output){stopBusy();status.textContent='';return;}
      if(input===output)throw Error('Choose a different output file so the original is kept.');
      started=true;cancel.hidden=false;status.textContent='Starting repair…';
      await api.startMerge(input,output);
    } catch(err) {if(active && attempt===epoch){showError(err);stopBusy();}}
  }
  launch.addEventListener('click',()=>{if(!launch.disabled)start();});
  openBook.addEventListener('click',()=>{if(!openBook.disabled && openPath)start(openPath);});
  cancel.addEventListener('click',async()=>{
    if(!started)return;
    cancel.disabled=true; const attempt=epoch;
    try {
      try {await book.analysis.invalidate();await book.clearPreview();}
      finally {if(active && started && attempt===epoch)await api.cancelRepair();}
    }
    catch(err){showError(err);}
    finally {if(active)cancel.disabled=false;}
  });
  return ()=>{active=false;epoch++;document.removeEventListener('app-focus-repair',focusOffer);stopReport();for(const stop of unsubscribers)stop?.();root.replaceChildren();};
}
