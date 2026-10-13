import test from "node:test";
import { installAudioStub } from "./audio-test-helper.js";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { parseHTML } from "linkedom";
import { createBookAPI } from "./api.js";
import { createBookController } from "./controller.js";
import { mountRepair } from "./analysis-view.js";
import { mountBookEditor } from "./view.js";

test("new book/task logs append without rebuilding prior nodes and mode changes rebuild the visible filter", async () => {
  const { document, window } = parseHTML(
    readFileSync(new URL("../../index.html", import.meta.url), "utf8"),
  );
  installAudioStub(document);
  const listeners = new Map();
  const Events = {
    On(name, callback) {
      const callbacks = listeners.get(name) || new Set();
      callbacks.add(callback);
      listeners.set(name, callbacks);
      return () => callbacks.delete(callback);
    },
  };
  const Call = {
    async ByName(name) {
      if (name.endsWith(".OpenProject")) return {revision:7,project:{id:"book",metadata:{title:"Book"},export:{profile:"standard",channels:"auto",normalize:false,format:"m4b",outputMode:"book"}},chapterRows:[],durationUs:0};
      if (name.endsWith(".StartExport")) return {requestId:"r",taskId:1,projectId:"book",revision:7};
      if (name.endsWith(".GetOperation")) return {requestId:"r",kind:"export",status:"running",taskId:1,projectId:"book",revision:7};
      if (name.endsWith(".LegalNotice")) return "Test notice";
      if (name === "chaptersmith/internal/gui.BuildInfoService.BuildInfo") return {version:"v1.0.1",buildTime:"2026-10-13T12:00:00Z"};
      if (name.endsWith(".GetHistory")) return [];
      throw Error(`Unexpected RPC ${name}`);
    },
  };
  // Execute the actual entry-point body with only its native Wails boundary
  // injected; the controller, editor and adapter remain real modules.
  const source = readFileSync(
    new URL("../main.js", import.meta.url),
    "utf8",
  ).replace(/^import\b[^;]*;\s*/gm, "");
  let controller;
  runInNewContext(source, {
    document,
    window,
    Call,
    Events,
    createBookAPI,
    createBookController: options => (controller = createBookController({...options, makeRequestId: () => "r"})),
    mountBookEditor,
    mountRepair,
    console,
  });
  await controller.open("book");
  await controller.exportProject("book.m4b");
  await Promise.resolve();
  const emit = (name, line) => {
    for (const callback of listeners.get(name) || [])
      callback({ data: { requestId: "r", taskId: 1, line } });
  };
  document.getElementById("aboutBtn").click();
  assert.equal(document.getElementById("buildInfo")?.textContent, "Version v1.0.1 · Built 2026-10-13T12:00:00Z");
  const log = document.getElementById("logView");
  const count = document.getElementById("logLineCount");
  emit("book-log", "Loading book");
  const first = log.firstChild;
  for (const callback of listeners.get("book-log") || []) callback({data:{requestId:"stale",taskId:99,line:"Stale"}});
  assert.equal(log.children.length, 1);
  emit("book-log", "[aac @ 0x123] Qavg: 12");
  emit("task-log", "ERROR: test error");
  assert.equal(log.firstChild === first, true);
  assert.equal(log.children.length, 2);
  assert.equal(log.lastChild.classList.contains("error"), true);
  assert.equal(count.textContent, "2/3 lines");
  const mode = document.getElementById("logModeBtn");
  mode.click();
  assert.equal(log.firstChild === first, false);
  assert.deepEqual(
    [...log.children].map((node) => node.textContent),
    ["Loading book", "[aac @ 0x123] Qavg: 12", "ERROR: test error"],
  );
  const fullFirst = log.firstChild;
  emit("book-log", "Book loaded");
  assert.equal(log.firstChild === fullFirst, true);
  assert.equal(count.textContent, "4 lines");
  mode.click();
  assert.equal(count.textContent, "3/4 lines");
  assert.deepEqual(
    [...log.children].map((node) => node.textContent),
    ["Loading book", "ERROR: test error", "Book loaded"],
  );
  window.dispatchEvent(new window.Event("pagehide"));
  emit("book-log", "Late log");
  assert.equal(log.children.length, 3);
});

test('actual entry point wires original-file repair and checked/unchecked prompt without loading the book', async () => {
  const {document,window}=parseHTML(readFileSync(new URL('../../index.html',import.meta.url),'utf8'));installAudioStub(document);
  const listeners=new Map(), calls=[];let taskId=1;
  const emit=(name,data)=>{for(const fn of listeners.get(name)||[])fn({data});};
  const Events={On(name,fn){const set=listeners.get(name)||new Set();set.add(fn);listeners.set(name,set);return()=>set.delete(fn);}};
  const Call={async ByName(name,...args){const method=name.split('.').at(-1);calls.push([method,...args]);
    if(method==='BuildInfo')return {version:'dev',buildTime:'unknown'};if(method==='LegalNotice')return 'notice';if(method==='GetHistory')return [];
    if(method==='SelectBookInput')return '/source/original.m4b';if(method==='SelectOutputFile')return '/output/fixed.m4b';
    if(method==='StartMerge'){emit('task-prompt-fix',{promptId:`prompt-${taskId}`,filename:'original.m4b'});return;}
    if(method==='RespondFixPromptOptions'){if(args[2]){emit('repair-analysis',{reportId:'r',taskId,status:'awaitingDecision',result:{origin:'repair',report:{id:'r',findings:[]}}});return 'r';}return '';}
    if(method==='GetRepairAnalysis')return {reportId:'r',taskId,status:'awaitingDecision',result:{origin:'repair',report:{id:'r',findings:[],sources:[],counts:{},coverage:[]}}};
    if(method==='CancelMerge'){emit('task-done',{taskID:taskId,success:false});return;}
    throw Error(`Unexpected ${method}`);
  }};
  const source=readFileSync(new URL('../main.js',import.meta.url),'utf8').replace(/^import\b[^;]*;\s*/gm,'');
  runInNewContext(source,{document,window,Call,Events,createBookAPI,createBookController,mountBookEditor,mountRepair,console});
  const tick=()=>new Promise(resolve=>setImmediate(resolve));
  for(const checked of [false,true]) {
    document.querySelector('[data-action="repair-file"]').click();await tick();await tick();
    const checkbox=document.querySelector('.repair-prompt input');assert.equal(checkbox.checked,false);checkbox.checked=checked;
    if(taskId > 1) {emit('task-done',{taskID:taskId-1,success:false});assert.equal(document.querySelector('[data-action="repair-file"]').disabled,true);}
    document.querySelector('[data-choice="fix"]').click();await tick();await tick();
    assert.deepEqual(calls.findLast(c=>c[0]==='RespondFixPromptOptions'),['RespondFixPromptOptions',`prompt-${taskId}`,'fix',checked]);
    if(checked) {assert.equal(document.querySelector('[data-action="proceed"]').textContent,'Continue repair');assert.ok(calls.some(c=>c[0]==='GetRepairAnalysis'));}
    else assert.equal(calls.some(c=>c[0]==='GetRepairAnalysis'),false);
    const cancel=[...document.querySelectorAll('#repairLauncher button')].find(n=>n.textContent==='Cancel repair');cancel.click();await tick();await tick();
    assert.equal(document.querySelector('[data-action="repair-file"]').disabled,false);
    taskId++;
  }
  assert.deepEqual(calls.find(c=>c[0]==='StartMerge'),['StartMerge','/source/original.m4b','/output/fixed.m4b']);
  assert.equal(calls.some(c=>['LoadBook','ApplyEdit','StartExport'].includes(c[0])),false);
  window.dispatchEvent(new window.Event('pagehide'));
  assert.ok([...listeners.values()].every(set=>set.size===0));
});
