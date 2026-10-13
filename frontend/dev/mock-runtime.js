// Browser-only stand-in for @wailsio/runtime, used by `npm run dev:mock` to
// exercise the interface without the native shell. Never bundled for release.
const listeners = new Map();
const emit = (name, data) => { for (const fn of listeners.get(name) || []) fn({ data }); };
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const SEC = 1000000;
const params = new URLSearchParams(location.search);

function tone(seconds = 12) {
  const rate = 8000, n = rate * seconds, buffer = new ArrayBuffer(44 + n * 2), v = new DataView(buffer);
  const text = (o, s) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  text(0, "RIFF"); v.setUint32(4, 36 + n * 2, true); text(8, "WAVEfmt "); v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); v.setUint16(22, 1, true); v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true); v.setUint16(34, 16, true); text(36, "data"); v.setUint32(40, n * 2, true);
  for (let i = 0; i < n; i++) v.setInt16(44 + i * 2, Math.sin(i / 9) * 2500 * Math.sin((i / n) * Math.PI), true);
  return URL.createObjectURL(new Blob([buffer], { type: "audio/wav" }));
}
const coverUrl = "data:image/svg+xml," + encodeURIComponent(
  `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="400"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#2b3a8f"/><stop offset="1" stop-color="#b0457a"/></linearGradient></defs><rect width="400" height="400" fill="url(#g)"/><text x="32" y="330" font-family="Georgia" font-size="44" fill="#fff">Monster</text><text x="32" y="376" font-family="Georgia" font-size="44" fill="#fff">Makers</text></svg>`);

const titles = ["Opening Credits", "Prologue", "The Workshop", "A Stranger at the Gate", "What the Ledger Said", "Night Shift", "The First Monster", "Smoke and Brass", "An Honest Mistake", "The Guild Decides", "Run", "Epilogue"];
let revision = 1, saved = false, undoStack = [], seq = 0;
let project = {
  id: "book-1", schemaVersion: 1,
  metadata: { title: "Monster Makers", artist: "E.A. Winters", composer: "Ralph Lister", album: "Monster Makers, Book 1", albumArtist: "E.A. Winters", genre: "Science Fiction & Fantasy", date: "2026-09-15", copyright: "©2026 Aethon Audio", comment: "", description: "When the last monster-maker in the city takes on an apprentice, neither of them expects the work to follow them home.", subtitle: "", grouping: "", extra: { series: "Monster Makers", "series-part": "1", language: "english" } },
  sources: [], chapters: [], coverPath: "cover", export: { profile: "standard", channels: "auto", normalize: false, outputMode: "book", format: "m4b" },
};
let rows = titles.map((title, i) => ({ id: `c${i + 1}`, title, durationUs: (i === 0 ? 42 : 1500 + ((i * 367) % 900)) * SEC + (i % 3) * 250000 }));
let hasCover = true;
const view = () => {
  let at = 0;
  const chapterRows = rows.map((row) => { const out = { ...row, startUs: at }; at += row.durationUs; return out; });
  return structuredClone({ project, revision, dirty: !saved, canUndo: undoStack.length > 0, durationUs: at, chapterRows, coverUrl: hasCover ? coverUrl : "", sourceWarning: "" });
};
const snapshot = () => undoStack.push(structuredClone({ project, rows, hasCover }));
const bump = () => { revision++; saved = false; return view(); };
const report = (origin) => ({
  id: "report-1", durationUs: 21000 * SEC, scannedUs: 21000 * SEC, repeatScannedUs: 21000 * SEC, detectorVersion: "repair-analysis-v1",
  coverage: [{ detector: "repetition", complete: true, scannedUs: 21000 * SEC }, { detector: "clipping", complete: true, scannedUs: 21000 * SEC }, { detector: "quiet", complete: true, scannedUs: 21000 * SEC }],
  counts: { repetition: 1, quiet: 1, timestamp_discontinuity: 1 }, limitations: [],
  sources: [{ sourceId: "s1", name: "Monster Makers.m4b", sizeBytes: 421338112, sha256: "9f2c4e0d7a51b6c3e8f0a1d2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6" }],
  findings: [
    { id: "f1", kind: "repetition", severity: "warning", detail: "12.5 s of audio appears twice.", startUs: 3125 * SEC, endUs: 3137 * SEC + 500000, secondStartUs: 3140 * SEC, secondEndUs: 3152 * SEC + 500000, sourceId: "s1", sourceStartUs: 3125 * SEC, sourceEndUs: 3137 * SEC + 500000, verification: "pcm16-exact" },
    { id: "f2", kind: "quiet", severity: "info", detail: "8.2 s below −60 dBFS.", startUs: 9001 * SEC, endUs: 9009 * SEC + 200000, sourceId: "s1", sourceStartUs: 9001 * SEC, sourceEndUs: 9009 * SEC + 200000 },
    { id: "f3", kind: "timestamp_discontinuity", severity: "info", detail: "Packet timestamps step back by 1024 samples.", startUs: null, sourceId: "s1", sourceStartUs: 14400 * SEC, sourceEndUs: 14400 * SEC + 23220, originalTimestampUs: 14400 * SEC },
  ],
});

const book = {
  async SelectBookInput() { return "/Users/demo/Audiobooks/Monster Makers"; },
  async SelectSidecar(kind) { return `/Users/demo/Audiobooks/Monster Makers/book.${kind === "cue" ? "cue" : "m3u"}`; },
  async LoadBook(requestId) {
    emit("book-operation", { requestId, kind: "load", status: "running", message: "Reading 12 audio files…" });
    await wait(params.has("slow") ? 60000 : 900);
    if (params.get("fail") === "load") throw new Error("No audio files were found in /Users/demo/Audiobooks/Monster Makers");
    revision++; saved = false; return view();
  },
  async OpenProject() { await wait(250); revision++; saved = true; return view(); },
  async ListProjects() { return [{ id: "book-1", title: "Monster Makers", updatedAt: "2026-10-10T18:42:00Z" }, { id: "book-2", title: "The Long Way to a Small, Angry Planet", updatedAt: "2026-10-08T09:15:00Z" }, { id: "book-3", title: "", updatedAt: "2026-09-30T21:03:00Z" }]; },
  async SaveProject() { await wait(200); saved = true; return view(); },
  async Undo() { const prior = undoStack.pop(); if (prior) ({ project, rows, hasCover } = prior); return bump(); },
  async SelectCover() { snapshot(); hasCover = true; return bump(); },
  async ApplyEdit(_, edit) {
    snapshot();
    const index = rows.findIndex((row) => row.id === edit.chapterId);
    if (edit.kind === "metadata") project = { ...project, metadata: edit.metadata };
    if (edit.kind === "export") project = { ...project, export: edit.export };
    if (edit.kind === "cover-remove") hasCover = false;
    if (edit.kind === "rename") rows[index] = { ...rows[index], title: edit.title };
    if (edit.kind === "move") rows.splice(edit.destination, 0, ...rows.splice(index, 1));
    if (edit.kind === "pauses") rows = rows.map((row, i) => (i < rows.length - 1 ? { ...row, durationUs: row.durationUs + edit.durationUs } : row));
    if (edit.kind === "merge") { rows[index - 1] = { ...rows[index - 1], durationUs: rows[index - 1].durationUs + rows[index].durationUs }; rows.splice(index, 1); }
    if (edit.kind === "split") for (const at of [...edit.positionsUs].sort((a, b) => b - a)) { const now = view().chapterRows, i = now.findIndex((r) => at > r.startUs && at < r.startUs + r.durationUs); if (i < 0) throw new Error(`split position ${at} is not inside a chapter`); const head = at - now[i].startUs; rows.splice(i, 1, { ...rows[i], durationUs: head }, { id: `n${++seq}`, title: "New chapter", durationUs: rows[i].durationUs - head }); }
    if (edit.kind === "silence") rows[index] = { ...rows[index], durationUs: rows[index].durationUs + edit.durationUs };
    if (edit.kind === "boundary") { const delta = edit.newStartUs - view().chapterRows[index].startUs; rows[index] = { ...rows[index], durationUs: rows[index].durationUs - delta }; if (edit.mode === "transfer") rows[index - 1] = { ...rows[index - 1], durationUs: rows[index - 1].durationUs + delta }; }
    await wait(120); return bump();
  },
  async PreviewChapter(requestId, rev) { await wait(500); return { requestId, revision: rev, projectId: project.id, mediaId: `m${++seq}`, url: tone(), durationUs: 12 * SEC, originUs: 0, oldMarkerUs: 0, newMarkerUs: 0 }; },
  async PreviewBoundary(requestId, rev, proposal) { await wait(500); return { requestId, revision: rev, projectId: project.id, mediaId: `m${++seq}`, url: tone(10), durationUs: 10 * SEC, originUs: Math.max(0, proposal.oldStartUs - 5 * SEC), oldMarkerUs: 5 * SEC, newMarkerUs: 5 * SEC + (proposal.newStartUs - proposal.oldStartUs) }; },
  async ReleaseMedia() {},
  async SuggestBoundaries(requestId, rev, opts = {}) {
    await wait(900);
    const current = view().chapterRows;
    return { requestId, revision: rev, projectId: project.id, proposals: params.get("suggest") === "none" ? [] : [2, 4, 7].map((i, n) => ({ chapterId: current[i].id, oldStartUs: current[i].startUs, newStartUs: current[i].startUs + [1, -2, 3][n] * 420000 })).filter((proposal) => !opts.chapterId || proposal.chapterId === opts.chapterId) };
  },
  async SuggestSplits(requestId, rev) {
    const base = { requestId, kind: "suggest", status: "running", projectId: project.id, revision: rev, message: "Listening for pauses" };
    for (let i = 1; i <= 20; i++) { await wait(params.has("slow") ? 1500 : 120); emit("book-operation", { ...base, current: i, total: 20 }); }
    const total = view().durationUs;
    return { requestId, revision: rev, projectId: project.id, proposals: params.get("suggest") === "none" ? [] : [0.07, 0.19, 0.33, 0.52, 0.71, 0.88].map((share, n) => ({ atUs: Math.floor(total * share), silenceUs: [2.4, 3.1, 2.0, 4.6, 2.2, 3.8][n] * SEC })) };
  },
  async MeasureChapterPauses(requestId, rev) { await wait(600); return { requestId, revision: rev, projectId: project.id, pauses: view().chapterRows.slice(1).map((row, i) => ({ chapterId: row.id, beforeUs: i % 3 ? 800000 : 0, afterUs: i % 2 ? 400000 : 0 })) }; },
  async PreviewPosition(requestId, rev, atUs) { await wait(200); return { requestId, revision: rev, projectId: project.id, mediaId: `m${++seq}`, url: tone(10), durationUs: 10 * SEC, originUs: Math.max(0, atUs - 5 * SEC), oldMarkerUs: 5 * SEC, newMarkerUs: 5 * SEC }; },
  async ApplyBoundaries() { snapshot(); return bump(); },
  async SelectExportTarget() { return "/Users/demo/Audiobooks/Monster Makers.m4b"; },
  async StartExport(requestId, rev, outputPath) {
    const base = { requestId, taskId: 7, projectId: project.id, revision: rev, kind: "export" };
    (async () => {
      for (let i = 1; i <= 12; i++) { await wait(params.has("slow") ? 4000 : 350); emit("book-log", { requestId, taskId: 7, line: `Encoding chapter ${i}/12: ${titles[i - 1]}` }); emit("book-operation", { ...base, status: "running", message: "Encoding audio", current: i, total: 12 }); }
      emit("book-operation", { ...base, status: "succeeded", message: "Export finished", paths: [outputPath], normalizationReport: project.export.normalize ? "Loudness −23.4 → −18.0 LUFS, true peak −3.1 dBTP (gain +5.4 dB)" : "" });
    })();
    return base;
  },
  async GetOperation(requestId) { return { requestId, taskId: 7, projectId: project.id, revision, kind: "export", status: "running", message: "Preparing" }; },
  async CancelOperation(requestId) { emit("book-operation", { requestId, status: "cancelled" }); },
  async AnalyzeAudio(requestId, rev) {
    const base = { requestId, kind: "analyze", status: "running", projectId: project.id, revision: rev };
    for (const [message, steps] of [["hash-before", 2], ["decode", 24], ["repeat-verify", 6], ["hash-after", 2]])
      for (let i = 1; i <= steps; i++) { await wait(params.has("slow") ? 1500 : 220); emit("book-operation", { ...base, message, current: i, total: steps }); }
    return { requestId, origin: "book", revision: rev, projectId: project.id, report: report("book") };
  },
  async PreviewAudioFinding(requestId, rev, reportId, findingId, occurrence) { await wait(400); return { requestId, reportId, mediaId: `m${++seq}`, url: tone(8), durationUs: 8 * SEC, originUs: (occurrence ? 3138 : 3123) * SEC, markerUs: 2 * SEC }; },
  async ReleaseAudioAnalysis() {},
};
let repairTask = 100;
const merger = {
  async LegalNotice() { return "This software uses libraries from the FFmpeg project (https://ffmpeg.org) under the LGPLv2.1. FFmpeg source code and build instructions: https://github.com/ChapterSmith/chaptersmith/releases"; },
  async GetHistory() {
    return [
      { id: 7, outputPath: "/Users/demo/Audiobooks/Monster Makers.m4b", startedAt: "2026-10-11T09:12:00Z", completedAt: "2026-10-11T09:14:31Z", success: true, projectSnapshot: JSON.stringify({ title: "Monster Makers", export: project.export }) },
      { id: 6, outputPath: "/Users/demo/Audiobooks/Old Recording_fixed.m4b", startedAt: "2026-10-10T17:40:00Z", completedAt: "2026-10-10T17:52:10Z", success: true },
      { id: 5, outputPath: "/Volumes/Backup/Big Book.flac", startedAt: "2026-10-09T21:05:00Z", completedAt: "2026-10-09T21:05:04Z", success: false, errorMessage: "destination volume is read-only" },
    ];
  },
  async GetTaskLogs() { return ["Loading book", "Encoding chapter 1/12", "Encoding chapter 2/12", "Export finished"]; },
  async SelectOutputFile(name) { return `/Users/demo/Audiobooks/${name}`; },
  async StartMerge() { ++repairTask; setTimeout(() => emit("task-prompt-fix", { promptId: `p${repairTask}`, filename: "Old Recording.m4b" }), 300); },
  async RespondFixPromptOptions(_, choice, analyze) {
    if (choice === "fix" && analyze) return "report-1";
    setTimeout(async () => { for (const phase of ["Reading chapters", "Converting to AAC", "Finalizing M4B"]) { emit("task-progress", { phase, message: phase }); await wait(900); } emit("task-done", { taskID: repairTask, success: choice !== "stop" }); }, 100);
    return "";
  },
  async GetRepairAnalysis(reportId) { await wait(700); return { reportId, taskId: repairTask, status: "awaitingDecision", result: { origin: "repair", report: report("repair") } }; },
  async PreviewRepairFinding(reportId) { return { reportId, mediaId: `m${++seq}`, url: tone(8), durationUs: 8 * SEC, originUs: 3123 * SEC, markerUs: 2 * SEC }; },
  async ReleaseRepairMedia() {},
  async ConfirmRepairAnalysis() { setTimeout(() => emit("task-done", { taskID: repairTask, success: true }), 1500); },
  async CancelMerge() { emit("task-done", { taskID: repairTask, success: false }); },
};

export const Call = {
  async ByName(name, ...args) {
    const [service, method] = name.split(".").slice(-2);
    const target = service === "BuildInfoService"
      ? { BuildInfo: () => ({ version: "development preview", buildTime: "not a release build" }) }
      : service === "BookService" ? book : merger;
    if (!target[method]) throw new Error(`mock: ${service}.${method} is not implemented`);
    return target[method](...args);
  },
};
export const Events = {
  On(name, handler) {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name).add(handler);
    return () => listeners.get(name).delete(handler);
  },
};
