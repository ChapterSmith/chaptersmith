import { Call, Events } from "@wailsio/runtime";
import { createBookAPI } from "./book/api.js";
import { createBookController } from "./book/controller.js";
import { mountBookEditor } from "./book/view.js";
import { mountRepair } from "./book/analysis-view.js";

const mergerCall = (method, ...args) =>
  Call.ByName(
    `chaptersmith/internal/gui.MergerService.${method}`,
    ...args,
  );
const bookAPI = createBookAPI(Call, Events);
const eventData = (event) =>
  Array.isArray(event?.data) ? event.data[0] : (event?.data ?? event);
const onMerger = (name, handler) => Events.On(name, event => handler(eventData(event)));
const repairAPI = {
  getRepair: id => mergerCall("GetRepairAnalysis", id),
  previewRepair: (...args) => mergerCall("PreviewRepairFinding", ...args),
  confirmRepair: (...args) => mergerCall("ConfirmRepairAnalysis", ...args),
  releaseRepairMedia: (...args) => mergerCall("ReleaseRepairMedia", ...args),
  cancelRepair: () => mergerCall("CancelMerge"),
  respondFixPrompt: (...args) => mergerCall("RespondFixPromptOptions", ...args),
  onRepair: handler => onMerger("repair-analysis", handler),
};
const controller = createBookController({ api: bookAPI, analysisAPI: repairAPI });
const cleanups = [
  mountBookEditor(document.getElementById("bookEditor"), controller, {
    playerHost: document.getElementById("playerDock"),
  }),
  mountRepair(document.getElementById("repairLauncher"), controller, {
    ...repairAPI,
    selectInput: () => bookAPI.call("SelectBookInput", "file"),
    selectOutput: name => mergerCall("SelectOutputFile", name),
    startMerge: (...args) => mergerCall("StartMerge", ...args),
    onPrompt: handler => onMerger("task-prompt-fix", handler),
    onProgress: handler => onMerger("task-progress", handler),
    onDone: handler => onMerger("task-done", handler),
  }),
];
let disposed = false;
cleanups[0].showProjects?.();

mergerCall("LegalNotice")
  .then((text) => {
    if (!disposed) document.getElementById("legalNotice").textContent = text;
  })
  .catch((error) => console.error("LegalNotice failed", error));

Call.ByName("chaptersmith/internal/gui.BuildInfoService.BuildInfo")
  .then(({ version, buildTime }) => {
    if (!disposed) document.getElementById("buildInfo").textContent =
      `Version ${version} · Built ${buildTime}`;
  })
  .catch((error) => console.error("BuildInfo failed", error));

const historyList = document.getElementById("historyList");
const logView = document.getElementById("logView");
const logCard = document.getElementById("logCard");
const logToggle = document.getElementById("logToggle");
const logModeBtn = document.getElementById("logModeBtn");
const logLineCount = document.getElementById("logLineCount");
const logModal = document.getElementById("logModal");
const modalTitle = document.getElementById("modalTitle");
const modalLogContent = document.getElementById("modalLogContent");
const allLogLines = [];
let fullLogMode = false,
  historyRequest = 0;
const NOISE_RE =
  /^\[(?:mov|mp4|m4a|3gp|3g2|mj2|aac|h264|hevc|opus|flac|vorbis)[, @]|^\[aac @|Qavg:|frames left in the queue|^\s*\d+%\s*\|/;
const isNoisyLine = (text) => !text?.trim() || NOISE_RE.test(text);

function appendLogNode(entry) {
  const line = document.createElement("div");
  line.className = "log-line" + (entry.isError ? " error" : "");
  line.textContent = entry.text;
  logView.append(line);
}
function updateLogLineCount() {
  logLineCount.textContent = !allLogLines.length
    ? ""
    : fullLogMode
      ? `${allLogLines.length} lines`
      : `${logView.childElementCount}/${allLogLines.length} lines`;
}
function renderLog() {
  logView.replaceChildren();
  const lines = fullLogMode
    ? allLogLines
    : allLogLines.filter((line) => !isNoisyLine(line.text));
  for (const entry of lines) appendLogNode(entry);
  logView.scrollTop = logView.scrollHeight;
  updateLogLineCount();
}
function appendLog(text, isError = false) {
  if (disposed) return;
  const entry = { text, isError };
  allLogLines.push(entry);
  if (fullLogMode || !isNoisyLine(text)) {
    appendLogNode(entry);
    logView.scrollTop = logView.scrollHeight;
  }
  updateLogLineCount();
}
logToggle.addEventListener("click", () => {
  logCard.classList.toggle("open");
  logToggle.setAttribute(
    "aria-expanded",
    String(logCard.classList.contains("open")),
  );
  if (logCard.classList.contains("open")) logView.scrollTop = logView.scrollHeight;
});
logModeBtn.addEventListener("click", () => {
  fullLogMode = !fullLogMode;
  logModeBtn.textContent = fullLogMode ? "Full" : "Simple";
  renderLog();
});
cleanups.push(
  bookAPI.onLog((data) => {
    const state = controller.getState();
    const op = state.exportOperation || state.exportResult;
    if (data?.line && op?.requestId === data.requestId && (!op.taskId || op.taskId === data.taskId))
      appendLog(data.line, /^ERROR:|history.*error/i.test(data.line));
  }),
);
cleanups.push(
  bookAPI.onOperation((status) => {
    if (status?.error || status?.status === "failed")
      appendLog(status.error || status.message || "Operation failed", true);
    if (["succeeded", "failed", "cancelled"].includes(status?.status))
      loadHistory();
  }),
);
// History and log events remain available for tasks produced by the legacy CLI.
cleanups.push(
  Events.On("task-log", (event) => {
    const data = eventData(event);
    if (data?.line) appendLog(data.line, /^ERROR:/i.test(data.line));
  }),
);
cleanups.push(Events.On("task-done", () => loadHistory()));

function formatDate(iso) {
  if (!iso) return "";
  const date = new Date(iso);
  return Number.isNaN(date.valueOf())
    ? iso
    : date.toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
}
async function loadHistory() {
  const request = ++historyRequest;
  try {
    const entries = await mergerCall("GetHistory", 30);
    if (disposed || request !== historyRequest) return;
    historyList.replaceChildren();
    if (!entries?.length) {
      const empty = document.createElement("div");
      empty.className = "history-empty";
      empty.textContent = "No previous tasks";
      historyList.append(empty);
    }
    for (const entry of entries || []) {
      const item = document.createElement("div");
      item.className = "history-item";
      const running = !entry.completedAt;
      const name = (entry.outputPath || "").replace(/^.*[/\\]/, "");
      const badge = document.createElement("div");
      badge.className =
        "status-badge " + (running ? "running" : entry.success ? "ok" : "fail");
      badge.textContent = running ? "…" : entry.success ? "OK" : "!";
      const info = document.createElement("div");
      info.className = "history-info";
      const title = document.createElement("div");
      title.className = "history-title";
      title.textContent = name || "Unknown";
      const meta = document.createElement("div");
      meta.className = "history-meta";
      meta.textContent =
        formatDate(entry.startedAt) +
        (entry.errorMessage ? " — " + entry.errorMessage : "");
      if (entry.projectSnapshot) {
        try {
          const snapshot = JSON.parse(entry.projectSnapshot);
          const details = document.createElement("details");
          const summary = document.createElement("summary"); summary.textContent = "Export snapshot";
          const content = document.createElement("pre");
          content.textContent = JSON.stringify(snapshot, null, 2);
          details.append(summary, content); info.append(details);
        } catch { /* Old history remains readable if a snapshot is unavailable. */ }
      }
      info.prepend(title, meta);
      const button = document.createElement("button");
      button.className = "btn btn-ghost btn-sm";
      button.textContent = "View log";
      button.addEventListener("click", () => showTaskLog(entry.id, name));
      item.append(badge, info, button);
      historyList.append(item);
    }
  } catch (error) {
    appendLog("Failed to load history: " + (error?.message || error), true);
  }
}
let modalFocus = null;
async function showTaskLog(id, name) {
  try {
    const lines = await mergerCall("GetTaskLogs", id);
    if (disposed) return;
    modalTitle.textContent = "Log: " + (name || "Task #" + id);
    modalLogContent.textContent =
      (lines || [])
        .map((line) => (typeof line === "string" ? line : line.message))
        .join("\n") || "(no log lines)";
  } catch (error) {
    modalTitle.textContent = "Error";
    modalLogContent.textContent =
      "Failed to load logs: " + (error?.message || error);
  }
  if (disposed) return;
  modalFocus = document.activeElement;
  logModal.classList.add("open");
  document.getElementById("modalClose").focus();
}
function closeLogModal() {
  logModal.classList.remove("open");
  modalFocus?.focus();
}
document.getElementById("modalClose").addEventListener("click", closeLogModal);
logModal.addEventListener("click", (event) => {
  if (event.target === logModal) closeLogModal();
});
logModal.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeLogModal();
  if (event.key === "Tab") {
    event.preventDefault();
    document.getElementById("modalClose").focus();
  }
});

const aboutModal = document.getElementById("aboutModal");
const aboutClose = document.getElementById("aboutClose");
let aboutFocus = null;
function closeAbout() {
  aboutModal.classList.remove("open");
  aboutFocus?.focus();
}
document.getElementById("aboutBtn").addEventListener("click", () => {
  aboutFocus = document.activeElement;
  aboutModal.classList.add("open");
  aboutClose.focus();
});
aboutClose.addEventListener("click", closeAbout);
aboutModal.addEventListener("click", (event) => {
  if (event.target === aboutModal) closeAbout();
});
aboutModal.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeAbout();
  if (event.key === "Tab") {
    event.preventDefault();
    aboutClose.focus();
  }
});

const tabButtons = [...document.querySelectorAll(".tab-btn")];
function selectTab(button) {
  for (const tab of tabButtons) {
    const selected = tab === button;
    tab.classList.toggle("active", selected);
    tab.setAttribute("aria-selected", String(selected));
    document
      .getElementById("tab-" + tab.dataset.tab)
      .classList.toggle("active", selected);
  }
  if (button.dataset.tab === "history") loadHistory();
}
for (const [index, button] of tabButtons.entries()) {
  button.addEventListener("click", () => selectTab(button));
  button.addEventListener("keydown", (event) => {
    if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
    event.preventDefault();
    const next =
      tabButtons[
        (index + (event.key === "ArrowRight" ? 1 : -1) + tabButtons.length) %
          tabButtons.length
      ];
    selectTab(next);
    next.focus();
  });
}
document.addEventListener("app-show-tab", (event) => {
  const target = tabButtons.find((tab) => tab.dataset.tab === event.tab);
  if (target && !target.classList.contains("active")) selectTab(target);
});
window.addEventListener(
  "pagehide",
  () => {
    disposed = true;
    for (const cleanup of cleanups) cleanup();
    controller.dispose();
  },
  { once: true },
);
loadHistory();
