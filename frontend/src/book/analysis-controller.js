// Report ownership stays with the service. The UI sends opaque IDs only.
export function createAnalysisController(api, player, onChange = () => {}) {
  const empty = () => ({origin: null, requestId: '', reportId: '', projectId: '', revision: null, report: null, media: null, status: 'idle', phase: '', current: 0, total: 0, error: '', previewing: false, taskId: null});
  let state = empty(), generation = 0, previewGeneration = 0, disposed = false, decisionLocked = false, cleanupError = null, clearing = Promise.resolve();
  const listeners = new Set(), releases = new Map();
  const update = patch => {
    if (disposed) return;
    state = {...state, ...patch}; onChange(state);
    for (const listener of listeners) listener(state);
  };
  const errorText = error => error?.message || String(error);
  const requestID = () => globalThis.crypto.randomUUID();
  function release(media, owner) {
    if (!media?.mediaId) return Promise.resolve();
    if (!releases.has(media.mediaId)) {
      // Save the promise before invoking the RPC, including synchronous adapters.
      const promise = Promise.resolve().then(() => owner.origin === 'repair'
        ? api.releaseRepairMedia(owner.reportId, media.mediaId) : api.releaseBookMedia(media.mediaId));
      releases.set(media.mediaId, promise);
      promise.catch(() => {});
    }
    return releases.get(media.mediaId);
  }
  async function stopPreview() {
    previewGeneration++;
    const owned = state.media;
    update({media: null, previewing: false});
    // clear() stops native playback synchronously, then awaits its owning release.
    try {
      if (owned) clearing = Promise.all([clearing, player.clear()]);
      await clearing;
      if (cleanupError) throw cleanupError;
    } catch (error) { cleanupError = error; update({error: errorText(error)}); throw error; }
  }
  async function invalidate() {
    const old = state;
    const epoch = ++generation; decisionLocked = true;
    const stopped = stopPreview();
    update(empty());
    let failure, retained = old;
    try { await stopped; } catch (error) { failure = error; }
    try {
      if (old.origin === 'book') {
        if (old.requestId && (old.status === 'analyzing' || old.previewing)) await api.cancelBook(old.requestId);
        if (old.reportId) {
          // ReleaseAudioAnalysis retires ownership even when media disposal
          // returns an error. Keep that error visible without reclaiming its ID.
          retained = {...old, reportId:'', report:null, requestId:''};
          await api.releaseBook(old.reportId);
        }
      } else if (old.origin === 'repair' && !['proceeding','cancelled'].includes(old.status)) await api.cancelRepair();
    } catch (error) { failure ||= error; }
    if (epoch === generation) {cleanupError = null; clearing = Promise.resolve();}
    if (failure) {
      if (epoch === generation) update({...retained, media:null, previewing:false, error:errorText(failure), status:'failed'});
      throw failure;
    }
  }

  function acceptRepairState(next) {
    if (disposed || state.origin !== 'repair' || next?.reportId !== state.reportId ||
        (state.taskId && next.taskId !== state.taskId)) return;
    const ranks = {analyzing: 0, awaitingDecision: 1, proceeding: 2, cancelled: 2, failed: 3};
    if ((ranks[next.status] ?? -1) < (ranks[state.status] ?? 0)) return;
    if (decisionLocked && ['analyzing','awaitingDecision'].includes(next.status)) return;
    const result = next.result;
    if (result && (result.origin !== 'repair' || result.report?.id !== next.reportId)) return;
    update({taskId:next.taskId, status:next.status, phase:next.phase || '', current:next.current, total:next.total,
      error:next.error || '', ...(result ? {report:result.report, projectId:result.projectId, revision:result.revision} : {})});
    if (['proceeding','cancelled','failed'].includes(next.status)) {
      decisionLocked = true;
      stopPreview().catch(() => {});
    }
  }
  const unsubscribe = [api.onRepair?.(acceptRepairState), api.onOperation?.(op => {
    if (state.origin !== 'book' || op?.requestId !== state.requestId || op.projectId !== state.projectId || op.revision !== state.revision) return;
    update({phase:op.message || op.phase || op.status, current:op.current || 0, total:op.total || 0, ...(op.error ? {error:op.error} : {})});
  })];
  const controller = {
    getState: () => state,
    subscribe(fn) {listeners.add(fn); fn(state); return () => listeners.delete(fn);},
    async analyzeBook(revision, projectId = '') {
      if (disposed) throw Error('Analysis controller disposed');
      const cleared = invalidate(), epoch = generation, requestId = requestID(); decisionLocked = false;
      update({...empty(), origin:'book', requestId, revision, projectId, status:'analyzing'});
      try {
        await cleared; await player.clear();
        if (disposed || epoch !== generation) return;
        const result = await api.analyzeBook(requestId, revision);
        if (disposed || epoch !== generation || result?.requestId !== requestId || result.origin !== 'book' || result.revision !== revision || (projectId && result.projectId !== projectId)) {
          if (result?.report?.id) await api.releaseBook(result.report.id);
          if (!disposed && epoch === generation) update({status:'failed', error:'The analysis result does not match the current version of the book.'});
          return;
        }
        update({report:result.report, reportId:result.report.id, projectId:result.projectId, status:'ready'});
      } catch(error) {if (!disposed && epoch === generation) {update({status:'failed',error:errorText(error)}); throw error;}}
    },
    acceptRepairState,
    async loadRepair(reportId) {
      if (disposed) return;
      const cleared = invalidate(), epoch = generation; decisionLocked = false;
      update({...empty(), origin:'repair', reportId, status:'analyzing'});
      try {await cleared; await player.clear(); if (disposed || epoch !== generation) return;
        const result = await api.getRepair(reportId); if (!disposed && epoch === generation) acceptRepairState(result);}
      catch(error) {if (!disposed && epoch === generation) {update({status:'failed',error:errorText(error)}); throw error;}}
    },
    async preview(findingId, occurrence = 0) {
      if (disposed || decisionLocked || !['ready','awaitingDecision'].includes(state.status) || state.previewing) throw Error('Preview is not available right now.');
      const finding = state.report?.findings?.find(f => f.id === findingId);
      if (!finding || finding.startUs == null || ![0,1].includes(occurrence) || (occurrence === 1 && finding.secondStartUs == null)) throw Error('This finding cannot be previewed.');
      const stopped = stopPreview();
      const epoch = generation, attempt = previewGeneration, owner = {...state}, requestId = requestID();
      update({previewing:true, requestId, error:''});
      try {
        await stopped; await player.clear();
        if (disposed || epoch !== generation || attempt !== previewGeneration || decisionLocked) return;
        const media = owner.origin === 'book'
          ? await api.previewBook(requestId, owner.revision, owner.reportId, findingId, occurrence)
          : await api.previewRepair(owner.reportId, findingId, occurrence);
        if (disposed || epoch !== generation || attempt !== previewGeneration || decisionLocked || media?.reportId !== owner.reportId || (owner.origin === 'book' && media.requestId !== requestId)) {
          await release(media, owner); return;
        }
        let accepted = false;
        try {
          player.load(media, () => release(media,owner)); accepted = true;
          update({media, previewing:false}); player.seek((media.markerUs || 0) / 1000000);
          await player.play();
        } finally {if (!accepted) await release(media,owner);}
      } catch(error) {if (!disposed && epoch === generation && attempt === previewGeneration) {update({previewing:false,error:errorText(error)}); throw error;}}
      finally {if (!disposed && epoch === generation && attempt === previewGeneration) update({previewing:false});}
    },
    async confirmRepair(proceed) {
      if (state.origin !== 'repair' || state.status !== 'awaitingDecision' || decisionLocked) throw Error('The report is not waiting for a decision.');
      decisionLocked = true; const reportId = state.reportId, epoch = generation;
      update({status:'deciding'});
      try {
        await stopPreview();
        if (disposed || epoch !== generation || state.status !== 'deciding') return;
        await api.confirmRepair(reportId, proceed);
        if (!disposed && epoch === generation && state.status === 'deciding') update({status:proceed ? 'proceeding' : 'cancelled'});
      } catch(error) {
        if (!proceed && epoch === generation) await api.cancelRepair();
        if (!disposed && epoch === generation) update({status:'failed',error:errorText(error)});
        throw error;
      }
    },
    stopPreview,
    dismiss: invalidate,
    invalidate,
    async dispose() {
      if (disposed) return;
      for (const stop of unsubscribe) stop?.();
      listeners.clear();
      const pending = invalidate(); disposed = true;
      await pending;
    },
  };
  return controller;
}

export async function respondFixPrompt(api, analysis, promptId, choice, analyze) {
  await analysis.invalidate();
  const reportId = await api.respondFixPrompt(promptId, choice, choice === 'fix' && analyze === true);
  if (reportId) await analysis.loadRepair(reportId);
  return reportId;
}
