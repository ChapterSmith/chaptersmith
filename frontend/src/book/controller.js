import { createAnalysisController } from "./analysis-controller.js";
import { initialState, reduce } from "./state.js";

function assertSafeNumbers(value, key = "") {
  if (
    typeof value === "number" &&
    (key.endsWith("Us") || key === "revision" || key === "destination") &&
    (!Number.isSafeInteger(value) || value < 0)
  )
    throw new Error(`${key} must be a nonnegative safe integer`);
  if (key === "mtimeNs" && typeof value !== "string")
    throw new Error("mtimeNs must remain a string");
  if (value && typeof value === "object")
    for (const [name, child] of Object.entries(value))
      assertSafeNumbers(child, name);
}

export function createBookController({
  api,
  analysisAPI = {},
  onState = () => {},
  makeRequestId = () => globalThis.crypto.randomUUID(),
}) {
  let state = initialState(),
    queue = Promise.resolve(),
    disposed = false,
    lastLoadInput = null,
    player = null,
    previewGeneration = 0,
    suggestionGeneration = 0,
    analysis = null;
  function clearPlayback() {
    previewGeneration++;
    const released = player?.clear();
    dispatch({ type: "preview-clear" });
    return released;
  }
  function clearPreview() {
    const stopping = analysis?.stopPreview();
    const clearing = clearPlayback();
    const pending = Promise.all([stopping, clearing]);
    pending.catch(() => {});
    return pending;
  }
  const subscribers = new Set();
  function dispatch(action) {
    if (disposed) return;
    const next = reduce(state, action);
    if (next === state) return;
    state = next;
    onState(state);
    for (const listener of subscribers) listener(state);
  }
  let analysisOperation = () => {};
  const stopOperation = api.onOperation((status) => {
    dispatch({ type: status?.kind === "export" ? "export-status" : "operation-status", status });
    analysisOperation(status);
  });
  analysis = createAnalysisController({
    analyzeBook: (id, revision) => api.call("AnalyzeAudio", id, revision),
    previewBook: (...args) => {
      if (state.operation || state.exportOperation || state.repairBusy) throw new Error("operation already running");
      return api.call("PreviewAudioFinding", ...args);
    },
    releaseBook: id => api.call("ReleaseAudioAnalysis", id),
    releaseBookMedia: id => api.call("ReleaseMedia", id),
    cancelBook: id => api.call("CancelOperation", id),
    onOperation: handler => { analysisOperation = handler; return () => {analysisOperation = () => {};}; },
    ...analysisAPI,
  }, {
    clear: clearPlayback,
    load: (...args) => player.load(...args),
    seek: seconds => player.seek(seconds),
    play: () => player.play(),
  }, value => dispatch({type: "analysis-state", analysis: value}));
  function analysisBusy() {
    return state.analysis?.previewing || ["analyzing", "awaitingDecision", "deciding"].includes(state.analysis?.status);
  }
  function enqueue(kind, action) {
    if (disposed) return Promise.reject(new Error("Controller disposed"));
    const promise = queue.then(async () => {
      if (disposed) throw new Error("Controller disposed");
      if (state.repairBusy || analysisBusy()) throw new Error("Audio analysis is active");
      if (state.exportOperation && !["edit", "save", "cover", "undo", "boundaries"].includes(kind))
        throw new Error("operation already running");
      const requestId = makeRequestId();
      const revision = state.view?.revision ?? null;
      dispatch({ type: "operation-start", requestId, kind });
      try {
        const result = await action(requestId, revision);
        if (result?.project) {
          assertSafeNumbers(result);
          dispatch({ type: "operation-result", requestId, revision, result });
          if (state.view !== result) throw new Error("Stale operation result");
        } else dispatch({ type: "operation-error", requestId, error: "" });
        return result;
      } catch (error) {
        dispatch({
          type: "operation-error",
          requestId,
          error: error?.message || String(error),
        });
        throw error;
      }
    });
    // A failed mutation must not poison the queue.
    queue = promise.catch(() => {});
    return promise;
  }
  function mutation(kind, method, ...args) {
    const invalidated = kind !== "save" && state.analysis?.origin ? analysis.invalidate() : null;
    if (kind !== "save") { clearPreview(); clearSuggestions(); }
    return enqueue(kind, async (_, revision) => {
      if (invalidated) await invalidated;
      if (revision === null) throw new Error("Load or open a book first");
      return api.call(method, revision, ...args);
    });
  }
  function clearSuggestions() {
    suggestionGeneration++;
    dispatch({ type: "suggestions-clear" });
  }
  function preview(method, input) {
      const stopped = analysis.stopPreview();
      clearPreview();
      const generation = previewGeneration;
      return enqueue("preview", async (requestId, revision) => {
        await stopped;
        if (generation !== previewGeneration) return null;
        if (revision === null) throw new Error("Load or open a book first");
        const projectId = state.view.project.id;
        const result = await api.call(
          method, requestId, revision, input,
        );
        let accepted = false;
        try {
          assertSafeNumbers(result);
          if (
            !disposed && player && generation === previewGeneration &&
            state.operation?.requestId === requestId &&
            state.view?.revision === revision &&
            state.view?.project.id === projectId && result.requestId === requestId &&
            result.revision === revision && result.projectId === projectId
          ) {
            player.load(result);
            accepted = true;
            // Streams are ready at once, so start without a second click.
            Promise.resolve(player.play?.()).catch(() => {});
            dispatch({ type: "preview-result", requestId, result });
          }
        } finally {
          if (!accepted && result?.mediaId) await api.call("ReleaseMedia", result.mediaId);
        }
        return result;
      });
  }
  return {
    getState: () => state,
    analysis,
    setRepairBusy: busy => dispatch({type: "repair-busy", busy}),
    async analyzeAudio() {
      await queue;
      if (disposed || !state.view) throw new Error("Load or open a book first");
      if (state.operation || state.exportOperation || state.repairBusy || analysisBusy()) throw new Error("operation already running");
      return analysis.analyzeBook(state.view.revision, state.view.project.id);
    },
    async selectExportTarget(directoryName = "") {
      await queue;
      if (!state.view) throw new Error("Load or open a book first");
      if (state.operation || state.exportOperation || state.repairBusy || analysisBusy()) throw new Error("operation already running");
      return api.call("SelectExportTarget", state.view.revision, directoryName);
    },
    async exportProject(outputPath) {
      await queue;
      if (disposed) throw new Error("Controller disposed");
      if (!state.view) throw new Error("Load or open a book first");
      if (state.operation || state.exportOperation || state.repairBusy || analysisBusy()) throw new Error("operation already running");
      clearPreview();
      const requestId = makeRequestId(), revision = state.view.revision;
      dispatch({type: "export-start", requestId});
      let acknowledged = false;
      try {
        const started = await api.call("StartExport", requestId, revision, outputPath);
        acknowledged = true;
        dispatch({type: "export-ack", started});
        const status = await api.call("GetOperation", requestId);
        dispatch({type: "export-status", status});
        return started;
      } catch (error) {
        dispatch({type: acknowledged ? "export-warning" : "export-error", requestId, error: error?.message || String(error)});
        throw error;
      }
    },
    attachPlayer(next) {
      player?.dispose();
      player = next;
    },
    releaseMedia: (id) => api.call("ReleaseMedia", id),
    previewEnded: () => player?.ended?.() ?? Promise.resolve(),
    clearPreview,
    previewChapter: (chapterId) => preview("PreviewChapter", chapterId),
    previewBoundary(proposal) {
      const suggestions = state.suggestions;
      if (!suggestions || suggestions.revision !== state.view?.revision ||
          suggestions.projectId !== state.view?.project.id ||
          !suggestions.proposals.some(p => p.chapterId === proposal.chapterId &&
            p.oldStartUs === proposal.oldStartUs && p.newStartUs === proposal.newStartUs))
        return Promise.reject(new Error("Suggestions no longer match the current revision"));
      return preview("PreviewBoundary", structuredClone(proposal));
    },
    suggestBoundaries(opts = { windowUs: 5000000, thresholdDb: -40, minSilenceUs: 500000 }) {
      clearSuggestions();
      const generation = suggestionGeneration;
      const options = structuredClone(opts);
      return enqueue("suggest", async (requestId, revision) => {
        assertSafeNumbers(options);
        if (revision === null) throw new Error("Load or open a book first");
        if (generation !== suggestionGeneration) throw new Error("Stale suggestions");
        const result = await api.call("SuggestBoundaries", requestId, revision, options);
        assertSafeNumbers(result);
        if (disposed || generation !== suggestionGeneration ||
            state.operation?.requestId !== requestId || state.view?.revision !== revision ||
            result.requestId !== requestId || result.revision !== revision ||
            result.projectId !== state.view?.project.id) throw new Error("Stale suggestions");
        // scope names the single chapter that was searched, if any.
        dispatch({ type: "suggestions-result", requestId, result: { ...result, scope: options.chapterId || "" } });
        return result;
      });
    },
    rejectSuggestions: clearSuggestions,
    // Proposed places to start new chapters, found by listening to the whole book.
    suggestSplits(opts) {
      clearSuggestions();
      const generation = suggestionGeneration;
      const options = structuredClone(opts);
      return enqueue("suggest", async (requestId, revision) => {
        assertSafeNumbers(options);
        if (revision === null) throw new Error("Load or open a book first");
        const result = await api.call("SuggestSplits", requestId, revision, options);
        assertSafeNumbers(result);
        if (disposed || generation !== suggestionGeneration ||
            state.operation?.requestId !== requestId || state.view?.revision !== revision ||
            result.requestId !== requestId || result.revision !== revision ||
            result.projectId !== state.view?.project.id) throw new Error("Stale suggestions");
        dispatch({ type: "splits-result", requestId, result });
        return result;
      });
    },
    previewPosition: (atUs) => preview("PreviewPosition", atUs),
    // The silence already present at each chapter start, for the current revision.
    measurePauses(opts) {
      const options = structuredClone(opts);
      return enqueue("suggest", async (requestId, revision) => {
        assertSafeNumbers(options);
        if (revision === null) throw new Error("Load or open a book first");
        const result = await api.call("MeasureChapterPauses", requestId, revision, options);
        assertSafeNumbers(result);
        if (disposed || state.view?.revision !== revision || result.requestId !== requestId ||
            result.revision !== revision || result.projectId !== state.view?.project.id) throw new Error("Stale pause measurement");
        return result;
      });
    },
    applyBoundaries(proposals) {
      const invalidated = state.analysis?.origin ? analysis.invalidate() : null;
      const suggestions = state.suggestions;
      const selected = structuredClone(proposals);
      clearPreview();
      return enqueue("boundaries", async (_, revision) => {
        if (invalidated) await invalidated;
        assertSafeNumbers(selected);
        if (!suggestions || state.suggestions !== suggestions || suggestions.revision !== revision ||
            suggestions.projectId !== state.view?.project.id)
          throw new Error("Suggestions no longer match the current revision");
        if (selected.some(p => !suggestions.proposals.some(s => s.chapterId === p.chapterId &&
            s.oldStartUs === p.oldStartUs && s.newStartUs === p.newStartUs)))
          throw new Error("Unknown boundary suggestion");
        return api.call("ApplyBoundaries", suggestions.revision, selected);
      });
    },
    subscribe(listener) {
      subscribers.add(listener);
      listener(state);
      return () => subscribers.delete(listener);
    },
    load(input) {
      const invalidated = analysis.invalidate();
      clearPreview();
      clearSuggestions();
      const request =
        typeof input === "string" ? { kind: input } : structuredClone(input);
      return enqueue("load", async (requestId) => {
        await invalidated;
        let path = request.inputPath;
        if (!path)
          path = await api.call("SelectBookInput", request.kind || "folder");
        if (!path) return null;
        lastLoadInput = {
          inputPath: path,
          playlistPath: request.playlistPath || "",
          cuePath: request.cuePath || "",
          chapterSource: request.chapterSource || "auto",
        };
        return api.call("LoadBook", requestId, lastLoadInput);
      });
    },
    open(id) {
      const invalidated = analysis.invalidate();
      clearPreview();
      clearSuggestions();
      return enqueue("open", async () => {await invalidated; return api.call("OpenProject", id);});
    },
    save: () => mutation("save", "SaveProject"),
    edit(edit) {
      try {
        assertSafeNumbers(edit);
      } catch (error) {
        return Promise.reject(error);
      }
      return mutation("edit", "ApplyEdit", structuredClone(edit));
    },
    selectCover: () => mutation("cover", "SelectCover"),
    undo: () => mutation("undo", "Undo"),
    async cancel() {
      const op = state.exportOperation || state.operation;
      if (!op || !["load", "preview", "suggest", "export"].includes(op.kind)) return;
      if (op.kind === "preview") clearPreview();
      if (op.kind === "suggest") clearSuggestions();
      try {
        await api.call("CancelOperation", op.requestId);
      } catch (error) {
        dispatch({
          type: op.kind === "export" ? "export-warning" : "operation-error",
          requestId: op.requestId,
          error: error?.message || String(error),
        });
        throw error;
      }
    },
    listProjects: () => api.call("ListProjects", 100),
    getLoadInput: () => (lastLoadInput ? structuredClone(lastLoadInput) : null),
    selectSidecar: (kind) =>
      enqueue("picker", () => api.call("SelectSidecar", kind)),
    dispose() {
      if (disposed) return;
      analysis.dispose().catch(() => {});
      disposed = true;
      previewGeneration++;
      player?.dispose();
      player = null;
      stopOperation();
      subscribers.clear();
    },
  };
}
