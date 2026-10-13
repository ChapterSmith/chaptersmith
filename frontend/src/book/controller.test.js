import test from "node:test";
import assert from "node:assert/strict";
import { createBookController } from "./controller.js";
import { createBookAPI } from "./api.js";
import { metadataSnapshot } from "./metadata.js";

const view = (revision = 7) => ({
  revision,
  dirty: true,
  canUndo: true,
  durationUs: 20000000,
  chapterRows: [],
  coverUrl: "",
  sourceWarning: "",
  project: {
    id: "a",
    schemaVersion: 1,
    metadata: {
      title: "Title",
      artist: "Author",
      composer: "Reader",
      album: "Album",
      albumArtist: "Other",
      genre: "Genre",
      date: "2026",
      copyright: "Copyright",
      comment: "Comment",
      description: "Description",
      subtitle: "Subtitle",
      grouping: "Group",
      extra: { hidden: "keep", other: "" },
    },
    sources: [
      {
        id: "s",
        path: "/audio",
        mtimeNs: "9223372036854775807",
        sizeBytes: 1,
        durationUs: 20000000,
        sampleRate: 44100,
        channels: 2,
      },
    ],
    chapters: [],
    coverPath: "",
    export: {
      profile: "standard",
      channels: "auto",
      normalize: false,
      outputMode: "book",
      format: "m4b",
    },
  },
});
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
};
function fixture(call) {
  let handler,
    cleanups = 0,
    sequence = 0;
  const api = {
    call,
    onOperation: (fn) => {
      handler = fn;
      return () => cleanups++;
    },
    onLog: () => () => cleanups++,
  };
  const controller = createBookController({
    api,
    makeRequestId: () => `r${++sequence}`,
  });
  return {
    controller,
    event: (data) => handler(data),
    cleanups: () => cleanups,
  };
}

test("blank values are sent literally and hidden Extra survives visible edits", async () => {
  let submitted;
  const { controller } = fixture(async (method, ...args) => {
    if (method === "OpenProject") return view();
    if (method === "ApplyEdit") {
      submitted = args[1].metadata;
      return {
        ...view(8),
        project: { ...view().project, metadata: submitted },
      };
    }
    throw new Error(method);
  });
  await controller.open("a");
  const original = controller.getState().view;
  const metadata = metadataSnapshot(original.project.metadata, {
    title: "",
    description: "",
  });
  assert.equal(original.project.metadata.title, "Title");
  await controller.edit({ kind: "metadata", metadata });
  assert.equal(submitted.title, "");
  assert.equal(submitted.description, "");
  assert.deepEqual(submitted.extra, { hidden: "keep", other: "" });
  assert.equal(submitted.subtitle, "Subtitle");
  assert.equal(submitted.albumArtist, "Other");
});

test("serialized edits use returned revision and capture their input snapshot", async () => {
  const first = deferred();
  const revisions = [];
  let count = 0;
  const { controller } = fixture(async (method, ...args) => {
    if (method === "OpenProject") return view();
    if (method === "ApplyEdit") {
      revisions.push(args[0]);
      if (++count === 1) return first.promise;
      assert.equal(args[1].metadata.title, "Second");
      return view(9);
    }
  });
  await controller.open("a");
  const p = controller.edit({
    kind: "metadata",
    metadata: metadataSnapshot(view().project.metadata, { title: "First" }),
  });
  const input = {
    kind: "metadata",
    metadata: metadataSnapshot(view().project.metadata, { title: "Second" }),
  };
  const q = controller.edit(input);
  input.metadata.title = "Unexpected mutation";
  await Promise.resolve();
  assert.deepEqual(revisions, [7]);
  first.resolve(view(8));
  await Promise.all([p, q]);
  assert.deepEqual(revisions, [7, 8]);
  assert.equal(controller.getState().view.revision, 9);
});

test("failure unlocks controls and queued save can recover", async () => {
  const { controller } = fixture(async (method) => {
    if (method === "ApplyEdit") throw new Error("write failed");
    return view();
  });
  await controller.open("a");
  await assert.rejects(
    controller.edit({ kind: "cover-remove" }),
    /write failed/,
  );
  assert.equal(controller.getState().operation, null);
  assert.equal(controller.getState().view.revision, 7);
  assert.match(controller.getState().error, /write failed/);
  await controller.save();
  assert.equal(controller.getState().error, "");
});

test("cancelled picker does not import or replace view", async () => {
  const calls = [];
  const { controller } = fixture(async (method) => {
    calls.push(method);
    return method === "SelectBookInput" ? "" : view();
  });
  await controller.open("a");
  const original = controller.getState().view;
  await controller.load({ kind: "folder" });
  assert.deepEqual(calls, ["OpenProject", "SelectBookInput"]);
  assert.equal(controller.getState().view, original);
  assert.equal(controller.getState().operation, null);
});

test("mtimeNs stays a string after load and edit", async () => {
  const { controller } = fixture(async () => view());
  await controller.load({ inputPath: "/audio", chapterSource: "embedded" });
  await controller.edit({ kind: "cover-remove" });
  assert.equal(
    controller.getState().view.project.sources[0].mtimeNs,
    "9223372036854775807",
  );
});

test("stale terminal event cannot replace view or unlock a newer operation", async () => {
  const reply = deferred();
  const f = fixture(async (method) =>
    method === "LoadBook" ? reply.promise : view(),
  );
  await f.controller.open("a");
  const p = f.controller.load({ inputPath: "/audio" });
  await Promise.resolve();
  f.event({ requestId: "r1", status: "failed", error: "old" });
  assert.equal(f.controller.getState().operation.requestId, "r2");
  assert.equal(f.controller.getState().error, "");
  f.event({ requestId: "r2", status: "succeeded" });
  reply.resolve(view(8));
  await p;
  assert.equal(f.controller.getState().view.revision, 8);
});

test("unsafe times reject before RPC and disposal cleans subscriptions", async () => {
  const calls = [];
  const f = fixture(async (method) => {
    calls.push(method);
    return view();
  });
  await f.controller.open("a");
  await assert.rejects(
    f.controller.edit({ kind: "boundary", newStartUs: 9007199254740992 }),
  );
  assert.deepEqual(calls, ["OpenProject"]);
  f.controller.dispose();
  assert.equal(f.cleanups(), 1);
  await assert.rejects(f.controller.save(), /disposed/);
});

test("terminal cancelled event rejects a late success reply without replacing the book", async () => {
  const reply = deferred();
  const f = fixture(async (method) =>
    method === "LoadBook" ? reply.promise : view(),
  );
  await f.controller.open("a");
  const p = f.controller.load({ inputPath: "/audio" });
  await Promise.resolve();
  f.event({
    requestId: "r2",
    kind: "load",
    status: "cancelled",
    message: "Cancelled",
  });
  reply.resolve(view(8));
  await assert.rejects(p, /stale/i);
  assert.equal(f.controller.getState().view.revision, 7);
  assert.equal(f.controller.getState().operation, null);
});

test("API uses BookService names, normalizes event payload and returns cleanup", async () => {
  const calls = [];
  const listeners = new Map();
  let removed = 0;
  const api = createBookAPI(
    (...args) => {
      calls.push(args);
      return Promise.resolve("ok");
    },
    {
      On: (name, fn) => {
        listeners.set(name, fn);
        return () => removed++;
      },
    },
  );
  await api.call("LoadBook", "request", { inputPath: "/x" });
  assert.deepEqual(calls, [
    [
      "chaptersmith/internal/gui.BookService.LoadBook",
      "request",
      { inputPath: "/x" },
    ],
  ]);
  let status, line;
  const a = api.onOperation((data) => (status = data));
  const b = api.onLog((data) => (line = data));
  listeners.get("book-operation")({ data: [{ requestId: "x" }] });
  listeners.get("book-log")({ data: { line: "hello" } });
  assert.equal(status.requestId, "x");
  assert.equal(line.line, "hello");
  a();
  b();
  assert.equal(removed, 2);
});

test('analysis uses edited revision, preserves silence RPC separation, and editing releases report', async () => {
  const calls=[]; const {controller}=fixture(async(method,...args)=>{
    calls.push([method,...args]);
    if(method==='OpenProject')return view();
    if(method==='ApplyEdit')return view(8);
    if(method==='AnalyzeAudio')return {requestId:args[0],revision:args[1],projectId:'a',origin:'book',report:{id:'analysis',findings:[]}};
  });
  controller.attachPlayer({clear(){},load(){},seek(){},play(){},dispose(){}});
  await controller.open('a');await controller.edit({kind:'cover-remove'});await controller.analyzeAudio();
  assert.equal(calls.find(c=>c[0]==='AnalyzeAudio')[2],8);
  assert.equal(calls.some(c=>c[0]==='SuggestBoundaries'),false);
  await controller.edit({kind:'cover-remove'});
  assert.equal(controller.analysis.getState().report,null);
  assert.equal(calls.filter(c=>c[0]==='ReleaseAudioAnalysis').length,1);
  controller.dispose();
});
