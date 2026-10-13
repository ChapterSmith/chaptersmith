import test from "node:test";
import { installAudioStub } from "./audio-test-helper.js";
import assert from "node:assert/strict";
import { parseHTML } from "linkedom";
import { createBookController } from "./controller.js";
import { mountMetadata } from "./metadata.js";
import { mountBookEditor } from "./view.js";

const metadata = () => ({
  title: "Title",
  artist: "Author",
  composer: "Reader",
  album: "Album",
  albumArtist: "Other",
  genre: "Genre",
  date: "2026",
  copyright: "Rights",
  comment: "Comment",
  description: "Description",
  subtitle: "Subtitle",
  grouping: "Group",
  extra: { hidden: "keep", "<tag>": "<script>" },
});
const view = (revision = 7, id = "a") => ({
  project: {
    id,
    schemaVersion: 1,
    metadata: metadata(),
    sources: [],
    chapters: [],
    coverPath: "",
    export: {},
  },
  revision,
  dirty: true,
  canUndo: true,
  durationUs: 1000000,
  chapterRows: [
    {
      id: "c",
      title: "<script>book</script>",
      startUs: 0,
      durationUs: 1000000,
    },
  ],
  coverUrl: "",
  sourceWarning: "",
});
const tick = () => new Promise((resolve) => setImmediate(resolve));
function fixture() {
  const { document, window } = parseHTML(
    "<html><body><main></main></body></html>",
  );
  installAudioStub(document);
  const calls = [];
  let current = view();
  let failSave = false;
  let failOpen = false;
  const api = {
    onOperation: () => () => {},
    call: async (method, ...args) => {
      calls.push([method, ...args]);
      if (method === "OpenProject") {
        if (failOpen) throw Error("open unavailable");
        current = { ...view(current.revision + 1, args[0]), dirty: false };
      }
      if (method === "ApplyEdit")
        current = {
          ...current,
          revision: current.revision + 1,
          project: { ...current.project, metadata: args[1].metadata },
          dirty: true,
        };
      if (method === "SelectCover")
        current = {
          ...current,
          revision: current.revision + 1,
          coverUrl: "/_media/cover",
        };
      if (method === "SaveProject") {
        if (failSave) throw Error("save unavailable");
        current = { ...current, dirty: false };
      }
      if (method === "ListProjects")
        return [
          { id: "b", title: "Other", updatedAt: "2026-10-10" },
          { id: "a", title: "Title", updatedAt: "2026-10-10" },
        ];
      if (method === "SelectBookInput" || method === "SelectSidecar") return "";
      return structuredClone(current);
    },
  };
  const controller = createBookController({
    api,
    makeRequestId: (() => {
      let i = 0;
      return () => String(++i);
    })(),
  });
  const root = document.querySelector("main");
  return {
    document,
    window,
    root,
    controller,
    calls,
    failSave: () => {
      failSave = true;
    },
    failOpen: () => {
      failOpen = true;
    },
  };
}
const input = (f, name, value) => {
  const field = f.root.querySelector(`[name="${name}"]`);
  field.value = value;
  field.dispatchEvent(new f.window.Event("input", { bubbles: true }));
};
const click = (f, selector) => f.root.querySelector(selector).click();

test("metadata form submits blanks literally without mutating view and keeps collapsed fields/Extra", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountMetadata(f.root, f.controller);
  input(f, "title", "");
  input(f, "description", "");
  assert.equal(f.controller.getState().view.project.metadata.title, "Title");
  await cleanup.submit();
  const sent = f.calls.find(([method]) => method === "ApplyEdit")[2].metadata;
  assert.equal(sent.title, "");
  assert.equal(sent.description, "");
  assert.equal(sent.subtitle, "Subtitle");
  assert.deepEqual(sent.extra, { hidden: "keep", "<tag>": "<script>" });
  assert.equal(f.root.querySelector("script"), null);
  assert.equal(cleanup.isDirty(), false);
  cleanup();
});

test("pending metadata draft survives unrelated cover revision and becomes clean after applying", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountMetadata(f.root, f.controller);
  input(f, "description", "draft");
  // A cover selection returns new metadata snapshot without accepting the draft.
  await f.controller.selectCover();
  assert.equal(f.root.querySelector('[name="description"]').value, "draft");
  assert.equal(cleanup.isDirty(), true);
  await cleanup.submit();
  assert.equal(
    f.controller.getState().view.project.metadata.description,
    "draft",
  );
  assert.equal(cleanup.isDirty(), false);
  cleanup();
});

test("dirty switch offers Back then Save and opens only after full draft is saved", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  input(f, "description", "");
  click(f, '[data-action="projects"]');
  await tick();
  click(f, '[data-project-id="b"]');
  assert.equal(f.calls.filter(([m]) => m === "OpenProject").length, 1);
  click(f, '[data-choice="back"]');
  await tick();
  assert.equal(f.controller.getState().view.project.id, "a");
  click(f, '[data-project-id="b"]');
  click(f, '[data-choice="save"]');
  await tick();
  await tick();
  assert.deepEqual(
    f.calls
      .filter(([m]) => ["ApplyEdit", "SaveProject", "OpenProject"].includes(m))
      .map(([m]) => m),
    ["OpenProject", "ApplyEdit", "SaveProject", "OpenProject"],
  );
  assert.equal(f.controller.getState().view.project.id, "b");
  cleanup();
});

test("dirty switch Discard opens selected book and failed Save keeps dialog/book usable", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  input(f, "title", "draft");
  click(f, '[data-action="projects"]');
  await tick();
  click(f, '[data-project-id="b"]');
  f.failSave();
  click(f, '[data-choice="save"]');
  await tick();
  await tick();
  assert.equal(f.controller.getState().view.project.id, "a");
  assert.equal(f.root.querySelector('[data-choice="discard"]').disabled, false);
  assert.match(f.root.textContent, /save unavailable/);
  click(f, '[data-choice="discard"]');
  await tick();
  assert.equal(f.controller.getState().view.project.id, "b");
  cleanup();
});

test("Extra rows support deliberate removal and chapter titles remain text", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  click(f, '[data-remove-extra="hidden"]');
  f.root
    .querySelector("form")
    .dispatchEvent(
      new f.window.Event("submit", { bubbles: true, cancelable: true }),
    );
  await tick();
  assert.deepEqual(f.controller.getState().view.project.metadata.extra, {
    "<tag>": "<script>",
  });
  click(f, '[data-edit-tab="chapters"]');
  assert.equal(
    f.root.querySelector('[data-chapter-id="c"] [name="title"]').value,
    "<script>book</script>",
  );
  assert.equal(f.root.querySelector("script"), null);
  cleanup();
});

test("Discard reopening the same saved project clears drafts so later Save cannot reapply them", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  input(f, "title", "Draft to discard");
  input(f, "description", "");
  click(f, '[data-remove-extra="hidden"]');
  click(f, '[data-action="projects"]');
  await tick();
  click(f, '[data-project-id="a"]');
  click(f, '[data-choice="discard"]');
  await tick();
  assert.equal(f.controller.getState().view.project.id, "a");
  assert.equal(f.root.querySelector('[name="title"]').value, "Title");
  assert.equal(
    f.root.querySelector('[name="description"]').value,
    "Description",
  );
  assert.equal(f.root.querySelector("[data-extra-key]").value, "hidden");
  assert.equal(f.root.querySelector(".book-dirty").textContent, "Saved");
  click(f, '[data-action="save"]');
  await tick();
  assert.equal(
    f.calls.some(([method]) => method === "ApplyEdit"),
    false,
  );
  assert.equal(f.controller.getState().view.project.metadata.title, "Title");
  cleanup();
});

test("failed Discard switch preserves the current book and pending draft", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  input(f, "title", "Keep after failed open");
  click(f, '[data-action="projects"]');
  await tick();
  click(f, '[data-project-id="a"]');
  f.failOpen();
  click(f, '[data-choice="discard"]');
  await tick();
  assert.equal(f.controller.getState().view.project.metadata.title, "Title");
  assert.equal(
    f.root.querySelector('[name="title"]').value,
    "Keep after failed open",
  );
  assert.equal(
    f.root.querySelector(".book-dirty").textContent,
    "Unapplied edits",
  );
  assert.equal(f.root.querySelector('[data-choice="back"]').disabled, false);
  cleanup();
});

test("cancelled Discard load picker preserves the current book and pending draft", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  input(f, "title", "Keep after cancelled picker");
  click(f, '[data-action="folder"]');
  click(f, '[data-choice="discard"]');
  await tick();
  assert.equal(f.controller.getState().view.project.metadata.title, "Title");
  assert.equal(
    f.root.querySelector('[name="title"]').value,
    "Keep after cancelled picker",
  );
  assert.equal(
    f.root.querySelector(".book-dirty").textContent,
    "Unapplied edits",
  );
  assert.equal(f.root.querySelector('[data-action="save"]').disabled, false);
  cleanup();
});

test("cancelled sidecar picker unlocks controls and preserves previous choice", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  click(f, '[data-action="playlist"]');
  await tick();
  assert.equal(f.controller.getState().operation, null);
  assert.equal(f.root.querySelector('[data-action="folder"]').disabled, false);
  cleanup();
});

test("edit tabs can be switched with arrow keys", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountBookEditor(f.root, f.controller);
  const keyboard = new f.window.Event("keydown", {
    bubbles: true,
    cancelable: true,
  });
  keyboard.key = "ArrowRight";
  f.root.querySelector('[data-edit-tab="metadata"]').dispatchEvent(keyboard);
  assert.equal(
    f.root
      .querySelector('[data-edit-tab="chapters"]')
      .getAttribute("aria-selected"),
    "true",
  );
  cleanup();
});

test("duplicate Extra keys reject the draft and leave controls available", async () => {
  const f = fixture();
  await f.controller.open("a");
  const cleanup = mountMetadata(f.root, f.controller);
  const keys = f.root.querySelectorAll("[data-extra-key]");
  keys[1].value = "hidden";
  keys[1].dispatchEvent(new f.window.Event("input", { bubbles: true }));
  await assert.rejects(cleanup.submit(), /Duplicate/);
  assert.equal(f.controller.getState().operation, null);
  assert.equal(
    f.calls.some(([method]) => method === "ApplyEdit"),
    false,
  );
  assert.equal(f.root.querySelector('[type="submit"]').disabled, false);
  keys[1].value = "newKey";
  await cleanup.submit();
  assert.deepEqual(f.controller.getState().view.project.metadata.extra, {
    hidden: "keep",
    newKey: "<script>",
  });
  cleanup();
});

test('normal analysis has a distinct entry point from silence adjustment', async () => {
  const f=fixture(); const cleanup=mountBookEditor(f.root,f.controller);
  const analyze=f.root.querySelector('[data-action="analyze"]');
  assert.ok(analyze); assert.equal(analyze.hidden,true);
  await f.controller.open('a'); assert.equal(analyze.hidden,false);
  assert.equal(analyze.textContent,'Analyze audio');
  assert.ok(f.root.querySelector('[data-action="suggest"]').textContent.includes('suggestions'));
  assert.notEqual(analyze.closest('[role="tabpanel"]'), f.root.querySelector('[data-action="suggest"]').closest('[role="tabpanel"]'));
  cleanup(); f.controller.dispose();
});

test('repair launcher picks a source and output, preserves book, and recovers checked prompt', async () => {
  const {mountRepair}=await import('./analysis-view.js');
  const f=fixture(), calls=[], events={}; await f.controller.open('a'); const original=f.controller.getState().view;
  const root=f.document.createElement('section'); const api={
    selectInput:async()=>{calls.push('input');return '/book/source.m4b';},selectOutput:async()=>{calls.push('output');return '/book/fixed.m4b';},
    startMerge:async(...args)=>{calls.push(['start',...args]);events.prompt({promptId:'prompt',filename:'<img src=x>'});},
    onPrompt:fn=>{events.prompt=fn;return()=>delete events.prompt;},onProgress:fn=>{events.progress=fn;return()=>delete events.progress;},onDone:fn=>{events.done=fn;return()=>delete events.done;},
    respondFixPrompt:async(...args)=>{calls.push(['respond',...args]);return args[2]?'repair':'';},cancelRepair:async()=>calls.push('cancel')};
  f.controller.analysis.loadRepair=async id=>calls.push(['recover',id]);
  const cleanup=mountRepair(root,f.controller,api);
  root.querySelector('[data-action="repair-file"]').click();await tick();await tick();
  assert.deepEqual(calls.slice(0,3),['input','output',['start','/book/source.m4b','/book/fixed.m4b']]);
  assert.equal(f.controller.getState().view,original);assert.equal(f.controller.getState().repairBusy,true);
  assert.equal(root.querySelector('img'),null);const checkbox=root.querySelector('input[type="checkbox"]'); assert.equal(!!checkbox.checked,false);checkbox.checked=true;
  root.querySelector('[data-choice="fix"]').click();await tick();
  assert.deepEqual(calls.slice(-2),[['respond','prompt','fix',true],['recover','repair']]);
  events.done({success:true}); assert.equal(f.controller.getState().repairBusy,false);
  cleanup();assert.deepEqual(Object.keys(events),[]);f.controller.dispose();
});

test('repair picker cancellation and source overwrite rejection never start a job', async () => {
  const {mountRepair}=await import('./analysis-view.js');
  for(const output of ['', '/book/source.m4b']) {
    const f=fixture(),root=f.document.createElement('section');let starts=0;
    const cleanup=mountRepair(root,f.controller,{selectInput:async()=>'/book/source.m4b',selectOutput:async()=>output,startMerge:async()=>starts++,onPrompt:()=>()=>{},onProgress:()=>()=>{},onDone:()=>()=>{},cancelRepair:async()=>{}});
    root.querySelector('[data-action="repair-file"]').click(); await tick();await tick();
    assert.equal(starts,0);assert.equal(f.controller.getState().repairBusy,false); cleanup();f.controller.dispose();
  }
});

test('repair offers the open single-file book without asking for the input again', async () => {
  const {mountRepair}=await import('./analysis-view.js');
  const f=fixture(), calls=[]; await f.controller.open('a');
  const root=f.document.createElement('section');
  const api={selectInput:async()=>{calls.push('input');return '/other.m4b';},selectOutput:async name=>{calls.push(['output',name]);return '/out/fixed.m4b';},
    startMerge:async(...args)=>{calls.push(['start',...args]);},onPrompt:()=>()=>{},onProgress:()=>()=>{},onDone:()=>()=>{},cancelRepair:async()=>{}};
  const cleanup=mountRepair(root,f.controller,api);
  const offer=root.querySelector('[data-action="repair-open"]');
  assert.equal(offer.hidden,true);
  assert.equal(root.querySelector('[data-action="repair-file"]').textContent,'Choose a file to repair…');
  const view=f.controller.getState().view;
  f.controller.getState().view.project.sources=[{id:'s',path:'/books/My Book.m4b'}];
  f.controller.setRepairBusy(true);f.controller.setRepairBusy(false);
  assert.equal(offer.hidden,false);assert.equal(offer.textContent,'Repair “My Book.m4b”');
  assert.equal(root.querySelector('[data-action="repair-file"]').textContent,'Choose another file…');
  offer.click();await tick();await tick();
  assert.deepEqual(calls,[['output','My Book_fixed.m4b'],['start','/books/My Book.m4b','/out/fixed.m4b']]);
  assert.equal(f.controller.getState().view,view);
  cleanup();f.controller.dispose();
});
