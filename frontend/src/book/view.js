import { element, mountMetadata } from "./metadata.js";
import { mountChapters } from "./chapters.js";
import { mountExport } from "./export.js";
import { analysisProgress, createEtaEstimator, mountAnalysis } from "./analysis-view.js";
import { createPlayer } from "./player.js";
import { displayTimeUs, formatTimeUs } from "./state.js";
import { icon } from "./icons.js";

const busyLabels = {
  load: "Opening the book…",
  open: "Opening the project…",
  preview: "Preparing the preview…",
  suggest: "Looking for silence near chapter starts…",
  edit: "Applying the change…",
  boundaries: "Moving chapter starts…",
  cover: "Updating the cover…",
  save: "Saving the project…",
  undo: "Undoing…",
  picker: "Waiting for your choice…",
};

const clock = (seconds) => {
  const total = Number.isFinite(seconds) && seconds > 0 ? Math.floor(seconds) : 0;
  const hours = Math.floor(total / 3600);
  const rest = `${String(Math.floor((total % 3600) / 60)).padStart(hours ? 2 : 1, "0")}:${String(total % 60).padStart(2, "0")}`;
  return hours ? `${hours}:${rest}` : rest;
};

function projectDate(value) {
  const date = new Date(value);
  return !value || Number.isNaN(date.valueOf())
    ? value || ""
    : date.toLocaleString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
}

// The player host lets the application shell keep the transport visible in
// every view; without one the transport stays inside the editor.
export function mountBookEditor(root, controller, { playerHost = null } = {}) {
  const document = root.ownerDocument;
  let active = true,
    localBusy = false,
    currentTab = "metadata",
    pendingSwitch = null,
    dialogFocus = null,
    localError = "",
    openPanelShown = false,
    shownProjectId = null,
    lastAnalysis = { origin: null, status: "idle" },
    lastExport = null;
  root.classList.add("book-root");
  const button = (text, action, parent, { variant = "btn-ghost", glyph = "" } = {}) => {
    const node = element(document, "button", text, `btn ${variant}`);
    node.type = "button";
    node.dataset.action = action;
    if (glyph) node.prepend(icon(document, glyph));
    parent?.append(node);
    return node;
  };

  // ---- Open panel: the start screen, and the "open another book" sheet ----
  const openPanel = element(document, "section", "", "open-panel");
  const hero = element(document, "div", "", "open-hero");
  const heroMark = element(document, "div", "", "open-mark");
  heroMark.append(icon(document, "open"));
  const heroText = element(document, "div", "", "open-text");
  heroText.append(
    element(document, "h2", "Open an audiobook"),
    element(
      document,
      "p",
      "Pick a folder of audio files or a single audiobook. Your source files are never changed.",
    ),
  );
  const closeOpen = button("Close", "close-open", null, { variant: "btn-quiet btn-sm", glyph: "close" });
  hero.append(heroMark, heroText, closeOpen);
  const openActions = element(document, "div", "", "open-actions");
  const folder = button("Open folder", "folder", openActions, { variant: "btn-accent btn-lg", glyph: "folder" }),
    file = button("Open audio file", "file", openActions, { variant: "btn-ghost btn-lg", glyph: "file" }),
    projects = button("Saved projects", "projects", openActions, { variant: "btn-quiet btn-lg", glyph: "projects" });

  const choices = element(document, "details", "", "import-options");
  choices.append(element(document, "summary", "Import options"));
  const choicesBody = element(document, "div", "", "import-options-body");
  const sourceRow = element(document, "label", "", "field-row");
  sourceRow.append(element(document, "span", "Chapters from", "field-label"));
  const source = element(document, "select", "", "field-input");
  source.name = "chapterSource";
  for (const [value, text] of [
    ["auto", "Automatic"],
    ["embedded", "Embedded chapters"],
    ["files", "Audio files"],
    ["cue", "CUE sheet"],
  ]) {
    const option = element(document, "option", text);
    option.value = value;
    source.append(option);
  }
  sourceRow.append(source);
  const sidecarRow = (label, empty, selectAction, clearAction) => {
    const row = element(document, "div", "", "field-row");
    row.append(element(document, "span", label, "field-label"));
    const value = element(document, "span", empty, "path-value");
    row.append(value);
    return {
      row,
      value,
      select: button("Choose…", selectAction, row, { variant: "btn-ghost btn-sm" }),
      clear: button("Clear", clearAction, row, { variant: "btn-quiet btn-sm" }),
    };
  };
  const NO_PLAYLIST = "Detected automatically",
    NO_CUE = "Detected automatically";
  const playlistRow = sidecarRow("Playlist", NO_PLAYLIST, "playlist", "clear-playlist"),
    cueRow = sidecarRow("CUE sheet", NO_CUE, "cue", "clear-cue");
  const playlist = playlistRow.value,
    cue = cueRow.value,
    selectPlaylist = playlistRow.select,
    selectCue = cueRow.select,
    clearPlaylist = playlistRow.clear,
    clearCue = cueRow.clear;
  let playlistPath = "",
    cuePath = "";
  choicesBody.append(sourceRow, playlistRow.row, cueRow.row);
  const retry = button("Retry import with these options", "retry", choicesBody, { variant: "btn-accent btn-sm" });
  retry.hidden = true;
  choices.append(choicesBody);
  const projectList = element(document, "section", "", "project-list");
  projectList.hidden = true;
  openPanel.append(hero, openActions, choices, projectList);

  const error = element(document, "p", "", "book-error banner");
  error.setAttribute("role", "alert");
  const progress = element(document, "div", "", "busy-banner");
  progress.hidden = true;
  const spinner = element(document, "span", "", "spinner");
  spinner.setAttribute("aria-hidden", "true");
  const status = element(document, "span", "", "busy-text");
  status.setAttribute("role", "status");
  const busyBody = element(document, "div", "", "strip-body");
  const busyTrack = element(document, "div", "", "progress-track");
  const busyFill = element(document, "div", "", "progress-fill");
  busyTrack.append(busyFill);
  busyBody.append(status, busyTrack);
  const busyPercent = element(document, "span", "", "strip-percent");
  progress.append(spinner, busyBody, busyPercent);
  const cancel = button("Cancel import", "cancel", progress, { variant: "btn-ghost btn-sm" });

  // ---- Background work: analysis progress and completion notices ----
  const analysisStrip = element(document, "div", "", "busy-banner analysis-strip");
  analysisStrip.hidden = true;
  const stripSpinner = element(document, "span", "", "spinner");
  stripSpinner.setAttribute("aria-hidden", "true");
  const stripBody = element(document, "div", "", "strip-body");
  const stripText = element(document, "span", "", "busy-text");
  stripText.setAttribute("role", "status");
  const stripTrack = element(document, "div", "", "progress-track");
  const stripFill = element(document, "div", "", "progress-fill");
  stripTrack.append(stripFill);
  stripBody.append(stripText, stripTrack);
  const stripPercent = element(document, "span", "", "strip-percent");
  analysisStrip.append(stripSpinner, stripBody, stripPercent);
  const cancelAnalysis = button("Cancel", "cancel-analysis", analysisStrip, { variant: "btn-ghost btn-sm" });
  const stripEta = createEtaEstimator();
  const toasts = element(document, "div", "", "toasts");
  toasts.setAttribute("aria-live", "polite");
  const toastTimers = new Set();
  function notify(text, { tone = "info", actionLabel = "", onAction = null } = {}) {
    const toast = element(document, "div", "", "toast");
    toast.dataset.tone = tone;
    toast.setAttribute("role", tone === "error" ? "alert" : "status");
    toast.append(element(document, "span", text, "toast-text"));
    const dismiss = () => {
      clearTimeout(timer);
      toastTimers.delete(timer);
      toast.remove();
    };
    if (actionLabel) {
      const action = button(actionLabel, "toast-action", toast, { variant: "btn-ghost btn-sm" });
      action.addEventListener("click", () => {
        dismiss();
        onAction?.();
      });
    }
    const close = button("", "toast-close", toast, { variant: "btn-icon", glyph: "close" });
    close.setAttribute("aria-label", "Dismiss notification");
    close.addEventListener("click", dismiss);
    // Errors stay until dismissed; routine notices clear themselves.
    const timer = tone === "error" ? null : setTimeout(dismiss, 12000);
    timer?.unref?.();
    if (timer) toastTimers.add(timer);
    toasts.append(toast);
  }
  // The shell owns the top-level views; ask it to bring one forward.
  function showAppTab(tab) {
    const event = new document.defaultView.Event("app-show-tab", { bubbles: true });
    event.tab = tab;
    root.dispatchEvent(event);
  }
  function showSection(name) {
    currentTab = name;
    showAppTab("book");
    update(controller.getState());
    tabButtons.get(name).focus();
  }
  function announce(state) {
    const analysis = state.analysis || { origin: null, status: "idle" };
    if (lastAnalysis.status === "analyzing" && analysis.origin === lastAnalysis.origin) {
      const count = analysis.report?.findings?.length || 0;
      const found = count === 1 ? "1 finding" : `${count} findings`;
      if (analysis.status === "ready")
        notify(`Audio analysis finished — ${count ? found : "nothing to report"}`, { tone: "success", actionLabel: "View report", onAction: () => showSection("analysis") });
      else if (analysis.status === "awaitingDecision")
        notify(`Analysis before repair is ready — ${count ? found : "nothing to report"}`, { tone: "success", actionLabel: "Open Repair", onAction: () => showAppTab("repair") });
      else if (analysis.status === "failed")
        notify(`Audio analysis failed${analysis.error ? `: ${analysis.error}` : ""}`, { tone: "error" });
    }
    lastAnalysis = { origin: analysis.origin, status: analysis.status };
    const done = state.exportResult;
    if (lastExport && !state.exportOperation && done?.requestId === lastExport) {
      if (done.status === "succeeded")
        notify("Export finished", { tone: "success", actionLabel: "View result", onAction: () => showSection("export") });
      else if (done.status === "failed")
        notify(`Export failed${done.error ? `: ${done.error}` : ""}`, { tone: "error", actionLabel: "View result", onAction: () => showSection("export") });
    }
    lastExport = state.exportOperation?.requestId || null;
  }

  // ---- Book header ----
  const summary = element(document, "section", "", "book-header book-summary");
  const thumb = element(document, "div", "", "cover-thumb");
  const cover = element(document, "img", "", "book-cover");
  cover.alt = "Book cover";
  cover.hidden = true;
  const thumbEmpty = icon(document, "image");
  thumb.append(cover, thumbEmpty);
  const summaryText = element(document, "div", "", "book-heading");
  const title = element(document, "h2", "");
  const detail = element(document, "p", "", "book-detail"),
    warning = element(document, "p", "", "book-error");
  warning.setAttribute("role", "status");
  summaryText.append(title, detail, warning);
  const bookActions = element(document, "div", "", "book-actions");
  const dirty = element(document, "span", "", "book-dirty");
  dirty.setAttribute("aria-live", "polite");
  bookActions.append(dirty);
  const openAnother = button("Open…", "open-panel", bookActions, { glyph: "open" });
  const undo = button("Undo", "undo", bookActions, { glyph: "undo" });
  const save = button("Save project", "save", bookActions, { variant: "btn-accent", glyph: "save" });
  summary.append(thumb, summaryText, bookActions);

  // ---- Section tabs ----
  const tabs = element(document, "div", "", "section-tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "Book sections");
  const tabNames = ["metadata", "chapters", "analysis", "export"];
  const tabButtons = new Map(),
    panels = new Map();
  const chapterCount = element(document, "span", "", "tab-count");
  const analysisBadge = element(document, "span", "", "tab-count");
  for (const [name, text] of [
    ["metadata", "Details"],
    ["chapters", "Chapters"],
    ["analysis", "Analysis"],
    ["export", "Export"],
  ]) {
    const tab = element(document, "button", text, "section-tab");
    tab.type = "button";
    tab.dataset.action = `tab-${name}`;
    tab.dataset.editTab = name;
    tab.setAttribute("role", "tab");
    tab.id = `book-tab-${name}`;
    tab.setAttribute("aria-controls", `book-panel-${name}`);
    if (name === "chapters") tab.append(chapterCount);
    if (name === "analysis") tab.append(analysisBadge);
    tab.addEventListener("click", () => {
      currentTab = name;
      update(controller.getState());
    });
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
      event.preventDefault();
      const step = event.key === "ArrowRight" ? 1 : -1;
      currentTab = tabNames[(tabNames.indexOf(name) + step + tabNames.length) % tabNames.length];
      update(controller.getState());
      tabButtons.get(currentTab).focus();
    });
    tabs.append(tab);
    tabButtons.set(name, tab);
    const panel = element(document, "div", "", "tab-panel");
    panel.setAttribute("role", "tabpanel");
    panel.id = `book-panel-${name}`;
    panel.setAttribute("aria-labelledby", tab.id);
    panels.set(name, panel);
  }
  const metadataPanel = panels.get("metadata"),
    chaptersPanel = panels.get("chapters"),
    analysisTab = panels.get("analysis"),
    exportTab = panels.get("export");
  const draftNotice = element(document, "div", "", "draft-notice");
  draftNotice.setAttribute("role", "status");
  draftNotice.append(
    element(document, "span", "Apply or discard your changes in Details to keep editing here."),
  );
  const toDetails = button("Go to Details", "goto-details", draftNotice, { variant: "btn-ghost btn-sm" });
  draftNotice.hidden = true;

  metadataPanel.classList.add("details-layout");
  const coverColumn = element(document, "div", "", "cover-column");
  const coverFrame = element(document, "div", "", "cover-frame");
  const coverLarge = element(document, "img");
  coverLarge.alt = "Book cover";
  coverLarge.hidden = true;
  const coverEmpty = element(document, "div", "", "cover-empty");
  coverEmpty.append(icon(document, "image"), element(document, "span", "No cover"));
  coverFrame.append(coverLarge, coverEmpty);
  const coverActions = element(document, "div", "", "cover-actions");
  const selectCover = button("Choose cover…", "cover", coverActions, { variant: "btn-ghost btn-sm" }),
    removeCover = button("Remove", "cover-remove", coverActions, { variant: "btn-quiet btn-sm" });
  coverColumn.append(coverFrame, coverActions);
  metadataPanel.append(coverColumn);

  const analysisIntro = element(document, "div", "", "panel-intro");
  const analysisCopy = element(document, "div");
  analysisCopy.append(
    element(document, "h3", "Audio analysis"),
    element(
      document,
      "p",
      "Scan the edited book for repeated passages, decoding errors, timestamp gaps, clipping and long silence. The scan never changes the book. To move chapter starts onto silence, use the tool in Chapters.",
    ),
  );
  analysisIntro.append(analysisCopy);
  const analyze = button("Analyze audio", "analyze", analysisIntro, { variant: "btn-accent", glyph: "analyze" });
  const analysisPanel = element(document, "section", "", "analysis-report");
  analysisTab.append(analysisIntro, analysisPanel);
  const exportPanel = element(document, "section", "", "book-export");
  exportTab.append(exportPanel);

  // ---- Unsaved-changes dialog ----
  const dialog = element(document, "div", "", "modal-overlay");
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.setAttribute("aria-labelledby", "book-switch-title");
  const dialogCard = element(document, "div", "", "modal modal-narrow");
  const dialogBody = element(document, "div", "", "modal-body");
  const heading = element(document, "h3", "Save changes before switching books?");
  heading.id = "book-switch-title";
  const dialogError = element(document, "p", "", "book-error");
  dialogError.setAttribute("role", "alert");
  const dialogActions = element(document, "div", "", "action-row dialog-actions");
  const dialogButtons = new Map();
  for (const [choice, text, variant] of [
    ["save", "Save", "btn-accent"],
    ["discard", "Discard", "btn-ghost"],
    ["back", "Back", "btn-quiet"],
  ]) {
    const node = button(text, `switch-${choice}`, dialogActions, { variant });
    node.dataset.choice = choice;
    dialogButtons.set(choice, node);
  }
  dialogBody.append(
    heading,
    element(document, "p", "This book has unsaved changes."),
    dialogError,
    dialogActions,
  );
  dialogCard.append(dialogBody);
  dialog.append(dialogCard);

  // ---- Transport ----
  const audition = element(document, "section", "", "player book-audition");
  audition.setAttribute("aria-label", "Preview player");
  const audio = element(document, "audio");
  audio.preload = "metadata";
  audio.setAttribute("aria-label", "Chapter preview");
  const playbackError = element(document, "p", "", "book-error");
  playbackError.setAttribute("role", "alert");
  const player = createPlayer({
    audio,
    releaseMedia: controller.releaseMedia,
    onError: (message) => {
      playbackError.textContent = message;
    },
  });
  controller.attachPlayer(player);
  const play = element(document, "button", "", "player-toggle");
  play.type = "button";
  play.dataset.action = "play-preview";
  play.setAttribute("aria-label", "Play preview");
  const playIcon = icon(document, "play"),
    pauseIcon = icon(document, "pause");
  pauseIcon.hidden = true;
  play.append(playIcon, pauseIcon);
  const elapsed = element(document, "span", "0:00", "player-time"),
    remaining = element(document, "span", "0:00", "player-time");
  const seek = element(document, "input", "", "player-seek");
  seek.type = "range";
  seek.min = "0";
  seek.max = "0";
  seek.step = "0.01";
  seek.value = "0";
  seek.setAttribute("aria-label", "Preview position");
  const transport = element(document, "div", "", "player-transport");
  transport.append(elapsed, seek, remaining);
  const playerInfo = element(document, "div", "", "player-info");
  const idle = element(document, "span", "Previews play here", "player-idle");
  const markers = element(document, "p", "", "player-markers");
  markers.setAttribute("aria-live", "polite");
  playerInfo.append(idle, markers, playbackError);
  audition.title = "Previews play your edit without the export profile or loudness normalization";
  audition.append(play, transport, playerInfo, audio);
  const isPlaying = () => audio.paused === false && !audio.ended;
  function refreshTransport() {
    const duration = Number.isFinite(audio.duration) ? audio.duration : 0;
    const position = Number.isFinite(audio.currentTime) ? audio.currentTime : 0;
    seek.max = String(duration);
    seek.value = String(Math.min(position, duration));
    seek.style.setProperty("--fill", duration ? `${(position / duration) * 100}%` : "0%");
    elapsed.textContent = clock(position);
    remaining.textContent = clock(duration);
    playIcon.hidden = isPlaying();
    pauseIcon.hidden = !isPlaying();
    play.setAttribute("aria-label", isPlaying() ? "Pause preview" : "Play preview");
  }
  const transportEvents = ["timeupdate", "durationchange", "loadedmetadata", "play", "pause", "ended", "emptied"];
  for (const name of transportEvents) audio.addEventListener(name, refreshTransport);
  play.addEventListener("click", () => {
    playbackError.textContent = "";
    if (isPlaying()) player.pause();
    else player.play();
  });
  seek.addEventListener("input", () => player.seek(Number(seek.value)));

  root.append(
    error,
    progress,
    openPanel,
    summary,
    analysisStrip,
    tabs,
    draftNotice,
    ...panels.values(),
    dialog,
    toasts,
  );
  (playerHost || root).append(audition);
  const exports = mountExport(exportPanel, controller);
  const analysisCleanup = mountAnalysis(analysisPanel, controller.analysis, controller, { origin: "book" });
  const metadata = mountMetadata(metadataPanel, controller);
  const chapters = mountChapters(chaptersPanel, controller);

  function message(err) {
    localError = err?.message || String(err);
    error.textContent = localError;
  }
  async function run(action) {
    if (localBusy || controller.getState().operation) return;
    localBusy = true;
    localError = "";
    update(controller.getState());
    try {
      return await action();
    } catch (err) {
      if (active) message(err);
    } finally {
      localBusy = false;
      if (active) update(controller.getState());
    }
  }
  async function saveBook() {
    await metadata.submit();
    return controller.save();
  }
  function closeDialog() {
    dialog.classList.remove("open");
    pendingSwitch = null;
    dialogError.textContent = "";
    dialogFocus?.focus();
  }
  async function switchBook(action) {
    if (controller.getState().exportOperation) return;
    if (controller.getState().view?.dirty || metadata.isDirty()) {
      pendingSwitch = action;
      dialogFocus = document.activeElement;
      dialog.classList.add("open");
      dialogButtons.get("save").focus();
      update(controller.getState());
    } else await run(action);
  }
  for (const [choice, node] of dialogButtons) {
    node.addEventListener("click", async () => {
      if (choice === "back") {
        closeDialog();
        update(controller.getState());
        return;
      }
      const action = pendingSwitch;
      if (!action) return;
      await run(async () => {
        try {
          if (choice === "save") await saveBook();
          const result = await action();
          if (choice === "discard" && result) metadata.discard();
          closeDialog();
        } catch (err) {
          dialogError.textContent = err?.message || String(err);
          throw err;
        }
      });
    });
  }
  dialog.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !localBusy) {
      event.preventDefault();
      closeDialog();
      update(controller.getState());
    }
    if (event.key === "Tab") {
      const available = [...dialogButtons.values()].filter(
        (node) => !node.disabled,
      );
      if (!available.length) {
        event.preventDefault();
        return;
      }
      const first = available[0],
        last = available.at(-1);
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
  });
  const options = () => ({
    playlistPath,
    cuePath,
    chapterSource: source.value || "auto",
  });
  // A cancelled picker resolves to null and leaves the open sheet in place.
  const loadBook = (input) =>
    switchBook(async () => {
      const result = await controller.load({ ...input, ...options() });
      if (result) openPanelShown = false;
      return result;
    }).catch(message);
  folder.addEventListener("click", () => loadBook({ kind: "folder" }));
  file.addEventListener("click", () => loadBook({ kind: "file" }));
  retry.addEventListener("click", () => {
    const input = controller.getLoadInput();
    if (input) loadBook(input);
  });
  openAnother.addEventListener("click", () => {
    openPanelShown = !openPanelShown;
    update(controller.getState());
    if (openPanelShown) folder.focus();
  });
  closeOpen.addEventListener("click", () => {
    openPanelShown = false;
    update(controller.getState());
    openAnother.focus();
  });
  toDetails.addEventListener("click", () => {
    currentTab = "metadata";
    update(controller.getState());
    tabButtons.get("metadata").focus();
  });
  save.addEventListener("click", () => run(saveBook));
  analyze.addEventListener("click", () => controller.analyzeAudio().catch(message));
  cancelAnalysis.addEventListener("click", () => controller.analysis.dismiss().catch(message));
  undo.addEventListener("click", () => run(() => controller.undo()));
  selectCover.addEventListener("click", () =>
    run(() => controller.selectCover()),
  );
  removeCover.addEventListener("click", () =>
    run(() => controller.edit({ kind: "cover-remove" })),
  );
  cancel.addEventListener("click", () => controller.cancel().catch(message));
  selectPlaylist.addEventListener("click", () =>
    run(async () => {
      const path = await controller.selectSidecar("playlist");
      if (path) {
        playlistPath = path;
        playlist.textContent = path;
      }
    }),
  );
  selectCue.addEventListener("click", () =>
    run(async () => {
      const path = await controller.selectSidecar("cue");
      if (path) {
        cuePath = path;
        cue.textContent = path;
      }
    }),
  );
  clearPlaylist.addEventListener("click", () => {
    playlistPath = "";
    playlist.textContent = NO_PLAYLIST;
  });
  clearCue.addEventListener("click", () => {
    cuePath = "";
    cue.textContent = NO_CUE;
  });
  function renderProjects(list) {
    projectList.replaceChildren(
      element(document, "h3", "Saved projects", "card-title"),
    );
    if (!list?.length)
      projectList.append(element(document, "p", "No saved projects yet", "muted"));
    for (const project of list || []) {
      const open = element(document, "button", "", "project-row");
      open.type = "button";
      open.dataset.action = "open";
      open.dataset.projectId = project.id;
      open.append(
        element(document, "span", project.title || "(Untitled book)", "project-title"),
        element(document, "span", projectDate(project.updatedAt), "project-date"),
      );
      projectList.append(open);
      open.addEventListener("click", () =>
        switchBook(() => controller.open(project.id)).catch(message),
      );
    }
    projectList.hidden = false;
  }
  projects.addEventListener("click", () =>
    run(async () => {
      const list = await controller.listProjects();
      if (active) renderProjects(list);
    }),
  );
  function update(state) {
    markers.textContent = state.analysis?.media
      ? `Preview starts at ${formatTimeUs(state.analysis.media.originUs || 0)} in the book · marker at ${formatTimeUs(state.analysis.media.markerUs || 0)}`
      : state.preview && (state.preview.oldMarkerUs || state.preview.newMarkerUs)
        ? `Boundary in preview: ${formatTimeUs(state.preview.oldMarkerUs)} → ${formatTimeUs(state.preview.newMarkerUs)} (window starts at ${formatTimeUs(state.preview.originUs)})`
        : "";
    const analysisBusy = state.analysis?.previewing || ["analyzing", "awaitingDecision", "deciding"].includes(state.analysis?.status);
    const busy = localBusy || !!state.operation || state.repairBusy || analysisBusy,
      hasView = !!state.view,
      draft = metadata.isDirty(),
      switching = !!pendingSwitch;
    if (hasView && state.view.project.id !== shownProjectId) {
      // A different book arrived: leave the open sheet and its project list.
      openPanelShown = false;
      projectList.hidden = true;
    }
    shownProjectId = hasView ? state.view.project.id : null;
    for (const node of root.querySelectorAll("button,input,textarea,select"))
      if (!analysisPanel.contains(node)) node.disabled = busy || switching;
    for (const node of dialogButtons.values()) node.disabled = busy;
    // Moving between sections never changes the book, so it stays available.
    for (const tab of tabButtons.values()) tab.disabled = switching;
    toDetails.disabled = switching;
    // Background work can always be cancelled, and notices always answered.
    cancelAnalysis.disabled = false;
    for (const node of toasts.querySelectorAll("button")) node.disabled = false;
    const scanning = state.analysis?.origin === "book" && state.analysis.status === "analyzing";
    const remaining = stripEta(state.analysis);
    analysisStrip.hidden = !scanning;
    if (scanning) {
      const live = analysisProgress(state.analysis);
      stripText.textContent = `Analyzing audio — ${[live.label, live.step, remaining].filter(Boolean).join(" · ")}. Editing is paused until the scan finishes.`;
      stripTrack.classList.toggle("indeterminate", live.fraction === null);
      stripFill.style.width = live.fraction === null ? "" : `${live.fraction * 100}%`;
      stripPercent.textContent = live.percent;
    }
    const reported = state.analysis?.origin === "book" && state.analysis.report;
    analysisBadge.textContent = scanning ? "…" : reported ? String(reported.findings?.length || 0) : "";
    analysisBadge.classList.toggle("is-running", scanning);
    cancel.disabled =
      !["load", "preview", "suggest"].includes(state.operation?.kind) ||
      state.operation?.status === "cancelling";
    analyze.hidden = !hasView;
    analyze.disabled = busy || switching || !hasView || draft || !!state.exportOperation;
    save.disabled = busy || switching || !hasView;
    const hasMedia = !!audio.getAttribute("src");
    play.disabled = seek.disabled = busy || switching || !hasMedia;
    idle.hidden = hasMedia;
    audition.classList.toggle("has-media", hasMedia);
    refreshTransport();
    undo.disabled = busy || switching || !state.view?.canUndo || draft;
    selectCover.disabled = removeCover.disabled = busy || switching || !hasView;
    const fieldset = metadataPanel.querySelector("fieldset");
    if (fieldset) fieldset.disabled = busy || switching || !hasView;
    for (const input of metadataPanel.querySelectorAll("input,textarea,button"))
      input.disabled = busy || switching || !hasView;
    dirty.textContent = hasView
      ? draft
        ? "Unapplied edits"
        : state.view.dirty
          ? "Unsaved changes"
          : "Saved"
      : "";
    dirty.dataset.state = !hasView ? "" : draft ? "draft" : state.view.dirty ? "unsaved" : "saved";
    progress.hidden = !state.operation;
    cancel.hidden = !["load", "preview", "suggest"].includes(state.operation?.kind);
    cancel.textContent = state.operation?.kind === "preview" ? "Cancel preview" : state.operation?.kind === "suggest" ? "Cancel suggestions" : "Cancel import";
    status.textContent =
      state.operation?.message ||
      (state.operation ? busyLabels[state.operation.kind] || `${state.operation.kind}…` : "");
    // Operations that report totals, such as listening for pauses, show how far they are.
    const share = state.operation?.total > 0 ? Math.min(1, state.operation.current / state.operation.total) : null;
    busyTrack.hidden = share === null;
    busyFill.style.width = share === null ? "0%" : `${share * 100}%`;
    busyPercent.textContent = share === null ? "" : `${Math.round(share * 100)}%`;
    error.textContent = state.error || localError;
    retry.hidden = !state.error || !controller.getLoadInput();
    if (!retry.hidden) {
      choices.open = true;
      openPanelShown = true;
    }
    root.classList.toggle("has-book", hasView);
    openPanel.hidden = hasView && !openPanelShown;
    openPanel.classList.toggle("is-sheet", hasView);
    closeOpen.hidden = !hasView;
    openAnother.setAttribute("aria-expanded", String(hasView && openPanelShown));
    summary.hidden = tabs.hidden = !hasView;
    for (const [name, panel] of panels) panel.hidden = !hasView || currentTab !== name;
    draftNotice.hidden = !hasView || !draft || currentTab === "metadata";
    for (const [name, tab] of tabButtons) {
      tab.setAttribute("aria-selected", String(currentTab === name));
      tab.classList.toggle("active", currentTab === name);
      tab.tabIndex = currentTab === name ? 0 : -1;
    }
    chapters.setDisabled(busy || switching || draft);
    exports.setDisabled(busy || switching || draft);
    if (state.exportOperation) {
      for (const node of [folder,file,projects,retry,selectPlaylist,selectCue,clearPlaylist,clearCue,source]) node.disabled = true;
      for (const node of projectList.querySelectorAll("button")) node.disabled = true;
    }
    if (!hasView) return;
    const view = state.view;
    title.textContent = view.project.metadata.title || "(Untitled book)";
    const count = view.chapterRows?.length || 0;
    detail.textContent = [
      view.project.metadata.artist,
      `${count} ${count === 1 ? "chapter" : "chapters"}`,
      displayTimeUs(view.durationUs),
    ].filter(Boolean).join(" · ");
    chapterCount.textContent = String(count);
    warning.textContent = view.sourceWarning || "";
    for (const [image, empty] of [[cover, thumbEmpty], [coverLarge, coverEmpty]]) {
      image.hidden = !view.coverUrl;
      empty.hidden = !!view.coverUrl;
      if (view.coverUrl) image.src = view.coverUrl;
      else image.removeAttribute("src");
    }
  }
  const draftChanged = () => { controller.clearPreview(); update(controller.getState()); };
  const stopAnnouncing = controller.subscribe(announce);
  root.addEventListener("book-draft-change", draftChanged);
  const unsubscribe = controller.subscribe(update);
  const cleanup = () => {
    active = false;
    controller.clearPreview();
    player.dispose();
    for (const name of transportEvents) audio.removeEventListener(name, refreshTransport);
    unsubscribe();
    stopAnnouncing();
    for (const timer of toastTimers) clearTimeout(timer);
    analysisCleanup();
    metadata();
    chapters();
    exports();
    root.removeEventListener("book-draft-change", draftChanged);
    audition.remove();
    root.replaceChildren();
    root.classList.remove("book-root", "has-book");
  };
  // Lets the shell fill the start screen with saved projects without
  // surfacing an error when none can be listed.
  cleanup.showProjects = async () => {
    try {
      const list = await controller.listProjects();
      if (active && list?.length && !controller.getState().view) renderProjects(list);
    } catch { /* The explicit "Saved projects" button reports failures. */ }
  };
  return cleanup;
}
