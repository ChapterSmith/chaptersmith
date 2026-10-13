export function initialState() {
  return {
    view: null,
    analysis: null,
    repairBusy: false,
    operation: null,
    exportOperation: null,
    exportResult: null,
    preview: null,
    suggestions: null,
    splits: null,
    error: "",
  };
}

export function reduce(state, action) {
  const op = state.operation;
  switch (action.type) {
    case "repair-busy":
      return {...state, repairBusy: action.busy};
    case "analysis-state":
      return {...state, analysis: action.analysis};
    case "export-start":
      return {...state, exportOperation: {requestId: action.requestId, projectId: state.view.project.id, revision: state.view.revision, kind: "export", status: "running"}, exportResult: null, error: ""};
    case "export-ack": {
      const active = state.exportOperation, started = action.started;
      if (!active || started.requestId !== active.requestId || started.projectId !== active.projectId || started.revision !== active.revision) return state;
      return {...state, exportOperation: {...active, taskId: started.taskId}};
    }
    case "export-warning":
      if (state.exportOperation?.requestId !== action.requestId) return state;
      return {...state, error: action.error};
    case "export-error":
      if (state.exportOperation?.requestId !== action.requestId) return state;
      return {...state, exportOperation: null, error: action.error};
    case "export-status": {
      const active = state.exportOperation, status = action.status;
      if (!active || status?.requestId !== active.requestId || status.kind !== "export" ||
          status.projectId !== active.projectId || status.revision !== active.revision ||
          (active.taskId && status.taskId !== active.taskId)) return state;
      if (["succeeded", "failed", "cancelled"].includes(status.status))
        return {...state, exportOperation: null, exportResult: {...active, ...status}, error: status.error || (status.status === "cancelled" ? "Export cancelled" : "")};
      return {...state, exportOperation: {...active, ...status}};
    }
    case "preview-clear":
      return state.preview ? { ...state, preview: null } : state;
    case "suggestions-clear":
      return state.suggestions || state.splits ? { ...state, suggestions: null, splits: null } : state;
    case "preview-result":
    case "splits-result":
    case "suggestions-result":
      if (!op || action.requestId !== op.requestId ||
          action.result.requestId !== op.requestId || action.result.revision !== state.view?.revision ||
          action.result.projectId !== state.view?.project.id) return state;
      return { ...state, [{ "preview-result": "preview", "splits-result": "splits" }[action.type] || "suggestions"]: action.result };

    case "view":
      if (
        !action.view ||
        !Number.isSafeInteger(action.view.revision) ||
        (state.view && action.view.revision < state.view.revision)
      )
        return state;
      return { ...state, view: action.view };
    case "operation-start":
      return {
        ...state,
        operation: {
          requestId: action.requestId,
          kind: action.kind,
          revision: state.view?.revision ?? null,
          projectId: state.view?.project.id ?? "",
          status: "running",
        },
        error: "",
      };
    case "operation-status": {
      const status = action.status;
      if (!op || status?.requestId !== op.requestId) return state;
      if (status.kind && status.kind !== op.kind) return state;
      if (
        op.kind !== "load" &&
        op.kind !== "open" &&
        ((status.projectId && status.projectId !== op.projectId) ||
          (status.revision != null && status.revision !== op.revision))
      )
        return state;
      if (status.status === "failed" || status.status === "cancelled") {
        return {
          ...state,
          operation: null,
          error:
            status.error ||
            status.message ||
            (status.status === "cancelled"
              ? "Operation cancelled"
              : "Operation failed"),
        };
      }
      if (op.status === "succeeded") return state;
      // Events report the operation's committed revision. Keep the request's
      // original revision for the RPC result check, including first load.
      return {
        ...state,
        operation: {
          ...op,
          ...status,
          revision: op.revision,
          projectId: op.projectId,
        },
      };
    }
    case "operation-result":
      if (
        !op ||
        action.requestId !== op.requestId ||
        action.revision !== op.revision ||
        action.revision !== (state.view?.revision ?? null)
      )
        return state;
      if (
        !action.result ||
        !Number.isSafeInteger(action.result.revision) ||
        (state.view && action.result.revision < state.view.revision)
      )
        return state;
      if (
        op.kind !== "load" &&
        op.kind !== "open" &&
        action.result.project.id !== op.projectId
      )
        return state;
      return {
        ...state,
        view: action.result,
        operation: null,
        preview: null,
        suggestions: null,
        splits: null,
        error: "",
      };
    case "operation-error":
      if (!op || action.requestId !== op.requestId) return state;
      return { ...state, operation: null, error: action.error || "" };
    default:
      return state;
  }
}

export function parseTimeUs(text) {
  const match = /^(\d{2,}):([0-5]\d):([0-5]\d)(?:\.(\d{1,6}))?$/.exec(text);
  if (!match) throw new Error("Use HH:MM:SS.ffffff");
  const value =
    (BigInt(match[1]) * 3600n + BigInt(match[2]) * 60n + BigInt(match[3])) *
      1000000n +
    BigInt((match[4] || "").padEnd(6, "0"));
  if (value > BigInt(Number.MAX_SAFE_INTEGER))
    throw new Error("Time exceeds exact JavaScript range");
  return Number(value);
}

export function formatTimeUs(us) {
  if (!Number.isSafeInteger(us) || us < 0)
    throw new Error("Time must be a nonnegative safe integer");
  const value = BigInt(us);
  const seconds = value / 1000000n;
  return `${String(seconds / 3600n).padStart(2, "0")}:${String((seconds / 60n) % 60n).padStart(2, "0")}:${String(seconds % 60n).padStart(2, "0")}.${String(value % 1000000n).padStart(6, "0")}`;
}

// Read-only times drop the microsecond padding; editable fields keep the
// exact HH:MM:SS.ffffff form that parseTimeUs accepts.
export function displayTimeUs(us) {
  const [clock, fraction] = formatTimeUs(us).split(".");
  const milliseconds = fraction.slice(0, 3).replace(/0+$/, "");
  return milliseconds ? `${clock}.${milliseconds}` : clock;
}
