import { element } from "./metadata.js";
import { icon } from "./icons.js";

export const exportDefaults = Object.freeze({
  profile: "standard",
  channels: "auto",
  normalize: false,
  outputMode: "book",
  format: "m4b",
});

export function exportExplanation(settings) {
  const lossless = ["flac", "wav"].includes(settings.format);
  const rates = { economy: "48/80", standard: "64/96", high: "96/160" };
  const parts = [lossless
    ? "AAC bitrate is not applicable to lossless FLAC/WAV."
    : `AAC ${rates[settings.profile] || rates.standard} kb/s mono/stereo at 44.1 kHz.`];
  if (settings.profile === "preserve")
    parts.push("Preserve copies compatible AAC only when edits and settings allow it; otherwise a safe re-encode fallback is reported.");
  if (settings.format === "m4b" && settings.outputMode !== "book")
    parts.push(settings.outputMode === "both"
      ? "book.m4b plus chapters/*.m4a." : "Chapters are .m4a files.");
  if (lossless && settings.outputMode !== "chapters")
    parts.push("FLAC/WAV books do not contain the full M4B chapter table.");
  if (settings.format === "wav") parts.push("WAV does not store cover artwork.");
  parts.push("Whole-book loudness normalization targets −18 LUFS and at most −2 dBTP using constant gain without a limiter; the actual result is reported after export.");
  return parts.join(" ");
}

// The same "<title>-<author>" proposal the save dialog makes for a single
// file, for the folder that holds per-chapter files.
export function suggestedExportName(metadata = {}) {
  const clean = text => String(text || "").replace(/[\u0000-\u001f\u007f/\\:*?"<>|]/g, " ").trim().split(/\s+/).filter(Boolean).join(" ");
  const name = [clean(metadata.title), clean(metadata.artist)].filter(Boolean).join("-").replace(/[. ]+$/, "").slice(0, 150).replace(/[. ]+$/, "");
  return name || "Book export";
}

const optionLabels = {
  profile: { preserve: "Preserve original audio", economy: "Economy", standard: "Standard", high: "High" },
  channels: { auto: "Automatic", mono: "Mono", stereo: "Stereo" },
  outputMode: { book: "One audiobook file", chapters: "One file per chapter", both: "Audiobook and chapter files" },
  format: { m4b: "M4B audiobook", m4a: "M4A", flac: "FLAC (lossless)", wav: "WAV (lossless)" },
};

export function mountExport(root, controller) {
  const document = root.ownerDocument;
  let active = true, busy = false, current = null, externalDisabled = false, namedFor = null, renamed = false;
  const intro = element(document, "div", "", "panel-intro");
  const introCopy = element(document, "div");
  introCopy.append(
    element(document, "h3", "Export book"),
    element(document, "p", "Writes a new audiobook from your edits. The original files stay untouched."),
  );
  intro.append(introCopy);
  const grid = element(document, "div", "", "field-grid");
  root.append(intro, grid);
  const fields = new Map();
  for (const [name, label, values] of [
    ["format", "Format", ["m4b", "m4a", "flac", "wav"]],
    ["outputMode", "Output", ["book", "chapters", "both"]],
    ["profile", "Quality", ["preserve", "economy", "standard", "high"]],
    ["channels", "Channels", ["auto", "mono", "stereo"]],
  ]) {
    const row = element(document, "label", "", "field");
    row.append(element(document, "span", label, "field-label"));
    const select = element(document, "select", "", "field-input");
    select.name = name;
    for (const value of values) {
      const option = element(document, "option", optionLabels[name][value]);
      option.value = value;
      select.append(option);
    }
    row.append(select);
    grid.append(row);
    fields.set(name, select);
    select.addEventListener("change", submitSettings);
  }
  const normalizeRow = element(document, "label", "", "check-row");
  const normalize = element(document, "input");
  normalize.type = "checkbox";
  normalize.name = "normalize";
  const normalizeCopy = element(document, "span");
  normalizeCopy.append(
    element(document, "span", "Normalize whole-book loudness", "check-title"),
    element(document, "span", "One constant gain towards −18 LUFS, never above −2 dBTP", "check-hint"),
  );
  normalizeRow.append(normalize, normalizeCopy);
  root.append(normalizeRow);
  normalize.addEventListener("change", submitSettings);

  function submitSettings() {
    // Capture the user input before run() renders the committed busy state.
    const requested = {
      ...current,
      ...Object.fromEntries([...fields].map(([key, node]) => [key, node.value])),
      normalize: normalize.checked,
    };
    return run(() => controller.edit({ kind: "export", export: requested }));
  }
  const explanation = element(document, "p", "", "hint-box");
  root.append(explanation);
  const directoryRow = element(document, "label", "", "field");
  directoryRow.append(element(document, "span", "New export folder name", "field-label"));
  const directory = element(document, "input", "", "field-input");
  directory.name = "directoryName";
  directory.value = "Book export";
  directory.addEventListener("input", () => { renamed = true; });
  directoryRow.append(directory);
  root.append(directoryRow);
  const actions = element(document, "div", "", "action-row form-actions");
  const start = element(document, "button", "Choose destination and export", "btn btn-accent");
  start.type = "button";
  start.dataset.action = "export";
  start.prepend(icon(document, "export"));
  const cancel = element(document, "button", "Cancel export", "btn btn-ghost");
  cancel.type = "button";
  cancel.dataset.action = "cancel-export";
  actions.append(start, cancel);
  const running = element(document, "div", "", "export-progress");
  const progress = element(document, "p");
  progress.setAttribute("role", "status");
  const track = element(document, "div", "", "progress-track");
  const fill = element(document, "div", "", "progress-fill");
  track.append(fill);
  running.append(progress, track);
  const result = element(document, "pre", "", "result-box");
  result.setAttribute("aria-live", "polite");
  const error = element(document, "p", "", "book-error banner");
  error.setAttribute("role", "alert");
  root.append(actions, running, result, error);

  async function run(action) {
    if (busy) return;
    busy = true;
    error.textContent = "";
    update(controller.getState());
    try {
      return await action();
    } catch (e) {
      if (active) error.textContent = e?.message || String(e);
    } finally {
      busy = false;
      if (active) update(controller.getState());
    }
  }
  start.addEventListener("click", () => run(async () => {
    const path = await controller.selectExportTarget(directory.value);
    if (path) await controller.exportProject(path);
  }));
  cancel.addEventListener("click", () => controller.cancel().catch(e => {
    if (active) error.textContent = e?.message || String(e);
  }));

  function update(state) {
    root.hidden = !state.view;
    current = state.view?.project.export || exportDefaults;
    // Propose a name for each book once; a name the user typed is kept.
    if (state.view && state.view.project.id !== namedFor) {
      namedFor = state.view.project.id;
      renamed = false;
    }
    if (state.view && !renamed) directory.value = suggestedExportName(state.view.project.metadata);
    const editingDisabled = busy || externalDisabled || !!state.operation || !state.view;
    for (const [key, node] of fields) {
      const selected = [...node.options].find(option => option.value === current[key]);
      if (selected) selected.selected = true;
      node.disabled = editingDisabled;
    }
    normalize.checked = current.normalize;
    normalize.disabled = editingDisabled;
    explanation.textContent = exportExplanation(current);
    directoryRow.hidden = current.outputMode === "book";
    directory.disabled = editingDisabled || !!state.exportOperation;
    start.disabled = editingDisabled || !!state.exportOperation;
    cancel.hidden = !state.exportOperation;
    cancel.disabled = state.exportOperation?.status === "cancelling";
    const op = state.exportOperation;
    progress.textContent = op
      ? `Exporting snapshot revision ${op.revision}${state.view?.revision !== op.revision ? ` (current revision ${state.view.revision})` : ""}: ${op.message || op.status}${op.total ? ` — ${op.current}/${op.total}` : ""}`
      : "";
    running.hidden = !op;
    track.classList.toggle("indeterminate", !!op && !op.total);
    fill.style.width = op?.total ? `${Math.min(100, (op.current / op.total) * 100)}%` : "0%";
    const done = state.exportResult;
    result.textContent = done ? [
      `Snapshot revision ${done.revision}: ${done.message || done.status}`,
      ...(done.paths || []), done.normalizationReport || "", done.error || "",
    ].filter(Boolean).join("\n") : "";
    result.hidden = !done;
    result.dataset.status = done?.status || "";
  }
  const unsubscribe = controller.subscribe(update);
  const dispose = () => {
    active = false;
    unsubscribe();
    root.replaceChildren();
  };
  dispose.setDisabled = value => {
    externalDisabled = value;
    update(controller.getState());
  };
  return dispose;
}
